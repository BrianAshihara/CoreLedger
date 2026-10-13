package postgres

import (
	"errors"
	"io"
	"net"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

const idleInTransactionTimeout = "25P03" // idle_in_transaction_session_timeout

// IsUnavailable informa se o erro significa que o banco está fora de alcance
// ou foi derrubado: conexão recusada, conexão perdida no meio de uma query,
// ou o servidor encerrando/reiniciando (classe 57 do SQLSTATE).
//
// Para o cliente isso é transitório (503 SERVICE_BUSY). Repetir com a mesma
// Idempotency-Key é sempre seguro, inclusive quando a conexão caiu durante o
// COMMIT e não dá para saber se ele aconteceu: se aconteceu, o retry vira
// replay; se não, o lançamento é feito.
func IsUnavailable(err error) bool {
	if err == nil {
		return false
	}
	var connectErr *pgconn.ConnectError
	if errors.As(err, &connectErr) {
		return true
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		// 57P01 admin_shutdown, 57P02 crash_shutdown, 57P03 cannot_connect_now...
		// 25P03: o servidor encerrou a sessão por ficar parada no meio da
		// transação (ADR 0012); nada foi gravado e repetir é seguro.
		return strings.HasPrefix(pgErr.Code, "57") || pgErr.Code == idleInTransactionTimeout
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		return true
	}
	if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
		return true
	}
	// pgx descreve uma conexão que já morreu com esta mensagem fixa.
	return strings.Contains(err.Error(), "conn closed")
}
