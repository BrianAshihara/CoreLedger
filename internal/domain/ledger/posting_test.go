package ledger

import (
	"errors"
	"testing"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/domain/money"
)

func TestPostingValidate(t *testing.T) {
	a, b, c := uuid.New(), uuid.New(), uuid.New()
	valid := func() Posting {
		return Posting{
			TransactionID: uuid.New(),
			Type:          TypeTransfer,
			Currency:      "BRL",
			Legs:          []Leg{{a, -200}, {b, 200}},
		}
	}

	if err := valid().Validate(); err != nil {
		t.Fatalf("valid posting rejected: %v", err)
	}

	threeLegs := valid()
	threeLegs.Legs = []Leg{{a, -10000}, {b, 9700}, {c, 300}}
	if err := threeLegs.Validate(); err != nil {
		t.Fatalf("multi-leg posting rejected: %v", err)
	}

	maxLegs := valid()
	maxLegs.Legs = []Leg{{a, -money.MaxAmount}, {b, money.MaxAmount}}
	if err := maxLegs.Validate(); err != nil {
		t.Fatalf("legs at MaxAmount rejected: %v", err)
	}

	for _, tc := range []struct {
		name   string
		mutate func(*Posting)
	}{
		{"missing transaction id", func(p *Posting) { p.TransactionID = uuid.Nil }},
		{"unknown type", func(p *Posting) { p.Type = "REFUND" }},
		{"missing currency", func(p *Posting) { p.Currency = "" }},
		{"bad currency", func(p *Posting) { p.Currency = "brl" }},
		{"no legs", func(p *Posting) { p.Legs = nil }},
		{"one leg", func(p *Posting) { p.Legs = []Leg{{a, 0}} }},
		{"unbalanced", func(p *Posting) { p.Legs = []Leg{{a, -200}, {b, 199}} }},
		{"zero leg", func(p *Posting) { p.Legs = []Leg{{a, -200}, {b, 200}, {c, 0}} }},
		{"duplicate account", func(p *Posting) { p.Legs = []Leg{{a, -200}, {a, 200}} }},
		{"nil account", func(p *Posting) { p.Legs = []Leg{{uuid.Nil, -200}, {b, 200}} }},
		{"above MaxAmount", func(p *Posting) { p.Legs = []Leg{{a, -money.MaxAmount - 1}, {b, money.MaxAmount + 1}} }},
		// Pernas acima do teto que, somadas, dariam a volta no int64: o teto
		// pega antes, e a soma com checagem pegaria de qualquer forma.
		{"overflow attempt", func(p *Posting) {
			p.Legs = []Leg{{a, 1 << 62}, {b, 1 << 62}, {c, -(1 << 62)}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := valid()
			tc.mutate(&p)
			if err := p.Validate(); !errors.Is(err, ErrInvalidPosting) {
				t.Fatalf("want ErrInvalidPosting, got %v", err)
			}
		})
	}
}
