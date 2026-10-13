// Package config lê a configuração das variáveis de ambiente.
//
// Cada subcomando carrega só as variáveis que usa (SPEC 14.3), e todos os
// problemas são reportados de uma vez, para que um ambiente mal configurado
// falhe logo com a lista completa.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Getenv tem a assinatura de os.Getenv; os testes passam uma busca em map.
type Getenv func(key string) string

type Log struct {
	Level  slog.Level
	Format string // "text" ou "json"
}

// Serve é a configuração do `ledger serve`.
type Serve struct {
	DatabaseURL    string
	HTTPAddr       string
	RequestTimeout time.Duration
	DBLockTimeout  time.Duration
	DBMaxConns     int32
	AdminToken     string
	Log            Log
}

// Migrate é a configuração do `ledger migrate`.
type Migrate struct {
	DatabaseURL string
	Log         Log
}

const (
	defaultHTTPAddr       = ":8080"
	defaultRequestTimeout = 5 * time.Second
	maxRequestTimeout     = 60 * time.Second
	defaultDBLockTimeout  = 2 * time.Second
	defaultDBMaxConns     = 10
	maxDBMaxConns         = 500
	minAdminTokenLength   = 16
)

func LoadServe(getenv Getenv) (Serve, error) {
	var errs []error
	cfg := Serve{
		DatabaseURL:    databaseURL(getenv, "DATABASE_URL", &errs),
		HTTPAddr:       stringOr(getenv("HTTP_ADDR"), defaultHTTPAddr),
		RequestTimeout: duration(getenv, "REQUEST_TIMEOUT", defaultRequestTimeout, maxRequestTimeout, &errs),
		DBLockTimeout:  duration(getenv, "DB_LOCK_TIMEOUT", defaultDBLockTimeout, maxRequestTimeout, &errs),
		DBMaxConns:     int32InRange(getenv, "DB_MAX_CONNS", defaultDBMaxConns, 1, maxDBMaxConns, &errs),
		AdminToken:     adminToken(getenv, &errs),
		Log:            logConfig(getenv, &errs),
	}
	// Um lock_timeout maior que o prazo da requisição nunca dispararia: o
	// contexto expiraria antes e o cliente veria um erro menos claro.
	if cfg.DBLockTimeout >= cfg.RequestTimeout {
		errs = append(errs, fmt.Errorf("DB_LOCK_TIMEOUT (%s) must be shorter than REQUEST_TIMEOUT (%s)", cfg.DBLockTimeout, cfg.RequestTimeout))
	}
	return cfg, errors.Join(errs...)
}

// Reconcile é a configuração do `ledger reconcile`. Usa o papel da aplicação:
// a reconciliação só lê.
type Reconcile struct {
	DatabaseURL string
	Log         Log
}

func LoadReconcile(getenv Getenv) (Reconcile, error) {
	var errs []error
	cfg := Reconcile{
		DatabaseURL: databaseURL(getenv, "DATABASE_URL", &errs),
		Log:         logConfig(getenv, &errs),
	}
	return cfg, errors.Join(errs...)
}

func LoadMigrate(getenv Getenv) (Migrate, error) {
	var errs []error
	cfg := Migrate{
		DatabaseURL: databaseURL(getenv, "MIGRATE_DATABASE_URL", &errs),
		Log:         logConfig(getenv, &errs),
	}
	return cfg, errors.Join(errs...)
}

// databaseURL exige uma URL postgres://. DSNs no formato chave=valor são
// rejeitados de propósito: um formato só mantém .env, docs e mensagens de
// erro consistentes.
func databaseURL(getenv Getenv, key string, errs *[]error) string {
	raw := strings.TrimSpace(getenv(key))
	if raw == "" {
		*errs = append(*errs, fmt.Errorf("%s is required", key))
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") || u.Host == "" {
		// Não ecoa o valor: ele contém senha.
		*errs = append(*errs, fmt.Errorf("%s must be a postgres:// URL", key))
		return ""
	}
	return raw
}

// adminToken exige um token de admin com tamanho mínimo (SPEC 12.1). Espaços
// nas pontas não são removidos: o token é comparado exatamente como foi
// configurado. Como a URL do banco, o valor nunca aparece em mensagens.
func adminToken(getenv Getenv, errs *[]error) string {
	v := getenv("ADMIN_TOKEN")
	switch n := utf8.RuneCountInString(v); {
	case n == 0:
		*errs = append(*errs, errors.New("ADMIN_TOKEN is required"))
	case n < minAdminTokenLength:
		*errs = append(*errs, fmt.Errorf("ADMIN_TOKEN must have at least %d characters", minAdminTokenLength))
	}
	return v
}

func logConfig(getenv Getenv, errs *[]error) Log {
	cfg := Log{Level: slog.LevelInfo, Format: "text"}

	if v := strings.TrimSpace(getenv("LOG_LEVEL")); v != "" {
		if err := cfg.Level.UnmarshalText([]byte(v)); err != nil {
			*errs = append(*errs, fmt.Errorf("LOG_LEVEL must be one of debug, info, warn, error; got %q", v))
		}
	}

	switch v := strings.ToLower(strings.TrimSpace(getenv("LOG_FORMAT"))); v {
	case "":
	case "text", "json":
		cfg.Format = v
	default:
		*errs = append(*errs, fmt.Errorf("LOG_FORMAT must be text or json; got %q", v))
	}
	return cfg
}

func duration(getenv Getenv, key string, def, maxValue time.Duration, errs *[]error) time.Duration {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 || d > maxValue {
		*errs = append(*errs, fmt.Errorf("%s must be a duration between 0 and %s (e.g. 5s); got %q", key, maxValue, v))
		return def
	}
	return d
}

func int32InRange(getenv Getenv, key string, def, minValue, maxValue int32, errs *[]error) int32 {
	v := strings.TrimSpace(getenv(key))
	if v == "" {
		return def
	}
	n, err := strconv.ParseInt(v, 10, 32)
	if err != nil || n < int64(minValue) || n > int64(maxValue) {
		*errs = append(*errs, fmt.Errorf("%s must be an integer between %d and %d; got %q", key, minValue, maxValue, v))
		return def
	}
	return int32(n)
}

func stringOr(v, def string) string {
	if v = strings.TrimSpace(v); v != "" {
		return v
	}
	return def
}
