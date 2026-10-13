// Package httpapi é o transporte HTTP: rotas, middlewares e handlers. Os
// handlers decodificam, chamam um serviço de aplicação e codificam a
// resposta; regra de negócio fica em outro lugar (SPEC 14).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
)

// Pinger informa se o banco responde. O *pgxpool.Pool o satisfaz; a interface
// existe para testar o readiness sem banco.
type Pinger interface {
	Ping(ctx context.Context) error
}

type Deps struct {
	Logger         *slog.Logger
	DB             Pinger
	RequestTimeout time.Duration
	Accounts       *accounts.Service
	Movements      *movements.Service
	Reconcile      *reconcile.Service
	AdminToken     string
}

const readinessTimeout = time.Second

func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	// A ordem importa: o request ID precisa existir antes de qualquer log, e o
	// access log precisa enxergar o status escrito pelo recoverer.
	r.Use(requestID)
	r.Use(accessLog(d.Logger))
	r.Use(recoverer(d.Logger))
	r.Use(timeout(d.RequestTimeout))

	// Definidos antes das sub-rotas, para que o chi os herde nelas.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errRouteNotFound) })
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) { writeError(w, r, errMethodNotAllowed) })

	// Endpoints operacionais não têm versão (SPEC 12.1).
	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(d.DB, d.Logger))

	h := &handlers{logger: d.Logger, accounts: d.Accounts, movements: d.Movements, reconcile: d.Reconcile}
	r.Route("/v1", func(r chi.Router) {
		r.Post("/accounts", h.createAccount)
		r.Get("/accounts/{id}", h.getAccount)
		r.Get("/accounts/{id}/balance", h.getBalance)
		r.Get("/accounts/{id}/entries", h.listEntries)
		r.Post("/transfers", h.createTransfer)
		r.Get("/transactions/{id}", h.getTransaction)

		// Tudo sob /v1/admin exige o token, inclusive rotas inexistentes:
		// sem token, o cliente não descobre nem quais rotas de admin existem.
		r.Route("/admin", func(r chi.Router) {
			r.Use(requireAdmin(d.AdminToken))
			r.Patch("/accounts/{id}/status", h.changeAccountStatus)
			r.Post("/deposits", h.createDeposit)
			r.Post("/withdrawals", h.createWithdrawal)
			r.Get("/reconciliation", h.reconciliation)
		})
	})

	return r
}

// healthz informa que o processo está vivo. Nunca toca em dependências, para
// que uma queda do banco não faça o processo parecer morto.
func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz informa se o processo pode receber tráfego, ou seja, se o banco
// responde dentro de readinessTimeout.
func readyz(db Pinger, logger *slog.Logger) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readinessTimeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			logger.WarnContext(r.Context(), "readiness check failed",
				"request_id", RequestIDFrom(r.Context()), "error", err)
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
	}
}
