package ledger

import (
	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

// Check identifica uma checagem de reconciliação (SPEC 13).
type Check string

const (
	// I5: saldo guardado = soma das pernas da conta.
	CheckBalanceMatchesLedger Check = "BALANCE_MATCHES_LEDGER"
	// Saldo guardado = balance_after da última perna (ou 0 sem pernas).
	CheckLastEntryMatchesBalance Check = "LAST_ENTRY_MATCHES_BALANCE"
	// I6: sequências da conta são exatamente 1..last_entry_seq.
	CheckSequenceGapFree Check = "SEQUENCE_GAP_FREE"
	// I6: cada balance_after = balance_after anterior + amount.
	CheckBalanceAfterChain Check = "BALANCE_AFTER_CHAIN"
	// I1: toda transação tem 2+ pernas que somam zero.
	CheckTransactionBalanced Check = "TRANSACTION_BALANCED"
	// I9: por moeda, a soma de todos os saldos é zero.
	CheckCurrencyZeroSum Check = "CURRENCY_ZERO_SUM"
)

// Issue é uma inconsistência encontrada pela reconciliação. Os campos que não
// se aplicam à checagem ficam no valor zero. Expected é o valor que o ledger
// diz que deveria existir; Actual é o que está gravado.
type Issue struct {
	Check         Check
	AccountID     uuid.UUID
	TransactionID uuid.UUID
	Currency      money.Currency
	AccountSeq    int64
	Expected      int64
	Actual        int64
	Detail        string
}
