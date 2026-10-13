package httpapi

import (
	"net/http"
	"testing"

	"github.com/brianashihara/coreledger/internal/testdb"
)

// Readiness com um pool real: 200 enquanto o PostgreSQL responde, 503 quando
// o pool não consegue mais alcançá-lo.
func TestReadyzWithRealDatabase(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)
	h := newTestRouter(d.Pool, nil)

	if rec := do(t, h, http.MethodGet, "/readyz", nil); rec.Code != http.StatusOK {
		t.Fatalf("with database: got %d %s", rec.Code, rec.Body)
	}

	d.Pool.Close()
	if rec := do(t, h, http.MethodGet, "/readyz", nil); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("with closed pool: got %d %s", rec.Code, rec.Body)
	}
}
