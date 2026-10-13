package postgres_test

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func insertKey(ctx context.Context, tx pgx.Tx, key string, txn uuid.UUID) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO idempotency_keys (key, request_method, request_path, request_fingerprint, transaction_id)
		VALUES ($1, 'POST', '/v1/transfers', $2, $3)`, key, make([]byte, 32), txn)
	return err
}

// A FK para a transação é adiada (a chave é reservada antes de a transação
// existir), mas no COMMIT ela precisa apontar para uma transação real.
func TestIdempotencyKeyMustReferenceATransactionAtCommit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)

	err := f.inTx(t, f.super, func(tx pgx.Tx) error {
		return insertKey(ctx, tx, "orphan-key", uuid.Must(uuid.NewV7()))
	})
	expectViolation(t, err, foreignKeyViolation, "idempotency_keys_transaction_id_fkey")

	if err := f.inTx(t, f.super, func(tx pgx.Tx) error {
		return insertKey(ctx, tx, "good-key", f.existingTxn)
	}); err != nil {
		t.Fatalf("a key pointing to an existing transaction must be accepted: %v", err)
	}
}

func TestIdempotencyKeyFormatAndUniqueness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)
	if err := f.inTx(t, f.super, func(tx pgx.Tx) error { return insertKey(ctx, tx, "k1", f.existingTxn) }); err != nil {
		t.Fatal(err)
	}

	// O CHECK de formato roda antes dos índices únicos, então estes casos só
	// podem violar o formato.
	for name, key := range map[string]string{
		"empty":        "",
		"256 chars":    strings.Repeat("k", 256),
		"control char": "k\t1",
		"non-ASCII":    "chave-ação",
	} {
		t.Run(name, func(t *testing.T) {
			err := f.inTx(t, f.super, func(tx pgx.Tx) error { return insertKey(ctx, tx, key, f.existingTxn) })
			expectViolation(t, err, checkViolation, "idempotency_keys_key_format")
		})
	}

	// Chave repetida, apontando para outra transação, para violar só a PK.
	other := uuid.Must(uuid.NewV7())
	if err := f.inTx(t, f.super, func(tx pgx.Tx) error {
		if err := insertTxn(ctx, tx, other, "BRL"); err != nil {
			return err
		}
		if err := insertEntry(ctx, tx, other, f.alice, "BRL", -1, 2); err != nil {
			return err
		}
		return insertEntry(ctx, tx, other, f.bob, "BRL", 1, 2)
	}); err != nil {
		t.Fatal(err)
	}
	err := f.inTx(t, f.super, func(tx pgx.Tx) error { return insertKey(ctx, tx, "k1", other) })
	expectViolation(t, err, uniqueViolation, "idempotency_keys_pkey")

	// Uma transação do ledger tem no máximo uma chave (I7).
	err = f.inTx(t, f.super, func(tx pgx.Tx) error { return insertKey(ctx, tx, "k2", f.existingTxn) })
	expectViolation(t, err, uniqueViolation, "idempotency_keys_transaction_id_key")
}

func TestIdempotencyKeysAreImmutable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newLedgerFixture(t)
	if err := f.inTx(t, f.super, func(tx pgx.Tx) error { return insertKey(ctx, tx, "k1", f.existingTxn) }); err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		`UPDATE idempotency_keys SET request_path = '/v1/admin/deposits' WHERE key = 'k1'`,
		`DELETE FROM idempotency_keys WHERE key = 'k1'`,
	} {
		_, err := f.super.Exec(ctx, stmt)
		expectViolation(t, err, restrictViolation, "")
		_, err = f.db.Pool.Exec(ctx, stmt)
		expectViolation(t, err, permissionDenied, "")
	}
	_, err := f.super.Exec(ctx, `TRUNCATE idempotency_keys`)
	expectViolation(t, err, restrictViolation, "")
}
