package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/golang-migrate/migrate/v4"
	migratepgx "github.com/golang-migrate/migrate/v4/database/pgx/v5"
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/brianashihara/coreledger/db/migrations"
)

// Migrator aplica as migrations embutidas no binário.
type Migrator struct {
	m *migrate.Migrate
}

// OpenMigrationDB abre um handle database/sql para o golang-migrate.
// afterConnect pode ser nil; o helper de teste o usa para rodar as migrations
// como ledger_owner via SET ROLE, igual ao `ledger migrate` em desenvolvimento.
func OpenMigrationDB(databaseURL string, afterConnect func(context.Context, *pgx.Conn) error) (*sql.DB, error) {
	connCfg, err := pgx.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("invalid database URL")
	}
	var opts []stdlib.OptionOpenDB
	if afterConnect != nil {
		opts = append(opts, stdlib.OptionAfterConnect(afterConnect))
	}
	return stdlib.OpenDB(*connCfg, opts...), nil
}

// NewMigrator assume a posse de db: Close também o fecha.
func NewMigrator(db *sql.DB) (*Migrator, error) {
	src, err := iofs.New(migrations.FS, ".")
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("load embedded migrations: %w", err)
	}
	driver, err := migratepgx.WithInstance(db, &migratepgx.Config{})
	if err != nil {
		_ = src.Close()
		_ = db.Close()
		return nil, fmt.Errorf("open migration driver: %w", err)
	}
	m, err := migrate.NewWithInstance("iofs", src, "pgx5", driver)
	if err != nil {
		_ = src.Close()
		_ = driver.Close() // também fecha db
		return nil, fmt.Errorf("create migrator: %w", err)
	}
	return &Migrator{m: m}, nil
}

// Up aplica todas as migrations pendentes. Já estar atualizado não é erro.
func (mg *Migrator) Up() error {
	if err := mg.m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Down reverte exatamente n migrations (n >= 1).
func (mg *Migrator) Down(n int) error {
	if n < 1 {
		return fmt.Errorf("down: step count must be at least 1, got %d", n)
	}
	return mg.m.Steps(-n)
}

// DownAll reverte todas as migrations. Apaga todos os dados.
func (mg *Migrator) DownAll() error {
	if err := mg.m.Down(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return err
	}
	return nil
}

// Version devolve a versão aplicada; 0 significa nenhuma migration aplicada.
func (mg *Migrator) Version() (version uint, dirty bool, err error) {
	version, dirty, err = mg.m.Version()
	if errors.Is(err, migrate.ErrNilVersion) {
		return 0, false, nil
	}
	return version, dirty, err
}

func (mg *Migrator) Close() error {
	srcErr, dbErr := mg.m.Close()
	return errors.Join(srcErr, dbErr)
}
