package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier é o subconjunto do pgx usado para ler o estado do schema;
// *pgxpool.Pool e pgx.Tx o satisfazem.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	ErrSchemaMissing  = errors.New("database has no migrations applied")
	ErrSchemaDirty    = errors.New("database schema is dirty")
	ErrSchemaMismatch = errors.New("database schema version does not match this binary")
)

const undefinedTable = "42P01"

// CheckSchema verifica se o banco está exatamente na versão want e não está
// dirty (SPEC 15.2). Lê schema_migrations com um SELECT simples em vez de
// passar pelo golang-migrate, que pegaria um advisory lock e poderia tentar
// criar a tabela. O papel da aplicação não pode fazer nenhuma das duas coisas.
func CheckSchema(ctx context.Context, db Querier, want uint) error {
	const hint = "run `make migrate-up` (or `go run ./cmd/ledger migrate up`)"

	var (
		version uint64 // o pgx rejeita bigint negativo ao ler em uint64
		dirty   bool
	)
	err := db.QueryRow(ctx, `SELECT version, dirty FROM schema_migrations LIMIT 1`).Scan(&version, &dirty)
	var pgErr *pgconn.PgError
	switch {
	case errors.As(err, &pgErr) && pgErr.Code == undefinedTable, errors.Is(err, pgx.ErrNoRows):
		return fmt.Errorf("%w: this binary expects version %d; %s", ErrSchemaMissing, want, hint)
	case err != nil:
		return fmt.Errorf("read schema version: %w", err)
	}

	if dirty {
		return fmt.Errorf("%w at version %d: a migration failed halfway; fix it manually, then use `ledger migrate` to recover",
			ErrSchemaDirty, version)
	}
	if version != uint64(want) {
		if version > uint64(want) {
			return fmt.Errorf("%w: database is at version %d but this binary expects %d; the database is newer than the binary, deploy a newer build",
				ErrSchemaMismatch, version, want)
		}
		return fmt.Errorf("%w: database is at version %d but this binary expects %d; %s",
			ErrSchemaMismatch, version, want, hint)
	}
	return nil
}
