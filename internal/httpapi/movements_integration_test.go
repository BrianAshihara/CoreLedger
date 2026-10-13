package httpapi

import (
	"context"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/testdb"
)

func withKey(k string) reqOpt { return withHeader(idempotencyKeyHeader, k) }

func newKey() string { return "k-" + uuid.NewString() }

func (a *api) deposit(id string, amount int64) {
	a.t.Helper()
	rec := a.do(http.MethodPost, "/v1/admin/deposits",
		fmt.Sprintf(`{"account_id":%q,"amount":%d,"currency":"BRL"}`, id, amount), asAdmin(), withKey(newKey()))
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("deposit: %d %s", rec.Code, rec.Body)
	}
}

func (a *api) balance(id string) float64 {
	a.t.Helper()
	rec := a.do(http.MethodGet, "/v1/accounts/"+id+"/balance", "")
	if rec.Code != http.StatusOK {
		a.t.Fatalf("balance: %d %s", rec.Code, rec.Body)
	}
	return jsonBody(a.t, rec)["balance"].(float64)
}

func (a *api) transactionCount() int {
	a.t.Helper()
	var n int
	if err := a.db.Superuser(a.t).QueryRow(context.Background(), `SELECT count(*) FROM ledger_transactions`).Scan(&n); err != nil {
		a.t.Fatal(err)
	}
	return n
}

func transferBody(from, to string, amount string) string {
	return fmt.Sprintf(`{"from_account_id":%q,"to_account_id":%q,"amount":%s,"currency":"BRL"}`, from, to, amount)
}

func TestMoneyMovementEndpoints(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)

	a.deposit(alice, 100_000)

	rec := a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "20000"), withKey(newKey()))
	if rec.Code != http.StatusCreated || rec.Header().Get(replayedHeader) != "" {
		t.Fatalf("transfer: %d %s (replayed=%q)", rec.Code, rec.Body, rec.Header().Get(replayedHeader))
	}
	body := jsonBody(t, rec)
	if keys := slices.Sorted(maps.Keys(body)); !slices.Equal(keys, []string{"amount", "created_at", "currency", "status", "transaction_id", "type"}) {
		t.Fatalf("response keys %v", keys)
	}
	if body["type"] != "TRANSFER" || body["status"] != "COMPLETED" || body["amount"] != float64(20_000) || body["currency"] != "BRL" {
		t.Fatalf("unexpected body %v", body)
	}

	rec = a.do(http.MethodPost, "/v1/admin/withdrawals",
		fmt.Sprintf(`{"account_id":%q,"amount":5000,"currency":"BRL"}`, bob), asAdmin(), withKey(newKey()))
	if rec.Code != http.StatusCreated || jsonBody(t, rec)["type"] != "WITHDRAWAL" {
		t.Fatalf("withdrawal: %d %s", rec.Code, rec.Body)
	}

	if a.balance(alice) != 80_000 || a.balance(bob) != 15_000 {
		t.Fatalf("alice=%v bob=%v", a.balance(alice), a.balance(bob))
	}

	// Detalhe da transação: as pernas, com a WORLD pelo código de sistema.
	id := body["transaction_id"].(string)
	rec = a.do(http.MethodGet, "/v1/transactions/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get transaction: %d %s", rec.Code, rec.Body)
	}
	detail := jsonBody(t, rec)
	entries := detail["entries"].([]any)
	if detail["transaction_id"] != id || len(entries) != 2 {
		t.Fatalf("detail %v", detail)
	}
	amounts := map[string]float64{}
	for _, raw := range entries {
		e := raw.(map[string]any)
		if _, leaked := e["balance_after"]; leaked {
			t.Fatal("public transaction detail must not expose balance_after")
		}
		amounts[e["account_id"].(string)] = e["amount"].(float64)
	}
	if amounts[alice] != -20_000 || amounts[bob] != 20_000 {
		t.Fatalf("entry amounts %v", amounts)
	}
}

func TestTransactionDetailShowsWorldBySystemCode(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)

	rec := a.do(http.MethodPost, "/v1/admin/deposits",
		fmt.Sprintf(`{"account_id":%q,"amount":700,"currency":"BRL"}`, alice), asAdmin(), withKey(newKey()))
	id := jsonBody(t, rec)["transaction_id"].(string)

	detail := jsonBody(t, a.do(http.MethodGet, "/v1/transactions/"+id, ""))
	var world map[string]any
	for _, raw := range detail["entries"].([]any) {
		if e := raw.(map[string]any); e["system_code"] == "WORLD" {
			world = e
		}
	}
	if world == nil || world["account_id"] != nil || world["amount"] != float64(-700) {
		t.Fatalf("WORLD entry %v in %v", world, detail)
	}

	expectError(t, a.do(http.MethodGet, "/v1/transactions/not-a-uuid", ""), 404, "TRANSACTION_NOT_FOUND")
	expectError(t, a.do(http.MethodGet, "/v1/transactions/"+uuid.NewString(), ""), 404, "TRANSACTION_NOT_FOUND")
}

// Passos 1 a 3 da ordem de validação (SPEC 10) e as regras do amount (SPEC 5).
func TestMoneyRequestValidation(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)
	a.deposit(alice, 1_000)
	k := withKey(newKey())

	for _, tc := range []struct {
		name   string
		body   string
		opts   []reqOpt
		status int
		code   string
	}{
		{"unknown field", `{"from_account_id":"a","to_account_id":"b","amount":1,"currency":"BRL","memo":"x"}`, []reqOpt{k}, 400, "INVALID_REQUEST"},
		{"malformed JSON", `{"amount":`, []reqOpt{k}, 400, "INVALID_REQUEST"},
		{"missing key", transferBody(alice, bob, "100"), nil, 400, "MISSING_IDEMPOTENCY_KEY"},
		{"missing key beats bad amount", transferBody(alice, bob, `"100"`), nil, 400, "MISSING_IDEMPOTENCY_KEY"},
		{"key too long", transferBody(alice, bob, "100"), []reqOpt{withKey(strings.Repeat("k", 256))}, 400, "INVALID_REQUEST"},
		{"amount as string", transferBody(alice, bob, `"100"`), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount with fraction", transferBody(alice, bob, "10.5"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount as decimal", transferBody(alice, bob, "100.0"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount with exponent", transferBody(alice, bob, "1e3"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount null", transferBody(alice, bob, "null"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount missing", fmt.Sprintf(`{"from_account_id":%q,"to_account_id":%q,"currency":"BRL"}`, alice, bob), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount zero", transferBody(alice, bob, "0"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount negative", transferBody(alice, bob, "-100"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"amount above maximum", transferBody(alice, bob, "1000000000001"), []reqOpt{k}, 400, "INVALID_AMOUNT"},
		{"same account", transferBody(alice, alice, "100"), []reqOpt{k}, 422, "SAME_ACCOUNT_TRANSFER"},
		{"insufficient funds", transferBody(alice, bob, "1001"), []reqOpt{k}, 422, "INSUFFICIENT_FUNDS"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			expectError(t, a.do(http.MethodPost, "/v1/transfers", tc.body, tc.opts...), tc.status, tc.code)
		})
	}

	// Depósito e saque são de admin.
	body := fmt.Sprintf(`{"account_id":%q,"amount":10,"currency":"BRL"}`, alice)
	expectError(t, a.do(http.MethodPost, "/v1/admin/deposits", body, withKey(newKey())), 401, "UNAUTHORIZED")
	expectError(t, a.do(http.MethodPost, "/v1/admin/withdrawals", body, withKey(newKey())), 401, "UNAUTHORIZED")

	if n := a.transactionCount(); n != 1 {
		t.Fatalf("%d transactions after only rejected requests (want the 1 deposit)", n)
	}
}

func TestReplayAndConflictOverHTTP(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)
	a.deposit(alice, 1_000)
	k := newKey()

	first := a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "300"), withKey(k))
	// Mesmo pedido com espaços e chaves em outra ordem: o fingerprint é do
	// pedido decodificado, então é replay.
	reordered := fmt.Sprintf(`{ "currency": "BRL", "amount": 300, "to_account_id": %q, "from_account_id": %q }`, bob, alice)
	second := a.do(http.MethodPost, "/v1/transfers", reordered, withKey(k))

	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("statuses %d then %d, want 201 then 200", first.Code, second.Code)
	}
	if second.Header().Get(replayedHeader) != "true" {
		t.Fatal("replay must carry Idempotent-Replayed: true")
	}
	if first.Body.String() != second.Body.String() {
		t.Fatalf("replay body differs:\n%s\n%s", first.Body, second.Body)
	}

	expectError(t, a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "301"), withKey(k)), 409, "IDEMPOTENCY_CONFLICT")
	if a.balance(alice) != 700 {
		t.Fatalf("alice = %v, the transfer must have happened exactly once", a.balance(alice))
	}
}

// fire dispara as requisições ao mesmo tempo, liberadas por uma barreira.
func fire(reqs []func() *httptest.ResponseRecorder) []*httptest.ResponseRecorder {
	out := make([]*httptest.ResponseRecorder, len(reqs))
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i, req := range reqs {
		wg.Go(func() {
			<-start
			out[i] = req()
		})
	}
	close(start)
	wg.Wait()
	return out
}

// C4 (SPEC 21.2): 20 requisições simultâneas com a mesma chave e o mesmo
// corpo. Exatamente uma transação; todas as respostas com o mesmo
// transaction_id; uma 201 e o resto 200 com replay.
func TestC4ConcurrentDuplicatesCreateOneTransaction(t *testing.T) {
	a := newAPI(t, testdb.WithMaxConns(25))
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)
	a.deposit(alice, 1_000)
	before := a.transactionCount()
	k := newKey()

	reqs := make([]func() *httptest.ResponseRecorder, 20)
	for i := range reqs {
		reqs[i] = func() *httptest.ResponseRecorder {
			return a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "100"), withKey(k))
		}
	}
	recs := fire(reqs)

	created, replayed := 0, 0
	ids := map[string]bool{}
	for i, rec := range recs {
		switch {
		case rec.Code == http.StatusCreated:
			created++
		case rec.Code == http.StatusOK && rec.Header().Get(replayedHeader) == "true":
			replayed++
		default:
			t.Fatalf("request %d: %d %s", i, rec.Code, rec.Body)
		}
		ids[jsonBody(t, rec)["transaction_id"].(string)] = true
	}
	if created != 1 || replayed != 19 || len(ids) != 1 {
		t.Fatalf("created=%d replayed=%d distinct ids=%d, want 1/19/1", created, replayed, len(ids))
	}
	if n := a.transactionCount() - before; n != 1 {
		t.Fatalf("%d transactions created, want 1", n)
	}
	if a.balance(alice) != 900 || a.balance(bob) != 100 {
		t.Fatalf("alice=%v bob=%v: money moved more than once", a.balance(alice), a.balance(bob))
	}
}

// C5 (SPEC 21.2): 2 requisições simultâneas com a mesma chave e corpos
// diferentes. Uma passa, a outra recebe 409; uma única transação.
func TestC5ConcurrentConflictingRequests(t *testing.T) {
	for round := range 10 {
		t.Run(fmt.Sprintf("round %d", round), func(t *testing.T) {
			a := newAPI(t, testdb.WithMaxConns(10))
			alice := a.createAccount()["id"].(string)
			bob := a.createAccount()["id"].(string)
			a.deposit(alice, 1_000)
			before := a.transactionCount()
			k := newKey()

			recs := fire([]func() *httptest.ResponseRecorder{
				func() *httptest.ResponseRecorder {
					return a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "100"), withKey(k))
				},
				func() *httptest.ResponseRecorder {
					return a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "250"), withKey(k))
				},
			})

			codes := []int{recs[0].Code, recs[1].Code}
			slices.Sort(codes)
			if !slices.Equal(codes, []int{http.StatusCreated, http.StatusConflict}) {
				t.Fatalf("statuses %v, want one 201 and one 409 (%s / %s)", codes, recs[0].Body, recs[1].Body)
			}
			if n := a.transactionCount() - before; n != 1 {
				t.Fatalf("%d transactions created, want 1", n)
			}
			if got := 1_000 - a.balance(alice); got != 100 && got != 250 {
				t.Fatalf("alice was debited %v", got)
			}
		})
	}
}

// Lock preso: 503 SERVICE_BUSY com Retry-After, e nada gravado.
func TestLockTimeoutIsServiceBusy(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	a := newAPIWithLockTimeout(t, 200*time.Millisecond)
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)
	a.deposit(alice, 1_000)

	holder, err := a.db.Superuser(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, uuid.MustParse(bob)); err != nil {
		t.Fatal(err)
	}

	rec := a.do(http.MethodPost, "/v1/transfers", transferBody(alice, bob, "100"), withKey(newKey()))
	expectError(t, rec, 503, "SERVICE_BUSY")
	if rec.Header().Get("Retry-After") != "1" {
		t.Fatalf("Retry-After = %q, want 1", rec.Header().Get("Retry-After"))
	}
	_ = holder.Rollback(ctx)
	if a.balance(alice) != 1_000 {
		t.Fatal("a request that timed out moved money")
	}
}
