package posting

import (
	"context"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/postgres"
)

// Injeção de falha (SPEC Fase 6): a conexão de banco morre no meio de um
// lançamento, depois de a primeira perna já ter sido gravada. O PostgreSQL
// desfaz a transação inteira (I8), a reconciliação continua OK e o pool
// descarta a conexão morta, então o próximo lançamento funciona.
func TestBackendKilledMidPostingLeavesNoState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	e := newEnv(t)
	alice, bob := e.newUser(), e.newUser()
	e.deposit(alice, 1_000)
	txnsBefore, entriesBefore := e.ledgerSize()

	paused := make(chan struct{})
	resume := make(chan struct{})
	e.engine.afterEntry = func(entry int) error {
		if entry == 0 {
			close(paused)
			<-resume
		}
		return nil
	}

	result := make(chan error, 1)
	go func() {
		_, err := e.transfer(alice, bob, 400)
		result <- err
	}()

	select {
	case <-paused:
	case <-time.After(10 * time.Second):
		t.Fatal("posting never reached the first entry")
	}

	// A única sessão deste banco com um ID de transação atribuído é a do
	// lançamento pausado: ela já escreveu a primeira perna.
	var killed int
	err := e.db.Superuser(t).QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE pg_terminate_backend(pid))
		FROM pg_stat_activity
		WHERE datname = current_database() AND pid <> pg_backend_pid() AND backend_xid IS NOT NULL`,
	).Scan(&killed)
	close(resume)
	if err != nil {
		t.Fatal(err)
	}
	if killed != 1 {
		t.Fatalf("terminated %d backends, want exactly the posting one", killed)
	}

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("the posting reported success after its connection was killed")
		}
		// Precisa ser reconhecido como indisponibilidade, para a API
		// responder 503 (repetível) e não 500.
		if !postgres.IsUnavailable(err) {
			t.Fatalf("a killed connection must be classified as unavailable, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the posting did not return after its connection was killed")
	}
	e.engine.afterEntry = nil

	if txns, entries := e.ledgerSize(); txns != txnsBefore || entries != entriesBefore {
		t.Fatalf("partial state persisted: %d/%d -> %d/%d", txnsBefore, entriesBefore, txns, entries)
	}
	if e.balance(alice) != 1_000 || e.balance(bob) != 0 {
		t.Fatalf("balances changed: alice %d, bob %d", e.balance(alice), e.balance(bob))
	}

	// O pool se recupera sozinho: a conexão morta é descartada.
	if _, err := e.transfer(alice, bob, 400); err != nil {
		t.Fatalf("posting after the failure: %v", err)
	}
	if e.balance(alice) != 600 || e.balance(bob) != 400 {
		t.Fatalf("after recovery: alice %d, bob %d", e.balance(alice), e.balance(bob))
	}
	// A reconciliação roda no cleanup do env (varredura de invariantes).
}
