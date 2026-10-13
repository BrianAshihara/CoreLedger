package postgres_test

import (
	"context"
	"testing"

	"github.com/brianashihara/coreledger/db/migrations"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

func TestMigrationsUpDownUp(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.NewEmpty(t)
	super := d.Superuser(t)

	latest, err := migrations.Latest()
	if err != nil {
		t.Fatal(err)
	}

	m, err := postgres.NewMigrator(d.OwnerSQL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	assertVersion := func(want uint) {
		t.Helper()
		v, dirty, err := m.Version()
		if err != nil {
			t.Fatal(err)
		}
		if v != want || dirty {
			t.Fatalf("version=%d dirty=%v, want version=%d clean", v, dirty, want)
		}
	}
	tableExists := func(name string) bool {
		t.Helper()
		var ok bool
		if err := super.QueryRow(ctx, `SELECT to_regclass($1) IS NOT NULL`, "public."+name).Scan(&ok); err != nil {
			t.Fatal(err)
		}
		return ok
	}

	assertVersion(0)

	if err := m.Up(); err != nil {
		t.Fatal(err)
	}
	assertVersion(latest)
	if !tableExists("currencies") {
		t.Fatal("currencies missing after up")
	}

	if err := m.Up(); err != nil {
		t.Fatalf("second Up must be a no-op: %v", err)
	}

	if err := m.Down(1); err != nil {
		t.Fatal(err)
	}
	assertVersion(latest - 1)

	if err := m.DownAll(); err != nil {
		t.Fatal(err)
	}
	assertVersion(0)
	if tableExists("currencies") {
		t.Fatal("currencies still exists after down --all")
	}

	if err := m.Up(); err != nil {
		t.Fatalf("up after down must succeed: %v", err)
	}
	assertVersion(latest)
}

func TestDownRequiresPositiveCount(t *testing.T) {
	t.Parallel()
	d := testdb.NewEmpty(t)
	m, err := postgres.NewMigrator(d.OwnerSQL(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = m.Close() }()

	for _, n := range []int{0, -1} {
		if err := m.Down(n); err == nil {
			t.Errorf("Down(%d) must fail", n)
		}
	}
}

func TestMigrationsAreOwnedByOwnerRole(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)

	rows, err := d.Superuser(t).Query(context.Background(),
		`SELECT tablename, tableowner FROM pg_tables WHERE schemaname = 'public'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var table, owner string
		if err := rows.Scan(&table, &owner); err != nil {
			t.Fatal(err)
		}
		n++
		if owner != testdb.OwnerRole {
			t.Errorf("table %s owned by %s, want %s", table, owner, testdb.OwnerRole)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no tables found")
	}
}
