package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/brianashihara/coreledger/db/migrations"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/platform/config"
	"github.com/brianashihara/coreledger/internal/platform/logging"
	"github.com/brianashihara/coreledger/internal/postgres"
)

// errInconsistent faz o processo sair com código 1 quando a reconciliação
// encontra problemas, para que um cron ou o CI percebam sem ler o JSON. O
// relatório já foi impresso, então main não repete a mensagem.
var errInconsistent = errors.New("ledger is inconsistent")

// Entre as consultas da reconciliação não há espera nenhuma; um minuto parado
// só acontece com o processo travado ou a conexão perdida.
const reconcileIdleTimeout = time.Minute

// reconcileCmd roda as mesmas checagens do GET /v1/admin/reconciliation e
// imprime o mesmo JSON (SPEC 13). Só lê; nunca corrige nada.
func reconcileCmd(ctx context.Context, getenv func(string) string, stdout, logOut io.Writer) error {
	cfg, err := config.LoadReconcile(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	logger := logging.New(logOut, cfg.Log)

	// A reconciliação só lê, mas o snapshot é uma transação aberta: o mesmo
	// limite evita uma sessão órfã segurando recursos no servidor (ADR 0012).
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, postgres.PoolOptions{
		MaxConns:                 2,
		IdleInTransactionTimeout: reconcileIdleTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	want, err := migrations.Latest()
	if err != nil {
		return err
	}
	if err := postgres.CheckSchema(ctx, pool, want); err != nil {
		return err
	}

	report, err := reconcile.NewService(postgres.NewStore(pool)).Run(ctx)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(report.View()); err != nil {
		return err
	}

	if report.Status != reconcile.StatusOK {
		logger.Error("reconciliation found inconsistencies", "issues", len(report.Issues))
		return errInconsistent
	}
	return nil
}
