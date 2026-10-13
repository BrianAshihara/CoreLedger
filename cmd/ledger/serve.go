package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/brianashihara/coreledger/db/migrations"
	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/httpapi"
	"github.com/brianashihara/coreledger/internal/platform/config"
	"github.com/brianashihara/coreledger/internal/platform/logging"
	"github.com/brianashihara/coreledger/internal/postgres"
)

const shutdownTimeout = 15 * time.Second

func serve(ctx context.Context, getenv func(string) string, logOut io.Writer) error {
	cfg, err := config.LoadServe(getenv)
	if err != nil {
		return fmt.Errorf("invalid configuration:\n%w", err)
	}
	logger := logging.New(logOut, cfg.Log)

	// Uma transação parada por mais tempo que o prazo da requisição não serve
	// a mais ninguém: o servidor a encerra e libera os locks (ADR 0012).
	pool, err := postgres.NewPool(ctx, cfg.DatabaseURL, postgres.PoolOptions{
		MaxConns:                 cfg.DBMaxConns,
		IdleInTransactionTimeout: cfg.RequestTimeout,
	})
	if err != nil {
		return err
	}
	defer pool.Close()

	want, err := migrations.Latest()
	if err != nil {
		return err
	}
	// A API nunca migra sozinha (SPEC 15.2): recusa rodar contra um schema
	// para o qual este binário não foi feito.
	if err := postgres.CheckSchema(ctx, pool, want); err != nil {
		return err
	}

	store := postgres.NewStore(pool)
	handler := httpapi.NewRouter(httpapi.Deps{
		Logger:         logger,
		DB:             pool,
		RequestTimeout: cfg.RequestTimeout,
		Accounts:       accounts.NewService(store),
		Movements:      movements.NewService(store, posting.New(cfg.DBLockTimeout), cfg.DBLockTimeout),
		Reconcile:      reconcile.NewService(store),
		AdminToken:     cfg.AdminToken,
	})
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		// Deixa margem para escrever a resposta depois do prazo da requisição.
		WriteTimeout: cfg.RequestTimeout + 5*time.Second,
		IdleTimeout:  60 * time.Second,
		ErrorLog:     slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	logger.Info("server started", "addr", ln.Addr().String(), "schema_version", want)

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	// Para de aceitar conexões e deixa as requisições em andamento terminarem.
	logger.Info("shutting down", "timeout", shutdownTimeout)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	logger.Info("server stopped")
	return nil
}
