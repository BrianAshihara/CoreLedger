package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

const (
	validURL   = "postgres://ledger_app:secret@localhost:5432/ledger_dev?sslmode=disable"
	validToken = "0123456789abcdef"
)

func env(m map[string]string) Getenv {
	return func(k string) string { return m[k] }
}

// serveEnv devolve o mínimo exigido pelo serve, mais os extras.
func serveEnv(extra map[string]string) Getenv {
	m := map[string]string{"DATABASE_URL": validURL, "ADMIN_TOKEN": validToken}
	for k, v := range extra {
		m[k] = v
	}
	return env(m)
}

func TestLoadServeDefaults(t *testing.T) {
	cfg, err := LoadServe(serveEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	want := Serve{
		DatabaseURL:    validURL,
		HTTPAddr:       ":8080",
		RequestTimeout: 5 * time.Second,
		DBLockTimeout:  2 * time.Second,
		DBMaxConns:     10,
		AdminToken:     validToken,
		Log:            Log{Level: slog.LevelInfo, Format: "text"},
	}
	if cfg != want {
		t.Fatalf("got %+v, want %+v", cfg, want)
	}
}

func TestLoadServeOverrides(t *testing.T) {
	cfg, err := LoadServe(serveEnv(map[string]string{
		"HTTP_ADDR":       "127.0.0.1:9090",
		"REQUEST_TIMEOUT": "750ms",
		"DB_LOCK_TIMEOUT": "300ms",
		"DB_MAX_CONNS":    "25",
		"LOG_LEVEL":       "debug",
		"LOG_FORMAT":      "JSON",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.HTTPAddr != "127.0.0.1:9090" || cfg.RequestTimeout != 750*time.Millisecond || cfg.DBLockTimeout != 300*time.Millisecond ||
		cfg.DBMaxConns != 25 || cfg.Log.Level != slog.LevelDebug || cfg.Log.Format != "json" {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
}

func TestLoadServeReportsEveryProblem(t *testing.T) {
	_, err := LoadServe(env(map[string]string{
		"REQUEST_TIMEOUT": "-1s",
		"DB_MAX_CONNS":    "zero",
		"LOG_LEVEL":       "loud",
		"LOG_FORMAT":      "xml",
	}))
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, key := range []string{"DATABASE_URL", "ADMIN_TOKEN", "REQUEST_TIMEOUT", "DB_MAX_CONNS", "LOG_LEVEL", "LOG_FORMAT"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not mention %s:\n%v", key, err)
		}
	}
}

func TestDatabaseURLValidation(t *testing.T) {
	for _, tc := range []struct {
		name, value string
		ok          bool
	}{
		{"postgres scheme", validURL, true},
		{"postgresql scheme", "postgresql://u:p@localhost/db", true},
		{"keyword dsn", "host=localhost user=u", false},
		{"wrong scheme", "mysql://u:p@localhost/db", false},
		{"no host", "postgres:///db", false},
		{"blank", "   ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadServe(serveEnv(map[string]string{"DATABASE_URL": tc.value}))
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
		})
	}
}

func TestInvalidURLIsNotEchoed(t *testing.T) {
	_, err := LoadServe(serveEnv(map[string]string{"DATABASE_URL": "mysql://user:hunter2@localhost/db"}))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("error must exist and must not contain the password: %v", err)
	}
}

// Um lock_timeout que não é menor que o prazo da requisição nunca dispararia.
func TestLockTimeoutMustBeShorterThanRequestTimeout(t *testing.T) {
	for _, tc := range []struct {
		request, lock string
		ok            bool
	}{
		{"5s", "2s", true},
		{"5s", "5s", false},
		{"1s", "2s", false},
		{"", "10s", false}, // REQUEST_TIMEOUT padrão é 5s
	} {
		_, err := LoadServe(serveEnv(map[string]string{"REQUEST_TIMEOUT": tc.request, "DB_LOCK_TIMEOUT": tc.lock}))
		if (err == nil) != tc.ok {
			t.Errorf("request=%q lock=%q: ok=%v, err=%v", tc.request, tc.lock, tc.ok, err)
		}
	}
}

func TestAdminTokenValidation(t *testing.T) {
	for _, tc := range []struct {
		name, token string
		ok          bool
	}{
		{"16 characters", "0123456789abcdef", true},
		{"long", strings.Repeat("x", 64), true},
		{"16 multibyte characters", strings.Repeat("ç", 16), true},
		{"missing", "", false},
		{"15 characters", "0123456789abcde", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := LoadServe(serveEnv(map[string]string{"ADMIN_TOKEN": tc.token}))
			if (err == nil) != tc.ok {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
			if err != nil && tc.token != "" && strings.Contains(err.Error(), tc.token) {
				t.Fatal("the token must never appear in error messages")
			}
			if tc.ok && cfg.AdminToken != tc.token {
				t.Fatalf("token = %q", cfg.AdminToken)
			}
		})
	}
}

// O reconcile só lê: usa o DATABASE_URL da aplicação e nada mais, nem o
// ADMIN_TOKEN nem a credencial de dono do schema.
func TestLoadReconcileNeedsOnlyTheAppDatabase(t *testing.T) {
	cfg, err := LoadReconcile(env(map[string]string{"DATABASE_URL": validURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != validURL {
		t.Fatalf("got %q", cfg.DatabaseURL)
	}
	if _, err := LoadReconcile(env(map[string]string{"MIGRATE_DATABASE_URL": validURL})); err == nil {
		t.Fatal("DATABASE_URL must be required")
	}
}

func TestLoadMigrateNeedsOnlyItsOwnVariables(t *testing.T) {
	// Sem DATABASE_URL: o `migrate` não pode exigir as variáveis da aplicação.
	cfg, err := LoadMigrate(env(map[string]string{"MIGRATE_DATABASE_URL": validURL}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != validURL {
		t.Fatalf("got %q", cfg.DatabaseURL)
	}

	if _, err := LoadMigrate(env(map[string]string{"DATABASE_URL": validURL})); err == nil {
		t.Fatal("MIGRATE_DATABASE_URL must be required")
	}
}
