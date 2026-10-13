package main

import (
	"bytes"
	"io"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/httpapi"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// Roda a demo inteira contra a API real (router, serviços e PostgreSQL), para
// que uma mudança na API que quebre o roteiro seja pega pelos testes e não na
// frente de quem está assistindo.
func TestDemoPassesAgainstTheRealAPI(t *testing.T) {
	db := testdb.New(t)
	const token = "demo-test-admin-token"
	const lockTimeout = 2 * time.Second
	store := postgres.NewStore(db.Pool)
	srv := httptest.NewServer(httpapi.NewRouter(httpapi.Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             db.Pool,
		RequestTimeout: 5 * time.Second,
		Accounts:       accounts.NewService(store),
		Movements:      movements.NewService(store, posting.New(lockTimeout), lockTimeout),
		Reconcile:      reconcile.NewService(store),
		AdminToken:     token,
	}))
	defer srv.Close()

	var out bytes.Buffer
	err := run(t.Context(), config{BaseURL: srv.URL, AdminToken: token}, &out)
	if err != nil {
		t.Fatalf("demo failed: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), ", 0 failed.") {
		t.Fatalf("unexpected summary:\n%s", out.String())
	}
	if strings.Contains(out.String(), token) {
		t.Fatal("the admin token leaked into the output")
	}
}

func TestAPIURL(t *testing.T) {
	for _, tc := range []struct {
		env  map[string]string
		want string
	}{
		{map[string]string{}, "http://localhost:8080"},
		{map[string]string{"HTTP_ADDR": ":9090"}, "http://localhost:9090"},
		{map[string]string{"HTTP_ADDR": "127.0.0.1:8081"}, "http://127.0.0.1:8081"},
		{map[string]string{"DEMO_API_URL": "http://example.test:1/", "HTTP_ADDR": ":9090"}, "http://example.test:1"},
	} {
		if got := apiURL(func(k string) string { return tc.env[k] }); got != tc.want {
			t.Errorf("apiURL(%v) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestBRL(t *testing.T) {
	for cents, want := range map[int64]string{
		0:          "0.00 BRL",
		5:          "0.05 BRL",
		100000:     "1,000.00 BRL",
		-20000:     "-200.00 BRL",
		1234567890: "12,345,678.90 BRL",
	} {
		if got := brl(cents); got != want {
			t.Errorf("brl(%d) = %q, want %q", cents, got, want)
		}
	}
}
