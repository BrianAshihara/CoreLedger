package movements_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/idempotency"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

type env struct {
	t        *testing.T
	db       *testdb.DB
	store    *postgres.Store
	accounts *accounts.Service
	svc      *movements.Service
	world    uuid.UUID
}

func newEnv(t *testing.T, lockTimeout time.Duration, opts ...testdb.Option) *env {
	t.Helper()
	d := testdb.New(t, opts...)
	store := postgres.NewStore(d.Pool)
	e := &env{
		t:        t,
		db:       d,
		store:    store,
		accounts: accounts.NewService(store),
		svc:      movements.NewService(store, posting.New(lockTimeout), lockTimeout),
	}
	if err := d.Superuser(t).QueryRow(context.Background(),
		`SELECT id FROM accounts WHERE system_code = 'WORLD' AND currency = 'BRL'`).Scan(&e.world); err != nil {
		t.Fatal(err)
	}
	// Varredura de invariantes (SPEC 21.5).
	t.Cleanup(func() {
		if t.Failed() {
			return
		}
		r, err := reconcile.NewService(store).Run(context.Background())
		if err != nil || r.Status != reconcile.StatusOK {
			t.Errorf("reconciliation after test: %v %+v", err, r.Issues)
		}
	})
	return e
}

func (e *env) user() string {
	e.t.Helper()
	a, err := e.accounts.Create(context.Background(), accounts.CreateInput{OwnerID: "owner", Currency: "BRL"})
	if err != nil {
		e.t.Fatal(err)
	}
	return a.ID.String()
}

func key() string { return "key-" + uuid.NewString() }

func (e *env) deposit(id string, amount int64) movements.Result {
	e.t.Helper()
	r, err := e.svc.Deposit(context.Background(), movements.ExternalInput{IdempotencyKey: key(), AccountID: id, Amount: amount, Currency: "BRL"})
	if err != nil {
		e.t.Fatalf("deposit: %v", err)
	}
	return r
}

func (e *env) balance(id string) int64 {
	e.t.Helper()
	a, err := e.store.GetAccount(context.Background(), uuid.MustParse(id))
	if err != nil {
		e.t.Fatal(err)
	}
	return a.Balance
}

func (e *env) transactionCount() int64 {
	e.t.Helper()
	var n int64
	if err := e.db.Superuser(e.t).QueryRow(context.Background(), `SELECT count(*) FROM ledger_transactions`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func TestDepositWithdrawTransfer(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice, bob := e.user(), e.user()

	dep := e.deposit(alice, 100_000)
	if dep.Replayed || dep.Transaction.Type != ledger.TypeDeposit || dep.Transaction.Amount() != 100_000 {
		t.Fatalf("deposit result %+v", dep)
	}

	tr, err := e.svc.Transfer(ctx, movements.TransferInput{IdempotencyKey: key(), FromAccountID: alice, ToAccountID: bob, Amount: 20_000, Currency: "BRL"})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Transaction.Type != ledger.TypeTransfer || tr.Transaction.Amount() != 20_000 || len(tr.Transaction.Entries) != 2 {
		t.Fatalf("transfer result %+v", tr)
	}

	wd, err := e.svc.Withdraw(ctx, movements.ExternalInput{IdempotencyKey: key(), AccountID: bob, Amount: 5_000, Currency: "BRL"})
	if err != nil {
		t.Fatal(err)
	}
	if wd.Transaction.Type != ledger.TypeWithdrawal || wd.Transaction.Amount() != 5_000 {
		t.Fatalf("withdrawal result %+v", wd)
	}

	if e.balance(alice) != 80_000 || e.balance(bob) != 15_000 || e.balance(e.world.String()) != -95_000 {
		t.Fatalf("balances alice=%d bob=%d world=%d", e.balance(alice), e.balance(bob), e.balance(e.world.String()))
	}

	got, err := e.svc.Transaction(ctx, tr.Transaction.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != tr.Transaction.ID || len(got.Entries) != 2 {
		t.Fatalf("Transaction() = %+v", got)
	}
	if _, err := e.svc.Transaction(ctx, uuid.Must(uuid.NewV7())); !errors.Is(err, ledger.ErrTransactionNotFound) {
		t.Fatalf("unknown transaction: want ErrTransactionNotFound, got %v", err)
	}
}

// Ordem de validação da transferência (SPEC 10): quando várias coisas estão
// erradas, o erro devolvido é o do primeiro passo que falha. Nenhum caso
// deixa nada gravado.
func TestTransferValidationOrder(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice, bob, suspended := e.user(), e.user(), e.user()
	e.deposit(alice, 1_000)
	if _, err := e.accounts.ChangeStatus(ctx, uuid.MustParse(suspended), "SUSPENDED"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.db.Superuser(t).Exec(ctx, `INSERT INTO currencies (code, exponent) VALUES ('USD', 2)`); err != nil {
		t.Fatal(err)
	}
	usd, err := e.accounts.Create(ctx, accounts.CreateInput{OwnerID: "carol", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	unknown := uuid.Must(uuid.NewV7()).String()
	before := e.transactionCount()

	for _, tc := range []struct {
		name     string
		from, to string
		amount   int64
		currency string
		want     error
	}{
		{"3. amount beats currency", alice, alice, 0, "XYZ", money.ErrInvalidAmount},
		{"3. amount above maximum", alice, bob, money.MaxAmount + 1, "BRL", money.ErrInvalidAmount},
		{"4. currency beats same account", alice, alice, 10, "XYZ", money.ErrUnsupportedCurrency},
		{"4. missing currency", alice, bob, 10, "", money.ErrCurrencyRequired},
		{"5. same account beats unknown account", alice, alice, 10, "BRL", ledger.ErrSameAccountTransfer},
		{"5. malformed id is not found", "not-a-uuid", bob, 10, "BRL", account.ErrNotFound},
		{"8. unknown account", alice, unknown, 10, "BRL", account.ErrNotFound},
		{"8. system account is not found", alice, e.world.String(), 10, "BRL", account.ErrNotFound},
		{"9. inactive beats currency mismatch", suspended, usd.ID.String(), 10, "BRL", account.ErrNotActive},
		{"10. currency mismatch beats funds", usd.ID.String(), alice, 10, "BRL", ledger.ErrCurrencyMismatch},
		{"11. insufficient funds", alice, bob, 1_001, "BRL", ledger.ErrInsufficientFunds},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := e.svc.Transfer(ctx, movements.TransferInput{
				IdempotencyKey: key(), FromAccountID: tc.from, ToAccountID: tc.to, Amount: tc.amount, Currency: tc.currency,
			})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}

	if got := e.transactionCount(); got != before {
		t.Fatalf("rejected transfers created %d transactions", got-before)
	}
}

func TestDepositAndWithdrawalRules(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice := e.user()
	e.deposit(alice, 500)

	// Só contas USER recebem depósito; a WORLD se comporta como inexistente.
	_, err := e.svc.Deposit(ctx, movements.ExternalInput{IdempotencyKey: key(), AccountID: e.world.String(), Amount: 10, Currency: "BRL"})
	if !errors.Is(err, account.ErrNotFound) {
		t.Fatalf("deposit into WORLD: want ErrNotFound, got %v", err)
	}
	_, err = e.svc.Withdraw(ctx, movements.ExternalInput{IdempotencyKey: key(), AccountID: alice, Amount: 501, Currency: "BRL"})
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("withdraw more than the balance: want ErrInsufficientFunds, got %v", err)
	}
	if _, err := e.accounts.ChangeStatus(ctx, uuid.MustParse(alice), "SUSPENDED"); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Withdraw(ctx, movements.ExternalInput{IdempotencyKey: key(), AccountID: alice, Amount: 1, Currency: "BRL"})
	if !errors.Is(err, account.ErrNotActive) {
		t.Fatalf("withdraw from SUSPENDED: want ErrNotActive, got %v", err)
	}
}

func TestReplayReturnsTheOriginalTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice, bob := e.user(), e.user()
	e.deposit(alice, 1_000)
	before := e.transactionCount()

	in := movements.TransferInput{IdempotencyKey: key(), FromAccountID: alice, ToAccountID: bob, Amount: 300, Currency: "BRL"}
	first, err := e.svc.Transfer(ctx, in)
	if err != nil {
		t.Fatal(err)
	}
	// O mesmo pedido com o UUID em maiúsculas: depois de decodificado é o
	// mesmo pedido, então é replay, não conflito.
	in.ToAccountID = strings.ToUpper(bob)
	second, err := e.svc.Transfer(ctx, in)
	if err != nil {
		t.Fatal(err)
	}

	if first.Replayed || !second.Replayed {
		t.Fatalf("replayed flags: first=%v second=%v", first.Replayed, second.Replayed)
	}
	if second.Transaction.ID != first.Transaction.ID || !second.Transaction.CreatedAt.Equal(first.Transaction.CreatedAt) {
		t.Fatalf("replay returned %s, original was %s", second.Transaction.ID, first.Transaction.ID)
	}
	if got := e.transactionCount() - before; got != 1 {
		t.Fatalf("%d transactions created, want 1", got)
	}
	if e.balance(alice) != 700 || e.balance(bob) != 300 {
		t.Fatalf("balances alice=%d bob=%d: money moved twice?", e.balance(alice), e.balance(bob))
	}
}

func TestKeyReusedWithDifferentRequestIsConflict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice, bob := e.user(), e.user()
	e.deposit(alice, 1_000)

	k := key()
	if _, err := e.svc.Transfer(ctx, movements.TransferInput{IdempotencyKey: k, FromAccountID: alice, ToAccountID: bob, Amount: 200, Currency: "BRL"}); err != nil {
		t.Fatal(err)
	}
	_, err := e.svc.Transfer(ctx, movements.TransferInput{IdempotencyKey: k, FromAccountID: alice, ToAccountID: bob, Amount: 500, Currency: "BRL"})
	if !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("different amount: want ErrConflict, got %v", err)
	}

	// A mesma chave em outro endpoint também é conflito (SPEC 11.1).
	d := key()
	if _, err := e.svc.Deposit(ctx, movements.ExternalInput{IdempotencyKey: d, AccountID: bob, Amount: 50, Currency: "BRL"}); err != nil {
		t.Fatal(err)
	}
	_, err = e.svc.Withdraw(ctx, movements.ExternalInput{IdempotencyKey: d, AccountID: bob, Amount: 50, Currency: "BRL"})
	if !errors.Is(err, idempotency.ErrConflict) {
		t.Fatalf("same body on another endpoint: want ErrConflict, got %v", err)
	}
}

// SPEC 11.3: um pedido recusado não grava a chave. Depois que o motivo da
// recusa some, o retry com a mesma chave é avaliado de novo e pode passar.
func TestFailedRequestDoesNotConsumeKey(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 2*time.Second)
	alice, bob := e.user(), e.user()
	in := movements.TransferInput{IdempotencyKey: key(), FromAccountID: alice, ToAccountID: bob, Amount: 400, Currency: "BRL"}

	if _, err := e.svc.Transfer(ctx, in); !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}
	e.deposit(alice, 400)
	r, err := e.svc.Transfer(ctx, in)
	if err != nil {
		t.Fatalf("retry after the deposit: %v", err)
	}
	if r.Replayed {
		t.Fatal("the retry is a new posting, not a replay")
	}
}

// Um lock preso vira ErrLockTimeout (503 na API), sem gravar nada e sem
// consumir a chave.
func TestLockTimeoutIsTransient(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t, 200*time.Millisecond)
	alice, bob := e.user(), e.user()
	e.deposit(alice, 1_000)

	holder, err := e.db.Superuser(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, uuid.MustParse(bob)); err != nil {
		t.Fatal(err)
	}
	in := movements.TransferInput{IdempotencyKey: key(), FromAccountID: alice, ToAccountID: bob, Amount: 10, Currency: "BRL"}
	_, err = e.svc.Transfer(ctx, in)
	_ = holder.Rollback(ctx)
	if !errors.Is(err, postgres.ErrLockTimeout) {
		t.Fatalf("want ErrLockTimeout, got %v", err)
	}

	if _, err := e.svc.Transfer(ctx, in); err != nil {
		t.Fatalf("retry with the same key after the lock was released: %v", err)
	}
}

// C6 (SPEC 21.2): fechar uma conta enquanto transferências para ela estão em
// andamento. Ou o fechamento falha por saldo, ou as transferências falham por
// conta inativa; nunca sobra uma conta CLOSED com saldo.
func TestC6CloseRacesWithIncomingTransfers(t *testing.T) {
	// Cada rodada é um subteste: o banco e o pool dela são liberados ao fim da
	// rodada, sem acumular conexões (SPEC 21.0).
	for round := range 10 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) { c6Round(t, round) })
	}
}

func c6Round(t *testing.T, round int) {
	ctx := context.Background()
	e := newEnv(t, 2*time.Second, testdb.WithMaxConns(25))
	source, target := e.user(), e.user()
	e.deposit(source, 10_000)
	targetID := uuid.MustParse(target)

	const transfers = 20
	start := make(chan struct{})
	var wg sync.WaitGroup
	transferErrs := make([]error, transfers)
	var closeErr error
	for i := range transfers {
		wg.Go(func() {
			<-start
			_, transferErrs[i] = e.svc.Transfer(ctx, movements.TransferInput{
				IdempotencyKey: key(), FromAccountID: source, ToAccountID: target, Amount: 100, Currency: "BRL",
			})
		})
	}
	wg.Go(func() {
		<-start
		_, closeErr = e.accounts.ChangeStatus(ctx, targetID, "CLOSED")
	})
	close(start)
	wg.Wait()

	var ok int
	for i, err := range transferErrs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, account.ErrNotActive):
		default:
			t.Fatalf("round %d, transfer %d: unexpected error %v", round, i, err)
		}
	}

	final, err := e.store.GetAccount(ctx, targetID)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status == account.StatusClosed && final.Balance != 0 {
		t.Fatalf("round %d: CLOSED account holds %d", round, final.Balance)
	}
	switch {
	case closeErr == nil:
		// Fechou primeiro: nenhuma transferência pode ter passado.
		if ok != 0 || final.Balance != 0 {
			t.Fatalf("round %d: closed, yet %d transfers succeeded (balance %d)", round, ok, final.Balance)
		}
	case errors.Is(closeErr, account.ErrHasBalance):
		if ok == 0 || final.Status != account.StatusActive || final.Balance != int64(ok)*100 {
			t.Fatalf("round %d: close refused but ok=%d status=%s balance=%d", round, ok, final.Status, final.Balance)
		}
	default:
		t.Fatalf("round %d: unexpected close error %v", round, closeErr)
	}
}
