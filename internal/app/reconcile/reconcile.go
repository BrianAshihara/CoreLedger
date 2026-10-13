// Package reconcile verifica se o banco ainda obedece às invariantes do ledger
// (SPEC 13). Só detecta e relata; correção automática está fora de escopo de
// propósito: num sistema financeiro, consertar dado sozinho esconde o bug que
// o corrompeu.
package reconcile

import (
	"context"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/postgres"
)

type Status string

const (
	StatusOK           Status = "OK"
	StatusInconsistent Status = "INCONSISTENT"
)

type Report struct {
	Status              Status
	CheckedAccounts     int64
	CheckedTransactions int64
	Issues              []ledger.Issue
}

type Service struct {
	store *postgres.Store
}

func NewService(store *postgres.Store) *Service {
	return &Service{store: store}
}

// Run executa todas as checagens num único snapshot do banco, então o
// resultado é coerente mesmo com lançamentos acontecendo em paralelo.
func (s *Service) Run(ctx context.Context) (Report, error) {
	var r Report
	err := s.store.ReadSnapshot(ctx, func(snap *postgres.Snapshot) error {
		var err error
		if r.CheckedAccounts, err = snap.CountAccounts(ctx); err != nil {
			return err
		}
		if r.CheckedTransactions, err = snap.CountLedgerTransactions(ctx); err != nil {
			return err
		}
		r.Issues, err = snap.ReconcileIssues(ctx)
		return err
	})
	if err != nil {
		return Report{}, err
	}
	r.Status = StatusOK
	if len(r.Issues) > 0 {
		r.Status = StatusInconsistent
	}
	return r, nil
}
