package postgres_test

import (
	"context"
	"errors"
	"testing"

	"github.com/brianashihara/coreledger/db/migrations"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

func latest(t *testing.T) uint {
	t.Helper()
	v, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestCheckSchemaAcceptsCurrentVersion(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)
	// Roda como ledger_app, igual ao `serve`.
	if err := postgres.CheckSchema(context.Background(), d.Pool, latest(t)); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSchemaRefusesUnmigratedDatabase(t *testing.T) {
	t.Parallel()
	d := testdb.NewEmpty(t)
	err := postgres.CheckSchema(context.Background(), d.Pool, latest(t))
	if !errors.Is(err, postgres.ErrSchemaMissing) {
		t.Fatalf("want ErrSchemaMissing, got %v", err)
	}
}

func TestCheckSchemaRefusesEmptyMigrationTable(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.NewEmpty(t)
	// O golang-migrate cria a tabela antes da primeira migration; uma queda
	// logo depois a deixa vazia.
	if _, err := d.Superuser(t).Exec(ctx, `
		CREATE TABLE schema_migrations (version bigint PRIMARY KEY, dirty boolean NOT NULL);
		GRANT SELECT ON schema_migrations TO ledger_app`); err != nil {
		t.Fatal(err)
	}
	err := postgres.CheckSchema(ctx, d.Pool, latest(t))
	if !errors.Is(err, postgres.ErrSchemaMissing) {
		t.Fatalf("want ErrSchemaMissing, got %v", err)
	}
}

func TestCheckSchemaRefusesDirtyDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	if _, err := d.Superuser(t).Exec(ctx, `UPDATE schema_migrations SET dirty = true`); err != nil {
		t.Fatal(err)
	}
	err := postgres.CheckSchema(ctx, d.Pool, latest(t))
	if !errors.Is(err, postgres.ErrSchemaDirty) {
		t.Fatalf("want ErrSchemaDirty, got %v", err)
	}
}

func TestCheckSchemaRefusesOlderAndNewerVersions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	want := latest(t)

	// Simula o binário uma versão à frente do banco.
	if err := postgres.CheckSchema(ctx, d.Pool, want+1); !errors.Is(err, postgres.ErrSchemaMismatch) {
		t.Fatalf("older database: want ErrSchemaMismatch, got %v", err)
	}
	// E depois o banco à frente do binário.
	if _, err := d.Superuser(t).Exec(ctx, `UPDATE schema_migrations SET version = version + 1`); err != nil {
		t.Fatal(err)
	}
	if err := postgres.CheckSchema(ctx, d.Pool, want); !errors.Is(err, postgres.ErrSchemaMismatch) {
		t.Fatalf("newer database: want ErrSchemaMismatch, got %v", err)
	}
}
