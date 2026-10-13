package ledger

import (
	"time"

	"github.com/google/uuid"
)

// HistoryEntry é uma linha do histórico de uma conta: a perna dela numa
// transação, com o saldo que resultou. A sequência de balance_after explica,
// linha a linha, como a conta chegou ao saldo atual (SPEC 7.3, I6).
type HistoryEntry struct {
	ID            uuid.UUID
	TransactionID uuid.UUID
	Type          TransactionType
	Amount        int64 // com sinal: negativo é débito
	BalanceAfter  int64
	AccountSeq    int64
	CreatedAt     time.Time
}
