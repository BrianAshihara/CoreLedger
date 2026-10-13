package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/brianashihara/coreledger/internal/postgres/sqlc"
)

// IdempotencyRecord é uma chave já usada e o pedido que a usou.
type IdempotencyRecord struct {
	Key           string
	Method        string
	Path          string
	Fingerprint   [32]byte
	TransactionID uuid.UUID
}

// ClaimIdempotencyKey tenta reservar a chave para transactionID, dentro da
// transação atual (SPEC 11.2).
//
// Se a chave estiver livre, a reserva é feita e claimed é true: ela só passa
// a valer se esta transação confirmar. Se outra transação já tiver reservado
// a chave e ainda estiver em andamento, o INSERT espera por ela (limitado pelo
// lock_timeout). Se a chave já estava em uso, claimed é false e existing traz
// o registro que a usou.
func (t *Tx) ClaimIdempotencyKey(ctx context.Context, r IdempotencyRecord) (claimed bool, existing IdempotencyRecord, err error) {
	_, err = t.q.ClaimIdempotencyKey(ctx, sqlc.ClaimIdempotencyKeyParams{
		Key:                r.Key,
		RequestMethod:      r.Method,
		RequestPath:        r.Path,
		RequestFingerprint: r.Fingerprint[:],
		TransactionID:      r.TransactionID,
	})
	if err == nil {
		return true, IdempotencyRecord{}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return false, IdempotencyRecord{}, fmt.Errorf("claim idempotency key: %w", LockError(err))
	}

	// ON CONFLICT DO NOTHING não devolveu linha: a chave já existe. Em READ
	// COMMITTED este SELECT enxerga o registro confirmado por quem a usou.
	row, err := t.q.GetIdempotencyKey(ctx, r.Key)
	if err != nil {
		return false, IdempotencyRecord{}, fmt.Errorf("read idempotency key: %w", err)
	}
	existing = IdempotencyRecord{
		Key:           row.Key,
		Method:        row.RequestMethod,
		Path:          row.RequestPath,
		TransactionID: row.TransactionID,
	}
	if copy(existing.Fingerprint[:], row.RequestFingerprint) != len(existing.Fingerprint) {
		return false, IdempotencyRecord{}, fmt.Errorf("idempotency key %q has a malformed fingerprint", r.Key)
	}
	return false, existing, nil
}
