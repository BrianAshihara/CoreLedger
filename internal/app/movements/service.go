// Package movements reúne as operações que movem dinheiro (SPEC 10):
// depósito, saque e transferência. Cada uma valida a entrada, monta uma
// Posting e a lança pelo motor dentro do fluxo de idempotência.
package movements

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/idempotency"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
)

// Padrões de rota que entram no fingerprint. Usar a mesma chave em outro
// endpoint é conflito, mesmo com o mesmo corpo.
const (
	PathTransfers   = "/v1/transfers"
	PathDeposits    = "/v1/admin/deposits"
	PathWithdrawals = "/v1/admin/withdrawals"

	worldCode = "WORLD"
)

type Service struct {
	store       *postgres.Store
	engine      *posting.Engine
	lockTimeout time.Duration
}

func NewService(store *postgres.Store, engine *posting.Engine, lockTimeout time.Duration) *Service {
	return &Service{store: store, engine: engine, lockTimeout: lockTimeout}
}

// Result é a transação que responde ao pedido. Replayed indica que ela veio
// de um pedido anterior com a mesma chave (SPEC 11.4).
type Result struct {
	Transaction ledger.PostedTransaction
	Replayed    bool
}

type TransferInput struct {
	IdempotencyKey string
	FromAccountID  string
	ToAccountID    string
	Amount         int64
	Currency       string
}

// Transfer move dinheiro entre duas contas USER (SPEC 10). Os passos 1 e 2 da
// ordem de validação (corpo e presença da chave) ficam com a camada HTTP;
// daqui em diante a ordem é a do spec.
func (s *Service) Transfer(ctx context.Context, in TransferInput) (Result, error) {
	// 3. valor
	if err := money.ValidateAmount(in.Amount); err != nil {
		return Result{}, err
	}
	// 4. moeda
	currency, err := s.supportedCurrency(ctx, in.Currency)
	if err != nil {
		return Result{}, err
	}
	// 5. origem diferente do destino. Um ID malformado não pode existir.
	from, err := parseAccountID(in.FromAccountID)
	if err != nil {
		return Result{}, err
	}
	to, err := parseAccountID(in.ToAccountID)
	if err != nil {
		return Result{}, err
	}
	if from == to {
		return Result{}, ledger.ErrSameAccountTransfer
	}

	canonical := struct {
		From     string `json:"from_account_id"`
		To       string `json:"to_account_id"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}{from.String(), to.String(), in.Amount, string(currency)}

	// 6 a 12 acontecem dentro da transação de banco.
	return s.run(ctx, in.IdempotencyKey, PathTransfers, canonical, func(ctx context.Context, tx *postgres.Tx, txnID uuid.UUID) error {
		if err := requireUserAccounts(ctx, tx, from, to); err != nil {
			return err
		}
		_, err := s.engine.Post(ctx, tx, ledger.Posting{
			TransactionID: txnID,
			Type:          ledger.TypeTransfer,
			Currency:      currency,
			Legs: []ledger.Leg{
				{AccountID: from, Amount: -in.Amount},
				{AccountID: to, Amount: in.Amount},
			},
		})
		return err
	})
}

type ExternalInput struct {
	IdempotencyKey string
	AccountID      string
	Amount         int64
	Currency       string
}

// Deposit traz dinheiro de fora do sistema: WORLD -X, conta +X (SPEC 7.5).
func (s *Service) Deposit(ctx context.Context, in ExternalInput) (Result, error) {
	return s.external(ctx, in, ledger.TypeDeposit, PathDeposits)
}

// Withdraw leva dinheiro para fora do sistema: conta -X, WORLD +X.
func (s *Service) Withdraw(ctx context.Context, in ExternalInput) (Result, error) {
	return s.external(ctx, in, ledger.TypeWithdrawal, PathWithdrawals)
}

func (s *Service) external(ctx context.Context, in ExternalInput, typ ledger.TransactionType, path string) (Result, error) {
	if err := money.ValidateAmount(in.Amount); err != nil {
		return Result{}, err
	}
	currency, err := s.supportedCurrency(ctx, in.Currency)
	if err != nil {
		return Result{}, err
	}
	id, err := parseAccountID(in.AccountID)
	if err != nil {
		return Result{}, err
	}

	canonical := struct {
		Account  string `json:"account_id"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
	}{id.String(), in.Amount, string(currency)}

	return s.run(ctx, in.IdempotencyKey, path, canonical, func(ctx context.Context, tx *postgres.Tx, txnID uuid.UUID) error {
		if err := requireUserAccounts(ctx, tx, id); err != nil {
			return err
		}
		world, err := tx.GetSystemAccount(ctx, worldCode, currency)
		if err != nil {
			return fmt.Errorf("find %s account for %s: %w", worldCode, currency, err)
		}
		userAmount := in.Amount
		if typ == ledger.TypeWithdrawal {
			userAmount = -in.Amount
		}
		_, err = s.engine.Post(ctx, tx, ledger.Posting{
			TransactionID: txnID,
			Type:          typ,
			Currency:      currency,
			Legs: []ledger.Leg{
				{AccountID: id, Amount: userAmount},
				{AccountID: world.ID, Amount: -userAmount},
			},
		})
		return err
	})
}

// Transaction devolve uma transação lançada com todas as pernas.
func (s *Service) Transaction(ctx context.Context, id uuid.UUID) (ledger.PostedTransaction, error) {
	return s.store.GetLedgerTransaction(ctx, id)
}

// run executa o fluxo de idempotência e monta a resposta a partir do que está
// gravado. Pedido novo e replay leem a transação do mesmo jeito, então as
// duas respostas são idênticas (SPEC 11.4).
func (s *Service) run(ctx context.Context, key, path string, canonical any,
	post func(ctx context.Context, tx *postgres.Tx, txnID uuid.UUID) error,
) (Result, error) {
	fp, err := idempotency.Fingerprint("POST", path, canonical)
	if err != nil {
		return Result{}, err
	}
	txnID, err := uuid.NewV7()
	if err != nil {
		return Result{}, fmt.Errorf("generate transaction id: %w", err)
	}

	req := idempotency.Request{Key: key, Method: "POST", Path: path, Fingerprint: fp}
	outcome, err := idempotency.Run(ctx, s.store, req, txnID, s.lockTimeout, func(tx *postgres.Tx) error {
		return post(ctx, tx, txnID)
	})
	if err != nil {
		return Result{}, postgres.LockError(err)
	}

	txn, err := s.store.GetLedgerTransaction(ctx, outcome.TransactionID)
	if err != nil {
		return Result{}, fmt.Errorf("load transaction %s: %w", outcome.TransactionID, err)
	}
	return Result{Transaction: txn, Replayed: outcome.Replayed}, nil
}

func (s *Service) supportedCurrency(ctx context.Context, code string) (money.Currency, error) {
	c, err := money.ParseCurrency(code)
	if err != nil {
		return "", err
	}
	ok, err := s.store.CurrencyExists(ctx, c)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", money.ErrUnsupportedCurrency
	}
	return c, nil
}

// parseAccountID trata um ID malformado como conta inexistente, como nas
// rotas (SPEC 12.1).
func parseAccountID(s string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return uuid.Nil, account.ErrNotFound
	}
	return id, nil
}

// requireUserAccounts é o passo 8 do SPEC 10: as contas precisam existir e ser
// USER. Contas SYSTEM se comportam como inexistentes. A leitura não precisa de
// lock: tipo e existência nunca mudam (não há DELETE, e a aplicação não pode
// alterar a coluna kind). Status, moeda e saldo mudam, e por isso são
// checados pelo motor, já com a linha travada.
func requireUserAccounts(ctx context.Context, tx *postgres.Tx, ids ...uuid.UUID) error {
	for _, id := range ids {
		a, err := tx.GetAccount(ctx, id)
		if err != nil {
			return err
		}
		if a.Kind != account.KindUser {
			return account.ErrNotFound
		}
	}
	return nil
}
