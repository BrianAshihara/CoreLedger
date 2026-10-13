package accounts

import (
	"context"
	"errors"
	"math"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
)

const (
	DefaultHistoryLimit = 50
	MaxHistoryLimit     = 200
)

// ErrInvalidLimit indica um tamanho de página fora de 1..MaxHistoryLimit.
var ErrInvalidLimit = errors.New("limit must be between 1 and 200")

// HistoryPage é uma página do histórico. NextBeforeSeq é nil na última
// página; senão, é o valor a passar como beforeSeq para ler a próxima.
type HistoryPage struct {
	Entries       []ledger.HistoryEntry
	NextBeforeSeq *int64
}

// History lista as pernas de uma conta USER, da mais nova para a mais antiga
// (SPEC 12.3). beforeSeq nil começa do início (a perna mais recente).
//
// A paginação é estável com lançamentos em andamento: cada página pede
// sequências menores que a última lida, e pernas novas sempre recebem
// sequências maiores. Nenhuma linha é pulada ou repetida entre páginas.
func (s *Service) History(ctx context.Context, id uuid.UUID, beforeSeq *int64, limit int) (HistoryPage, error) {
	if limit < 1 || limit > MaxHistoryLimit {
		return HistoryPage{}, ErrInvalidLimit
	}
	if _, err := s.Get(ctx, id); err != nil { // contas SYSTEM se comportam como inexistentes
		return HistoryPage{}, err
	}

	before := int64(math.MaxInt64)
	if beforeSeq != nil {
		before = *beforeSeq
	}
	// Pede uma linha a mais só para saber se existe próxima página.
	entries, err := s.store.ListAccountEntries(ctx, id, before, int32(limit+1)) //nolint:gosec // limit <= 200
	if err != nil {
		return HistoryPage{}, err
	}

	page := HistoryPage{Entries: entries}
	if len(entries) > limit {
		page.Entries = entries[:limit]
		next := page.Entries[limit-1].AccountSeq
		page.NextBeforeSeq = &next
	}
	return page, nil
}
