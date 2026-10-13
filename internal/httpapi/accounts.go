package httpapi

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/domain/account"
)

type accountResponse struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	Currency  string    `json:"currency"`
	Status    string    `json:"status"`
	Balance   int64     `json:"balance"`
	CreatedAt time.Time `json:"created_at"`
}

type balanceResponse struct {
	AccountID string `json:"account_id"`
	Currency  string `json:"currency"`
	Balance   int64  `json:"balance"`
}

func toAccountResponse(a account.Account) accountResponse {
	return accountResponse{
		ID:        a.ID.String(),
		OwnerID:   a.OwnerID,
		Currency:  string(a.Currency),
		Status:    string(a.Status),
		Balance:   a.Balance,
		CreatedAt: a.CreatedAt.UTC(),
	}
}

// POST /v1/accounts
func (h *handlers) createAccount(w http.ResponseWriter, r *http.Request) {
	var req struct {
		OwnerID  string `json:"owner_id"`
		Currency string `json:"currency"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	a, err := h.accounts.Create(r.Context(), accounts.CreateInput{OwnerID: req.OwnerID, Currency: req.Currency})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/v1/accounts/"+a.ID.String())
	writeJSON(w, http.StatusCreated, toAccountResponse(a))
}

// GET /v1/accounts/{id}
func (h *handlers) getAccount(w http.ResponseWriter, r *http.Request) {
	a, err := h.accountFromPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountResponse(a))
}

// GET /v1/accounts/{id}/balance
func (h *handlers) getBalance(w http.ResponseWriter, r *http.Request) {
	a, err := h.accountFromPath(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, balanceResponse{
		AccountID: a.ID.String(),
		Currency:  string(a.Currency),
		Balance:   a.Balance,
	})
}

// PATCH /v1/admin/accounts/{id}/status
func (h *handlers) changeAccountStatus(w http.ResponseWriter, r *http.Request) {
	id, err := accountIDParam(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	var req struct {
		Status string `json:"status"`
	}
	if err := decodeJSON(w, r, &req); err != nil {
		h.fail(w, r, err)
		return
	}
	a, err := h.accounts.ChangeStatus(r.Context(), id, req.Status)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, toAccountResponse(a))
}

func (h *handlers) accountFromPath(r *http.Request) (account.Account, error) {
	id, err := accountIDParam(r)
	if err != nil {
		return account.Account{}, err
	}
	return h.accounts.Get(r.Context(), id)
}

// accountIDParam trata um ID malformado como conta inexistente (SPEC 12.1):
// um ID que não é UUID não pode existir.
func accountIDParam(r *http.Request) (uuid.UUID, error) {
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		return uuid.Nil, account.ErrNotFound
	}
	return id, nil
}
