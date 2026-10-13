package postgres_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/brianashihara/coreledger/internal/testdb"
)

// expectedAppPrivileges é o conjunto completo de privilégios de tabela que o
// ledger_app deve ter (SPEC 15.1.1). Toda migration que cria tabela ou
// concede privilégio atualiza este map: privilégio faltando ou sobrando
// derruba o teste.
var expectedAppPrivileges = map[string][]string{
	"currencies":        {"SELECT"},
	"schema_migrations": {"SELECT"},
	// UPDATE só nas colunas que mudam com o tempo; currency, kind e owner_id
	// nunca podem ser reescritos pela aplicação.
	"accounts": {
		"INSERT", "SELECT",
		"UPDATE(balance)", "UPDATE(last_entry_seq)", "UPDATE(status)", "UPDATE(updated_at)",
	},
	// O ledger só cresce: a aplicação lê e insere, nunca altera nem apaga (I4).
	"ledger_transactions": {"INSERT", "SELECT"},
	"ledger_entries":      {"INSERT", "SELECT"},
	"idempotency_keys":    {"INSERT", "SELECT"},
}

func TestAppRolePrivilegesAreExactlyTheExpectedOnes(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)

	// Lê as ACLs direto do catálogo: o information_schema só mostra grants
	// de papéis dos quais o usuário atual é membro. Grants por coluna
	// aparecem como, por exemplo, "UPDATE(balance)".
	rows, err := d.Superuser(t).Query(context.Background(), `
		WITH app AS (SELECT oid FROM pg_roles WHERE rolname = $1)
		SELECT c.relname, a.privilege_type
		FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL aclexplode(c.relacl) a
		WHERE n.nspname = 'public' AND a.grantee = (SELECT oid FROM app)
		UNION ALL
		SELECT c.relname, a.privilege_type || '(' || att.attname || ')'
		FROM pg_attribute att
		JOIN pg_class c ON c.oid = att.attrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		CROSS JOIN LATERAL aclexplode(att.attacl) a
		WHERE n.nspname = 'public' AND att.attnum > 0 AND NOT att.attisdropped
		  AND a.grantee = (SELECT oid FROM app)
		ORDER BY 1, 2`, testdb.AppRole)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()

	got := map[string][]string{}
	for rows.Next() {
		var table, privilege string
		if err := rows.Scan(&table, &privilege); err != nil {
			t.Fatal(err)
		}
		got[table] = append(got[table], privilege)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}

	tables := slices.Sorted(maps.Keys(expectedAppPrivileges))
	for table := range got {
		if !slices.Contains(tables, table) {
			tables = append(tables, table)
		}
	}
	for _, table := range tables {
		// Ordena em Go: a ordenação do banco depende do collation do servidor.
		want := strings.Join(slices.Sorted(slices.Values(expectedAppPrivileges[table])), ",")
		have := strings.Join(slices.Sorted(slices.Values(got[table])), ",")
		if want != have {
			t.Errorf("ledger_app on %s: have [%s], want [%s]", table, have, want)
		}
	}
}

func TestSchemaPrivileges(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)

	var appUsage, appCreate, publicUsage bool
	err := d.Superuser(t).QueryRow(context.Background(), `
		SELECT has_schema_privilege($1, 'public', 'USAGE'),
		       has_schema_privilege($1, 'public', 'CREATE'),
		       EXISTS (SELECT 1 FROM pg_namespace n, aclexplode(n.nspacl) a
		               WHERE n.nspname = 'public' AND a.grantee = 0)`, testdb.AppRole,
	).Scan(&appUsage, &appCreate, &publicUsage)
	if err != nil {
		t.Fatal(err)
	}
	if !appUsage || appCreate || publicUsage {
		t.Fatalf("app USAGE=%v (want true), app CREATE=%v (want false), PUBLIC has privileges=%v (want false)",
			appUsage, appCreate, publicUsage)
	}
}

func TestAppRoleCannotWriteOrCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)

	var n int
	if err := d.Pool.QueryRow(ctx, `SELECT count(*) FROM currencies`).Scan(&n); err != nil {
		t.Fatalf("app must read currencies: %v", err)
	}

	for _, stmt := range []string{
		`INSERT INTO currencies (code, exponent) VALUES ('USD', 2)`,
		`UPDATE currencies SET exponent = 3`,
		`DELETE FROM currencies`,
		`INSERT INTO schema_migrations (version, dirty) VALUES (999, false)`,
		`UPDATE schema_migrations SET dirty = true`,
		`CREATE TABLE sneaky (id int)`,
	} {
		_, err := d.Pool.Exec(ctx, stmt)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42501" { // insufficient_privilege
			t.Errorf("%s: want permission denied (42501), got %v", stmt, err)
		}
	}
}
