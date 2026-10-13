package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeDB struct{ err error }

func (f fakeDB) Ping(context.Context) error { return f.err }

func newTestRouter(db Pinger, logs io.Writer) http.Handler {
	if logs == nil {
		logs = io.Discard
	}
	return NewRouter(Deps{
		Logger:         slog.New(slog.NewJSONHandler(logs, nil)),
		DB:             db,
		RequestTimeout: time.Second,
	})
}

func do(t *testing.T, h http.Handler, method, path string, header http.Header) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequestWithContext(t.Context(), method, path, nil)
	for k, v := range header {
		req.Header[k] = v
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("Content-Type = %q", ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("invalid JSON %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestHealthzIgnoresDatabase(t *testing.T) {
	h := newTestRouter(fakeDB{err: errors.New("down")}, nil)
	rec := do(t, h, http.MethodGet, "/healthz", nil)
	if rec.Code != http.StatusOK || decode(t, rec)["status"] != "ok" {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
}

func TestReadyz(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dbErr      error
		wantStatus int
		wantBody   string
	}{
		{"database reachable", nil, http.StatusOK, "ready"},
		{"database unreachable", errors.New("connection refused"), http.StatusServiceUnavailable, "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, newTestRouter(fakeDB{err: tc.dbErr}, nil), http.MethodGet, "/readyz", nil)
			if rec.Code != tc.wantStatus || decode(t, rec)["status"] != tc.wantBody {
				t.Fatalf("got %d %s", rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "refused") {
				t.Fatal("internal error leaked to the client")
			}
		})
	}
}

func TestRequestIDIsGeneratedWhenMissing(t *testing.T) {
	rec := do(t, newTestRouter(fakeDB{}, nil), http.MethodGet, "/healthz", nil)
	if id := rec.Header().Get(requestIDHeader); len(id) != 36 {
		t.Fatalf("want a generated UUID, got %q", id)
	}
}

func TestRequestIDIsEchoedWhenSafe(t *testing.T) {
	h := http.Header{requestIDHeader: {"client-abc_123.4:5"}}
	rec := do(t, newTestRouter(fakeDB{}, nil), http.MethodGet, "/healthz", h)
	if got := rec.Header().Get(requestIDHeader); got != "client-abc_123.4:5" {
		t.Fatalf("got %q", got)
	}
}

func TestRequestIDIsReplacedWhenUnsafe(t *testing.T) {
	for _, bad := range []string{
		"has space",
		"line\nbreak",
		`quote"injection`,
		strings.Repeat("a", 129),
	} {
		h := http.Header{requestIDHeader: {bad}}
		rec := do(t, newTestRouter(fakeDB{}, nil), http.MethodGet, "/healthz", h)
		if got := rec.Header().Get(requestIDHeader); got == bad || len(got) != 36 {
			t.Errorf("unsafe id %q: got %q, want a generated UUID", bad, got)
		}
	}
}

func TestAccessLogUsesRoutePattern(t *testing.T) {
	var logs bytes.Buffer
	h := http.Header{requestIDHeader: {"req-1"}}
	do(t, newTestRouter(fakeDB{}, &logs), http.MethodGet, "/readyz", h)

	var line map[string]any
	if err := json.Unmarshal(logs.Bytes(), &line); err != nil {
		t.Fatalf("log is not one JSON line: %q", logs.String())
	}
	for k, want := range map[string]any{
		"msg": "request", "request_id": "req-1", "method": "GET", "route": "/readyz", "status": float64(200),
	} {
		if line[k] != want {
			t.Errorf("log %s = %v, want %v", k, line[k], want)
		}
	}
	if _, ok := line["duration"]; !ok {
		t.Error("log has no duration")
	}
}

func TestPanicBecomesInternalError(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	h := requestID(accessLog(logger)(recoverer(logger)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("secret internal detail")
	}))))

	rec := do(t, h, http.MethodGet, "/boom", nil)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status %d", rec.Code)
	}
	body := decode(t, rec)
	errObj, _ := body["error"].(map[string]any)
	if errObj["code"] != "INTERNAL_ERROR" || errObj["request_id"] != rec.Header().Get(requestIDHeader) {
		t.Fatalf("unexpected body %v", body)
	}
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatal("panic value leaked to the client")
	}
	if !strings.Contains(logs.String(), "secret internal detail") || !strings.Contains(logs.String(), `"status":500`) {
		t.Fatalf("panic and status 500 must be logged: %s", logs.String())
	}
}

func TestTimeoutSetsDeadline(t *testing.T) {
	var deadline time.Time
	var ok bool
	h := timeout(50 * time.Millisecond)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		deadline, ok = r.Context().Deadline()
	}))
	do(t, h, http.MethodGet, "/", nil)
	if !ok || time.Until(deadline) > 50*time.Millisecond {
		t.Fatalf("deadline=%v ok=%v", deadline, ok)
	}
}
