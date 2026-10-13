// Package posting é o motor de lançamentos (SPEC 9): o único código que
// escreve nas tabelas do ledger e nas colunas balance e last_entry_seq das
// contas. Depósitos, saques e transferências são todos Postings lançadas
// aqui.
package posting

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
)

type Engine struct {
	lockTimeout time.Duration

	// afterEntry, quando definido, roda depois de cada perna gravada. Só os
	// testes deste pacote o usam, para injetar uma falha no meio do lançamento
	// e provar que nada fica gravado (I8). Não é exportado nem configurável
	// de fora.
	afterEntry func(entry int) error
}

// New cria o motor. lockTimeout limita a espera por cada lock de conta
// (SPEC 9.3).
func New(lockTimeout time.Duration) *Engine {
	return &Engine{lockTimeout: lockTimeout}
}

// Post lança p dentro da transação tx, que já precisa estar aberta: assim o
// chamador inclui outras escritas (como a chave de idempotência) no mesmo
// commit. Quem confirma é o chamador; os triggers adiados verificam I1 no
// COMMIT. Se Post devolver erro, o chamador precisa desfazer a transação.
//
// Algoritmo (SPEC 9.1):
//  1. valida a posting sem tocar no banco;
//  2. ordena as contas por ID e trava cada uma, nessa ordem;
//  3. checa existência, status e moeda de cada conta, já com o lock;
//  4. checa saldo suficiente para contas USER;
//  5. grava a transação, as pernas e os novos saldos.
//
// A ordem fixa dos locks evita deadlock: duas transferências opostas (A->B e
// B->A) travam A e B na mesma ordem, então uma simplesmente espera a outra.
func (e *Engine) Post(ctx context.Context, tx *postgres.Tx, p ledger.Posting) (ledger.PostedTransaction, error) {
	if err := p.Validate(); err != nil {
		return ledger.PostedTransaction{}, err
	}

	legs := slices.Clone(p.Legs)
	slices.SortFunc(legs, func(a, b ledger.Leg) int { return bytes.Compare(a.AccountID[:], b.AccountID[:]) })

	if err := tx.SetLockTimeout(ctx, e.lockTimeout); err != nil {
		return ledger.PostedTransaction{}, err
	}

	// Um SELECT ... FOR UPDATE por conta, em ordem: a ordem dos locks fica
	// explícita e auditável.
	accounts := make([]account.Account, len(legs))
	for i, leg := range legs {
		a, err := tx.GetAccountForUpdate(ctx, leg.AccountID)
		if errors.Is(err, account.ErrNotFound) {
			return ledger.PostedTransaction{}, fmt.Errorf("%w: %s", account.ErrNotFound, leg.AccountID)
		}
		if err != nil {
			return ledger.PostedTransaction{}, postgres.LockError(err)
		}
		accounts[i] = a
	}

	// Regras que dependem do estado só são checadas depois dos locks: com a
	// linha travada, o que foi lido é o valor confirmado mais recente e
	// ninguém consegue mudá-lo até o fim desta transação.
	//
	// Duas passadas, na ordem do SPEC 10: primeiro o status de todas as
	// contas, depois a moeda de todas. Assim o erro devolvido não depende da
	// ordem dos IDs.
	for _, a := range accounts {
		if a.Status != account.StatusActive {
			return ledger.PostedTransaction{}, fmt.Errorf("%w: %s is %s", account.ErrNotActive, a.ID, a.Status)
		}
	}
	for _, a := range accounts {
		if a.Currency != p.Currency {
			return ledger.PostedTransaction{}, fmt.Errorf("%w: account %s is %s, transaction is %s",
				ledger.ErrCurrencyMismatch, a.ID, a.Currency, p.Currency)
		}
	}

	newBalances := make([]int64, len(legs))
	for i, leg := range legs {
		a := accounts[i]
		balance, err := money.Add(a.Balance, leg.Amount)
		if err != nil {
			return ledger.PostedTransaction{}, fmt.Errorf("account %s: %w", a.ID, err)
		}
		// I3: conta USER nunca fica negativa. Contas SYSTEM (WORLD) podem.
		if a.Kind == account.KindUser && balance < 0 {
			return ledger.PostedTransaction{}, fmt.Errorf("%w: account %s", ledger.ErrInsufficientFunds, a.ID)
		}
		newBalances[i] = balance
	}

	createdAt, err := tx.InsertLedgerTransaction(ctx, p.TransactionID, p.Type, p.Currency)
	if err != nil {
		return ledger.PostedTransaction{}, err
	}
	posted := ledger.PostedTransaction{
		ID:        p.TransactionID,
		Type:      p.Type,
		Currency:  p.Currency,
		CreatedAt: createdAt,
		Entries:   make([]ledger.PostedEntry, 0, len(legs)),
	}

	for i, leg := range legs {
		a := accounts[i]
		entryID, err := uuid.NewV7()
		if err != nil {
			return ledger.PostedTransaction{}, fmt.Errorf("generate entry id: %w", err)
		}
		// O lock garante que nenhuma outra transação usa a mesma sequência; o
		// UNIQUE (account_id, account_seq) é a segunda linha de defesa (SPEC 8.4).
		seq := a.LastEntrySeq + 1
		entryAt, err := tx.InsertLedgerEntry(ctx, postgres.NewEntry{
			ID:            entryID,
			TransactionID: p.TransactionID,
			AccountID:     a.ID,
			Currency:      p.Currency,
			Amount:        leg.Amount,
			AccountSeq:    seq,
			BalanceAfter:  newBalances[i],
		})
		if err != nil {
			return ledger.PostedTransaction{}, err
		}
		if err := tx.ApplyAccountPosting(ctx, a.ID, newBalances[i], a.LastEntrySeq, seq); err != nil {
			return ledger.PostedTransaction{}, err
		}
		posted.Entries = append(posted.Entries, ledger.PostedEntry{
			ID:           entryID,
			AccountID:    a.ID,
			SystemCode:   a.SystemCode,
			Amount:       leg.Amount,
			AccountSeq:   seq,
			BalanceAfter: newBalances[i],
			CreatedAt:    entryAt,
		})

		if e.afterEntry != nil {
			if err := e.afterEntry(i); err != nil {
				return ledger.PostedTransaction{}, err
			}
		}
	}

	return posted, nil
}
