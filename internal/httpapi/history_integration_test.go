package httpapi

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/testdb"
)

type historyPage struct {
	seqs []int64
	data []map[string]any
	next string // "" quando next_cursor é null
}

func (a *api) entries(id string, limit int, cursor string) historyPage {
	a.t.Helper()
	q := url.Values{}
	if limit > 0 {
		q.Set("limit", fmt.Sprint(limit))
	}
	if cursor != "" {
		q.Set("cursor", cursor)
	}
	rec := a.do(http.MethodGet, "/v1/accounts/"+id+"/entries?"+q.Encode(), "")
	if rec.Code != http.StatusOK {
		a.t.Fatalf("entries: %d %s", rec.Code, rec.Body)
	}
	body := jsonBody(a.t, rec)
	var p historyPage
	for _, raw := range body["data"].([]any) {
		e := raw.(map[string]any)
		p.data = append(p.data, e)
		p.seqs = append(p.seqs, int64(e["sequence"].(float64)))
	}
	if next, ok := body["next_cursor"].(string); ok {
		p.next = next
	} else if body["next_cursor"] != nil {
		a.t.Fatalf("next_cursor must be a string or null, got %v", body["next_cursor"])
	}
	return p
}

func (a *api) transfer(from, to string, amount int64) {
	a.t.Helper()
	rec := a.do(http.MethodPost, "/v1/transfers", transferBody(from, to, fmt.Sprint(amount)), withKey(newKey()))
	if rec.Code != http.StatusCreated {
		a.t.Fatalf("transfer: %d %s", rec.Code, rec.Body)
	}
}

func TestAccountHistoryPagination(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	bob := a.createAccount()["id"].(string)

	a.deposit(alice, 1_000) // seq 1: +1000 -> 1000
	for i := range 6 {      // seq 2..7: -100, +50, -100, +50, -100, +50
		if i%2 == 0 {
			a.transfer(alice, bob, 100)
		} else {
			a.transfer(bob, alice, 50)
		}
	}

	p1 := a.entries(alice, 3, "")
	p2 := a.entries(alice, 3, p1.next)
	p3 := a.entries(alice, 3, p2.next)
	for i, tc := range []struct {
		page     historyPage
		want     []int64
		hasNext  bool
		pageName string
	}{
		{p1, []int64{7, 6, 5}, true, "page 1"},
		{p2, []int64{4, 3, 2}, true, "page 2"},
		{p3, []int64{1}, false, "page 3"},
	} {
		if fmt.Sprint(tc.page.seqs) != fmt.Sprint(tc.want) || (tc.page.next != "") != tc.hasNext {
			t.Fatalf("%s (%d): seqs %v next %q, want %v hasNext=%v", tc.pageName, i, tc.page.seqs, tc.page.next, tc.want, tc.hasNext)
		}
	}

	// Newest first: seq 7 is the last +50; seq 1 is the deposit.
	newest, oldest := p1.data[0], p3.data[0]
	if newest["direction"] != "CREDIT" || newest["amount"] != float64(50) || newest["type"] != "TRANSFER" || newest["balance_after"] != float64(850) {
		t.Fatalf("newest entry %v", newest)
	}
	if oldest["direction"] != "CREDIT" || oldest["amount"] != float64(1000) || oldest["type"] != "DEPOSIT" || oldest["balance_after"] != float64(1000) {
		t.Fatalf("oldest entry %v", oldest)
	}
	if debit := p1.data[1]; debit["direction"] != "DEBIT" || debit["amount"] != float64(100) {
		t.Fatalf("seq 6 must be a debit of 100 shown as a positive amount, got %v", debit)
	}

	// Default limit and an account with no entries.
	if p := a.entries(alice, 0, ""); len(p.seqs) != 7 || p.next != "" {
		t.Fatalf("default limit page: %v next %q", p.seqs, p.next)
	}
	carol := a.createAccount()["id"].(string)
	if p := a.entries(carol, 0, ""); len(p.data) != 0 || p.next != "" {
		t.Fatalf("empty account page: %v next %q", p.data, p.next)
	}
}

func TestAccountHistoryValidation(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	b64 := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

	for _, q := range []string{"limit=0", "limit=201", "limit=-1", "limit=abc", "limit=1.5"} {
		expectError(t, a.do(http.MethodGet, "/v1/accounts/"+alice+"/entries?"+q, ""), 400, "INVALID_REQUEST")
	}
	if rec := a.do(http.MethodGet, "/v1/accounts/"+alice+"/entries?limit=200", ""); rec.Code != http.StatusOK {
		t.Fatalf("limit=200 must be accepted: %d", rec.Code)
	}
	for _, c := range []string{"garbage!", b64("seq:abc"), b64("seq:1"), b64("page:5")} {
		expectError(t, a.do(http.MethodGet, "/v1/accounts/"+alice+"/entries?cursor="+url.QueryEscape(c), ""), 400, "INVALID_CURSOR")
	}
	for _, id := range []string{"not-a-uuid", "01a12684-0000-7000-8000-000000000000", a.worldID()} {
		expectError(t, a.do(http.MethodGet, "/v1/accounts/"+id+"/entries", ""), 404, "ACCOUNT_NOT_FOUND")
	}
}

// Critério de aceite da Fase 5: a paginação é estável com lançamentos
// acontecendo. Enquanto o histórico de 40 pernas é lido página a página, mais
// 30 créditos chegam na conta. Seguindo os cursores, o resultado é exatamente
// 40..1, sem pular nem repetir nada.
func TestAccountHistoryIsStableUnderConcurrentPostings(t *testing.T) {
	a := newAPI(t, testdb.WithMaxConns(10))
	alice := a.createAccount()["id"].(string)
	for range 40 {
		a.deposit(alice, 10)
	}

	first := a.entries(alice, 10, "")
	seen := append([]int64(nil), first.seqs...)

	// t.Fatalf não pode ser chamado fora da goroutine do teste, então a
	// goroutine dos depósitos só registra a falha com t.Errorf.
	var wg sync.WaitGroup
	wg.Go(func() {
		body := fmt.Sprintf(`{"account_id":%q,"amount":10,"currency":"BRL"}`, alice)
		for range 30 {
			if rec := a.do(http.MethodPost, "/v1/admin/deposits", body, asAdmin(), withKey(newKey())); rec.Code != http.StatusCreated {
				t.Errorf("concurrent deposit: %d %s", rec.Code, rec.Body)
				return
			}
		}
	})
	for cursor := first.next; cursor != ""; {
		p := a.entries(alice, 10, cursor)
		seen = append(seen, p.seqs...)
		cursor = p.next
		time.Sleep(5 * time.Millisecond) // deixa os depósitos intercalarem com as páginas
	}
	wg.Wait()

	if len(seen) != 40 {
		t.Fatalf("read %d entries following the cursors, want 40: %v", len(seen), seen)
	}
	for i, seq := range seen {
		if want := int64(40 - i); seq != want {
			t.Fatalf("position %d: seq %d, want %d (full: %v)", i, seq, want, seen)
		}
	}
	if p := a.entries(alice, 200, ""); len(p.seqs) != 70 || p.seqs[0] != 70 {
		t.Fatalf("after the concurrent deposits the history must have 70 entries, newest 70: got %d, newest %v", len(p.seqs), p.seqs[0])
	}
}

func TestReconciliationEndpoint(t *testing.T) {
	t.Parallel()
	a := newAPI(t)
	alice := a.createAccount()["id"].(string)
	a.deposit(alice, 1_000)

	expectError(t, a.do(http.MethodGet, "/v1/admin/reconciliation", ""), 401, "UNAUTHORIZED")

	rec := a.do(http.MethodGet, "/v1/admin/reconciliation", "", asAdmin())
	body := jsonBody(t, rec)
	if rec.Code != http.StatusOK || body["status"] != "OK" || len(body["issues"].([]any)) != 0 ||
		body["checked_accounts"] != float64(2) || body["checked_transactions"] != float64(1) {
		t.Fatalf("healthy ledger: %d %v", rec.Code, body)
	}

	// Corrompe o saldo guardado sem lançamento, como só um bug faria.
	if _, err := a.db.Superuser(t).Exec(context.Background(),
		`UPDATE accounts SET balance = balance + 1 WHERE id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	rec = a.do(http.MethodGet, "/v1/admin/reconciliation", "", asAdmin())
	body = jsonBody(t, rec)
	if rec.Code != http.StatusOK || body["status"] != "INCONSISTENT" {
		t.Fatalf("corrupted ledger: %d %v", rec.Code, body)
	}
	var found bool
	for _, raw := range body["issues"].([]any) {
		i := raw.(map[string]any)
		if i["check"] == "BALANCE_MATCHES_LEDGER" {
			found = true
			if i["account_id"] != alice || i["expected"] != float64(1000) || i["actual"] != float64(1001) || i["currency"] != "BRL" {
				t.Fatalf("issue %v", i)
			}
			if _, ok := i["transaction_id"]; ok {
				t.Fatal("transaction_id must be omitted when it does not apply")
			}
		}
	}
	if !found {
		t.Fatalf("no BALANCE_MATCHES_LEDGER issue in %v", body["issues"])
	}
}
