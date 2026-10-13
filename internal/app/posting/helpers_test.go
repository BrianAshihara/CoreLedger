package posting

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// env reúne o que um teste do motor precisa: banco isolado, motor, serviço
// de contas, reconciliação e o ID da conta WORLD de BRL.
type env struct {
	t        *testing.T
	db       *testdb.DB
	store    *postgres.Store
	engine   *Engine
	accounts *accounts.Service
	recon    *reconcile.Service
	world    uuid.UUID
}

func newEnv(t *testing.T, opts ...testdb.Option) *env {
	t.Helper()
	d := testdb.New(t, opts...)
	store := postgres.NewStore(d.Pool)
	e := &env{
		t:        t,
		db:       d,
		store:    store,
		engine:   New(5 * time.Second),
		accounts: accounts.NewService(store),
		recon:    reconcile.NewService(store),
	}
	if err := d.Superuser(t).QueryRow(context.Background(),
		`SELECT id FROM accounts WHERE system_code = 'WORLD' AND currency = 'BRL'`).Scan(&e.world); err != nil {
		t.Fatal(err)
	}
	// Varredura de invariantes (SPEC 21.5): todo teste termina reconciliado.
	t.Cleanup(func() {
		if !t.Failed() {
			e.assertReconciled()
		}
	})
	return e
}

func (e *env) newUser() uuid.UUID {
	e.t.Helper()
	a, err := e.accounts.Create(context.Background(), accounts.CreateInput{OwnerID: "owner", Currency: "BRL"})
	if err != nil {
		e.t.Fatal(err)
	}
	return a.ID
}

func (e *env) post(typ ledger.TransactionType, legs ...ledger.Leg) (ledger.PostedTransaction, error) {
	var posted ledger.PostedTransaction
	err := e.store.WithTx(context.Background(), func(tx *postgres.Tx) error {
		var err error
		posted, err = e.engine.Post(context.Background(), tx, ledger.Posting{
			TransactionID: uuid.Must(uuid.NewV7()),
			Type:          typ,
			Currency:      "BRL",
			Legs:          legs,
		})
		return err
	})
	return posted, err
}

func (e *env) deposit(to uuid.UUID, amount int64) {
	e.t.Helper()
	if _, err := e.post(ledger.TypeDeposit, ledger.Leg{AccountID: e.world, Amount: -amount}, ledger.Leg{AccountID: to, Amount: amount}); err != nil {
		e.t.Fatalf("deposit: %v", err)
	}
}

func (e *env) transfer(from, to uuid.UUID, amount int64) (ledger.PostedTransaction, error) {
	return e.post(ledger.TypeTransfer, ledger.Leg{AccountID: from, Amount: -amount}, ledger.Leg{AccountID: to, Amount: amount})
}

func (e *env) account(id uuid.UUID) account.Account {
	e.t.Helper()
	a, err := e.store.GetAccount(context.Background(), id)
	if err != nil {
		e.t.Fatal(err)
	}
	return a
}

func (e *env) balance(id uuid.UUID) int64 {
	e.t.Helper()
	return e.account(id).Balance
}

// ledgerSize conta transações e pernas, para provar que uma falha não deixou
// nada gravado (I8).
func (e *env) ledgerSize() (txns, entries int64) {
	e.t.Helper()
	err := e.db.Superuser(e.t).QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM ledger_transactions), (SELECT count(*) FROM ledger_entries)`).Scan(&txns, &entries)
	if err != nil {
		e.t.Fatal(err)
	}
	return txns, entries
}

func (e *env) setStatus(id uuid.UUID, status string) {
	e.t.Helper()
	if _, err := e.accounts.ChangeStatus(context.Background(), id, status); err != nil {
		e.t.Fatal(err)
	}
}

func (e *env) assertReconciled() {
	e.t.Helper()
	r, err := e.recon.Run(context.Background())
	if err != nil {
		e.t.Fatalf("reconcile: %v", err)
	}
	if r.Status != reconcile.StatusOK {
		e.t.Fatalf("reconciliation found issues: %+v", r.Issues)
	}
}
