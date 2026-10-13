// Package postgres concentra toda interação com o PostgreSQL: pools de
// conexão, migrations, checagem de schema e (a partir da Fase 2)
// repositórios. É o único pacote que importa o código gerado pelo sqlc.
package postgres

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PoolOptions configura NewPool.
type PoolOptions struct {
	MaxConns int32

	// IdleInTransactionTimeout faz o próprio PostgreSQL encerrar uma sessão
	// que fica parada no meio de uma transação por mais tempo que isso,
	// liberando os locks dela (ADR 0012). Zero mantém o padrão do servidor.
	//
	// Protege contra conexões órfãs: se a rede cai do lado da API sem o
	// servidor perceber, a transação aberta seguraria os locks das contas
	// indefinidamente. Nossas transações nunca ficam paradas de propósito; os
	// comandos são enviados um atrás do outro.
	IdleInTransactionTimeout time.Duration
}

// NewPool abre um pool e verifica se o banco responde, para que o `serve`
// falhe na inicialização e não na primeira requisição.
func NewPool(ctx context.Context, databaseURL string, opts PoolOptions) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		// Erros do pgx podem incluir a connection string; a mensagem fica genérica.
		return nil, fmt.Errorf("invalid database URL")
	}
	cfg.MaxConns = opts.MaxConns
	if opts.IdleInTransactionTimeout > 0 {
		// Enviado na abertura de cada conexão; vale para a sessão inteira.
		cfg.ConnConfig.RuntimeParams["idle_in_transaction_session_timeout"] =
			strconv.FormatInt(opts.IdleInTransactionTimeout.Milliseconds(), 10)
	}

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect to database %s:%d/%s: %w",
			cfg.ConnConfig.Host, cfg.ConnConfig.Port, cfg.ConnConfig.Database, err)
	}
	return pool, nil
}
