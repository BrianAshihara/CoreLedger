package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/idempotency"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
)

// apiError é um erro como o cliente o vê: status HTTP, código estável e
// mensagem fixa por código (SPEC 12.4). Detalhes internos vão só para o log.
type apiError struct {
	Status  int
	Code    string
	Message string
}

var (
	errInvalidRequest          = apiError{http.StatusBadRequest, "INVALID_REQUEST", "The request is invalid"}
	errInvalidAmount           = apiError{http.StatusBadRequest, "INVALID_AMOUNT", "Amount must be an integer greater than 0 and at most 1000000000000"}
	errMissingIdempotencyKey   = apiError{http.StatusBadRequest, "MISSING_IDEMPOTENCY_KEY", "The Idempotency-Key header is required"}
	errInvalidCursor           = apiError{http.StatusBadRequest, "INVALID_CURSOR", "The pagination cursor is invalid"}
	errUnauthorized            = apiError{http.StatusUnauthorized, "UNAUTHORIZED", "Missing or invalid credentials"}
	errAccountNotFound         = apiError{http.StatusNotFound, "ACCOUNT_NOT_FOUND", "Account not found"}
	errTransactionNotFound     = apiError{http.StatusNotFound, "TRANSACTION_NOT_FOUND", "Transaction not found"}
	errRouteNotFound           = apiError{http.StatusNotFound, "ROUTE_NOT_FOUND", "Route not found"}
	errMethodNotAllowed        = apiError{http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "Method not allowed for this route"}
	errIdempotencyConflict     = apiError{http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different request"}
	errInvalidStatusTransition = apiError{http.StatusConflict, "INVALID_STATUS_TRANSITION", "Invalid account status transition"}
	errAccountHasBalance       = apiError{http.StatusConflict, "ACCOUNT_HAS_BALANCE", "Account balance must be zero to close it"}
	errUnsupportedMediaType    = apiError{http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json"}
	errUnsupportedCurrency     = apiError{http.StatusUnprocessableEntity, "UNSUPPORTED_CURRENCY", "Currency is not supported"}
	errAccountNotActive        = apiError{http.StatusUnprocessableEntity, "ACCOUNT_NOT_ACTIVE", "Account is not active"}
	errCurrencyMismatch        = apiError{http.StatusUnprocessableEntity, "CURRENCY_MISMATCH", "Account currency does not match the request currency"}
	errInsufficientFunds       = apiError{http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS", "Insufficient account balance"}
	errSameAccountTransfer     = apiError{http.StatusUnprocessableEntity, "SAME_ACCOUNT_TRANSFER", "Source and destination accounts must be different"}
	errInternal                = apiError{http.StatusInternalServerError, "INTERNAL_ERROR", "Internal server error"}
	errServiceBusy             = apiError{http.StatusServiceUnavailable, "SERVICE_BUSY", "The service is busy, retry shortly"}
)

// toAPIError traduz erros das camadas internas para o contrato da API.
// Qualquer erro não reconhecido vira INTERNAL_ERROR: na dúvida, não se vaza
// nada. Isso inclui constraints do banco disparando e postings malformadas,
// que indicam bug na validação em Go (SPEC 12.4).
func toAPIError(err error) apiError {
	switch {
	case errors.Is(err, errMalformedBody),
		errors.Is(err, account.ErrInvalidOwnerID),
		errors.Is(err, account.ErrInvalidStatus),
		errors.Is(err, money.ErrCurrencyRequired),
		errors.Is(err, idempotency.ErrInvalidKey),
		errors.Is(err, accounts.ErrInvalidLimit):
		return errInvalidRequest
	case errors.Is(err, errBadCursor):
		return errInvalidCursor
	case errors.Is(err, money.ErrInvalidAmount):
		return errInvalidAmount
	case errors.Is(err, idempotency.ErrMissingKey):
		return errMissingIdempotencyKey
	case errors.Is(err, errUnsupportedMedia):
		return errUnsupportedMediaType
	case errors.Is(err, account.ErrNotFound):
		return errAccountNotFound
	case errors.Is(err, ledger.ErrTransactionNotFound):
		return errTransactionNotFound
	case errors.Is(err, idempotency.ErrConflict):
		return errIdempotencyConflict
	case errors.Is(err, account.ErrInvalidStatusTransition):
		return errInvalidStatusTransition
	case errors.Is(err, account.ErrHasBalance):
		return errAccountHasBalance
	case errors.Is(err, money.ErrUnsupportedCurrency):
		return errUnsupportedCurrency
	case errors.Is(err, account.ErrNotActive):
		return errAccountNotActive
	case errors.Is(err, ledger.ErrCurrencyMismatch):
		return errCurrencyMismatch
	case errors.Is(err, ledger.ErrInsufficientFunds):
		return errInsufficientFunds
	case errors.Is(err, ledger.ErrSameAccountTransfer):
		return errSameAccountTransfer
	// Transitórios: lock preso, prazo da requisição estourado ou banco fora do
	// ar (conexão recusada, derrubada ou servidor reiniciando). O cliente pode
	// tentar de novo com a mesma chave, sem risco de duplicar.
	case errors.Is(err, postgres.ErrLockTimeout),
		errors.Is(err, context.DeadlineExceeded),
		postgres.IsUnavailable(err):
		return errServiceBusy
	default:
		return errInternal
	}
}
