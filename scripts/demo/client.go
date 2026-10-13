package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type config struct {
	BaseURL    string
	AdminToken string
	Color      bool
}

// request descreve uma chamada à API. O corpo é codificado como JSON.
type request struct {
	method string
	path   string
	body   any
	key    string // Idempotency-Key
	admin  bool   // envia Authorization: Bearer <ADMIN_TOKEN>
}

type response struct {
	status  int
	header  http.Header
	body    []byte
	elapsed time.Duration
}

// Headers que contam algo sobre o comportamento da API; os demais (Date,
// Content-Length...) só poluiriam a saída.
var shownHeaders = []string{"Location", "Idempotent-Replayed", "Retry-After", "WWW-Authenticate", "X-Request-Id"}

type demo struct {
	cfg    config
	out    io.Writer
	client *http.Client
	runID  string

	// Contas criadas no começo e usadas pelos passos seguintes.
	alice, bob string

	step   int
	passed int
	failed int
}

func newDemo(cfg config, out io.Writer, runID string) *demo {
	return &demo{
		cfg: cfg,
		out: out,
		client: &http.Client{
			Timeout: 15 * time.Second,
			// Os passos de concorrência abrem dezenas de conexões de uma vez.
			Transport: &http.Transport{MaxIdleConnsPerHost: 64},
		},
		runID: runID,
	}
}

func (d *demo) key(name string) string { return "demo-" + d.runID + "-" + name }

// send faz a chamada sem imprimir nada.
func (d *demo) send(ctx context.Context, r request) (response, error) {
	var body io.Reader
	if r.body != nil {
		b, err := json.Marshal(r.body)
		if err != nil {
			return response{}, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, r.method, d.cfg.BaseURL+r.path, body)
	if err != nil {
		return response{}, err
	}
	if r.body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.key != "" {
		req.Header.Set("Idempotency-Key", r.key)
	}
	if r.admin {
		req.Header.Set("Authorization", "Bearer "+d.cfg.AdminToken)
	}

	start := time.Now()
	res, err := d.client.Do(req)
	if err != nil {
		return response{}, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return response{}, err
	}
	return response{status: res.StatusCode, header: res.Header, body: b, elapsed: time.Since(start)}, nil
}

// call faz a chamada e imprime a requisição e a resposta cruas.
func (d *demo) call(ctx context.Context, r request) (response, error) {
	res, err := d.send(ctx, r)
	if err != nil {
		return res, err
	}
	d.showRequest(r)
	d.showResponse(res)
	return res, nil
}

func (d *demo) showRequest(r request) {
	p := d.paint(cyan)
	fmt.Fprintln(d.out, p("  > "+r.method+" "+r.path))
	if r.admin {
		fmt.Fprintln(d.out, p("  > Authorization: Bearer "+mask(d.cfg.AdminToken)))
	}
	if r.key != "" {
		fmt.Fprintln(d.out, p("  > Idempotency-Key: "+r.key))
	}
	if r.body != nil {
		b, _ := json.MarshalIndent(r.body, "", "  ")
		fmt.Fprintln(d.out, p("  > Content-Type: application/json"))
		d.lines("  > ", string(b), p)
	}
}

func (d *demo) showResponse(res response) {
	color := green
	switch {
	case res.status >= 500:
		color = red
	case res.status >= 400:
		color = yellow
	}
	p := d.paint(color)
	fmt.Fprintf(d.out, "%s  %s\n", p(fmt.Sprintf("  < %d %s", res.status, http.StatusText(res.status))),
		d.paint(dim)(fmt.Sprintf("(%s)", ms(res.elapsed))))
	for _, h := range shownHeaders {
		if v := res.header.Get(h); v != "" {
			fmt.Fprintln(d.out, p("  < "+h+": "+v))
		}
	}
	if len(res.body) > 0 {
		var pretty bytes.Buffer
		text := string(res.body)
		if json.Indent(&pretty, res.body, "", "  ") == nil {
			text = pretty.String()
		}
		d.lines("  < ", strings.TrimRight(text, "\n"), p)
	}
}

func (d *demo) lines(prefix, text string, p func(string) string) {
	for line := range strings.SplitSeq(text, "\n") {
		fmt.Fprintln(d.out, p(prefix+line))
	}
}

// section abre um passo numerado com uma linha explicando o que ele prova.
func (d *demo) section(title, why string) {
	d.step++
	fmt.Fprintln(d.out)
	fmt.Fprintln(d.out, d.paint(bold)(fmt.Sprintf("%d. %s", d.step, title)))
	if why != "" {
		for line := range strings.SplitSeq(why, "\n") {
			fmt.Fprintln(d.out, d.paint(dim)("   "+line))
		}
	}
	fmt.Fprintln(d.out)
}

func (d *demo) note(format string, args ...any) {
	fmt.Fprintln(d.out, "  -> "+fmt.Sprintf(format, args...))
}

func (d *demo) check(ok bool, format string, args ...any) bool {
	msg := fmt.Sprintf(format, args...)
	if ok {
		d.passed++
		fmt.Fprintln(d.out, d.paint(green)("  [ok]   ")+msg)
	} else {
		d.failed++
		fmt.Fprintln(d.out, d.paint(red)("  [FAIL] ")+msg)
	}
	return ok
}

func (d *demo) checkStatus(res response, want int, code string) bool {
	got := fmt.Sprintf("%d", res.status)
	if c := errorCode(res); c != "" {
		got += " " + c
	}
	wantText := fmt.Sprintf("%d %s", want, http.StatusText(want))
	if code != "" {
		wantText = fmt.Sprintf("%d %s", want, code)
	}
	ok := res.status == want && (code == "" || errorCode(res) == code)
	if ok {
		return d.check(true, "responded %s", wantText)
	}
	return d.check(false, "expected %s, got %s", wantText, got)
}

// outcome é o resultado de uma requisição disparada em paralelo.
type outcome struct {
	res     response
	retries int
	err     error
}

// burst dispara todas as requisições ao mesmo tempo (elas esperam um portão
// comum) e devolve os resultados na mesma ordem.
//
// Um 503 SERVICE_BUSY é repetido com a mesma Idempotency-Key, como um cliente
// correto faria: repetir é seguro, nunca move dinheiro duas vezes.
func (d *demo) burst(ctx context.Context, reqs []request) []outcome {
	out := make([]outcome, len(reqs))
	gate := make(chan struct{})
	var wg sync.WaitGroup
	for i, r := range reqs {
		wg.Go(func() {
			<-gate
			out[i] = d.sendRetrying(ctx, r)
		})
	}
	close(gate)
	wg.Wait()
	return out
}

func (d *demo) sendRetrying(ctx context.Context, r request) outcome {
	var o outcome
	for attempt := 0; ; attempt++ {
		o.res, o.err = d.send(ctx, r)
		if o.err != nil || o.res.status != http.StatusServiceUnavailable || attempt == 5 {
			return o
		}
		o.retries++
		wait := time.Second
		if s, err := strconv.Atoi(o.res.header.Get("Retry-After")); err == nil {
			wait = time.Duration(s) * time.Second
		}
		select {
		case <-ctx.Done():
			o.err = ctx.Err()
			return o
		case <-time.After(wait):
		}
	}
}

func errorCode(res response) string {
	var e struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if json.Unmarshal(res.body, &e) != nil {
		return ""
	}
	return e.Error.Code
}

func decode[T any](res response) (T, error) {
	var v T
	err := json.Unmarshal(res.body, &v)
	return v, err
}

func isReplay(res response) bool {
	return res.status == http.StatusOK && res.header.Get("Idempotent-Replayed") == "true"
}

// brl formata centavos: 100000 vira "1,000.00 BRL".
func brl(cents int64) string {
	sign := ""
	if cents < 0 {
		sign = "-"
		cents = -cents
	}
	units := strconv.FormatInt(cents/100, 10)
	var b strings.Builder
	for i, c := range units {
		if i > 0 && (len(units)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	return fmt.Sprintf("%s%s.%02d BRL", sign, b.String(), cents%100)
}

func ms(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%.2f ms", float64(d.Microseconds())/1000)
	}
	return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000)
}

// mask esconde o token inteiro: a saída da demo pode ir para um print ou uma
// gravação de tela.
func mask(string) string { return strings.Repeat("*", 16) }

const (
	reset  = "\x1b[0m"
	bold   = "\x1b[1m"
	dim    = "\x1b[2m"
	red    = "\x1b[31m"
	green  = "\x1b[32m"
	yellow = "\x1b[33m"
	cyan   = "\x1b[36m"
)

func (d *demo) paint(code string) func(string) string {
	if !d.cfg.Color {
		return func(s string) string { return s }
	}
	return func(s string) string { return code + s + reset }
}
