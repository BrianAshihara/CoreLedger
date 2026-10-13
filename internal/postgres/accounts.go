package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres/sqlc"
)

// InsertAccount grava uma conta nova e devolve a linha como ficou no banco
// (com created_at e os valores padrão preenchidos).
func (r repo) InsertAccount(ctx context.Context, a account.Account) (account.Account, error) {
	row, err := r.q.InsertAccount(ctx, sqlc.InsertAccountParams{
		ID:         a.ID,
		Kind:       string(a.Kind),
		OwnerID:    optionalText(a.OwnerID),
		SystemCode: optionalText(a.SystemCode),
		Currency:   string(a.Currency),
	})
	if err != nil {
		return account.Account{}, fmt.Errorf("insert account: %w", err)
	}
	return toAccount(row), nil
}

// GetAccount devolve account.ErrNotFound se a conta não existir.
func (r repo) GetAccount(ctx context.Context, id uuid.UUID) (account.Account, error) {
	row, err := r.q.GetAccount(ctx, id)
	return accountResult(row, err)
}

// GetSystemAccount devolve a conta de sistema de um código e moeda, como a
// WORLD de BRL.
func (r repo) GetSystemAccount(ctx context.Context, code string, c money.Currency) (account.Account, error) {
	row, err := r.q.GetSystemAccount(ctx, sqlc.GetSystemAccountParams{
		SystemCode: optionalText(code),
		Currency:   string(c),
	})
	return accountResult(row, err)
}

// GetAccountForUpdate lê a conta e trava a linha até o fim da transação.
// Existe só em Tx: fora de transação o lock seria liberado na hora e não
// protegeria nada, então o compilador impede esse uso.
func (t *Tx) GetAccountForUpdate(ctx context.Context, id uuid.UUID) (account.Account, error) {
	row, err := t.q.GetAccountForUpdate(ctx, id)
	return accountResult(row, err)
}

// UpdateAccountStatus existe só em Tx, porque toda mudança de status precisa
// do lock tomado antes por GetAccountForUpdate.
func (t *Tx) UpdateAccountStatus(ctx context.Context, id uuid.UUID, status account.Status) (account.Account, error) {
	row, err := t.q.UpdateAccountStatus(ctx, sqlc.UpdateAccountStatusParams{ID: id, Status: string(status)})
	return accountResult(row, err)
}

// CurrencyExists informa se a moeda está semeada na tabela currencies.
func (r repo) CurrencyExists(ctx context.Context, c money.Currency) (bool, error) {
	ok, err := r.q.CurrencyExists(ctx, string(c))
	if err != nil {
		return false, fmt.Errorf("check currency: %w", err)
	}
	return ok, nil
}

func accountResult(row sqlc.Account, err error) (account.Account, error) {
	if errors.Is(err, pgx.ErrNoRows) {
		return account.Account{}, account.ErrNotFound
	}
	if err != nil {
		return account.Account{}, fmt.Errorf("query account: %w", err)
	}
	return toAccount(row), nil
}

func toAccount(row sqlc.Account) account.Account {
	return account.Account{
		ID:           row.ID,
		Kind:         account.Kind(row.Kind),
		OwnerID:      row.OwnerID.String,
		SystemCode:   row.SystemCode.String,
		Currency:     money.Currency(row.Currency),
		Status:       account.Status(row.Status),
		Balance:      row.Balance,
		LastEntrySeq: row.LastEntrySeq,
		CreatedAt:    row.CreatedAt,
		UpdatedAt:    row.UpdatedAt,
	}
}

// optionalText converte "" em NULL, para os campos que são nulos em um dos
// tipos de conta (owner_id em SYSTEM, system_code em USER).
func optionalText(s string) pgtype.Text {
	return pgtype.Text{String: s, Valid: s != ""}
}
