package posting

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// Testes de concorrência (SPEC 21.2). Não usam t.Parallel: cada um abre um
// pool grande para criar contenção real, e o testdb garante que só um teste
// pesado rode por vez, mesmo entre pacotes (SPEC 21.0). 100 goroutines
// disputando 25 conexões ainda brigam pelo mesmo lock de linha.

const contentionConns = 25

// race dispara n funções ao mesmo tempo: todas as goroutines esperam numa
// barreira e são liberadas juntas, para maximizar a disputa pelos locks.
func race(n int, fn func(i int) error) []error {
	errs := make([]error, n)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Go(func() {
			<-start
			errs[i] = fn(i)
		})
	}
	close(start)
	wg.Wait()
	return errs
}

// C1: saldo 10.000 e 100 transferências simultâneas de 200 para 100 contas
// diferentes. Exatamente 50 passam, 50 falham por saldo, e o saldo final é 0.
// Sem o lock, várias leriam o mesmo saldo e o gasto passaria do disponível.
func TestC1ConcurrentDebitsNeverOverspend(t *testing.T) {
	e := newEnv(t, testdb.WithMaxConns(contentionConns))
	source := e.newUser()
	e.deposit(source, 10_000)
	dests := make([]uuid.UUID, 100)
	for i := range dests {
		dests[i] = e.newUser()
	}

	errs := race(100, func(i int) error {
		_, err := e.transfer(source, dests[i], 200)
		return err
	})

	var ok, insufficient int
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
			if got := e.balance(dests[i]); got != 200 {
				t.Errorf("dest %d: balance %d after a successful transfer", i, got)
			}
		case errors.Is(err, ledger.ErrInsufficientFunds):
			insufficient++
			if got := e.balance(dests[i]); got != 0 {
				t.Errorf("dest %d: balance %d after a rejected transfer", i, got)
			}
		default:
			t.Errorf("transfer %d: unexpected error %v", i, err)
		}
	}
	if ok != 50 || insufficient != 50 {
		t.Fatalf("succeeded=%d insufficient=%d, want exactly 50 and 50", ok, insufficient)
	}
	if got := e.balance(source); got != 0 {
		t.Fatalf("source balance = %d, want 0", got)
	}
}

// C2: 50 transferências A->B e 50 B->A ao mesmo tempo. Com o lock na ordem
// "origem, depois destino", cada par oposto travaria um esperando o outro
// (deadlock). Com locks em ordem crescente de ID, todas passam.
func TestC2OppositeTransfersNeverDeadlock(t *testing.T) {
	e := newEnv(t, testdb.WithMaxConns(contentionConns))
	a, b := e.newUser(), e.newUser()
	e.deposit(a, 10_000)
	e.deposit(b, 10_000)

	errs := race(100, func(i int) error {
		from, to := a, b
		if i%2 == 1 {
			from, to = b, a
		}
		_, err := e.transfer(from, to, 100)
		return err
	})

	for i, err := range errs {
		if err != nil {
			t.Errorf("transfer %d: %v", i, err) // inclui deadlock_detected (40P01)
		}
	}
	if total := e.balance(a) + e.balance(b); total != 20_000 {
		t.Fatalf("A+B = %d, want 20000", total)
	}
	if e.balance(a) != 10_000 || e.balance(b) != 10_000 {
		t.Fatalf("A = %d, B = %d; 50 each way must cancel out", e.balance(a), e.balance(b))
	}
}

// C3: 100 transferências simultâneas para o mesmo destino. A sequência do
// destino fica 1..100 sem buraco nem repetição, e o saldo é a soma dos
// créditos.
func TestC3ConcurrentCreditsKeepSequenceGapFree(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t, testdb.WithMaxConns(contentionConns))
	dest := e.newUser()
	sources := make([]uuid.UUID, 100)
	for i := range sources {
		sources[i] = e.newUser()
		e.deposit(sources[i], 1_000)
	}

	errs := race(100, func(i int) error {
		_, err := e.transfer(sources[i], dest, 1_000)
		return err
	})
	for i, err := range errs {
		if err != nil {
			t.Errorf("transfer %d: %v", i, err)
		}
	}

	if got := e.balance(dest); got != 100_000 {
		t.Fatalf("dest balance = %d, want 100000", got)
	}
	var n, minSeq, maxSeq, distinct int64
	err := e.db.Superuser(t).QueryRow(ctx, `
		SELECT count(*), min(account_seq), max(account_seq), count(DISTINCT account_seq)
		FROM ledger_entries WHERE account_id = $1`, dest).Scan(&n, &minSeq, &maxSeq, &distinct)
	if err != nil {
		t.Fatal(err)
	}
	if n != 100 || minSeq != 1 || maxSeq != 100 || distinct != 100 {
		t.Fatalf("dest sequence: count=%d min=%d max=%d distinct=%d, want 100/1/100/100", n, minSeq, maxSeq, distinct)
	}
}
