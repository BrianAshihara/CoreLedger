// Package money reúne os tipos e as regras de valores monetários (SPEC 5).
// É Go puro: não conhece banco nem HTTP.
package money

import (
	"errors"
	"regexp"
)

// Currency é um código ISO 4217, sempre em maiúsculas (por exemplo, "BRL").
type Currency string

var (
	// ErrCurrencyRequired indica moeda ausente ou vazia na requisição.
	ErrCurrencyRequired = errors.New("currency is required")
	// ErrUnsupportedCurrency indica um código que não está semeado na tabela
	// currencies, inclusive códigos fora do formato (o spec não normaliza "brl").
	ErrUnsupportedCurrency = errors.New("unsupported currency")
)

var currencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// ParseCurrency valida só o formato. Saber se a moeda é suportada depende da
// tabela currencies, então essa checagem fica com quem tem acesso ao banco.
func ParseCurrency(s string) (Currency, error) {
	if s == "" {
		return "", ErrCurrencyRequired
	}
	if !currencyCode.MatchString(s) {
		return "", ErrUnsupportedCurrency
	}
	return Currency(s), nil
}
