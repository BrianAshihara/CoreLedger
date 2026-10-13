package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres/sqlc"
)

// ErrLockTimeout indica que um lock de linha não foi obtido dentro do
// lock_timeout. É transitório: a operação pode ser tentada de novo (na API,
// vira 503 SERVICE_BUSY).
var ErrLockTimeout = errors.New("timed out waiting for a database lock")

const lockNotAvailable = "55P03"

// SetLockTimeout limita, só nesta transação, quanto tempo cada lock pode ser
// esperado (SPEC 9.3). Um lock preso vira um erro limpo, não uma espera
// infinita.
func (t *Tx) SetLockTimeout(ctx context.Context, d time.Duration) error {
	_, err := t.db.Exec(ctx, `SELECT set_config('lock_timeout', $1, true)`, fmt.Sprintf("%dms", d.Milliseconds()))
	if err != nil {
		return fmt.Errorf("set lock_timeout: %w", err)
	}
	return nil
}

// LockError traduz o erro de lock_timeout do PostgreSQL para ErrLockTimeout.
func LockError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == lockNotAvailable {
		return fmt.Errorf("%w: %w", ErrLockTimeout, err)
	}
	return err
}

// GetLedgerTransaction lê uma transação lançada com todas as pernas. Devolve
// ledger.ErrTransactionNotFound se ela não existir.
func (r repo) GetLedgerTransaction(ctx context.Context, id uuid.UUID) (ledger.PostedTransaction, error) {
	txn, err := r.q.GetLedgerTransaction(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ledger.PostedTransaction{}, ledger.ErrTransactionNotFound
	}
	if err != nil {
		return ledger.PostedTransaction{}, fmt.Errorf("query ledger transaction: %w", err)
	}
	rows, err := r.q.ListLedgerTransactionEntries(ctx, id)
	if err != nil {
		return ledger.PostedTransaction{}, fmt.Errorf("query ledger entries: %w", err)
	}

	posted := ledger.PostedTransaction{
		ID:        txn.ID,
		Type:      ledger.TransactionType(txn.Type),
		Currency:  money.Currency(txn.Currency),
		CreatedAt: txn.CreatedAt,
		Entries:   make([]ledger.PostedEntry, 0, len(rows)),
	}
	for _, e := range rows {
		posted.Entries = append(posted.Entries, ledger.PostedEntry{
			ID:           e.ID,
			AccountID:    e.AccountID,
			SystemCode:   e.SystemCode.String,
			Amount:       e.Amount,
			AccountSeq:   e.AccountSeq,
			BalanceAfter: e.BalanceAfter,
			CreatedAt:    e.CreatedAt,
		})
	}
	return posted, nil
}

// ListAccountEntries devolve até limit pernas da conta com sequência menor que
// beforeSeq, da mais nova para a mais antiga.
func (r repo) ListAccountEntries(ctx context.Context, accountID uuid.UUID, beforeSeq int64, limit int32) ([]ledger.HistoryEntry, error) {
	rows, err := r.q.ListAccountEntries(ctx, sqlc.ListAccountEntriesParams{
		AccountID: accountID, BeforeSeq: beforeSeq, RowLimit: limit,
	})
	if err != nil {
		return nil, fmt.Errorf("query account entries: %w", err)
	}
	out := make([]ledger.HistoryEntry, 0, len(rows))
	for _, e := range rows {
		out = append(out, ledger.HistoryEntry{
			ID:            e.ID,
			TransactionID: e.TransactionID,
			Type:          ledger.TransactionType(e.Type),
			Amount:        e.Amount,
			BalanceAfter:  e.BalanceAfter,
			AccountSeq:    e.AccountSeq,
			CreatedAt:     e.CreatedAt,
		})
	}
	return out, nil
}

func (t *Tx) InsertLedgerTransaction(ctx context.Context, id uuid.UUID, typ ledger.TransactionType, c money.Currency) (time.Time, error) {
	createdAt, err := t.q.InsertLedgerTransaction(ctx, sqlc.InsertLedgerTransactionParams{
		ID: id, Type: string(typ), Currency: string(c),
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("insert ledger transaction: %w", err)
	}
	return createdAt, nil
}

type NewEntry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	AccountID     uuid.UUID
	Currency      money.Currency
	Amount        int64
	AccountSeq    int64
	BalanceAfter  int64
}

func (t *Tx) InsertLedgerEntry(ctx context.Context, e NewEntry) (time.Time, error) {
	createdAt, err := t.q.InsertLedgerEntry(ctx, sqlc.InsertLedgerEntryParams{
		ID:            e.ID,
		TransactionID: e.TransactionID,
		AccountID:     e.AccountID,
		Currency:      string(e.Currency),
		Amount:        e.Amount,
		AccountSeq:    e.AccountSeq,
		BalanceAfter:  e.BalanceAfter,
	})
	if err != nil {
		return time.Time{}, fmt.Errorf("insert ledger entry: %w", err)
	}
	return createdAt, nil
}

// ErrConcurrentWrite indica que a conta mudou entre o lock e a escrita, o que
// só acontece se alguém escrever nela sem o lock. Nunca deve ocorrer.
var ErrConcurrentWrite = errors.New("account changed without holding its lock")

// ApplyAccountPosting grava o novo saldo e a nova sequência da conta, desde
// que a sequência atual ainda seja expectedSeq.
func (t *Tx) ApplyAccountPosting(ctx context.Context, id uuid.UUID, balance, expectedSeq, newSeq int64) error {
	n, err := t.q.ApplyAccountPosting(ctx, sqlc.ApplyAccountPostingParams{
		ID: id, Balance: balance, ExpectedSeq: expectedSeq, NewSeq: newSeq,
	})
	if err != nil {
		return fmt.Errorf("apply posting to account: %w", err)
	}
	if n != 1 {
		return fmt.Errorf("%w: account %s", ErrConcurrentWrite, id)
	}
	return nil
}
