package money

import (
	"errors"
	"math"
)

// MaxAmount é o maior valor aceito numa operação, em unidades mínimas:
// R$ 10 bilhões (SPEC 5). Evita entradas absurdas e mantém qualquer soma
// realista longe do limite do int64. O banco tem um CHECK com o mesmo valor.
const MaxAmount int64 = 1_000_000_000_000

// ErrInvalidAmount indica um valor de requisição fora das regras do SPEC 5:
// precisa ser inteiro, maior que zero e no máximo MaxAmount.
var ErrInvalidAmount = errors.New("invalid amount")

// ValidateAmount checa o valor de uma requisição. A direção (débito ou
// crédito) vem da operação, então o valor pedido é sempre positivo.
func ValidateAmount(amount int64) error {
	if amount <= 0 || amount > MaxAmount {
		return ErrInvalidAmount
	}
	return nil
}

// ErrOverflow indica que uma soma passaria do limite do int64. Com MaxAmount
// isso é praticamente inalcançável; se acontecer, é bug, e a operação aborta
// em vez de dar a volta no número.
var ErrOverflow = errors.New("integer overflow in money arithmetic")

// Add soma dois valores com checagem de overflow. Dinheiro nunca pode "dar a
// volta": um saldo de MaxInt64 + 1 viraria um número negativo enorme.
func Add(a, b int64) (int64, error) {
	if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
		return 0, ErrOverflow
	}
	return a + b, nil
}
