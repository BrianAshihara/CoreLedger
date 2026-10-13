package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

type accountBody struct {
	OwnerID  string `json:"owner_id"`
	Currency string `json:"currency"`
}

type moneyBody struct {
	AccountID string `json:"account_id"`
	Amount    any    `json:"amount"`
	Currency  string `json:"currency"`
}

type transferBody struct {
	FromAccountID string `json:"from_account_id"`
	ToAccountID   string `json:"to_account_id"`
	Amount        any    `json:"amount"`
	Currency      string `json:"currency"`
}

type accountView struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Balance int64  `json:"balance"`
}

type movementView struct {
	TransactionID string `json:"transaction_id"`
}

type transactionView struct {
	Entries []struct {
		AccountID  *string `json:"account_id"`
		SystemCode string  `json:"system_code"`
		Amount     int64   `json:"amount"`
	} `json:"entries"`
}

type historyEntry struct {
	Direction    string `json:"direction"`
	Amount       int64  `json:"amount"`
	BalanceAfter int64  `json:"balance_after"`
	Sequence     int64  `json:"sequence"`
}

type historyPage struct {
	Data       []historyEntry `json:"data"`
	NextCursor *string        `json:"next_cursor"`
}

func (e historyEntry) signed() int64 {
	if e.Direction == "DEBIT" {
		return -e.Amount
	}
	return e.Amount
}

func run(ctx context.Context, cfg config, out io.Writer) error {
	if cfg.AdminToken == "" {
		return errors.New("ADMIN_TOKEN is not set (make demo loads it from .env)")
	}
	runID, err := newRunID()
	if err != nil {
		return err
	}
	d := newDemo(cfg, out, runID)

	fmt.Fprintln(out, d.paint(bold)("CoreLedger demo"))
	fmt.Fprintf(out, "API %s, run %s\n", cfg.BaseURL, runID)
	fmt.Fprintln(out, d.paint(dim)("Amounts travel as integer cents: 100000 means 1,000.00 BRL."))
	fmt.Fprintln(out, d.paint(dim)("Lines with > are what the client sent; lines with < are what the API answered."))

	for _, step := range []func(context.Context) error{
		d.health,
		d.accountsAndDeposit,
		d.transferAndReplay,
		d.rejections,
		d.concurrentSpending,
		d.concurrentDuplicates,
		d.statement,
		d.reconciliation,
	} {
		if err := step(ctx); err != nil {
			return err
		}
	}

	fmt.Fprintln(out)
	summary := fmt.Sprintf("%d checks passed, %d failed.", d.passed, d.failed)
	if d.failed > 0 {
		fmt.Fprintln(out, d.paint(red)(summary))
		return errChecksFailed
	}
	fmt.Fprintln(out, d.paint(green)(summary))
	fmt.Fprintln(out)
	fmt.Fprintln(out, "What this run showed:")
	for _, line := range []string{
		"every movement is a balanced double-entry transaction (money never appears or vanishes)",
		"a retried request never moves money twice, even 20 copies at the same instant",
		"concurrent spending can never overdraw an account: exactly 10 of 50 got through",
		"rejected requests change nothing and leave their key free for a later retry",
		"each account's history explains its balance, entry by entry",
		"an independent reconciliation agrees with every stored balance",
	} {
		fmt.Fprintln(out, "  - "+line)
	}
	return nil
}

func newRunID() (string, error) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (d *demo) health(ctx context.Context) error {
	d.section("Is the API up?",
		"/healthz only says the process is alive; /readyz also pings PostgreSQL.")
	res, err := d.call(ctx, request{method: http.MethodGet, path: "/healthz"})
	if err != nil {
		return fmt.Errorf("API not reachable at %s (start it in another terminal with: make run): %w", d.cfg.BaseURL, err)
	}
	d.checkStatus(res, http.StatusOK, "")
	res, err = d.call(ctx, request{method: http.MethodGet, path: "/readyz"})
	if err != nil {
		return err
	}
	if !d.checkStatus(res, http.StatusOK, "") {
		return errors.New("the API is up but not ready: is PostgreSQL running?")
	}
	return nil
}

func (d *demo) openAccount(ctx context.Context, owner string, show bool) (string, error) {
	r := request{method: http.MethodPost, path: "/v1/accounts", body: accountBody{OwnerID: owner, Currency: "BRL"}}
	var res response
	var err error
	if show {
		res, err = d.call(ctx, r)
	} else {
		res, err = d.send(ctx, r)
	}
	if err != nil {
		return "", err
	}
	acc, err := decode[accountView](res)
	if !d.check(res.status == http.StatusCreated && err == nil && acc.Balance == 0 && acc.Status == "ACTIVE",
		"%s: account opened, ACTIVE, balance 0", owner) {
		return "", fmt.Errorf("could not open account for %s", owner)
	}
	if !show {
		d.note("%s = %s", owner, acc.ID)
	}
	return acc.ID, nil
}

func (d *demo) deposit(ctx context.Context, account string, amount int64, key string, show bool) (response, error) {
	r := request{
		method: http.MethodPost, path: "/v1/admin/deposits", admin: true, key: key,
		body: moneyBody{AccountID: account, Amount: amount, Currency: "BRL"},
	}
	if show {
		return d.call(ctx, r)
	}
	return d.send(ctx, r)
}

func (d *demo) transfer(from, to string, amount any, key string) request {
	return request{
		method: http.MethodPost, path: "/v1/transfers", key: key,
		body: transferBody{FromAccountID: from, ToAccountID: to, Amount: amount, Currency: "BRL"},
	}
}

func (d *demo) balance(ctx context.Context, account string) (int64, error) {
	res, err := d.send(ctx, request{method: http.MethodGet, path: "/v1/accounts/" + account + "/balance"})
	if err != nil {
		return 0, err
	}
	v, err := decode[accountView](res)
	if err != nil || res.status != http.StatusOK {
		return 0, fmt.Errorf("read balance of %s: status %d", account, res.status)
	}
	return v.Balance, nil
}

// expectBalances confere os saldos atuais, lidos de GET /balance.
func (d *demo) expectBalances(ctx context.Context, want map[string]int64, names map[string]string) error {
	for _, name := range []string{"alice", "bob", "carol", "dave"} {
		id, ok := names[name]
		if !ok {
			continue
		}
		got, err := d.balance(ctx, id)
		if err != nil {
			return err
		}
		if got == want[name] {
			d.check(true, "%s balance is %s", name, brl(got))
		} else {
			d.check(false, "%s balance is %s, expected %s", name, brl(got), brl(want[name]))
		}
	}
	return nil
}

func (d *demo) accountsAndDeposit(ctx context.Context) error {
	d.section("Open two accounts and deposit 1,000.00 BRL into alice",
		"Every account starts ACTIVE with balance 0. A deposit is admin-only: money enters\n"+
			"the system through the WORLD system account, so the ledger always sums to zero.")
	var err error
	if d.alice, err = d.openAccount(ctx, "alice", true); err != nil {
		return err
	}
	fmt.Fprintln(d.out)
	if d.bob, err = d.openAccount(ctx, "bob", false); err != nil {
		return err
	}
	fmt.Fprintln(d.out)

	res, err := d.deposit(ctx, d.alice, 100000, d.key("deposit"), true)
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusCreated, "")
	mv, _ := decode[movementView](res)

	d.section("Look inside the deposit transaction",
		"Double entry: one transaction, two entries, and the amounts sum to exactly zero.\n"+
			"System accounts appear by code; balances of other parties are never exposed here.")
	res, err = d.call(ctx, request{method: http.MethodGet, path: "/v1/transactions/" + mv.TransactionID})
	if err != nil {
		return err
	}
	tx, err := decode[transactionView](res)
	var sum int64
	world := false
	for _, e := range tx.Entries {
		sum += e.Amount
		world = world || e.SystemCode == "WORLD"
	}
	d.check(err == nil && len(tx.Entries) == 2 && sum == 0 && world,
		"2 entries (alice +1,000.00, WORLD -1,000.00) summing to 0")
	return d.expectBalances(ctx, map[string]int64{"alice": 100000, "bob": 0}, map[string]string{"alice": d.alice, "bob": d.bob})
}

func (d *demo) transferAndReplay(ctx context.Context) error {
	names := map[string]string{"alice": d.alice, "bob": d.bob}

	d.section("Transfer 200.00 BRL from alice to bob",
		"Money movements require an Idempotency-Key chosen by the client.")
	tr := d.transfer(d.alice, d.bob, 20000, d.key("transfer"))
	res, err := d.call(ctx, tr)
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusCreated, "")
	mv, _ := decode[movementView](res)
	if err := d.expectBalances(ctx, map[string]int64{"alice": 80000, "bob": 20000}, names); err != nil {
		return err
	}

	d.section("Send the exact same request again",
		"A client that lost the response (timeout, dropped connection) just retries.\n"+
			"The API answers with the original result instead of moving money twice.")
	res, err = d.call(ctx, tr)
	if err != nil {
		return err
	}
	again, _ := decode[movementView](res)
	d.check(isReplay(res), "200 OK with Idempotent-Replayed: true")
	d.check(again.TransactionID == mv.TransactionID, "same transaction_id as the original")
	if err := d.expectBalances(ctx, map[string]int64{"alice": 80000, "bob": 20000}, names); err != nil {
		return err
	}

	d.section("Reuse the key for a different request",
		"Same key, different amount: almost certainly a client bug, so the API refuses it.")
	res, err = d.call(ctx, d.transfer(d.alice, d.bob, 30000, d.key("transfer")))
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusConflict, "IDEMPOTENCY_CONFLICT")
	return nil
}

func (d *demo) rejections(ctx context.Context) error {
	names := map[string]string{"alice": d.alice, "bob": d.bob}

	d.section("Try to spend more than alice has",
		"Balances can never go negative. The rejection writes nothing to the ledger.")
	big := d.transfer(d.alice, d.bob, 500000, d.key("big"))
	res, err := d.call(ctx, big)
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusUnprocessableEntity, "INSUFFICIENT_FUNDS")
	if err := d.expectBalances(ctx, map[string]int64{"alice": 80000, "bob": 20000}, names); err != nil {
		return err
	}

	d.section("A rejected request does not burn its key",
		"alice receives 5,000.00 BRL, then the client retries the rejected transfer with the\n"+
			"same key. Because the first attempt was rejected, this one is evaluated again.")
	res, err = d.deposit(ctx, d.alice, 500000, d.key("topup"), false)
	if err != nil {
		return err
	}
	d.check(res.status == http.StatusCreated, "deposited 5,000.00 BRL into alice")
	fmt.Fprintln(d.out)
	res, err = d.call(ctx, big)
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusCreated, "")
	if err := d.expectBalances(ctx, map[string]int64{"alice": 80000, "bob": 520000}, names); err != nil {
		return err
	}

	d.section("Invalid input never reaches the ledger",
		"Money is an integer number of cents: 10.5 is rejected, not rounded.\n"+
			"Deposits are admin-only: without the token the API answers 401.")
	res, err = d.call(ctx, d.transfer(d.alice, d.bob, 10.5, d.key("fraction")))
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusBadRequest, "INVALID_AMOUNT")
	fmt.Fprintln(d.out)
	res, err = d.call(ctx, request{
		method: http.MethodPost, path: "/v1/admin/deposits", key: d.key("noauth"),
		body: moneyBody{AccountID: d.alice, Amount: 100, Currency: "BRL"},
	})
	if err != nil {
		return err
	}
	d.checkStatus(res, http.StatusUnauthorized, "UNAUTHORIZED")
	return nil
}

func (d *demo) concurrentSpending(ctx context.Context) error {
	d.section("50 transfers at the same instant from an account that can pay only 10",
		"carol holds 1,000.00 BRL and 50 clients each try to send 100.00 BRL to dave at once.\n"+
			"Row locks make them queue on carol's balance: exactly 10 must succeed and 40 must be\n"+
			"refused. A naive read-then-write would let more than 10 through.")
	carol, err := d.openAccount(ctx, "carol", false)
	if err != nil {
		return err
	}
	dave, err := d.openAccount(ctx, "dave", false)
	if err != nil {
		return err
	}
	res, err := d.deposit(ctx, carol, 100000, d.key("carol"), false)
	if err != nil {
		return err
	}
	d.check(res.status == http.StatusCreated, "deposited 1,000.00 BRL into carol")
	fmt.Fprintln(d.out)

	reqs := make([]request, 50)
	for i := range reqs {
		reqs[i] = d.transfer(carol, dave, 10000, d.key(fmt.Sprintf("race-%02d", i)))
	}
	d.note("every request looks like this; only the Idempotency-Key changes (race-00 to race-49):")
	d.showRequest(reqs[0])
	fmt.Fprintln(d.out)
	start := time.Now()
	outcomes := d.burst(ctx, reqs)
	elapsed := time.Since(start)

	ok, refused, other, retries := 0, 0, 0, 0
	var sampleOK, sampleRefused *outcome
	for i := range outcomes {
		o := &outcomes[i]
		if o.err != nil {
			return o.err
		}
		retries += o.retries
		switch {
		case o.res.status == http.StatusCreated:
			ok++
			if sampleOK == nil {
				sampleOK = o
			}
		case errorCode(o.res) == "INSUFFICIENT_FUNDS":
			refused++
			if sampleRefused == nil {
				sampleRefused = o
			}
		default:
			other++
		}
	}
	d.note("50 requests sent in parallel, all answered in %s", ms(elapsed))
	if retries > 0 {
		d.note("%d answers were 503 SERVICE_BUSY and were retried with the same key", retries)
	}
	if sampleOK != nil {
		fmt.Fprintln(d.out)
		d.note("one of the accepted responses:")
		d.showResponse(sampleOK.res)
	}
	if sampleRefused != nil {
		fmt.Fprintln(d.out)
		d.note("one of the refused responses:")
		d.showResponse(sampleRefused.res)
	}
	fmt.Fprintln(d.out)
	d.check(ok == 10, "exactly 10 transfers accepted (got %d)", ok)
	d.check(refused == 40, "exactly 40 refused with INSUFFICIENT_FUNDS (got %d)", refused)
	d.check(other == 0, "no other kind of answer (got %d)", other)
	if err := d.expectBalances(ctx, map[string]int64{"carol": 0, "dave": 100000},
		map[string]string{"carol": carol, "dave": dave}); err != nil {
		return err
	}

	// O extrato da carol prova que a fila foi respeitada: sequências sem
	// buracos e cada saldo derivado do anterior, nunca negativo.
	res, err = d.send(ctx, request{method: http.MethodGet, path: "/v1/accounts/" + carol + "/entries?limit=200"})
	if err != nil {
		return err
	}
	page, err := decode[historyPage](res)
	d.check(err == nil && chainOK(page.Data) && len(page.Data) == 11,
		"carol's history: 11 entries, sequences 1..11, every balance_after = previous + amount, never negative")
	return nil
}

func (d *demo) concurrentDuplicates(ctx context.Context) error {
	d.section("20 copies of the same request at the same instant",
		"A client with aggressive retries fires the same transfer (bob sends 1.00 BRL to alice,\n"+
			"same key) 20 times concurrently. Exactly one is executed; the other 19 wait for it\n"+
			"and replay its result.")
	reqs := make([]request, 20)
	for i := range reqs {
		reqs[i] = d.transfer(d.bob, d.alice, 100, d.key("duplicate"))
	}
	d.note("the request, sent 20 times at once with the same key:")
	d.showRequest(reqs[0])
	fmt.Fprintln(d.out)
	outcomes := d.burst(ctx, reqs)

	created, replays := 0, 0
	txIDs := map[string]bool{}
	var sampleReplay *outcome
	for i := range outcomes {
		o := &outcomes[i]
		if o.err != nil {
			return o.err
		}
		switch {
		case o.res.status == http.StatusCreated:
			created++
		case isReplay(o.res):
			replays++
			if sampleReplay == nil {
				sampleReplay = o
			}
		}
		if mv, err := decode[movementView](o.res); err == nil && mv.TransactionID != "" {
			txIDs[mv.TransactionID] = true
		}
	}
	if sampleReplay != nil {
		d.note("one of the replayed responses:")
		d.showResponse(sampleReplay.res)
		fmt.Fprintln(d.out)
	}
	d.check(created == 1, "exactly one 201 Created (got %d)", created)
	d.check(replays == 19, "19 replays with Idempotent-Replayed: true (got %d)", replays)
	d.check(len(txIDs) == 1, "all 20 answers carry the same transaction_id (got %d distinct)", len(txIDs))
	return d.expectBalances(ctx, map[string]int64{"alice": 80100, "bob": 519900},
		map[string]string{"alice": d.alice, "bob": d.bob})
}

func (d *demo) statement(ctx context.Context) error {
	d.section("alice's statement, 3 entries per page",
		"Newest first. Each entry records the balance right after it, so the history explains\n"+
			"the balance. The cursor is opaque and pages stay stable while money keeps moving.")
	path := "/v1/accounts/" + d.alice + "/entries?limit=3"
	res, err := d.call(ctx, request{method: http.MethodGet, path: path})
	if err != nil {
		return err
	}
	first, err := decode[historyPage](res)
	if !d.check(err == nil && len(first.Data) == 3 && first.NextCursor != nil, "first page: 3 entries and a next_cursor") {
		return nil
	}
	fmt.Fprintln(d.out)
	res, err = d.call(ctx, request{method: http.MethodGet, path: path + "&cursor=" + url.QueryEscape(*first.NextCursor)})
	if err != nil {
		return err
	}
	second, err := decode[historyPage](res)
	d.check(err == nil && len(second.Data) == 2 && second.NextCursor == nil, "second page: the last 2 entries, next_cursor null")

	all := append(first.Data, second.Data...)
	d.check(chainOK(all), "5 entries: sequences 1..5 and an unbroken balance_after chain")
	d.check(len(all) > 0 && all[0].BalanceAfter == 80100, "the newest balance_after equals the current balance (%s)", brl(80100))
	return nil
}

// chainOK confere um histórico completo, do mais novo para o mais antigo:
// sequências n..1 sem buracos e cada saldo igual ao anterior mais o
// lançamento, sem nunca ficar negativo.
func chainOK(entries []historyEntry) bool {
	n := int64(len(entries))
	for i, e := range entries {
		if e.Sequence != n-int64(i) || e.BalanceAfter < 0 {
			return false
		}
		prev := int64(0)
		if i+1 < len(entries) {
			prev = entries[i+1].BalanceAfter
		}
		if e.BalanceAfter != prev+e.signed() {
			return false
		}
	}
	return true
}

func (d *demo) reconciliation(ctx context.Context) error {
	d.section("Reconcile the whole ledger",
		"Six independent checks over every account and transaction in the database (not only\n"+
			"this demo's), in one consistent snapshot. It reports and never corrects anything.")
	res, err := d.call(ctx, request{method: http.MethodGet, path: "/v1/admin/reconciliation", admin: true})
	if err != nil {
		return err
	}
	report, _ := decode[struct {
		Status string `json:"status"`
	}](res)
	d.check(res.status == http.StatusOK && report.Status == "OK", "status OK: stored balances, entries and sequences all agree")
	return nil
}
