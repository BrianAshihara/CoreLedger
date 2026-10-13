// Command resetdb recria do zero o banco usado pelo teste de carga (SPEC
// Fase 6) e aplica todas as migrations, como ledger_owner. Cada execução da
// carga começa do mesmo estado, então os números são comparáveis, e o banco
// de desenvolvimento nunca recebe dados de carga.
//
// Usa o TEST_DATABASE_URL (superusuário de teste) para criar o banco. Por
// segurança, só aceita nomes que começam com "ledger_load".
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/brianashihara/coreledger/internal/postgres"
)

func main() {
	name := flag.String("name", "ledger_load", "database to recreate (must start with ledger_load)")
	flag.Parse()
	if err := run(*name); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(name string) error {
	// Trava contra apagar o banco errado: este comando faz DROP DATABASE.
	if !strings.HasPrefix(name, "ledger_load") {
		return fmt.Errorf("refusing to recreate %q: the name must start with ledger_load", name)
	}
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		return fmt.Errorf("TEST_DATABASE_URL is required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return fmt.Errorf("connect with TEST_DATABASE_URL: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	ident := pgx.Identifier{name}.Sanitize()
	if _, err := conn.Exec(ctx, "DROP DATABASE IF EXISTS "+ident+" WITH (FORCE)"); err != nil {
		return fmt.Errorf("drop %s: %w", name, err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+ident+" OWNER ledger_owner"); err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}

	dbURL, err := url.Parse(admin)
	if err != nil {
		return fmt.Errorf("TEST_DATABASE_URL must be a postgres:// URL")
	}
	dbURL.Path = "/" + name

	// As migrations rodam como ledger_owner, igual ao banco de
	// desenvolvimento, para que donos e grants sejam os mesmos.
	db, err := postgres.OpenMigrationDB(dbURL.String(), func(ctx context.Context, c *pgx.Conn) error {
		_, err := c.Exec(ctx, "SET ROLE ledger_owner")
		return err
	})
	if err != nil {
		return err
	}
	m, err := postgres.NewMigrator(db)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()
	if err := m.Up(); err != nil {
		return fmt.Errorf("migrate %s: %w", name, err)
	}
	v, _, err := m.Version()
	if err != nil {
		return err
	}
	fmt.Printf("%s recreated at migration version %d\n", name, v)
	return nil
}
