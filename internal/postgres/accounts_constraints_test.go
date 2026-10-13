package postgres_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brianashihara/coreledger/internal/testdb"
)

// Estes testes ignoram o Go de propósito e falam SQL cru com o banco (SPEC
// 21.3): mesmo que a validação em Go tenha um bug, o PostgreSQL precisa
// recusar o estado inválido.

const (
	checkViolation      = "23514"
	uniqueViolation     = "23505"
	foreignKeyViolation = "23503"
	permissionDenied    = "42501"
)

func pgCode(err error) (code, constraint string) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code, pgErr.ConstraintName
	}
	return "", ""
}

func expectViolation(t *testing.T, err error, wantCode, wantConstraint string) {
	t.Helper()
	code, constraint := pgCode(err)
	if code != wantCode || (wantConstraint != "" && constraint != wantConstraint) {
		t.Fatalf("want %s on %q, got code=%q constraint=%q err=%v", wantCode, wantConstraint, code, constraint, err)
	}
}

func insertUserAccount(ctx context.Context, db *pgxpool.Pool, ownerID string) (uuid.UUID, error) {
	id := uuid.Must(uuid.NewV7())
	_, err := db.Exec(ctx, `INSERT INTO accounts (id, kind, owner_id, currency) VALUES ($1, 'USER', $2, 'BRL')`, id, ownerID)
	return id, err
}

func TestWorldAccountIsSeeded(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)

	var (
		n       int
		kind    string
		status  string
		balance int64
		ownerID *string
	)
	err := d.Superuser(t).QueryRow(context.Background(), `
		SELECT count(*) OVER (), kind, status, balance, owner_id
		FROM accounts WHERE system_code = 'WORLD' AND currency = 'BRL'`,
	).Scan(&n, &kind, &status, &balance, &ownerID)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || kind != "SYSTEM" || status != "ACTIVE" || balance != 0 || ownerID != nil {
		t.Fatalf("unexpected WORLD account: n=%d kind=%s status=%s balance=%d owner=%v", n, kind, status, balance, ownerID)
	}
}

func TestDatabaseRejectsInvalidAccounts(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	super := d.Superuser(t)

	for _, tc := range []struct {
		name, sql, code, constraint string
	}{
		{
			"negative USER balance",
			`INSERT INTO accounts (id, kind, owner_id, currency, balance) VALUES (gen_random_uuid(), 'USER', 'u1', 'BRL', -1)`,
			checkViolation, "accounts_user_balance_non_negative",
		},
		{
			"CLOSED with balance",
			`INSERT INTO accounts (id, kind, owner_id, currency, status, balance) VALUES (gen_random_uuid(), 'USER', 'u1', 'BRL', 'CLOSED', 10)`,
			checkViolation, "accounts_closed_zero_balance",
		},
		{
			"USER without owner",
			`INSERT INTO accounts (id, kind, currency) VALUES (gen_random_uuid(), 'USER', 'BRL')`,
			checkViolation, "accounts_kind_fields",
		},
		{
			"USER with system code",
			`INSERT INTO accounts (id, kind, owner_id, system_code, currency) VALUES (gen_random_uuid(), 'USER', 'u1', 'FEES', 'BRL')`,
			checkViolation, "accounts_kind_fields",
		},
		{
			"SYSTEM with owner",
			`INSERT INTO accounts (id, kind, owner_id, system_code, currency) VALUES (gen_random_uuid(), 'SYSTEM', 'u1', 'FEES', 'BRL')`,
			checkViolation, "accounts_kind_fields",
		},
		{
			"second WORLD for the same currency",
			`INSERT INTO accounts (id, kind, system_code, currency) VALUES (gen_random_uuid(), 'SYSTEM', 'WORLD', 'BRL')`,
			uniqueViolation, "accounts_system_code_currency_key",
		},
		{
			"lowercase system code",
			`INSERT INTO accounts (id, kind, system_code, currency) VALUES (gen_random_uuid(), 'SYSTEM', 'world', 'BRL')`,
			checkViolation, "accounts_system_code_format",
		},
		{
			"unseeded currency",
			`INSERT INTO accounts (id, kind, owner_id, currency) VALUES (gen_random_uuid(), 'USER', 'u1', 'USD')`,
			foreignKeyViolation, "accounts_currency_fkey",
		},
		{
			"unknown status",
			`INSERT INTO accounts (id, kind, owner_id, currency, status) VALUES (gen_random_uuid(), 'USER', 'u1', 'BRL', 'FROZEN')`,
			checkViolation, "accounts_status_check",
		},
		{
			"unknown kind",
			`INSERT INTO accounts (id, kind, owner_id, currency) VALUES (gen_random_uuid(), 'ADMIN', 'u1', 'BRL')`,
			checkViolation, "accounts_kind_check",
		},
		{
			"negative last_entry_seq",
			`INSERT INTO accounts (id, kind, owner_id, currency, last_entry_seq) VALUES (gen_random_uuid(), 'USER', 'u1', 'BRL', -1)`,
			checkViolation, "accounts_last_entry_seq_check",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := super.Exec(ctx, tc.sql)
			expectViolation(t, err, tc.code, tc.constraint)
		})
	}
}

func TestDatabaseRejectsInvalidOwnerIDs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	super := d.Superuser(t)

	for name, owner := range map[string]string{
		"empty":          "",
		"129 characters": strings.Repeat("a", 129),
		"leading space":  " user",
		"trailing space": "user ",
		"control char":   "us\ter",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := insertUserAccount(ctx, super, owner)
			expectViolation(t, err, checkViolation, "accounts_owner_id_valid")
		})
	}

	if _, err := insertUserAccount(ctx, super, strings.Repeat("ç", 128)); err != nil {
		t.Fatalf("128 multibyte characters must be accepted: %v", err)
	}
}

func TestClosingWithBalanceIsRejectedByDatabase(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)
	super := d.Superuser(t)

	id, err := insertUserAccount(ctx, super, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := super.Exec(ctx, `UPDATE accounts SET balance = 500 WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	_, err = super.Exec(ctx, `UPDATE accounts SET status = 'CLOSED' WHERE id = $1`, id)
	expectViolation(t, err, checkViolation, "accounts_closed_zero_balance")
}

func TestSystemAccountMayGoNegative(t *testing.T) {
	t.Parallel()
	d := testdb.New(t)
	_, err := d.Superuser(t).Exec(context.Background(),
		`UPDATE accounts SET balance = -100000 WHERE system_code = 'WORLD' AND currency = 'BRL'`)
	if err != nil {
		t.Fatalf("WORLD must be allowed to go negative: %v", err)
	}
}

func TestAppRoleCannotRewriteImmutableAccountColumns(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	d := testdb.New(t)

	id, err := insertUserAccount(ctx, d.Pool, "owner") // INSERT é permitido ao app
	if err != nil {
		t.Fatal(err)
	}

	for _, stmt := range []string{
		`UPDATE accounts SET currency = 'BRL' WHERE id = $1`,
		`UPDATE accounts SET owner_id = 'someone-else' WHERE id = $1`,
		`UPDATE accounts SET kind = 'SYSTEM' WHERE id = $1`,
		`UPDATE accounts SET created_at = now() WHERE id = $1`,
		`DELETE FROM accounts WHERE id = $1`,
	} {
		_, err := d.Pool.Exec(ctx, stmt, id)
		expectViolation(t, err, permissionDenied, "")
	}

	// As colunas que mudam com o tempo continuam graváveis.
	if _, err := d.Pool.Exec(ctx, `UPDATE accounts SET status = 'SUSPENDED', updated_at = now() WHERE id = $1`, id); err != nil {
		t.Fatalf("app must be able to update status: %v", err)
	}
}
