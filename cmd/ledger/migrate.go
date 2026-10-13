package main

import (
	"fmt"
	"io"
	"strconv"

	"github.com/brianashihara/coreledger/internal/platform/config"
	"github.com/brianashihara/coreledger/internal/platform/logging"
	"github.com/brianashihara/coreledger/internal/postgres"
)

func migrateCmd(args []string, getenv func(string) string, stdout, logOut io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("%w: migrate needs a subcommand", errUsage)
	}
	op, err := parseMigrateArgs(args)
	if err != nil {
		return err
	}

	cfg, err := config.LoadMigrate(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	logger := logging.New(logOut, cfg.Log)

	db, err := postgres.OpenMigrationDB(cfg.DatabaseURL, nil)
	if err != nil {
		return err
	}
	m, err := postgres.NewMigrator(db)
	if err != nil {
		return err
	}
	defer func() { _ = m.Close() }()

	switch op.kind {
	case "up":
		err = m.Up()
	case "down":
		logger.Warn("reverting migrations", "steps", op.steps)
		err = m.Down(op.steps)
	case "down-all":
		logger.Warn("reverting ALL migrations")
		err = m.DownAll()
	case "version":
	}
	if err != nil {
		return fmt.Errorf("migrate %s: %w", op.kind, err)
	}

	v, dirty, err := m.Version()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(stdout, "version %d (dirty: %v)\n", v, dirty)
	return err
}

type migrateOp struct {
	kind  string // up, down, down-all, version
	steps int
}

func parseMigrateArgs(args []string) (migrateOp, error) {
	switch {
	case len(args) == 1 && args[0] == "up":
		return migrateOp{kind: "up"}, nil
	case len(args) == 1 && args[0] == "version":
		return migrateOp{kind: "version"}, nil
	case len(args) == 2 && args[0] == "down" && args[1] == "--all":
		return migrateOp{kind: "down-all"}, nil
	case len(args) == 2 && args[0] == "down":
		n, err := strconv.Atoi(args[1])
		if err != nil || n < 1 {
			return migrateOp{}, fmt.Errorf("%w: migrate down needs a positive step count or --all, got %q", errUsage, args[1])
		}
		return migrateOp{kind: "down", steps: n}, nil
	case len(args) >= 1 && args[0] == "down":
		// Um `down` sem contagem é justamente o acidente que este comando evita.
		return migrateOp{}, fmt.Errorf("%w: migrate down needs a step count (e.g. `migrate down 1`) or --all", errUsage)
	default:
		return migrateOp{}, fmt.Errorf("%w: unknown migrate arguments %q", errUsage, args)
	}
}
