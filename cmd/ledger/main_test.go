package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

const testAdminToken = "test-admin-token-0123456789"

func TestParseMigrateArgs(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    migrateOp
		wantErr bool
	}{
		{args: []string{"up"}, want: migrateOp{kind: "up"}},
		{args: []string{"version"}, want: migrateOp{kind: "version"}},
		{args: []string{"down", "2"}, want: migrateOp{kind: "down", steps: 2}},
		{args: []string{"down", "--all"}, want: migrateOp{kind: "down-all"}},
		{args: []string{"down"}, wantErr: true},
		{args: []string{"down", "0"}, wantErr: true},
		{args: []string{"down", "-1"}, wantErr: true},
		{args: []string{"down", "all"}, wantErr: true},
		{args: []string{"sideways"}, wantErr: true},
		{args: []string{"up", "extra"}, wantErr: true},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			got, err := parseMigrateArgs(tc.args)
			if tc.wantErr {
				if !errors.Is(err, errUsage) {
					t.Fatalf("want usage error, got %v", err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

func TestRunRejectsUnknownCommands(t *testing.T) {
	for _, args := range [][]string{nil, {"launch"}, {"serve", "now"}} {
		err := run(context.Background(), args, func(string) string { return "" }, io.Discard, io.Discard)
		if !errors.Is(err, errUsage) {
			t.Errorf("%q: want usage error, got %v", args, err)
		}
	}
}

func TestServeRefusesInvalidConfiguration(t *testing.T) {
	err := serve(context.Background(), func(string) string { return "" }, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "DATABASE_URL") {
		t.Fatalf("want a configuration error naming DATABASE_URL, got %v", err)
	}
}

func TestServeRefusesUnmigratedDatabase(t *testing.T) {
	t.Parallel()
	d := testdb.NewEmpty(t)
	env := map[string]string{"DATABASE_URL": d.SuperuserURL(), "ADMIN_TOKEN": testAdminToken, "HTTP_ADDR": "127.0.0.1:0"}

	err := serve(context.Background(), func(k string) string { return env[k] }, io.Discard)
	if !errors.Is(err, postgres.ErrSchemaMissing) {
		t.Fatalf("want ErrSchemaMissing, got %v", err)
	}
}

// Ponta a ponta: o serve sobe num banco migrado, responde o /readyz e desliga
// limpo quando o contexto é cancelado (Ctrl+C ou SIGTERM).
func TestServeStartsAndShutsDownGracefully(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)
	env := map[string]string{
		"DATABASE_URL": d.SuperuserURL(),
		"ADMIN_TOKEN":  testAdminToken,
		"HTTP_ADDR":    "127.0.0.1:0",
		"LOG_FORMAT":   "text",
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	logs := &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- serve(ctx, func(k string) string { return env[k] }, logs) }()

	addr := waitForListenAddr(t, logs, done)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://%s/readyz", addr), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/readyz = %d", resp.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("serve did not stop after cancellation")
	}
	if !strings.Contains(logs.String(), "server stopped") {
		t.Fatalf("missing shutdown log:\n%s", logs.String())
	}
}

var listenAddr = regexp.MustCompile(`msg="server started" addr=(\S+)`)

func waitForListenAddr(t *testing.T, logs *syncBuffer, done <-chan error) string {
	t.Helper()
	deadline := time.After(15 * time.Second)
	for {
		if m := listenAddr.FindStringSubmatch(logs.String()); m != nil {
			return m[1]
		}
		select {
		case err := <-done:
			t.Fatalf("serve exited early: %v\n%s", err, logs.String())
		case <-deadline:
			t.Fatalf("server did not start:\n%s", logs.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}
