package postgres_test

import (
	"context"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// Reproduz a falha vista no teste de carga (ADR 0012): a API perde a conexão
// no meio de uma transação que trava uma conta, e o servidor não percebe. Sem
// o timeout, o lock ficaria preso para sempre; com ele, o próprio PostgreSQL
// encerra a sessão órfã e a conta volta a ser usável.
func TestPoolIdleInTransactionTimeoutReleasesLocks(t *testing.T) {
	db := testdb.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const idle = 300 * time.Millisecond
	pool, err := postgres.NewPool(ctx, db.SuperuserURL(), postgres.PoolOptions{
		MaxConns:                 2,
		IdleInTransactionTimeout: idle,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var setting string
	if err := pool.QueryRow(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&setting); err != nil {
		t.Fatal(err)
	}
	if setting != "300ms" {
		t.Fatalf("idle_in_transaction_session_timeout = %q, want 300ms", setting)
	}

	// A transação "órfã": trava a WORLD e nunca mais envia nada.
	orphan, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = orphan.Rollback(context.Background()) }()
	if _, err := orphan.Exec(ctx, "SELECT id FROM accounts WHERE system_code = 'WORLD' FOR UPDATE"); err != nil {
		t.Fatal(err)
	}

	// Outra sessão, sem o timeout, espera o lock da mesma linha. Ela só
	// consegue porque o servidor encerra a órfã.
	other := db.Superuser(t)
	start := time.Now()
	tx, err := other.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, "SET LOCAL lock_timeout = '10s'"); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT id FROM accounts WHERE system_code = 'WORLD' FOR UPDATE"); err != nil {
		t.Fatalf("lock was not released: %v", err)
	}
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("lock released after %s, want about %s", waited, idle)
	}

	// Se a API voltasse a usar a conexão órfã, o erro seria transitório (503),
	// nunca um 500.
	_, err = orphan.Exec(ctx, "SELECT 1")
	if err == nil {
		t.Fatal("orphaned transaction is still alive")
	}
	if !postgres.IsUnavailable(err) {
		t.Fatalf("orphan error not classified as unavailable: %v", err)
	}
}

func TestPoolWithoutIdleTimeoutKeepsServerDefault(t *testing.T) {
	db := testdb.New(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var want string
	if err := db.Superuser(t).QueryRow(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&want); err != nil {
		t.Fatal(err)
	}

	pool, err := postgres.NewPool(ctx, db.SuperuserURL(), postgres.PoolOptions{MaxConns: 1})
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()

	var got string
	if err := pool.QueryRow(ctx, "SHOW idle_in_transaction_session_timeout").Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("idle_in_transaction_session_timeout = %q, want the server default %q", got, want)
	}
}
