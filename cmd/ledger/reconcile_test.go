package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

func runReconcile(t *testing.T, d *testdb.DB) (map[string]any, error) {
	t.Helper()
	env := map[string]string{"DATABASE_URL": d.SuperuserURL()}
	var out bytes.Buffer
	err := run(context.Background(), []string{"reconcile"}, func(k string) string { return env[k] }, &out, io.Discard)
	var report map[string]any
	if jsonErr := json.Unmarshal(out.Bytes(), &report); jsonErr != nil && err == nil {
		t.Fatalf("stdout is not a JSON report: %q", out.String())
	}
	return report, err
}

// O CLI imprime o mesmo relatório do endpoint e sai com código 1 (via
// errInconsistent) quando há inconsistência, para cron e CI perceberem.
func TestReconcileCommand(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	super := d.Superuser(t)

	var world, alice uuid.UUID
	if err := super.QueryRow(ctx, `SELECT id FROM accounts WHERE system_code = 'WORLD'`).Scan(&world); err != nil {
		t.Fatal(err)
	}
	alice = uuid.Must(uuid.NewV7())
	if _, err := super.Exec(ctx, `INSERT INTO accounts (id, kind, owner_id, currency) VALUES ($1, 'USER', 'alice', 'BRL')`, alice); err != nil {
		t.Fatal(err)
	}
	store := postgres.NewStore(d.Pool)
	if err := store.WithTx(ctx, func(tx *postgres.Tx) error {
		_, err := posting.New(time.Second).Post(ctx, tx, ledger.Posting{
			TransactionID: uuid.Must(uuid.NewV7()), Type: ledger.TypeDeposit, Currency: "BRL",
			Legs: []ledger.Leg{{AccountID: world, Amount: -500}, {AccountID: alice, Amount: 500}},
		})
		return err
	}); err != nil {
		t.Fatal(err)
	}

	report, err := runReconcile(t, d)
	if err != nil || report["status"] != "OK" || report["checked_transactions"] != float64(1) {
		t.Fatalf("healthy ledger: err=%v report=%v", err, report)
	}

	if _, err := super.Exec(ctx, `UPDATE accounts SET balance = 0 WHERE id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	report, err = runReconcile(t, d)
	if !errors.Is(err, errInconsistent) || report["status"] != "INCONSISTENT" || len(report["issues"].([]any)) == 0 {
		t.Fatalf("corrupted ledger: err=%v report=%v", err, report)
	}
}

func TestReconcileRefusesUnmigratedDatabase(t *testing.T) {
	t.Parallel()
	d := testdb.NewEmpty(t)
	env := map[string]string{"DATABASE_URL": d.SuperuserURL()}
	err := run(context.Background(), []string{"reconcile"}, func(k string) string { return env[k] }, io.Discard, io.Discard)
	if !errors.Is(err, postgres.ErrSchemaMissing) {
		t.Fatalf("want ErrSchemaMissing, got %v", err)
	}
}
