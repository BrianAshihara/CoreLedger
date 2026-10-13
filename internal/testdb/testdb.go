// Package testdb dá a cada teste o seu próprio banco PostgreSQL (SPEC 21.0).
//
// Os bancos são clonados de um template que já tem todas as migrations
// aplicadas, então criar um é barato e os testes nunca compartilham estado. O
// template leva no nome um hash dos arquivos de migration, é montado sob um
// advisory lock (os pacotes de teste rodam como processos paralelos) e é
// publicado com um rename atômico, então um template pela metade nunca é usado.
//
// As conexões entregues aos testes rodam como ledger_app, o papel de runtime.
// Assim, um grant faltando ou um UPDATE/DELETE proibido falha aqui exatamente
// como falharia em produção.
package testdb

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/brianashihara/coreledger/db/migrations"
	"github.com/brianashihara/coreledger/internal/postgres"
)

const (
	// Os nomes dos papéis são uma convenção fixa, compartilhada com
	// scripts/db-setup.sql e com as migrations (SPEC 15.1.1).
	OwnerRole = "ledger_owner"
	AppRole   = "ledger_app"

	defaultMaxConns = 4
	staleAfter      = time.Hour

	// Prazo de cada operação administrativa (conectar, criar e remover banco).
	// É folgado de propósito: com vários pacotes de teste em paralelo, sob
	// -race e com o disco ocupado, criar e remover bancos pode levar bem mais
	// que alguns segundos. Um prazo curto vira falha intermitente de teste.
	adminOpTimeout = 2 * time.Minute

	// Constante arbitrária que identifica o advisory lock deste helper.
	advisoryLockKey int64 = 0x636f72656c6564 // "coreled"

	// Lock que serializa os testes pesados entre processos (ver WithMaxConns).
	heavyTestLockKey int64 = 0x636f72656c6568 // "coreleh"

	// Teto de conexões de um teste pesado. Com um teste pesado por vez, sobra
	// folga no max_connections padrão (100) para os testes leves em paralelo.
	maxHeavyConns = 25
)

var testDBName = regexp.MustCompile(`^test_(\d+)_[0-9a-f]+$`)

// DB é um banco de teste isolado.
type DB struct {
	// Nome do banco, por exemplo test_1760000000_9f2c4a1b.
	Name string
	// Pool conecta como ledger_app.
	Pool *pgxpool.Pool

	url string // credenciais do ledger_test, apontando para Name
}

type options struct {
	maxConns int32
}

type Option func(*options)

// WithMaxConns define o tamanho do pool do ledger_app, até maxHeavyConns.
//
// Um pool maior que o padrão marca o teste como pesado. Testes pesados
// esperam um advisory lock exclusivo do PostgreSQL antes de começar, então só
// um deles roda por vez em toda a máquina, inclusive entre pacotes, que o
// `go test` executa em processos paralelos. Sem isso, os testes de contenção
// de pacotes diferentes se somavam e estouravam o max_connections (erro
// 53300), de forma intermitente.
func WithMaxConns(n int32) Option {
	return func(o *options) { o.maxConns = n }
}

// New devolve um banco com todas as migrations aplicadas.
func New(t testing.TB, opts ...Option) *DB {
	t.Helper()
	admin := adminURL(t)
	tpl := ensureTemplate(t, admin)
	return create(t, admin, tpl, opts)
}

// NewEmpty devolve um banco sem nenhuma migration aplicada, pertencente ao
// ledger_owner como o banco de desenvolvimento. Serve aos testes de migration.
func NewEmpty(t testing.TB, opts ...Option) *DB {
	t.Helper()
	admin := adminURL(t)
	ensureTemplate(t, admin) // também limpa bancos abandonados
	return create(t, admin, "", opts)
}

// Superuser devolve um pool para o mesmo banco sem SET ROLE, para os poucos
// testes que precisam contornar privilégios ou triggers de propósito.
func (d *DB) Superuser(t testing.TB) *pgxpool.Pool {
	t.Helper()
	return newPool(t, d.url, "", 2)
}

// SuperuserURL é a connection string deste banco com as credenciais do
// TEST_DATABASE_URL, para testes que sobem os caminhos reais do binário (por
// exemplo o `serve`) a partir da configuração.
func (d *DB) SuperuserURL() string { return d.url }

// OwnerSQL devolve um handle database/sql que roda como ledger_owner, o papel
// que aplica as migrations. Quem chama fica responsável por ele
// (postgres.NewMigrator o fecha).
func (d *DB) OwnerSQL(t testing.TB) *sql.DB {
	t.Helper()
	db, err := postgres.OpenMigrationDB(d.url, setRole(OwnerRole))
	if err != nil {
		t.Fatal(err)
	}
	return db
}

// adminURL devolve o TEST_DATABASE_URL; sem ele, pula ou derruba o teste.
func adminURL(t testing.TB) string {
	t.Helper()
	if testing.Short() {
		t.Skip("testdb: skipping database test in -short mode")
	}
	raw := os.Getenv("TEST_DATABASE_URL")
	if raw == "" {
		if os.Getenv("CI") == "true" || os.Getenv("TESTDB_REQUIRED") == "1" {
			t.Fatal("testdb: TEST_DATABASE_URL is required (CI=true or TESTDB_REQUIRED=1)")
		}
		t.Skip("testdb: TEST_DATABASE_URL not set; skipping database test (see README)")
	}
	if _, err := withDatabase(raw, "postgres"); err != nil {
		t.Fatal("testdb: TEST_DATABASE_URL must be a postgres:// URL")
	}
	return raw
}

// withDatabase devolve rawURL apontando para outro banco. O
// ConnConfig.ConnString() do pgx devolve a string original mesmo depois de
// trocar o campo Database, por isso a URL é reescrita.
func withDatabase(rawURL, dbName string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		return "", fmt.Errorf("not a postgres:// URL")
	}
	u.Path = "/" + dbName
	u.RawPath = ""
	return u.String(), nil
}

func mustWithDatabase(t testing.TB, rawURL, dbName string) string {
	t.Helper()
	s, err := withDatabase(rawURL, dbName)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

var (
	templateOnce sync.Once
	templateName string
	templateErr  error
)

func ensureTemplate(t testing.TB, admin string) string {
	t.Helper()
	templateOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		templateName, templateErr = buildTemplate(ctx, admin)
	})
	if templateErr != nil {
		t.Fatalf("testdb: prepare template database: %v", templateErr)
	}
	return templateName
}

func buildTemplate(ctx context.Context, admin string) (string, error) {
	hash, err := migrations.Hash()
	if err != nil {
		return "", err
	}
	name := "ledger_tpl_" + hash
	tmp := name + "_tmp"

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		return "", fmt.Errorf("connect with TEST_DATABASE_URL: %w", err)
	}
	defer func() { _ = conn.Close(context.Background()) }()

	// Lock de sessão: serializa o trabalho no template entre processos do `go test`.
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, advisoryLockKey); err != nil {
		return "", fmt.Errorf("advisory lock: %w", err)
	}
	defer func() { _, _ = conn.Exec(context.Background(), `SELECT pg_advisory_unlock($1)`, advisoryLockKey) }()

	if err := pruneStale(ctx, conn); err != nil {
		return "", err
	}

	var exists bool
	if err := conn.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)`, name).Scan(&exists); err != nil {
		return "", err
	}
	if exists {
		return name, nil
	}

	// Remove templates de outros conteúdos de migration e qualquer sobra pela metade.
	old, err := databasesLike(ctx, conn, `ledger\_tpl\_%`)
	if err != nil {
		return "", err
	}
	for _, db := range old {
		if err := dropDatabase(ctx, conn, db); err != nil {
			return "", err
		}
	}

	if _, err := conn.Exec(ctx, fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, ident(tmp), ident(OwnerRole))); err != nil {
		return "", fmt.Errorf("create template: %w", err)
	}
	tmpURL, err := withDatabase(admin, tmp)
	if err != nil {
		return "", err
	}
	if err := migrateDatabase(tmpURL); err != nil {
		return "", fmt.Errorf("migrate template: %w", err)
	}
	// O RENAME (e depois o CREATE DATABASE ... TEMPLATE) exige que ninguém
	// esteja conectado; migrateDatabase já fechou as conexões dele.
	if _, err := conn.Exec(ctx, fmt.Sprintf(`ALTER DATABASE %s RENAME TO %s`, ident(tmp), ident(name))); err != nil {
		return "", fmt.Errorf("publish template: %w", err)
	}
	return name, nil
}

// migrateDatabase aplica todas as migrations como ledger_owner, para que
// donos e grants sejam exatamente os do banco de desenvolvimento.
func migrateDatabase(dbURL string) error {
	db, err := postgres.OpenMigrationDB(dbURL, setRole(OwnerRole))
	if err != nil {
		return err
	}
	m, err := postgres.NewMigrator(db)
	if err != nil {
		return err
	}
	upErr := m.Up()
	closeErr := m.Close()
	if upErr != nil {
		return upErr
	}
	return closeErr
}

// pruneStale remove bancos de teste abandonados por execuções interrompidas
// (o Ctrl+C pula o t.Cleanup). O nome de cada um carrega a hora de criação.
func pruneStale(ctx context.Context, conn *pgx.Conn) error {
	names, err := databasesLike(ctx, conn, `test\_%`)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-staleAfter).Unix()
	for _, name := range names {
		m := testDBName.FindStringSubmatch(name)
		if m == nil {
			continue
		}
		created, err := strconv.ParseInt(m[1], 10, 64)
		if err != nil || created >= cutoff {
			continue
		}
		if err := dropDatabase(ctx, conn, name); err != nil {
			return err
		}
	}
	return nil
}

func create(t testing.TB, admin, template string, opts []Option) *DB {
	t.Helper()
	o := options{maxConns: defaultMaxConns}
	for _, opt := range opts {
		opt(&o)
	}
	if o.maxConns > maxHeavyConns {
		t.Fatalf("testdb: WithMaxConns(%d) is above the limit of %d", o.maxConns, maxHeavyConns)
	}
	if o.maxConns > defaultMaxConns {
		holdHeavyTestLock(t, admin)
	}

	name := fmt.Sprintf("test_%d_%s", time.Now().Unix(), randomHex(t, 4))
	stmt := fmt.Sprintf(`CREATE DATABASE %s OWNER %s`, ident(name), ident(OwnerRole))
	if template != "" {
		stmt += " TEMPLATE " + ident(template)
	}

	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), adminOpTimeout)
	defer cancel()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testdb: connect to create %s (after %s): %v", name, since(start), err)
	}
	_, err = conn.Exec(ctx, stmt)
	_ = conn.Close(ctx)
	if err != nil {
		t.Fatalf("testdb: create database %s (after %s): %v", name, since(start), err)
	}

	d := &DB{Name: name, url: mustWithDatabase(t, admin, name)}

	// Registrado antes dos cleanups dos pools, então roda depois deles (LIFO).
	t.Cleanup(func() {
		if os.Getenv("KEEP_TEST_DB") == "1" {
			t.Logf("testdb: KEEP_TEST_DB=1, keeping database %s", name)
			return
		}
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), adminOpTimeout)
		defer cancel()
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			t.Errorf("testdb: connect to drop %s (after %s): %v", name, since(start), err)
			return
		}
		defer func() { _ = conn.Close(ctx) }()
		if err := dropDatabase(ctx, conn, name); err != nil {
			t.Errorf("testdb: %v (after %s)", err, since(start))
		}
	})

	d.Pool = newPool(t, d.url, AppRole, o.maxConns)
	return d
}

// holdHeavyTestLock espera o lock dos testes pesados e o segura até o fim do
// teste. O lock é de sessão, então vale enquanto esta conexão estiver aberta;
// se o processo morrer, o PostgreSQL o libera sozinho.
func holdHeavyTestLock(t testing.TB, admin string) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testdb: connect for the heavy test lock: %v", err)
	}
	start := time.Now()
	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, heavyTestLockKey); err != nil {
		_ = conn.Close(ctx)
		t.Fatalf("testdb: heavy test lock: %v", err)
	}
	if waited := since(start); waited > time.Second {
		t.Logf("testdb: waited %s for another heavy test to finish", waited)
	}
	// Registrado antes do banco e do pool, então é liberado por último (LIFO).
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, `SELECT pg_advisory_unlock($1)`, heavyTestLockKey)
		_ = conn.Close(ctx)
	})
}

func newPool(t testing.TB, dbURL, role string, maxConns int32) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(dbURL)
	if err != nil {
		t.Fatal("testdb: invalid database URL")
	}
	cfg.MaxConns = maxConns
	if role != "" {
		cfg.AfterConnect = setRole(role)
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), adminOpTimeout)
	defer cancel()
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("testdb: open pool (after %s): %v", since(start), err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("testdb: ping (after %s): %v", since(start), err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func setRole(role string) func(context.Context, *pgx.Conn) error {
	return func(ctx context.Context, conn *pgx.Conn) error {
		_, err := conn.Exec(ctx, "SET ROLE "+ident(role))
		return err
	}
}

func databasesLike(ctx context.Context, conn *pgx.Conn, pattern string) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT datname FROM pg_database WHERE datname LIKE $1`, pattern)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowTo[string])
}

func dropDatabase(ctx context.Context, conn *pgx.Conn, name string) error {
	if _, err := conn.Exec(ctx, fmt.Sprintf(`DROP DATABASE IF EXISTS %s WITH (FORCE)`, ident(name))); err != nil {
		return fmt.Errorf("drop database %s: %w", name, err)
	}
	return nil
}

func since(start time.Time) time.Duration {
	return time.Since(start).Round(time.Millisecond)
}

func ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

func randomHex(t testing.TB, n int) string {
	t.Helper()
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}
