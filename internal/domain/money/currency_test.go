package money

import (
	"errors"
	"testing"
)

func TestParseCurrency(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want error
	}{
		{"BRL", nil},
		{"USD", nil}, // formato válido; suporte é checado no banco
		{"", ErrCurrencyRequired},
		{"brl", ErrUnsupportedCurrency},
		{"Brl", ErrUnsupportedCurrency},
		{"BR", ErrUnsupportedCurrency},
		{"BRLX", ErrUnsupportedCurrency},
		{"R$", ErrUnsupportedCurrency},
		{" BRL", ErrUnsupportedCurrency},
		{"BR1", ErrUnsupportedCurrency},
	} {
		got, err := ParseCurrency(tc.in)
		if !errors.Is(err, tc.want) {
			t.Errorf("ParseCurrency(%q) error = %v, want %v", tc.in, err, tc.want)
			continue
		}
		if err == nil && string(got) != tc.in {
			t.Errorf("ParseCurrency(%q) = %q", tc.in, got)
		}
	}
}
