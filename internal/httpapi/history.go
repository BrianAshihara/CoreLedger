package httpapi

import (
	"encoding/base64"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/domain/ledger"
)

var errBadCursor = errors.New("invalid cursor")

// O cursor é opaco para o cliente: base64 de "seq:<account_seq>". O prefixo
// permite mudar o formato no futuro sem confundir cursores antigos.
const cursorPrefix = "seq:"

func encodeCursor(seq int64) string {
	return base64.RawURLEncoding.EncodeToString([]byte(cursorPrefix + strconv.FormatInt(seq, 10)))
}

func decodeCursor(s string) (int64, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return 0, errBadCursor
	}
	n, ok := strings.CutPrefix(string(raw), cursorPrefix)
	if !ok {
		return 0, errBadCursor
	}
	seq, err := strconv.ParseInt(n, 10, 64)
	// Sequências começam em 1, então um cursor válido nunca é menor que 2:
	// "sequências menores que 1" seria uma página vazia por construção.
	if err != nil || seq < 2 || strconv.FormatInt(seq, 10) != n {
		return 0, errBadCursor
	}
	return seq, nil
}

type historyEntryResponse struct {
	EntryID       string    `json:"entry_id"`
	TransactionID string    `json:"transaction_id"`
	Type          string    `json:"type"`
	Direction     string    `json:"direction"`
	Amount        int64     `json:"amount"`
	BalanceAfter  int64     `json:"balance_after"`
	Sequence      int64     `json:"sequence"`
	CreatedAt     time.Time `json:"created_at"`
}

type historyResponse struct {
	Data       []historyEntryResponse `json:"data"`
	NextCursor *string                `json:"next_cursor"`
}

// GET /v1/accounts/{id}/entries?limit=50&cursor=...
func (h *handlers) listEntries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	limit := accounts.DefaultHistoryLimit
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			h.fail(w, r, accounts.ErrInvalidLimit)
			return
		}
		limit = n
	}
	var before *int64
	if v := q.Get("cursor"); v != "" {
		seq, err := decodeCursor(v)
		if err != nil {
			h.fail(w, r, err)
			return
		}
		before = &seq
	}
	id, err := accountIDParam(r)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	page, err := h.accounts.History(r.Context(), id, before, limit)
	if err != nil {
		h.fail(w, r, err)
		return
	}

	resp := historyResponse{Data: make([]historyEntryResponse, 0, len(page.Entries))}
	for _, e := range page.Entries {
		resp.Data = append(resp.Data, toHistoryEntry(e))
	}
	if page.NextBeforeSeq != nil {
		c := encodeCursor(*page.NextBeforeSeq)
		resp.NextCursor = &c
	}
	writeJSON(w, http.StatusOK, resp)
}

// toHistoryEntry deriva a direção do sinal e mostra o valor absoluto
// (SPEC 12.3).
func toHistoryEntry(e ledger.HistoryEntry) historyEntryResponse {
	direction, amount := "CREDIT", e.Amount
	if e.Amount < 0 {
		direction, amount = "DEBIT", -e.Amount
	}
	return historyEntryResponse{
		EntryID:       e.ID.String(),
		TransactionID: e.TransactionID.String(),
		Type:          string(e.Type),
		Direction:     direction,
		Amount:        amount,
		BalanceAfter:  e.BalanceAfter,
		Sequence:      e.AccountSeq,
		CreatedAt:     e.CreatedAt.UTC(),
	}
}

// GET /v1/admin/reconciliation
//
// Responde 200 também quando encontra inconsistências: a checagem em si
// funcionou, e o resultado está no campo status. Inconsistências são logadas
// como ERROR, porque num ledger elas pedem atenção humana imediata.
func (h *handlers) reconciliation(w http.ResponseWriter, r *http.Request) {
	report, err := h.reconcile.Run(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	if len(report.Issues) > 0 {
		h.logger.ErrorContext(r.Context(), "reconciliation found inconsistencies",
			"request_id", RequestIDFrom(r.Context()),
			"issues", len(report.Issues))
	}
	writeJSON(w, http.StatusOK, report.View())
}
