package postgres

import (
	"context"
	"fmt"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
)

// CountAccounts e CountLedgerTransactions informam o tamanho do que foi
// verificado, para o relatório.
func (s *Snapshot) CountAccounts(ctx context.Context) (int64, error) {
	return s.q.CountAccounts(ctx)
}

func (s *Snapshot) CountLedgerTransactions(ctx context.Context) (int64, error) {
	return s.q.CountLedgerTransactions(ctx)
}

// ReconcileIssues roda todas as checagens de reconciliação (SPEC 13) e devolve
// as inconsistências encontradas. Só lê; nunca corrige nada. Existe só em
// Snapshot, para que todas as checagens vejam o mesmo instante do banco.
func (s *Snapshot) ReconcileIssues(ctx context.Context) ([]ledger.Issue, error) {
	var issues []ledger.Issue

	balances, err := s.q.ReconcileBalanceMatchesLedger(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckBalanceMatchesLedger, err)
	}
	for _, r := range balances {
		issues = append(issues, ledger.Issue{
			Check:     ledger.CheckBalanceMatchesLedger,
			AccountID: r.ID,
			Currency:  money.Currency(r.Currency),
			Expected:  r.LedgerBalance,
			Actual:    r.StoredBalance,
			Detail:    "stored balance differs from the sum of the account's entries",
		})
	}

	lasts, err := s.q.ReconcileLastEntryMatchesBalance(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckLastEntryMatchesBalance, err)
	}
	for _, r := range lasts {
		issue := ledger.Issue{
			Check:      ledger.CheckLastEntryMatchesBalance,
			AccountID:  r.ID,
			Currency:   money.Currency(r.Currency),
			AccountSeq: r.LastEntrySeq,
			Expected:   r.LastBalanceAfter.Int64, // 0 quando não há perna
			Actual:     r.StoredBalance,
			Detail:     "stored balance differs from the balance_after of the last entry",
		}
		if r.LastEntrySeq > 0 && !r.LastBalanceAfter.Valid {
			issue.Detail = "the entry at last_entry_seq does not exist"
		}
		issues = append(issues, issue)
	}

	gaps, err := s.q.ReconcileSequenceGapFree(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckSequenceGapFree, err)
	}
	for _, r := range gaps {
		issues = append(issues, ledger.Issue{
			Check:      ledger.CheckSequenceGapFree,
			AccountID:  r.ID,
			Currency:   money.Currency(r.Currency),
			AccountSeq: r.LastEntrySeq,
			Expected:   r.LastEntrySeq,
			Actual:     r.EntryCount,
			Detail:     fmt.Sprintf("entries are not exactly 1..last_entry_seq (count %d, max seq %d)", r.EntryCount, r.MaxSeq),
		})
	}

	chain, err := s.q.ReconcileBalanceAfterChain(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckBalanceAfterChain, err)
	}
	for _, r := range chain {
		issue := ledger.Issue{
			Check:      ledger.CheckBalanceAfterChain,
			AccountID:  r.AccountID,
			AccountSeq: r.AccountSeq,
			Actual:     r.BalanceAfter,
			Detail:     "balance_after is not the previous balance_after plus the amount",
		}
		if expected, err := money.Add(r.PreviousBalanceAfter, r.Amount); err == nil {
			issue.Expected = expected
		} else {
			issue.Detail = "previous balance_after plus the amount overflows"
		}
		issues = append(issues, issue)
	}

	txns, err := s.q.ReconcileTransactionsBalanced(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckTransactionBalanced, err)
	}
	for _, r := range txns {
		issues = append(issues, ledger.Issue{
			Check:         ledger.CheckTransactionBalanced,
			TransactionID: r.ID,
			Currency:      money.Currency(r.Currency),
			Expected:      0,
			Actual:        r.EntriesSum,
			Detail:        fmt.Sprintf("transaction must have 2+ entries summing to 0 (has %d entries)", r.EntryCount),
		})
	}

	sums, err := s.q.ReconcileCurrencyZeroSum(ctx)
	if err != nil {
		return nil, fmt.Errorf("check %s: %w", ledger.CheckCurrencyZeroSum, err)
	}
	for _, r := range sums {
		issues = append(issues, ledger.Issue{
			Check:    ledger.CheckCurrencyZeroSum,
			Currency: money.Currency(r.Currency),
			Expected: 0,
			Actual:   r.BalanceSum,
			Detail:   "the balances of all accounts in this currency do not sum to 0",
		})
	}

	return issues, nil
}
