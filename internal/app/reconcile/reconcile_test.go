package reconcile_test

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// fixture monta um ledger saudável: WORLD -> alice 1.000, alice -> bob 300.
type fixture struct {
	recon        *reconcile.Service
	super        *pgxpool.Pool
	alice, bob   uuid.UUID
	transferTxn  uuid.UUID
	depositTxn   uuid.UUID
	accountCount int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx := context.Background()
	d := testdb.New(t)
	store := postgres.NewStore(d.Pool)
	accts := accounts.NewService(store)
	engine := posting.New(5 * time.Second)
	f := &fixture{recon: reconcile.NewService(store), super: d.Superuser(t)}

	var world uuid.UUID
	if err := f.super.QueryRow(ctx, `SELECT id FROM accounts WHERE system_code = 'WORLD'`).Scan(&world); err != nil {
		t.Fatal(err)
	}
	for _, id := range []*uuid.UUID{&f.alice, &f.bob} {
		a, err := accts.Create(ctx, accounts.CreateInput{OwnerID: "owner", Currency: "BRL"})
		if err != nil {
			t.Fatal(err)
		}
		*id = a.ID
	}
	post := func(typ ledger.TransactionType, legs ...ledger.Leg) uuid.UUID {
		id := uuid.Must(uuid.NewV7())
		err := store.WithTx(ctx, func(tx *postgres.Tx) error {
			_, err := engine.Post(ctx, tx, ledger.Posting{TransactionID: id, Type: typ, Currency: "BRL", Legs: legs})
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.depositTxn = post(ledger.TypeDeposit, ledger.Leg{AccountID: world, Amount: -1_000}, ledger.Leg{AccountID: f.alice, Amount: 1_000})
	f.transferTxn = post(ledger.TypeTransfer, ledger.Leg{AccountID: f.alice, Amount: -300}, ledger.Leg{AccountID: f.bob, Amount: 300})
	f.accountCount = 3 // WORLD, alice, bob
	return f
}

// corrupt grava estado inválido de propósito. session_replication_role =
// replica desliga os triggers (inclusive os de imutabilidade e balanceamento)
// só nesta transação. Este caminho existe apenas em teste (SPEC 13).
func (f *fixture) corrupt(t *testing.T, sql string, args ...any) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.super.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL session_replication_role = replica`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, sql, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) run(t *testing.T) reconcile.Report {
	t.Helper()
	r, err := f.recon.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func checks(r reconcile.Report) []ledger.Check {
	var out []ledger.Check
	for _, i := range r.Issues {
		if !slices.Contains(out, i.Check) {
			out = append(out, i.Check)
		}
	}
	slices.Sort(out)
	return out
}

func TestHealthyLedgerIsOK(t *testing.T) {
	t.Parallel()
	f := newFixture(t)
	r := f.run(t)
	if r.Status != reconcile.StatusOK || len(r.Issues) != 0 {
		t.Fatalf("healthy ledger reported %s: %+v", r.Status, r.Issues)
	}
	if r.CheckedAccounts != f.accountCount || r.CheckedTransactions != 2 {
		t.Fatalf("checked %d accounts / %d transactions, want %d / 2", r.CheckedAccounts, r.CheckedTransactions, f.accountCount)
	}
}

// Cada tipo de inconsistência injetada precisa ser detectada pelas checagens
// certas, e o relatório precisa apontar a conta ou transação culpada.
func TestDetectsInjectedInconsistencies(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name   string
		inject func(t *testing.T, f *fixture)
		want   []ledger.Check
		verify func(t *testing.T, f *fixture, r reconcile.Report)
	}{
		{
			name: "stored balance changed without an entry",
			inject: func(t *testing.T, f *fixture) {
				f.corrupt(t, `UPDATE accounts SET balance = balance + 50 WHERE id = $1`, f.alice)
			},
			want: []ledger.Check{ledger.CheckBalanceMatchesLedger, ledger.CheckCurrencyZeroSum, ledger.CheckLastEntryMatchesBalance},
			verify: func(t *testing.T, f *fixture, r reconcile.Report) {
				i := find(t, r, ledger.CheckBalanceMatchesLedger)
				if i.AccountID != f.alice || i.Expected != 700 || i.Actual != 750 {
					t.Fatalf("issue %+v, want alice expected 700 actual 750", i)
				}
			},
		},
		{
			name: "entry balance_after rewritten",
			inject: func(t *testing.T, f *fixture) {
				f.corrupt(t, `UPDATE ledger_entries SET balance_after = 999 WHERE account_id = $1 AND account_seq = 1`, f.alice)
			},
			want: []ledger.Check{ledger.CheckBalanceAfterChain},
			verify: func(t *testing.T, f *fixture, r reconcile.Report) {
				// A perna 1 (999 != 0 + 1000) e a perna 2 (700 != 999 - 300) quebram a corrente.
				for _, i := range r.Issues {
					if i.AccountID != f.alice {
						t.Fatalf("unexpected issue %+v", i)
					}
				}
				if len(r.Issues) != 2 {
					t.Fatalf("want 2 broken links, got %+v", r.Issues)
				}
			},
		},
		{
			name: "entry deleted",
			inject: func(t *testing.T, f *fixture) {
				f.corrupt(t, `DELETE FROM ledger_entries WHERE account_id = $1 AND transaction_id = $2`, f.bob, f.transferTxn)
			},
			want: []ledger.Check{
				ledger.CheckBalanceMatchesLedger, ledger.CheckLastEntryMatchesBalance,
				ledger.CheckSequenceGapFree, ledger.CheckTransactionBalanced,
			},
			verify: func(t *testing.T, f *fixture, r reconcile.Report) {
				if i := find(t, r, ledger.CheckTransactionBalanced); i.TransactionID != f.transferTxn || i.Actual != -300 {
					t.Fatalf("issue %+v, want transfer transaction summing to -300", i)
				}
			},
		},
		{
			name: "transaction without entries",
			inject: func(t *testing.T, f *fixture) {
				f.corrupt(t, `INSERT INTO ledger_transactions (id, type, currency) VALUES ($1, 'DEPOSIT', 'BRL')`, uuid.Must(uuid.NewV7()))
			},
			want: []ledger.Check{ledger.CheckTransactionBalanced},
		},
		{
			name: "sequence gap",
			inject: func(t *testing.T, f *fixture) {
				f.corrupt(t, `UPDATE accounts SET last_entry_seq = 5 WHERE id = $1`, f.bob)
			},
			want: []ledger.Check{ledger.CheckLastEntryMatchesBalance, ledger.CheckSequenceGapFree},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFixture(t)
			tc.inject(t, f)
			r := f.run(t)
			if r.Status != reconcile.StatusInconsistent {
				t.Fatalf("status %s, want INCONSISTENT", r.Status)
			}
			if got := checks(r); !slices.Equal(got, tc.want) {
				t.Fatalf("failed checks %v, want %v\nissues: %+v", got, tc.want, r.Issues)
			}
			if tc.verify != nil {
				tc.verify(t, f, r)
			}
		})
	}
}

func find(t *testing.T, r reconcile.Report, c ledger.Check) ledger.Issue {
	t.Helper()
	for _, i := range r.Issues {
		if i.Check == c {
			return i
		}
	}
	t.Fatalf("no %s issue in %+v", c, r.Issues)
	return ledger.Issue{}
}
