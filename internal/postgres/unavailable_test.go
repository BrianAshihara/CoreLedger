package postgres

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUnavailable(t *testing.T) {
	// Porta onde nada escuta: pgx devolve um *pgconn.ConnectError.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, connectErr := pgx.Connect(ctx, "postgres://u:p@127.0.0.1:1/db?sslmode=disable&connect_timeout=2")
	if connectErr == nil {
		t.Fatal("expected a connection error")
	}

	for name, err := range map[string]error{
		"connection refused":     connectErr,
		"admin shutdown (57P01)": &pgconn.PgError{Code: "57P01"},
		"cannot connect (57P03)": fmt.Errorf("query: %w", &pgconn.PgError{Code: "57P03"}),
		"idle in tx (25P03)":     &pgconn.PgError{Code: "25P03"},
		"unexpected EOF":         fmt.Errorf("read: %w", io.ErrUnexpectedEOF),
		"conn closed":            errors.New("conn closed"),
	} {
		if !IsUnavailable(err) {
			t.Errorf("%s: want unavailable", name)
		}
	}

	for name, err := range map[string]error{
		"nil":                     nil,
		"unique violation":        &pgconn.PgError{Code: "23505"},
		"check violation":         &pgconn.PgError{Code: "23514"},
		"unrelated application":   errors.New("insufficient funds"),
		"lock timeout is its own": &pgconn.PgError{Code: "55P03"},
	} {
		if IsUnavailable(err) {
			t.Errorf("%s: must not be classified as unavailable", name)
		}
	}
}
