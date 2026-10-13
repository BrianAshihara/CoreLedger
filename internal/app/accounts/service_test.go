package accounts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/brianashihara/coreledger/internal/app/accounts"
	"github.com/brianashihara/coreledger/internal/domain/account"
	"github.com/brianashihara/coreledger/internal/domain/money"
	"github.com/brianashihara/coreledger/internal/postgres"
	"github.com/brianashihara/coreledger/internal/testdb"
)

func newService(t *testing.T) (*accounts.Service, *testdb.DB) {
	t.Helper()
	d := testdb.New(t)
	return accounts.NewService(postgres.NewStore(d.Pool)), d
}

func create(t *testing.T, s *accounts.Service) account.Account {
	t.Helper()
	a, err := s.Create(context.Background(), accounts.CreateInput{OwnerID: "user_123", Currency: "BRL"})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func worldID(t *testing.T, d *testdb.DB) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	err := d.Superuser(t).QueryRow(context.Background(),
		`SELECT id FROM accounts WHERE system_code = 'WORLD' AND currency = 'BRL'`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// setBalance grava um saldo direto, como superusuário. Existe só em teste:
// até a Fase 4 não há como mover dinheiro, e alguns testes precisam de uma
// conta com saldo (SPEC 21.0).
func setBalance(t *testing.T, d *testdb.DB, id uuid.UUID, balance int64) {
	t.Helper()
	if _, err := d.Superuser(t).Exec(context.Background(),
		`UPDATE accounts SET balance = $2 WHERE id = $1`, id, balance); err != nil {
		t.Fatal(err)
	}
}

func TestCreate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newService(t)

	a := create(t, s)
	if a.ID.Version() != 7 {
		t.Errorf("id %s is not a UUIDv7", a.ID)
	}
	if a.Kind != account.KindUser || a.OwnerID != "user_123" || a.Currency != "BRL" ||
		a.Status != account.StatusActive || a.Balance != 0 || a.CreatedAt.IsZero() {
		t.Fatalf("unexpected account %+v", a)
	}

	got, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got != a {
		t.Fatalf("stored account differs:\n got  %+v\n want %+v", got, a)
	}
}

func TestCreateValidation(t *testing.T) {
	t.Parallel()
	s, _ := newService(t)

	for _, tc := range []struct {
		name            string
		owner, currency string
		want            error
	}{
		{"invalid owner", " user", "BRL", account.ErrInvalidOwnerID},
		{"missing currency", "user", "", money.ErrCurrencyRequired},
		{"lowercase currency", "user", "brl", money.ErrUnsupportedCurrency},
		{"unseeded currency", "user", "USD", money.ErrUnsupportedCurrency},
		// O dono é validado antes da moeda: a ordem define o erro visível.
		{"both invalid", "", "xxx", account.ErrInvalidOwnerID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := s.Create(context.Background(), accounts.CreateInput{OwnerID: tc.owner, Currency: tc.currency})
			if !errors.Is(err, tc.want) {
				t.Fatalf("want %v, got %v", tc.want, err)
			}
		})
	}
}

func TestGetHidesMissingAndSystemAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, d := newService(t)

	for name, id := range map[string]uuid.UUID{
		"unknown": uuid.Must(uuid.NewV7()),
		"WORLD":   worldID(t, d),
	} {
		if _, err := s.Get(ctx, id); !errors.Is(err, account.ErrNotFound) {
			t.Errorf("%s: want ErrNotFound, got %v", name, err)
		}
	}
}

// Percorre todas as transições a partir de cada status real no banco.
func TestChangeStatusTransitions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, _ := newService(t)

	// Leva uma conta nova até o status from usando só transições válidas.
	accountIn := func(t *testing.T, from account.Status) account.Account {
		t.Helper()
		a := create(t, s)
		if from == account.StatusActive {
			return a
		}
		a, err := s.ChangeStatus(ctx, a.ID, string(from))
		if err != nil {
			t.Fatal(err)
		}
		return a
	}

	all := []account.Status{account.StatusActive, account.StatusSuspended, account.StatusClosed}
	for _, from := range all {
		for _, to := range all {
			t.Run(string(from)+"->"+string(to), func(t *testing.T) {
				a := accountIn(t, from)
				got, err := s.ChangeStatus(ctx, a.ID, string(to))

				if wantErr := account.CheckTransition(from, to, 0); wantErr != nil {
					if !errors.Is(err, account.ErrInvalidStatusTransition) {
						t.Fatalf("want ErrInvalidStatusTransition, got %v", err)
					}
					stored, _ := s.Get(ctx, a.ID)
					if stored.Status != from {
						t.Fatalf("status changed to %s after a rejected transition", stored.Status)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if got.Status != to {
					t.Fatalf("status = %s, want %s", got.Status, to)
				}
				if from == to && !got.UpdatedAt.Equal(a.UpdatedAt) {
					t.Fatal("a no-op transition must not write")
				}
				if from != to && !got.UpdatedAt.After(a.UpdatedAt) {
					t.Fatal("updated_at must advance on a real transition")
				}
			})
		}
	}
}

func TestCloseRequiresZeroBalance(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, d := newService(t)

	a := create(t, s)
	setBalance(t, d, a.ID, 500)

	if _, err := s.ChangeStatus(ctx, a.ID, "CLOSED"); !errors.Is(err, account.ErrHasBalance) {
		t.Fatalf("want ErrHasBalance, got %v", err)
	}
	stored, err := s.Get(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != account.StatusActive {
		t.Fatalf("status = %s, want ACTIVE", stored.Status)
	}
}

func TestChangeStatusRejectsUnknownStatusAndSystemAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, d := newService(t)
	a := create(t, s)

	if _, err := s.ChangeStatus(ctx, a.ID, "active"); !errors.Is(err, account.ErrInvalidStatus) {
		t.Errorf("lowercase status: want ErrInvalidStatus, got %v", err)
	}
	if _, err := s.ChangeStatus(ctx, worldID(t, d), "SUSPENDED"); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("WORLD: want ErrNotFound, got %v", err)
	}
	if _, err := s.ChangeStatus(ctx, uuid.Must(uuid.NewV7()), "SUSPENDED"); !errors.Is(err, account.ErrNotFound) {
		t.Errorf("unknown: want ErrNotFound, got %v", err)
	}

	var status string
	if err := d.Superuser(t).QueryRow(ctx,
		`SELECT status FROM accounts WHERE system_code = 'WORLD'`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "ACTIVE" {
		t.Fatalf("WORLD status changed to %s", status)
	}
}

// Prova que ChangeStatus usa o lock da linha (SPEC 7.1): enquanto outra
// transação segura a conta e credita um saldo, o fechamento espera; quando a
// outra confirma, ele enxerga o saldo novo e recusa. Sem o lock, o
// fechamento leria saldo zero e fecharia uma conta prestes a ter dinheiro.
func TestChangeStatusWaitsForRowLock(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	s, d := newService(t)
	a := create(t, s)

	tx, err := d.Superuser(t).Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM accounts WHERE id = $1 FOR UPDATE`, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE accounts SET balance = 700 WHERE id = $1`, a.ID); err != nil {
		t.Fatal(err)
	}

	done := make(chan error, 1)
	go func() {
		_, err := s.ChangeStatus(ctx, a.ID, "CLOSED")
		done <- err
	}()

	select {
	case err := <-done:
		t.Fatalf("ChangeStatus did not wait for the row lock (returned %v)", err)
	case <-time.After(300 * time.Millisecond):
	}

	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if !errors.Is(err, account.ErrHasBalance) {
			t.Fatalf("after the credit committed: want ErrHasBalance, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ChangeStatus still blocked after the lock was released")
	}
}
