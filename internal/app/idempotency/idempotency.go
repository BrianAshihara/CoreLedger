// Package idempotency garante que a mesma Idempotency-Key nunca produza duas
// transações do ledger (SPEC 11, I7), inclusive com pedidos duplicados
// chegando ao mesmo tempo.
//
// A chave é reservada dentro da mesma transação de banco do lançamento e
// antes de qualquer lock de conta:
//
//   - chave livre: reserva, lança, confirma. Se o lançamento falhar, o
//     rollback libera a chave junto (pedidos recusados não ficam gravados);
//   - chave em uso pelo mesmo pedido: devolve a transação original (replay);
//   - chave em uso por outro pedido: conflito;
//   - chave reservada por um pedido ainda em andamento: o INSERT espera no
//     índice único até ele terminar e então cai num dos casos acima.
package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/postgres"
)

var (
	ErrMissingKey = errors.New("idempotency key is required")
	ErrInvalidKey = errors.New("idempotency key must be 1 to 255 printable ASCII characters")
	ErrConflict   = errors.New("idempotency key was already used with a different request")
)

const maxKeyLength = 255

// ValidateKey aplica o formato do SPEC 7.4. O banco tem um CHECK equivalente.
func ValidateKey(key string) error {
	if key == "" {
		return ErrMissingKey
	}
	if len(key) > maxKeyLength {
		return ErrInvalidKey
	}
	for i := 0; i < len(key); i++ {
		if key[i] < ' ' || key[i] > '~' {
			return ErrInvalidKey
		}
	}
	return nil
}

// Request identifica um pedido para fins de idempotência.
type Request struct {
	Key         string
	Method      string // sempre "POST" hoje
	Path        string // padrão da rota, por exemplo "/v1/transfers"
	Fingerprint [32]byte
}

// Fingerprint é o SHA-256 de "método rota\n" seguido do JSON do pedido já
// decodificado e validado (SPEC 11.1). Como o JSON vem de uma struct, a ordem
// dos campos é fixa: espaços e ordem das chaves no corpo original não geram
// conflitos falsos.
func Fingerprint(method, path string, body any) ([32]byte, error) {
	canonical, err := json.Marshal(body)
	if err != nil {
		return [32]byte{}, fmt.Errorf("fingerprint: %w", err)
	}
	h := sha256.New()
	_, _ = fmt.Fprintf(h, "%s %s\n", method, path)
	_, _ = h.Write(canonical)
	var sum [32]byte
	copy(sum[:], h.Sum(nil))
	return sum, nil
}

// Outcome diz qual transação responde ao pedido e se ela foi criada agora ou
// é a de um pedido anterior idêntico.
type Outcome struct {
	TransactionID uuid.UUID
	Replayed      bool
}

// errReplay desfaz a transação de banco quando a chave já estava em uso: nada
// foi escrito, mas o SPEC 11.2 manda encerrar com ROLLBACK.
var errReplay = errors.New("idempotency replay")

// Run reserva a chave e, se ela estiver livre, chama post na mesma transação
// de banco. transactionID é o ID que post vai usar para a transação do
// ledger; ele é gerado antes, para que a chave possa referenciá-lo.
func Run(ctx context.Context, store *postgres.Store, req Request, transactionID uuid.UUID,
	lockTimeout time.Duration, post func(tx *postgres.Tx) error,
) (Outcome, error) {
	var existing postgres.IdempotencyRecord
	err := store.WithTx(ctx, func(tx *postgres.Tx) error {
		// O lock_timeout vale também para a espera pela chave: um pedido
		// duplicado preso atrás de outro vira SERVICE_BUSY, não espera infinita.
		if err := tx.SetLockTimeout(ctx, lockTimeout); err != nil {
			return err
		}
		claimed, rec, err := tx.ClaimIdempotencyKey(ctx, postgres.IdempotencyRecord{
			Key:           req.Key,
			Method:        req.Method,
			Path:          req.Path,
			Fingerprint:   req.Fingerprint,
			TransactionID: transactionID,
		})
		if err != nil {
			return err
		}
		if !claimed {
			existing = rec
			return errReplay
		}
		return post(tx)
	})

	switch {
	case err == nil:
		return Outcome{TransactionID: transactionID}, nil
	case errors.Is(err, errReplay):
		if existing.Method != req.Method || existing.Path != req.Path || existing.Fingerprint != req.Fingerprint {
			return Outcome{}, ErrConflict
		}
		return Outcome{TransactionID: existing.TransactionID, Replayed: true}, nil
	default:
		return Outcome{}, err
	}
}
