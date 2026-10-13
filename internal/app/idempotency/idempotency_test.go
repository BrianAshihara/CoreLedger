package idempotency

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateKey(t *testing.T) {
	for _, ok := range []string{"a", "6f1c-0192f3a1", "key with spaces and ~!@#", strings.Repeat("k", 255)} {
		if err := ValidateKey(ok); err != nil {
			t.Errorf("ValidateKey(%q) = %v", ok, err)
		}
	}
	if err := ValidateKey(""); !errors.Is(err, ErrMissingKey) {
		t.Errorf("empty key: want ErrMissingKey, got %v", err)
	}
	for _, bad := range []string{strings.Repeat("k", 256), "tab\there", "new\nline", "ação", "\x7f"} {
		if err := ValidateKey(bad); !errors.Is(err, ErrInvalidKey) {
			t.Errorf("ValidateKey(%q): want ErrInvalidKey, got %v", bad, err)
		}
	}
}

func TestFingerprint(t *testing.T) {
	type body struct {
		Account string `json:"account_id"`
		Amount  int64  `json:"amount"`
	}
	fp := func(path string, b body) [32]byte {
		t.Helper()
		sum, err := Fingerprint("POST", path, b)
		if err != nil {
			t.Fatal(err)
		}
		return sum
	}

	base := fp("/v1/admin/deposits", body{"a", 100})
	if base != fp("/v1/admin/deposits", body{"a", 100}) {
		t.Fatal("fingerprint is not deterministic")
	}
	if base == fp("/v1/admin/withdrawals", body{"a", 100}) {
		t.Fatal("different route must give a different fingerprint")
	}
	if base == fp("/v1/admin/deposits", body{"a", 101}) {
		t.Fatal("different amount must give a different fingerprint")
	}
	if base == fp("/v1/admin/deposits", body{"b", 100}) {
		t.Fatal("different account must give a different fingerprint")
	}
}
