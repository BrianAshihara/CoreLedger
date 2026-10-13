package postgres

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brianashihara/coreledger/internal/postgres/sqlc"
)

// Store é a porta de entrada da aplicação para o banco. Os métodos de leitura
// e escrita simples rodam direto no pool; tudo que precisa de atomicidade ou
// de lock roda dentro de WithTx.
type Store struct {
	repo
	pool *pgxpool.Pool
}

func NewStore(pool *pgxpool.Pool) *Store {
	return &Store{repo: repo{q: sqlc.New(pool)}, pool: pool}
}

// Tx expõe os mesmos métodos do Store, mais os que só fazem sentido dentro de
// uma transação aberta (locks e escritas no ledger).
type Tx struct {
	repo
	db pgx.Tx
}

// Snapshot é uma transação somente leitura em REPEATABLE READ: todas as
// leituras enxergam o mesmo instante do banco, mesmo com lançamentos
// acontecendo em paralelo (SPEC 13).
type Snapshot struct {
	repo
}

// WithTx roda fn numa transação READ COMMITTED (SPEC 9.2). Se fn devolver
// erro, ou entrar em panic, a transação é desfeita; senão, é confirmada. As
// transações são abertas pelos serviços de aplicação, nunca pelos handlers
// (SPEC 14).
func (s *Store) WithTx(ctx context.Context, fn func(tx *Tx) error) error {
	return s.run(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted}, func(tx pgx.Tx) error {
		return fn(&Tx{repo: repo{q: sqlc.New(tx)}, db: tx})
	})
}

// ReadSnapshot roda fn numa transação REPEATABLE READ, READ ONLY.
func (s *Store) ReadSnapshot(ctx context.Context, fn func(snap *Snapshot) error) error {
	opts := pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}
	return s.run(ctx, opts, func(tx pgx.Tx) error {
		return fn(&Snapshot{repo: repo{q: sqlc.New(tx)}})
	})
}

func (s *Store) run(ctx context.Context, opts pgx.TxOptions, fn func(pgx.Tx) error) (err error) {
	tx, err := s.pool.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(context.Background())
			panic(p)
		}
		if err != nil {
			// Contexto novo: se ctx já expirou, o rollback ainda precisa
			// chegar ao banco para liberar os locks.
			if rbErr := tx.Rollback(context.Background()); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
				err = errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
			}
		}
	}()

	if err = fn(tx); err != nil {
		return err
	}
	// Os constraint triggers adiados (I1) rodam aqui: uma transação
	// desbalanceada falha no commit e nada fica gravado.
	if err = tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// repo concentra as queries; Store, Tx e Snapshot o embutem para oferecer os
// mesmos métodos de leitura dentro e fora de transação.
type repo struct {
	q *sqlc.Queries
}
