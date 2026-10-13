package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/app/movements"
	"github.com/brianashihara/coreledger/internal/app/posting"
	"github.com/brianashihara/coreledger/internal/app/reconcile"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

const testAdminToken = "test-admin-token-0123456789"

type api struct {
	t  *testing.T
	h  http.Handler
	db *testdb.DB
}

func newAPI(t *testing.T, opts ...testdb.Option) *api {
	t.Helper()
	return newAPIWithLockTimeout(t, 2*time.Second, opts...)
}

func newAPIWithLockTimeout(t *testing.T, lockTimeout time.Duration, opts ...testdb.Option) *api {
	t.Helper()
	d := testdb.New(t, opts...)
	store := postgres.NewStore(d.Pool)
	h := NewRouter(Deps{
		Logger:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		DB:             d.Pool,
		RequestTimeout: 5 * time.Second,
		Accounts:       accounts.NewService(store),
		Movements:      movements.NewService(store, posting.New(lockTimeout), lockTimeout),
		Reconcile:      reconcile.NewService(store),
		AdminToken:     testAdminToken,
	})
	return &api{t: t, h: h, db: d}
}

type reqOpt func(*http.Request)

func withHeader(k, v string) reqOpt { return func(r *http.Request) { r.Header.Set(k, v) } }
func asAdmin() reqOpt               { return withHeader("Authorization", "Bearer "+testAdminToken) }

func (a *api) do(method, path, body string, opts ...reqOpt) *httptest.ResponseRecorder {
	a.t.Helper()
	req := httptest.NewRequestWithContext(a.t.Context(), method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for _, o := range opts {
		o(req)
	}
	rec := httptest.NewRecorder()
	a.h.ServeHTTP(rec, req)
	return rec
}

func (a *api) createAccount() map[string]any {
	a.t.Helper()
	rec := a.do(http.MethodPost, "/v1/accounts", `{"owner_id":"user_123","currency":"BRL"}`)
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	return jsonBody(a.t, rec)
}

func (a *api) worldID() string {
	a.t.Helper()
	var id string
	err := a.db.Superuser(a.t).QueryRow(context.Background(),
		`SELECT id::text FROM accounts WHERE system_code = 'WORLD' AND currency = 'BRL'`).Scan(&id)
	if err != nil {
		a.t.Fatal(err)
	}
	return id
}

func jsonBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return m
}

// expectError confere status, código e o envelope padrão (SPEC 12.4).
func expectError(t *testing.T, rec *httptest.ResponseRecorder, status int, code string) {
	t.Helper()
	if rec.Code != status {
		t.Fatalf("status = %d, want %d (body %s)", rec.Code, status, rec.Body)
	}
	e, _ := jsonBody(t, rec)["error"].(map[string]any)
	if e["code"] != code {
		t.Fatalf("code = %v, want %s", e["code"], code)
	}
	if msg, _ := e["message"].(string); msg == "" {
		t.Fatal("error message is empty")
	}
	if e["request_id"] != rec.Header().Get(requestIDHeader) {
		t.Fatalf("request_id %v does not match header %s", e["request_id"], rec.Header().Get(requestIDHeader))
	}
}

func TestCreateAccountEndpoint(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	rec := a.do(http.MethodPost, "/v1/accounts", `{"owner_id":"user_123","currency":"BRL"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	body := jsonBody(t, rec)

	keys := slices.Sorted(maps.Keys(body))
	if want := []string{"balance", "created_at", "currency", "id", "owner_id", "status"}; !slices.Equal(keys, want) {
		t.Fatalf("response keys %v, want %v", keys, want)
	}
	if body["owner_id"] != "user_123" || body["currency"] != "BRL" || body["status"] != "ACTIVE" || body["balance"] != float64(0) {
		t.Fatalf("unexpected body %v", body)
	}
	if loc := rec.Header().Get("Location"); loc != "/v1/accounts/"+body["id"].(string) {
		t.Fatalf("Location = %q", loc)
	}
	created, err := time.Parse(time.RFC3339Nano, body["created_at"].(string))
	if err != nil || created.Location() != time.UTC || !strings.HasSuffix(body["created_at"].(string), "Z") {
		t.Fatalf("created_at %v is not RFC 3339 UTC", body["created_at"])
	}
}

func TestCreateAccountRejectsInvalidInput(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	for _, tc := range []struct {
		name, body  string
		contentType string
		status      int
		code        string
	}{
		{"unknown field", `{"owner_id":"u","currency":"BRL","kind":"SYSTEM"}`, "application/json", 400, "INVALID_REQUEST"},
		{"missing owner", `{"currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
		{"null owner", `{"owner_id":null,"currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
		{"owner not a string", `{"owner_id":123,"currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
		{"owner with leading space", `{"owner_id":" u","currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
		{"owner too long", `{"owner_id":"` + strings.Repeat("a", 129) + `","currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
		{"missing currency", `{"owner_id":"u"}`, "application/json", 400, "INVALID_REQUEST"},
		{"lowercase currency", `{"owner_id":"u","currency":"brl"}`, "application/json", 422, "UNSUPPORTED_CURRENCY"},
		{"unseeded currency", `{"owner_id":"u","currency":"USD"}`, "application/json", 422, "UNSUPPORTED_CURRENCY"},
		{"malformed JSON", `{"owner_id":`, "application/json", 400, "INVALID_REQUEST"},
		{"two objects", `{"owner_id":"u","currency":"BRL"}{}`, "application/json", 400, "INVALID_REQUEST"},
		{"array", `[]`, "application/json", 400, "INVALID_REQUEST"},
		{"no content type", `{"owner_id":"u","currency":"BRL"}`, "", 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"text/plain", `{"owner_id":"u","currency":"BRL"}`, "text/plain", 415, "UNSUPPORTED_MEDIA_TYPE"},
		{"body over 64 KB", `{"owner_id":"` + strings.Repeat("a", 70<<10) + `","currency":"BRL"}`, "application/json", 400, "INVALID_REQUEST"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := a.do(http.MethodPost, "/v1/accounts", tc.body, func(r *http.Request) {
				r.Header.Set("Content-Type", tc.contentType)
			})
			expectError(t, rec, tc.status, tc.code)
		})
	}

	// Content-Type com parâmetro continua sendo JSON.
	rec := a.do(http.MethodPost, "/v1/accounts", `{"owner_id":"u","currency":"BRL"}`,
		withHeader("Content-Type", "application/json; charset=utf-8"))
	if rec.Code != http.StatusCreated {
		t.Fatalf("charset parameter: %d %s", rec.Code, rec.Body)
	}
}

func TestGetAccountAndBalance(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	created := a.createAccount()
	id := created["id"].(string)

	rec := a.do(http.MethodGet, "/v1/accounts/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: %d %s", rec.Code, rec.Body)
	}
	got := jsonBody(t, rec)
	for k, v := range created {
		if got[k] != v {
			t.Errorf("GET %s = %v, POST returned %v", k, got[k], v)
		}
	}

	rec = a.do(http.MethodGet, "/v1/accounts/"+id+"/balance", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("balance: %d %s", rec.Code, rec.Body)
	}
	bal := jsonBody(t, rec)
	if bal["account_id"] != id || bal["currency"] != "BRL" || bal["balance"] != float64(0) || len(bal) != 3 {
		t.Fatalf("unexpected balance body %v", bal)
	}
}

func TestPublicEndpointsHideMissingAndSystemAccounts(t *testing.T) {
	t.Parallel()
	a := newAPI(t)

	for name, id := range map[string]string{
		"malformed id": "not-a-uuid",
		"unknown id":   "01a12684-0000-7000-8000-000000000000",
		"WORLD":        a.worldID(),
	} {
		for _, suffix := range []string{"", "/balance"} {
			rec := a.do(http.MethodGet, "/v1/accounts/"+id+suffix, "")
			t.Run(name+suffix, func(t *testing.T) { expectError(t, rec, 404, "ACCOUNT_NOT_FOUND") })
		}
	}
}

func TestAdminRequiresToken(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	id := a.createAccount()["id"].(string)
	path := "/v1/admin/accounts/" + id + "/status"
	body := `{"status":"SUSPENDED"}`

	for name, auth := range map[string]string{
		"missing":        "",
		"wrong token":    "Bearer wrong-token-0123456789",
		"token prefix":   "Bearer " + testAdminToken[:10],
		"basic scheme":   "Basic " + testAdminToken,
		"bearer, empty":  "Bearer ",
		"token as-is":    testAdminToken,
		"token + suffix": "Bearer " + testAdminToken + "x",
	} {
		t.Run(name, func(t *testing.T) {
			rec := a.do(http.MethodPatch, path, body, withHeader("Authorization", auth))
			expectError(t, rec, 401, "UNAUTHORIZED")
			if rec.Header().Get("WWW-Authenticate") == "" {
				t.Fatal("missing WWW-Authenticate header")
			}
		})
	}

	// O esquema não diferencia maiúsculas de minúsculas (RFC 6750).
	rec := a.do(http.MethodPatch, path, body, withHeader("Authorization", "bearer "+testAdminToken))
	if rec.Code != http.StatusOK {
		t.Fatalf("lowercase scheme: %d %s", rec.Code, rec.Body)
	}
}

func TestChangeStatusEndpoint(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	id := a.createAccount()["id"].(string)
	path := "/v1/admin/accounts/" + id + "/status"

	rec := a.do(http.MethodPatch, path, `{"status":"SUSPENDED"}`, asAdmin())
	if rec.Code != http.StatusOK {
		t.Fatalf("suspend: %d %s", rec.Code, rec.Body)
	}
	if body := jsonBody(t, rec); body["status"] != "SUSPENDED" || body["id"] != id || len(body) != 6 {
		t.Fatalf("unexpected body %v", body)
	}

	expectError(t, a.do(http.MethodPatch, path, `{"status":"PAUSED"}`, asAdmin()), 400, "INVALID_REQUEST")
	expectError(t, a.do(http.MethodPatch, path, `{"status":"active"}`, asAdmin()), 400, "INVALID_REQUEST")
	expectError(t, a.do(http.MethodPatch, path, `{}`, asAdmin()), 400, "INVALID_REQUEST")

	// Fechar com saldo é proibido.
	if _, err := a.db.Superuser(t).Exec(context.Background(),
		`UPDATE accounts SET balance = 100 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	expectError(t, a.do(http.MethodPatch, path, `{"status":"CLOSED"}`, asAdmin()), 409, "ACCOUNT_HAS_BALANCE")

	// Com saldo zero, fecha; e CLOSED é terminal.
	if _, err := a.db.Superuser(t).Exec(context.Background(),
		`UPDATE accounts SET balance = 0 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	if rec := a.do(http.MethodPatch, path, `{"status":"CLOSED"}`, asAdmin()); rec.Code != http.StatusOK {
		t.Fatalf("close: %d %s", rec.Code, rec.Body)
	}
	if rec := a.do(http.MethodPatch, path, `{"status":"CLOSED"}`, asAdmin()); rec.Code != http.StatusOK {
		t.Fatalf("CLOSED -> CLOSED must be a no-op success: %d %s", rec.Code, rec.Body)
	}
	expectError(t, a.do(http.MethodPatch, path, `{"status":"ACTIVE"}`, asAdmin()), 409, "INVALID_STATUS_TRANSITION")

	// Contas inexistentes e de sistema são invisíveis também para o admin.
	expectError(t, a.do(http.MethodPatch, "/v1/admin/accounts/"+a.worldID()+"/status", `{"status":"SUSPENDED"}`, asAdmin()),
		404, "ACCOUNT_NOT_FOUND")
	expectError(t, a.do(http.MethodPatch, "/v1/admin/accounts/nope/status", `{"status":"SUSPENDED"}`, asAdmin()),
		404, "ACCOUNT_NOT_FOUND")
}

func TestUnknownRoutesAndMethods(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	id := a.createAccount()["id"].(string)

	expectError(t, a.do(http.MethodGet, "/v1/nothing-here", ""), 404, "ROUTE_NOT_FOUND")
	expectError(t, a.do(http.MethodGet, "/", ""), 404, "ROUTE_NOT_FOUND")
	expectError(t, a.do(http.MethodDelete, "/v1/accounts/"+id, ""), 405, "METHOD_NOT_ALLOWED")
	expectError(t, a.do(http.MethodPut, "/v1/accounts", ""), 405, "METHOD_NOT_ALLOWED")

	// Sob /v1/admin, sem token nada é revelado; com token, a rota inexistente é 404.
	expectError(t, a.do(http.MethodGet, "/v1/admin/secret", ""), 401, "UNAUTHORIZED")
	expectError(t, a.do(http.MethodGet, "/v1/admin/secret", "", asAdmin()), 404, "ROUTE_NOT_FOUND")
}

func TestDatabaseFailureIsInternalErrorWithoutDetails(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	id := a.createAccount()["id"].(string)

	a.db.Pool.Close()
	rec := a.do(http.MethodGet, "/v1/accounts/"+id, "")
	expectError(t, rec, 500, "INTERNAL_ERROR")
	if strings.Contains(strings.ToLower(rec.Body.String()), "pool") {
		t.Fatalf("internal detail leaked: %s", rec.Body)
	}
}
