package postgres_test

import (
	"context"
	"testing"

	"github.com/brianashihara/coreledger/internal/postgres/sqlc"
	"github.com/brianashihara/coreledger/internal/testdb"
)

// Prova o pipeline do sqlc de ponta a ponta: schema das migrations, código
// gerado e query rodando com o papel da aplicação.
func TestListCurrenciesReturnsSeededBRL(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)

	got, err := sqlc.New(d.Pool).ListCurrencies(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := []sqlc.Currency{{Code: "BRL", Exponent: 2}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}
