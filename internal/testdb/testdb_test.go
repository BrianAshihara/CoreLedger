package testdb

import (
	"context"
	"testing"
)

func TestNewGivesIsolatedDatabasesAsAppRole(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	a := New(t)
	b := New(t)
	if a.Name == b.Name {
		t.Fatalf("both tests got database %s", a.Name)
	}

	for _, d := range []*DB{a, b} {
		var dbName, role string
		if err := d.Pool.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&dbName, &role); err != nil {
			t.Fatal(err)
		}
		if dbName != d.Name {
			t.Errorf("pool connected to %s, want %s", dbName, d.Name)
		}
		if role != AppRole {
			t.Errorf("pool runs as %s, want %s", role, AppRole)
		}
	}
}

func TestSuperuserBypassesRole(t *testing.T) {
	t.Parallel()
	d := New(t)

	var super bool
	err := d.Superuser(t).QueryRow(context.Background(),
		`SELECT rolsuper FROM pg_roles WHERE rolname = current_user`).Scan(&super)
	if err != nil {
		t.Fatal(err)
	}
	if !super {
		t.Fatal("Superuser pool does not run as a superuser")
	}
}

func TestNewEmptyHasNoSchema(t *testing.T) {
	t.Parallel()
	d := NewEmpty(t)

	var n int
	err := d.Superuser(t).QueryRow(context.Background(),
		`SELECT count(*) FROM pg_tables WHERE schemaname = 'public'`).Scan(&n)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("empty database has %d tables in public", n)
	}
}

func TestWithDatabase(t *testing.T) {
	got, err := withDatabase("postgres://u:p%40ss@localhost:5432/postgres?sslmode=disable", "test_1_ab")
	if err != nil {
		t.Fatal(err)
	}
	want := "postgres://u:p%40ss@localhost:5432/test_1_ab?sslmode=disable"
	if got != want {
		t.Fatalf("got %s, want %s", got, want)
	}
	if _, err := withDatabase("host=localhost dbname=postgres", "x"); err == nil {
		t.Fatal("keyword/value strings must be rejected")
	}
}
