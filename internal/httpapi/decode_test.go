package httpapi

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

// O amount nunca passa por float64 (SPEC 5): só inteiros escritos como
// inteiros são aceitos aqui. O intervalo é checado depois, na aplicação.
func TestParseAmount(t *testing.T) {
	for raw, want := range map[string]int64{
		`1050`:                1050,
		`1`:                   1,
		`0`:                   0,  // aceito aqui, recusado pela aplicação
		`-5`:                  -5, // idem
		`9223372036854775807`: 9223372036854775807,
	} {
		got, err := parseAmount(json.RawMessage(raw))
		if err != nil || got != want {
			t.Errorf("parseAmount(%s) = %d, %v; want %d", raw, got, err, want)
		}
	}

	for _, raw := range []string{
		``,                    // campo ausente
		`null`,                // nulo
		`"1050"`,              // string
		`10.5`,                // fração
		`1050.0`,              // inteiro escrito como decimal
		`1e3`,                 // expoente
		`-`,                   // só o sinal
		`9223372036854775808`, // maior que int64
		`true`,
		`[1050]`,
		`{"value":1050}`,
	} {
		if _, err := parseAmount(json.RawMessage(raw)); !errors.Is(err, money.ErrInvalidAmount) {
			t.Errorf("parseAmount(%s): want ErrInvalidAmount, got %v", raw, err)
		}
	}
}
