// Package accounts reúne os casos de uso de contas: criar, consultar e mudar
// status (SPEC 7.1, Fase 2). Valida a entrada com as regras do domínio e abre
// as transações; não conhece HTTP.
package accounts

import (
	"context"
	"fmt"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
)

type Service struct {
	store *postgres.Store
}

func NewService(store *postgres.Store) *Service {
	return &Service{store: store}
}

type CreateInput struct {
	OwnerID  string
	Currency string
}

// Create abre uma conta USER, ACTIVE e com saldo zero. Não é idempotente
// (SPEC 11.1): um retry cria outra conta, o que é aceitável porque criar
// conta não move dinheiro.
func (s *Service) Create(ctx context.Context, in CreateInput) (account.Account, error) {
	// A ordem das validações define qual erro o cliente vê primeiro.
	if err := account.ValidateOwnerID(in.OwnerID); err != nil {
		return account.Account{}, err
	}
	currency, err := money.ParseCurrency(in.Currency)
	if err != nil {
		return account.Account{}, err
	}
	supported, err := s.store.CurrencyExists(ctx, currency)
	if err != nil {
		return account.Account{}, err
	}
	if !supported {
		return account.Account{}, money.ErrUnsupportedCurrency
	}

	id, err := uuid.NewV7()
	if err != nil {
		return account.Account{}, fmt.Errorf("generate account id: %w", err)
	}
	return s.store.InsertAccount(ctx, account.Account{
		ID:       id,
		Kind:     account.KindUser,
		OwnerID:  in.OwnerID,
		Currency: currency,
	})
}

// Get devolve uma conta USER. Contas SYSTEM se comportam como inexistentes,
// para não vazar nem a existência delas (SPEC 7.5).
func (s *Service) Get(ctx context.Context, id uuid.UUID) (account.Account, error) {
	a, err := s.store.GetAccount(ctx, id)
	if err != nil {
		return account.Account{}, err
	}
	if a.Kind != account.KindUser {
		return account.Account{}, account.ErrNotFound
	}
	return a, nil
}

// ChangeStatus aplica uma transição de status (operação de admin). Trava a
// linha da conta com o mesmo lock dos lançamentos, então a checagem de saldo
// para fechar a conta nunca corre em paralelo com um crédito (SPEC 7.1).
func (s *Service) ChangeStatus(ctx context.Context, id uuid.UUID, status string) (account.Account, error) {
	to, err := account.ParseStatus(status)
	if err != nil {
		return account.Account{}, err
	}

	var result account.Account
	err = s.store.WithTx(ctx, func(tx *postgres.Tx) error {
		a, err := tx.GetAccountForUpdate(ctx, id)
		if err != nil {
			return err
		}
		if a.Kind != account.KindUser {
			return account.ErrNotFound
		}
		if err := account.CheckTransition(a.Status, to, a.Balance); err != nil {
			return err
		}
		if a.Status == to {
			result = a // repetir o status atual não escreve nada
			return nil
		}
		result, err = tx.UpdateAccountStatus(ctx, id, to)
		return err
	})
	if err != nil {
		return account.Account{}, err
	}
	return result, nil
}
