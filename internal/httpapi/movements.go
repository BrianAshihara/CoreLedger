package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/idempotency"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
)

const (
	idempotencyKeyHeader = "Idempotency-Key"
	replayedHeader       = "Idempotent-Replayed"
)

// transactionResponse é a resposta de depósito, saque e transferência
// (SPEC 12.3). O status é sempre COMPLETED: no ledger só existem fatos
// lançados (SPEC 7.2); o campo existe para compatibilidade futura.
type transactionResponse struct {
	TransactionID string    `json:"transaction_id"`
	Type          string    `json:"type"`
	Status        string    `json:"status"`
	Amount        int64     `json:"amount"`
	Currency      string    `json:"currency"`
	CreatedAt     time.Time `json:"created_at"`
}

// entryResponse é uma perna em GET /v1/transactions/{id}. Contas de sistema
// aparecem pelo código (account_id null). Não expõe balance_after nem a
// sequência: a rota é pública, e isso revelaria o saldo da contraparte.
type entryResponse struct {
	AccountID  *string `json:"account_id"`
	SystemCode string  `json:"system_code,omitempty"`
	Amount     int64   `json:"amount"`
}

type transactionDetailResponse struct {
	transactionResponse
	Entries []entryResponse `json:"entries"`
}

func toTransactionResponse(t ledger.PostedTransaction) transactionResponse {
	return transactionResponse{
		TransactionID: t.ID.String(),
		Type:          string(t.Type),
		Status:        "COMPLETED",
		Amount:        t.Amount(),
		Currency:      string(t.Currency),
		CreatedAt:     t.CreatedAt.UTC(),
	}
}

// moneyRequest lê os passos 1 e 2 da ordem de validação (SPEC 10): corpo
// bem formado e Idempotency-Key presente. O amount chega cru e só é
// interpretado depois da chave: um erro nele é INVALID_AMOUNT (passo 3), não
// INVALID_REQUEST.
func moneyRequest(w http.ResponseWriter, r *http.Request, dst any, rawAmount *json.RawMessage) (key string, amount int64, err error) {
	if err := decodeJSON(w, r, dst); err != nil {
		return "", 0, err
	}
	key = r.Header.Get(idempotencyKeyHeader)
	if err := idempotency.ValidateKey(key); err != nil {
		return "", 0, err
	}
	amount, err = parseAmount(*rawAmount)
	return key, amount, err
}

// POST /v1/transfers
func (h *handlers) createTransfer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FromAccountID string          `json:"from_account_id"`
		ToAccountID   string          `json:"to_account_id"`
		Amount        json.RawMessage `json:"amount"`
		Currency      string          `json:"currency"`
	}
	key, amount, err := moneyRequest(w, r, &req, &req.Amount)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := h.movements.Transfer(r.Context(), movements.TransferInput{
		IdempotencyKey: key,
		FromAccountID:  req.FromAccountID,
		ToAccountID:    req.ToAccountID,
		Amount:         amount,
		Currency:       req.Currency,
	})
	h.respondMovement(w, r, key, res, err)
}

// POST /v1/admin/deposits
func (h *handlers) createDeposit(w http.ResponseWriter, r *http.Request) {
	h.external(w, r, h.movements.Deposit)
}

// POST /v1/admin/withdrawals
func (h *handlers) createWithdrawal(w http.ResponseWriter, r *http.Request) {
	h.external(w, r, h.movements.Withdraw)
}

func (h *handlers) external(w http.ResponseWriter, r *http.Request,
	op func(ctx context.Context, in movements.ExternalInput) (movements.Result, error),
) {
	var req struct {
		AccountID string          `json:"account_id"`
		Amount    json.RawMessage `json:"amount"`
		Currency  string          `json:"currency"`
	}
	key, amount, err := moneyRequest(w, r, &req, &req.Amount)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	res, err := op(r.Context(), movements.ExternalInput{
		IdempotencyKey: key,
		AccountID:      req.AccountID,
		Amount:         amount,
		Currency:       req.Currency,
	})
	h.respondMovement(w, r, key, res, err)
}

// respondMovement responde 201 para um lançamento novo e 200 com
// Idempotent-Replayed: true para um replay (SPEC 11.4). Registra no log o ID
// da transação e a chave, nunca o corpo do pedido (SPEC 14.4).
func (h *handlers) respondMovement(w http.ResponseWriter, r *http.Request, key string, res movements.Result, err error) {
	if err != nil {
		h.fail(w, r, err)
		return
	}
	h.logger.InfoContext(r.Context(), "money movement",
		"request_id", RequestIDFrom(r.Context()),
		"transaction_id", res.Transaction.ID.String(),
		"type", string(res.Transaction.Type),
		"idempotency_key", key,
		"replayed", res.Replayed)

	status := http.StatusCreated
	if res.Replayed {
		status = http.StatusOK
		w.Header().Set(replayedHeader, "true")
	}
	writeJSON(w, status, toTransactionResponse(res.Transaction))
}

// GET /v1/transactions/{id}
func (h *handlers) getTransaction(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		h.fail(w, r, ledger.ErrTransactionNotFound) // ID malformado não pode existir (SPEC 12.1)
		return
	}
	t, err := h.movements.Transaction(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	resp := transactionDetailResponse{
		transactionResponse: toTransactionResponse(t),
		Entries:             make([]entryResponse, 0, len(t.Entries)),
	}
	for _, e := range t.Entries {
		entry := entryResponse{Amount: e.Amount, SystemCode: e.SystemCode}
		if e.SystemCode == "" {
			id := e.AccountID.String()
			entry.AccountID = &id
		}
		resp.Entries = append(resp.Entries, entry)
	}
	writeJSON(w, http.StatusOK, resp)
}
