package httpapi

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
)

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// writeError escreve o envelope de erro padrão (SPEC 12.4).
func writeError(w http.ResponseWriter, r *http.Request, e apiError) {
	writeJSON(w, e.Status, errorBody{Error: errorDetail{
		Code:      e.Code,
		Message:   e.Message,
		RequestID: RequestIDFrom(r.Context()),
	}})
}

// handlers agrupa as dependências dos handlers de negócio.
type handlers struct {
	logger    *slog.Logger
	accounts  *accounts.Service
	movements *movements.Service
	reconcile *reconcile.Service
}

// fail responde com o erro de API correspondente. Erros internos são logados
// com a causa real, que nunca vai para o cliente.
func (h *handlers) fail(w http.ResponseWriter, r *http.Request, err error) {
	e := toAPIError(err)
	switch {
	case e == errServiceBusy:
		// Transitório: avisa o cliente que pode tentar de novo em 1 segundo.
		w.Header().Set("Retry-After", "1")
		h.logger.WarnContext(r.Context(), "request failed: service busy",
			"request_id", RequestIDFrom(r.Context()),
			"error", err)
	case e.Status >= http.StatusInternalServerError:
		h.logger.ErrorContext(r.Context(), "request failed",
			"request_id", RequestIDFrom(r.Context()),
			"error", err)
	}
	writeError(w, r, e)
}
