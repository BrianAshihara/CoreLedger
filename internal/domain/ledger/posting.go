// Package ledger define os conceitos de partidas dobradas (SPEC 7.2, 7.3, 9):
// a Posting que se quer lançar, suas pernas e o resultado lançado. A
// validação aqui é pura: tudo que dá para checar sem olhar o banco é checado
// antes de abrir qualquer lock.
package ledger

import (
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

type TransactionType string

const (
	TypeDeposit    TransactionType = "DEPOSIT"
	TypeWithdrawal TransactionType = "WITHDRAWAL"
	TypeTransfer   TransactionType = "TRANSFER"
)

// Leg é uma perna: um valor com sinal aplicado a uma conta. Negativo é
// débito, positivo é crédito.
type Leg struct {
	AccountID uuid.UUID
	Amount    int64
}

// Posting é o pedido de lançamento de uma transação. O ID vem pronto, gerado
// antes da transação de banco começar, para que outras escritas na mesma
// transação (como a chave de idempotência) possam referenciá-lo.
type Posting struct {
	TransactionID uuid.UUID
	Type          TransactionType
	Currency      money.Currency
	Legs          []Leg
}

// PostedEntry é uma perna como ficou gravada.
type PostedEntry struct {
	ID           uuid.UUID
	AccountID    uuid.UUID
	SystemCode   string // preenchido só para contas SYSTEM, como a WORLD
	Amount       int64
	AccountSeq   int64
	BalanceAfter int64
	CreatedAt    time.Time
}

// PostedTransaction é o fato registrado no ledger.
type PostedTransaction struct {
	ID        uuid.UUID
	Type      TransactionType
	Currency  money.Currency
	CreatedAt time.Time
	Entries   []PostedEntry
}

var (
	// ErrInvalidPosting marca qualquer posting malformada. Vinda de um
	// serviço de aplicação, indica bug nele: a entrada do cliente já devia ter
	// sido validada antes.
	ErrInvalidPosting = errors.New("invalid posting")

	ErrInsufficientFunds   = errors.New("insufficient funds")
	ErrCurrencyMismatch    = errors.New("currency mismatch")
	ErrSameAccountTransfer = errors.New("source and destination accounts are the same")
	ErrTransactionNotFound = errors.New("transaction not found")
)

// Amount é o valor movimentado pela transação: a soma das pernas positivas.
// Numa transação de duas pernas, é o valor pedido pelo cliente.
func (t PostedTransaction) Amount() int64 {
	var total int64
	for _, e := range t.Entries {
		if e.Amount > 0 {
			total += e.Amount
		}
	}
	return total
}

// Validate checa as regras que não dependem do banco (SPEC 9.1, passo 1):
// pelo menos 2 pernas, contas distintas, nenhum valor zero ou acima do
// MaxAmount, e soma exatamente zero, calculada sem risco de overflow.
func (p Posting) Validate() error {
	if p.TransactionID == uuid.Nil {
		return fmt.Errorf("%w: missing transaction id", ErrInvalidPosting)
	}
	switch p.Type {
	case TypeDeposit, TypeWithdrawal, TypeTransfer:
	default:
		return fmt.Errorf("%w: unknown transaction type %q", ErrInvalidPosting, p.Type)
	}
	if _, err := money.ParseCurrency(string(p.Currency)); err != nil {
		return fmt.Errorf("%w: %w", ErrInvalidPosting, err)
	}
	if len(p.Legs) < 2 {
		return fmt.Errorf("%w: a transaction needs at least 2 legs, got %d", ErrInvalidPosting, len(p.Legs))
	}

	seen := make(map[uuid.UUID]bool, len(p.Legs))
	var sum int64
	for _, leg := range p.Legs {
		if leg.AccountID == uuid.Nil {
			return fmt.Errorf("%w: leg without account id", ErrInvalidPosting)
		}
		if seen[leg.AccountID] {
			return fmt.Errorf("%w: account %s appears in more than one leg", ErrInvalidPosting, leg.AccountID)
		}
		seen[leg.AccountID] = true

		if leg.Amount == 0 {
			return fmt.Errorf("%w: leg for account %s has amount 0", ErrInvalidPosting, leg.AccountID)
		}
		if leg.Amount > money.MaxAmount || leg.Amount < -money.MaxAmount {
			return fmt.Errorf("%w: leg for account %s exceeds the maximum amount", ErrInvalidPosting, leg.AccountID)
		}
		var err error
		if sum, err = money.Add(sum, leg.Amount); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidPosting, err)
		}
	}
	if sum != 0 {
		return fmt.Errorf("%w: legs sum to %d, not 0", ErrInvalidPosting, sum)
	}
	return nil
}
