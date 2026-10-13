package posting

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/postgres"
)

func TestDepositAndTransfer(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()

	e.deposit(alice, 100_000)
	if got := e.balance(alice); got != 100_000 {
		t.Fatalf("alice = %d after deposit", got)
	}
	// A WORLD fica negativa: o valor absoluto é o dinheiro que entrou no sistema.
	if got := e.balance(e.world); got != -100_000 {
		t.Fatalf("WORLD = %d after deposit", got)
	}

	posted, err := e.transfer(alice, bob, 20_000)
	if err != nil {
		t.Fatal(err)
	}
	if e.balance(alice) != 80_000 || e.balance(bob) != 20_000 {
		t.Fatalf("alice = %d, bob = %d", e.balance(alice), e.balance(bob))
	}

	// Cada perna carrega a sequência e o saldo resultante da conta.
	if posted.Type != ledger.TypeTransfer || posted.Currency != "BRL" || len(posted.Entries) != 2 || posted.CreatedAt.IsZero() {
		t.Fatalf("unexpected posted transaction %+v", posted)
	}
	byAccount := map[uuid.UUID]ledger.PostedEntry{}
	for _, entry := range posted.Entries {
		byAccount[entry.AccountID] = entry
	}
	if a := byAccount[alice]; a.Amount != -20_000 || a.AccountSeq != 2 || a.BalanceAfter != 80_000 {
		t.Fatalf("alice entry %+v", a)
	}
	if b := byAccount[bob]; b.Amount != 20_000 || b.AccountSeq != 1 || b.BalanceAfter != 20_000 {
		t.Fatalf("bob entry %+v", b)
	}
	if e.account(alice).LastEntrySeq != 2 || e.account(bob).LastEntrySeq != 1 {
		t.Fatal("last_entry_seq not updated")
	}
}

func TestMultiLegPosting(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	customer, merchant, platform := e.newUser(), e.newUser(), e.newUser()
	e.deposit(customer, 10_000)

	// Pagamento com taxa (SPEC 34): três pernas, soma zero.
	_, err := e.post(ledger.TypeTransfer,
		ledger.Leg{AccountID: customer, Amount: -10_000},
		ledger.Leg{AccountID: merchant, Amount: 9_700},
		ledger.Leg{AccountID: platform, Amount: 300},
	)
	if err != nil {
		t.Fatal(err)
	}
	if e.balance(customer) != 0 || e.balance(merchant) != 9_700 || e.balance(platform) != 300 {
		t.Fatalf("balances %d/%d/%d", e.balance(customer), e.balance(merchant), e.balance(platform))
	}
}

// I3 + I8: saldo insuficiente é recusado e não deixa nada gravado.
func TestInsufficientFundsLeavesNoState(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 100)
	txnsBefore, entriesBefore := e.ledgerSize()

	_, err := e.transfer(alice, bob, 101)
	if !errors.Is(err, ledger.ErrInsufficientFunds) {
		t.Fatalf("want ErrInsufficientFunds, got %v", err)
	}

	if txns, entries := e.ledgerSize(); txns != txnsBefore || entries != entriesBefore {
		t.Fatalf("ledger grew after a rejected posting: %d/%d -> %d/%d", txnsBefore, entriesBefore, txns, entries)
	}
	a, b := e.account(alice), e.account(bob)
	if a.Balance != 100 || a.LastEntrySeq != 1 || b.Balance != 0 || b.LastEntrySeq != 0 {
		t.Fatalf("accounts changed: alice %+v, bob %+v", a, b)
	}

	// Gastar exatamente o saldo é permitido: o limite é zero, não um.
	if _, err := e.transfer(alice, bob, 100); err != nil {
		t.Fatalf("spending the exact balance: %v", err)
	}
}

// I8: uma falha depois de a primeira perna ser gravada desfaz tudo.
func TestFailureMidPostingLeavesNoState(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 1_000)
	txnsBefore, entriesBefore := e.ledgerSize()

	injected := errors.New("injected failure after the first entry")
	e.engine.afterEntry = func(entry int) error {
		if entry == 0 {
			return injected
		}
		return nil
	}
	_, err := e.transfer(alice, bob, 400)
	e.engine.afterEntry = nil
	if !errors.Is(err, injected) {
		t.Fatalf("want the injected failure, got %v", err)
	}

	if txns, entries := e.ledgerSize(); txns != txnsBefore || entries != entriesBefore {
		t.Fatalf("partial state persisted: %d/%d -> %d/%d", txnsBefore, entriesBefore, txns, entries)
	}
	if e.balance(alice) != 1_000 || e.balance(bob) != 0 {
		t.Fatalf("balances changed: alice %d, bob %d", e.balance(alice), e.balance(bob))
	}
}

func TestPostingRejectsInvalidAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 1_000)

	if _, err := e.transfer(alice, uuid.Must(uuid.NewV7()), 10); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("unknown destination: want ErrNotFound, got %v", err)
	}

	// Só contas ACTIVE movem dinheiro, nos dois lados (SPEC 7.1).
	e.setStatus(bob, "SUSPENDED")
	if _, err := e.transfer(alice, bob, 10); !errors.Is(err, account.ErrNotActive) {
		t.Errorf("suspended destination: want ErrNotActive, got %v", err)
	}
	if _, err := e.transfer(bob, alice, 10); !errors.Is(err, account.ErrNotActive) {
		t.Errorf("suspended source: want ErrNotActive, got %v", err)
	}
	e.setStatus(bob, "CLOSED")
	if _, err := e.transfer(alice, bob, 10); !errors.Is(err, account.ErrNotActive) {
		t.Errorf("closed destination: want ErrNotActive, got %v", err)
	}

	// Moeda da conta diferente da moeda da transação.
	if _, err := e.db.Superuser(t).Exec(ctx, `INSERT INTO currencies (code, exponent) VALUES ('USD', 2)`); err != nil {
		t.Fatal(err)
	}
	usd, err := e.accounts.Create(ctx, accounts.CreateInput{OwnerID: "carol", Currency: "USD"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.transfer(alice, usd.ID, 10); !errors.Is(err, ledger.ErrCurrencyMismatch) {
		t.Errorf("USD account in a BRL transaction: want ErrCurrencyMismatch, got %v", err)
	}

	if e.balance(alice) != 1_000 {
		t.Fatalf("alice = %d after only rejected postings", e.balance(alice))
	}
}

func TestInvalidPostingNeverReachesTheDatabase(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	alice := e.newUser()

	_, err := e.post(ledger.TypeTransfer, ledger.Leg{AccountID: alice, Amount: -5}, ledger.Leg{AccountID: e.world, Amount: 4})
	if !errors.Is(err, ledger.ErrInvalidPosting) {
		t.Fatalf("want ErrInvalidPosting, got %v", err)
	}
	if txns, entries := e.ledgerSize(); txns != 0 || entries != 0 {
		t.Fatalf("ledger has %d/%d rows", txns, entries)
	}
}

// SPEC 9.3: um lock preso vira erro limpo dentro do lock_timeout, em vez de
// uma espera sem fim.
func TestLockTimeout(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 1_000)

	holder, err := e.db.Superuser(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, bob); err != nil {
		t.Fatal(err)
	}

	fast := New(200 * time.Millisecond)
	start := time.Now()
	err = e.store.WithTx(ctx, func(tx *postgres.Tx) error {
		_, err := fast.Post(ctx, tx, ledger.Posting{
			TransactionID: uuid.Must(uuid.NewV7()),
			Type:          ledger.TypeTransfer,
			Currency:      "BRL",
			Legs:          []ledger.Leg{{AccountID: alice, Amount: -10}, {AccountID: bob, Amount: 10}},
		})
		return err
	})
	if !errors.Is(err, postgres.ErrLockTimeout) {
		t.Fatalf("want ErrLockTimeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("took %s; the lock timeout was 200ms", elapsed)
	}
	_ = holder.Rollback(ctx)

	if e.balance(alice) != 1_000 || e.balance(bob) != 0 {
		t.Fatal("a timed-out posting changed balances")
	}
}

// I6: depois de muitos lançamentos, as sequências são 1..n e cada
// balance_after é o anterior mais o valor.
func TestSequenceAndBalanceAfterChain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 1_000)
	for i := range 20 {
		from, to := alice, bob
		if i%3 == 2 {
			from, to = bob, alice
		}
		if _, err := e.transfer(from, to, 10); err != nil {
			t.Fatal(err)
		}
	}

	rows, err := e.db.Superuser(t).Query(ctx,
		`SELECT account_seq, amount, balance_after FROM ledger_entries WHERE account_id = $1 ORDER BY account_seq`, alice)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var want, running int64 = 1, 0
	for rows.Next() {
		var seq, amount, after int64
		if err := rows.Scan(&seq, &amount, &after); err != nil {
			t.Fatal(err)
		}
		running += amount
		if seq != want || after != running {
			t.Fatalf("entry %d: seq=%d balance_after=%d, want seq=%d balance_after=%d", want, seq, after, want, running)
		}
		want++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if running != e.balance(alice) || want-1 != e.account(alice).LastEntrySeq {
		t.Fatalf("chain ends at %d/seq %d, account has %d/seq %d", running, want-1, e.balance(alice), e.account(alice).LastEntrySeq)
	}
}
