package postgres_test

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brianashihara/coreledger/internal/testdb"
)

// Testes do ledger direto no banco, sem o motor em Go (SPEC 21.3). Cada um
// tenta gravar um estado proibido e exige que o PostgreSQL recuse.

const restrictViolation = "23001"

type ledgerFixture struct {
	db          *testdb.DB
	super       *pgxpool.Pool
	alice, bob  uuid.UUID
	usdAccount  uuid.UUID
	existingTxn uuid.UUID
}

// newLedgerFixture cria duas contas BRL, uma conta USD e uma transação
// válida já gravada (alice -100, bob +100), tudo como superusuário.
func newLedgerFixture(t *testing.T) *ledgerFixture {
	t.Helper()
	ctx := context.Background()
	d := testdb.New(t)
	f := &ledgerFixture{db: d, super: d.Superuser(t)}

	var err error
	if f.alice, err = insertUserAccount(ctx, f.super, "alice"); err != nil {
		t.Fatal(err)
	}
	if f.bob, err = insertUserAccount(ctx, f.super, "bob"); err != nil {
		t.Fatal(err)
	}
	f.usdAccount = uuid.Must(uuid.NewV7())
	if _, err := f.super.Exec(ctx, `
		INSERT INTO currencies (code, exponent) VALUES ('USD', 2);
		INSERT INTO accounts (id, kind, owner_id, currency) VALUES ('`+f.usdAccount.String()+`', 'USER', 'carol', 'USD')`,
	); err != nil {
		t.Fatal(err)
	}

	f.existingTxn = uuid.Must(uuid.NewV7())
	err = f.inTx(t, f.super, func(tx pgx.Tx) error {
		if err := insertTxn(ctx, tx, f.existingTxn, "BRL"); err != nil {
			return err
		}
		if err := insertEntry(ctx, tx, f.existingTxn, f.alice, "BRL", -100, 1); err != nil {
			return err
		}
		return insertEntry(ctx, tx, f.existingTxn, f.bob, "BRL", 100, 1)
	})
	if err != nil {
		t.Fatalf("a balanced transaction must be accepted: %v", err)
	}
	return f
}

func (f *ledgerFixture) inTx(t *testing.T, db *pgxpool.Pool, fn func(pgx.Tx) error) error {
	t.Helper()
	ctx := context.Background()
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback(ctx)
		return err
	}
	return tx.Commit(ctx) // os triggers adiados (I1) rodam aqui
}

func insertTxn(ctx context.Context, tx pgx.Tx, id uuid.UUID, currency string) error {
	_, err := tx.Exec(ctx, `INSERT INTO ledger_transactions (id, type, currency) VALUES ($1, 'TRANSFER', $2)`, id, currency)
	return err
}

func insertEntry(ctx context.Context, tx pgx.Tx, txn, account uuid.UUID, currency string, amount, seq int64) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO ledger_entries (id, transaction_id, account_id, currency, amount, account_seq, balance_after)
		VALUES ($1, $2, $3, $4, $5, $6, $5)`,
		uuid.Must(uuid.NewV7()), txn, account, currency, amount, seq)
	return err
}

// I1: transações desbalanceadas, com uma perna só ou sem pernas são
// recusadas no COMMIT pelo constraint trigger adiado.
func TestDatabaseRejectsUnbalancedTransactions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)

	for _, tc := range []struct {
		name string
		legs []int64
	}{
		{"sum is not zero", []int64{-100, 99}},
		{"single entry", []int64{-100}},
		{"no entries", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := f.inTx(t, f.super, func(tx pgx.Tx) error {
				txn := uuid.Must(uuid.NewV7())
				if err := insertTxn(ctx, tx, txn, "BRL"); err != nil {
					return err
				}
				accounts := []uuid.UUID{f.alice, f.bob}
				for i, amount := range tc.legs {
					if err := insertEntry(ctx, tx, txn, accounts[i], "BRL", amount, 2); err != nil {
						return err
					}
				}
				return nil
			})
			expectViolation(t, err, checkViolation, "")
		})
	}
}

// O trigger adiado vale também para o papel da aplicação: não há como
// confirmar uma transação desbalanceada mesmo sem passar pelo motor.
func TestAppRoleCannotCommitUnbalancedTransaction(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)

	err := f.inTx(t, f.db.Pool, func(tx pgx.Tx) error {
		txn := uuid.Must(uuid.NewV7())
		if err := insertTxn(ctx, tx, txn, "BRL"); err != nil {
			return err
		}
		return insertEntry(ctx, tx, txn, f.alice, "BRL", 500, 2)
	})
	expectViolation(t, err, checkViolation, "")
}

// I2, I6 e I10 em cada perna.
func TestDatabaseRejectsInvalidEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)

	// Cada caso viola exatamente uma regra, para que a constraint esperada
	// seja a única candidata. A perna vai numa transação nova (moeda BRL),
	// exceto quando o caso precisa da transação que já existe.
	for _, tc := range []struct {
		name             string
		existingTxn      bool
		account          uuid.UUID
		currency         string
		amount, seq      int64
		code, constraint string
	}{
		{"zero amount", false, f.alice, "BRL", 0, 2, checkViolation, "ledger_entries_amount_valid"},
		{"above MAX_AMOUNT", false, f.alice, "BRL", 1_000_000_000_001, 2, checkViolation, "ledger_entries_amount_valid"},
		{"below -MAX_AMOUNT", false, f.alice, "BRL", -1_000_000_000_001, 2, checkViolation, "ledger_entries_amount_valid"},
		{"zero account_seq", false, f.alice, "BRL", 5, 0, checkViolation, "ledger_entries_account_seq_check"},
		{"duplicate account_seq", false, f.alice, "BRL", 5, 1, uniqueViolation, "ledger_entries_account_seq_key"},
		{"entry currency differs from account", false, f.usdAccount, "BRL", 5, 1, foreignKeyViolation, "ledger_entries_account_fkey"},
		{"entry currency differs from transaction", false, f.usdAccount, "USD", 5, 1, foreignKeyViolation, "ledger_entries_transaction_fkey"},
		{"second leg for the same account", true, f.bob, "BRL", 5, 2, uniqueViolation, "ledger_entries_transaction_account_key"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A constraint dispara no INSERT da perna, antes do trigger adiado.
			err := f.inTx(t, f.super, func(tx pgx.Tx) error {
				txn := f.existingTxn
				if !tc.existingTxn {
					txn = uuid.Must(uuid.NewV7())
					if err := insertTxn(ctx, tx, txn, "BRL"); err != nil {
						return err
					}
				}
				return insertEntry(ctx, tx, txn, tc.account, tc.currency, tc.amount, tc.seq)
			})
			expectViolation(t, err, tc.code, tc.constraint)
		})
	}
}

// I4: nem o superusuário altera ou apaga o ledger; a aplicação nem tem o
// privilégio.
func TestLedgerIsAppendOnly(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)

	for _, stmt := range []string{
		`UPDATE ledger_entries SET amount = 1 WHERE transaction_id = $1`,
		`DELETE FROM ledger_entries WHERE transaction_id = $1`,
		`UPDATE ledger_transactions SET type = 'DEPOSIT' WHERE id = $1`,
		`DELETE FROM ledger_transactions WHERE id = $1`,
	} {
		_, err := f.super.Exec(ctx, stmt, f.existingTxn)
		expectViolation(t, err, restrictViolation, "")

		_, err = f.db.Pool.Exec(ctx, stmt, f.existingTxn)
		expectViolation(t, err, permissionDenied, "")
	}

	for _, table := range []string{"ledger_entries", "ledger_transactions"} {
		_, err := f.super.Exec(ctx, `TRUNCATE `+table+` CASCADE`)
		expectViolation(t, err, restrictViolation, "")
	}

	var n int
	if err := f.super.QueryRow(ctx, `SELECT count(*) FROM ledger_entries`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("ledger_entries has %d rows after the rejected mutations, want 2", n)
	}
}
