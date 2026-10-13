package account

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateOwnerID(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		ok   bool
	}{
		{"simple", "user_123", true},
		{"one character", "a", true},
		{"128 characters", strings.Repeat("a", 128), true},
		{"128 multibyte characters", strings.Repeat("ç", 128), true},
		{"inner spaces", "Maria da Silva", true},
		{"unicode", "usuário-ñ-日本", true},
		{"empty", "", false},
		{"129 characters", strings.Repeat("a", 129), false},
		{"leading space", " user", false},
		{"trailing space", "user ", false},
		{"trailing tab", "user\t", false},
		{"leading non-breaking space", " user", false},
		{"newline inside", "us\ner", false},
		{"null byte", "us\x00er", false},
		{"delete character", "us\x7fer", false},
		{"invalid utf-8", "us\xffer", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateOwnerID(tc.in)
			if tc.ok && err != nil {
				t.Fatalf("want valid, got %v", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidOwnerID) {
				t.Fatalf("want ErrInvalidOwnerID, got %v", err)
			}
		})
	}
}

func TestParseStatus(t *testing.T) {
	for _, s := range []string{"ACTIVE", "SUSPENDED", "CLOSED"} {
		if got, err := ParseStatus(s); err != nil || string(got) != s {
			t.Errorf("ParseStatus(%q) = %q, %v", s, got, err)
		}
	}
	for _, s := range []string{"", "active", "Closed", "DELETED", " ACTIVE"} {
		if _, err := ParseStatus(s); !errors.Is(err, ErrInvalidStatus) {
			t.Errorf("ParseStatus(%q): want ErrInvalidStatus, got %v", s, err)
		}
	}
}

// Matriz completa: toda combinação de origem, destino e saldo (zero ou não).
func TestCheckTransition(t *testing.T) {
	const (
		ok         = "ok"
		invalid    = "invalid"
		hasBalance = "has balance"
	)
	for _, tc := range []struct {
		from, to Status
		zero     string // resultado com saldo zero
		nonZero  string // resultado com saldo diferente de zero
	}{
		{StatusActive, StatusActive, ok, ok},
		{StatusActive, StatusSuspended, ok, ok},
		{StatusActive, StatusClosed, ok, hasBalance},
		{StatusSuspended, StatusActive, ok, ok},
		{StatusSuspended, StatusSuspended, ok, ok},
		{StatusSuspended, StatusClosed, ok, hasBalance},
		{StatusClosed, StatusActive, invalid, invalid},
		{StatusClosed, StatusSuspended, invalid, invalid},
		{StatusClosed, StatusClosed, ok, ok},
	} {
		for _, c := range []struct {
			balance int64
			want    string
		}{{0, tc.zero}, {500, tc.nonZero}, {-500, tc.nonZero}} {
			err := CheckTransition(tc.from, tc.to, c.balance)
			var got string
			switch {
			case err == nil:
				got = ok
			case errors.Is(err, ErrInvalidStatusTransition):
				got = invalid
			case errors.Is(err, ErrHasBalance):
				got = hasBalance
			default:
				got = err.Error()
			}
			if got != c.want {
				t.Errorf("%s -> %s with balance %d: got %s, want %s", tc.from, tc.to, c.balance, got, c.want)
			}
		}
	}
}
