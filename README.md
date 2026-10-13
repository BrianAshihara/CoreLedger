# CoreLedger

A double-entry ledger engine in Go and PostgreSQL. It simulates the core money-movement infrastructure of a fintech and exists to demonstrate **correctness under concurrency, transactional integrity, idempotency and auditability**, not feature count.

It does not move real money, connect to banks or payment networks, or handle card data.

> Every change to money is an immutable, balanced, append-only ledger transaction. Nothing else may change a balance.

The full specification is in [SPEC.md](SPEC.md). How it works is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), the complete endpoint reference in [docs/API.md](docs/API.md), and every decision with its alternatives in [docs/adr](docs/adr).

## Status

| Phase | Scope | Status |
|---|---|---|
| 1 | Foundation: config, logging, migrations, schema check, health endpoints, test database helper, CI | **Done** |
| 2 | Accounts: create, read, balance, admin status changes, error model | **Done** |
| 3 | Ledger posting engine (double entry, locking, concurrency) and reconciliation core | **Done** |
| 4 | Transfers, deposits, withdrawals, idempotency | **Done** |
| 5 | Account history (cursor pagination) and reconciliation (endpoint and CLI) | **Done** |
| 6 | Load testing, failure injection, documentation (MVP complete) | Done |

Only what is marked done exists in the code.

Concurrency is proven, not assumed: with the race detector on, 100 concurrent transfers of 200 from a balance of 10 000 produce **exactly** 50 successes every time (C1), 50 + 50 opposite transfers never deadlock (C2), and 100 concurrent credits leave the destination's sequence exactly 1..100 (C3). Each test passed 20 runs in a row. See [ADR 0004](docs/adr/0004-sorted-row-locks-read-committed.md).

Idempotency is proven the same way: 20 concurrent identical requests create exactly one transaction, with one `201` and nineteen `200` replays carrying the same `transaction_id` (C4), and two concurrent requests with the same key and different bodies give one `201` and one `409` (C5). See [ADR 0008](docs/adr/0008-idempotency-key-claimed-in-posting-transaction.md).

## Why this design

A naive "UPDATE balance" model loses money in five ways: a crash between the debit and the credit, two concurrent requests spending the same balance, a retried request applied twice, no history to explain a balance, and silent corruption with nothing to compare against. The roadmap answers each one:

| Failure | Answer | Phase |
|---|---|---|
| Crash halfway | One database transaction per posting; all or nothing | 3 |
| Double spending | `SELECT ... FOR UPDATE` row locks, always in ascending account ID order (no deadlocks) | 3 |
| Duplicate requests | `Idempotency-Key` claimed inside the posting transaction | 4 |
| Unexplainable balances | Immutable entries carrying `balance_after` and a gap-free per-account sequence | 3, 5 |
| Silent corruption | Reconciliation of stored balances against the sum of entries | 3, 5 |

Invariants are enforced twice: by Go code, and by PostgreSQL itself (constraints, deferred constraint triggers, and a runtime role that cannot `UPDATE` or `DELETE` ledger tables).

## Architecture

A modular monolith: one binary, one database.

```text
HTTP (chi) -> application services -> ledger posting engine -> PostgreSQL
```

The database has three roles with different powers:

| Role | Used by | Can |
|---|---|---|
| `ledger_owner` | `ledger migrate` | create and alter the schema |
| `ledger_app` | `ledger serve` | only what each migration grants, table by table |
| `ledger_test` | tests | create and drop one database per test |

Every privilege of `ledger_app` is granted explicitly by the migration that creates the table, and a test asserts the complete list (see [ADR 0002](docs/adr/0002-privileges-in-migrations.md)).

### Project layout

```text
cmd/ledger/            the binary: serve, migrate
db/migrations/         SQL migrations, embedded in the binary
db/queries/            SQL consumed by sqlc
internal/domain/       pure Go rules: money, account, ledger (no database, no HTTP)
internal/app/          use cases; opens database transactions
internal/app/posting/  the posting engine: the only writer of ledger tables and balances
internal/app/reconcile/ checks stored balances and history against the ledger (read-only)
internal/httpapi/      router, middleware, handlers, error mapping
internal/postgres/     pool, transactions, migrations, schema check, repositories
internal/postgres/sqlc generated code (do not edit)
internal/platform/     config (env) and logging (slog)
internal/testdb/       one PostgreSQL database per test
scripts/db-setup.sql   one-time roles and dev database
```

## Prerequisites

No Docker, containers or WSL are used anywhere ([ADR 0001](docs/adr/0001-no-container-tooling.md)). Everything runs natively on Windows, Linux and macOS.

| Tool | Version | Notes |
|---|---|---|
| Go | 1.25+ | |
| PostgreSQL | 16+ | Running locally on port 5432 |
| GNU make | any | Optional; every target has a plain command below |
| sqlc | 1.31.1 | Only to regenerate code. Exact version: CI fails on a different one |
| golangci-lint | 2.14.0 | Only to lint |
| migrate CLI | 4.20.1 | Only to create new migration files |

### Windows (PowerShell, no WSL)

- **Go:** official MSI from <https://go.dev/dl/>.
- **PostgreSQL:** EDB installer from <https://www.postgresql.org/download/windows/>, with Command Line Tools. Add `C:\Program Files\PostgreSQL\<version>\bin` to `PATH`.
- **make:** `winget install ezwinports.make`.
- **sqlc, golangci-lint, migrate:** download the `windows_amd64` release zips from GitHub, put the `.exe` files in a folder on `PATH`. Use the sqlc release binary, not `go install` (it may require cgo).

### Linux (Debian/Ubuntu)

```sh
sudo apt install postgresql-common
sudo /usr/share/postgresql-common/pgdg/apt.postgresql.org.sh
sudo apt install postgresql-18
sudo systemctl enable --now postgresql
```

### macOS

```sh
brew install postgresql@18 && brew services start postgresql@18
```

## Getting started

```powershell
git clone https://github.com/brianashihara/coreledger.git
cd coreledger
Copy-Item .env.example .env      # sh: cp .env.example .env
make db-setup                    # once; asks for the postgres superuser password
make migrate-up
make run
```

`make db-setup` only creates three roles and the `ledger_dev` database. The passwords in `.env.example` are for local development only.

Then, in another terminal:

```powershell
make demo
```

The demo is a narrated walkthrough against the running API. It prints every raw HTTP request and response and checks each result:

- it opens accounts, makes a deposit, and looks inside the balanced transaction;
- it transfers money, replays the same request, and reuses the key for a different one;
- it overdraws an account, then retries the rejected key after a top-up;
- it sends invalid input;
- it fires **50 concurrent transfers at an account that can pay only 10** and **20 concurrent copies of one request**;
- it pages through a statement and reconciles the ledger.

It takes about a second and exits with code 1 if any check fails. `NO_COLOR=1` or `go run ./scripts/demo -no-color` turns colors off, and `DEMO_API_URL` points it at another address.

`ledger serve` never migrates by itself. It refuses to start when the database has no schema, a dirty schema (a migration failed halfway), or a version different from the one the binary was built for.

## API

| Method and path | Auth | Description |
|---|---|---|
| `GET /healthz` | | Process is alive (never touches the database) |
| `GET /readyz` | | Database reachable within 1s, else `503` |
| `POST /v1/accounts` | | Create a `USER` account (`ACTIVE`, balance 0) |
| `GET /v1/accounts/{id}` | | Read an account |
| `GET /v1/accounts/{id}/balance` | | Read the balance |
| `GET /v1/accounts/{id}/entries` | | History, newest first: `?limit=1..200` (default 50) and `&cursor=<next_cursor>` |
| `PATCH /v1/admin/accounts/{id}/status` | admin | `ACTIVE ↔ SUSPENDED`, `→ CLOSED` only with zero balance; `CLOSED` is terminal |
| `POST /v1/transfers` | | Move money between two `USER` accounts. Requires `Idempotency-Key` |
| `POST /v1/admin/deposits` | admin | Money enters the system: `WORLD −X`, account `+X`. Requires `Idempotency-Key` |
| `POST /v1/admin/withdrawals` | admin | Money leaves the system: account `−X`, `WORLD +X`. Requires `Idempotency-Key` |
| `GET /v1/transactions/{id}` | | A transaction and its signed entries |
| `GET /v1/admin/reconciliation` | admin | Checks every balance and the whole history against the ledger; never fixes anything |

Amounts are integers in minor units (`R$ 10.50` is `1050`), greater than 0 and at most `1000000000000`. `10.5`, `"1050"`, `1e3` and `null` are rejected. Admin routes require `Authorization: Bearer <ADMIN_TOKEN>`. System accounts (such as `WORLD`, the counterpart of deposits and withdrawals) behave as non-existent on every endpoint.

Every error has the same shape, with a stable code and the request ID that also appears in the `X-Request-Id` header and in the server log:

```json
{ "error": { "code": "UNSUPPORTED_CURRENCY", "message": "Currency is not supported", "request_id": "01a1268b-..." } }
```

| Code | HTTP | When |
|---|---|---|
| `INVALID_REQUEST` | 400 | Malformed JSON, unknown field, invalid `owner_id`, status, `Idempotency-Key` or `limit` |
| `INVALID_AMOUNT` | 400 | Amount missing, not an integer, not positive, or above the maximum |
| `MISSING_IDEMPOTENCY_KEY` | 400 | Money-movement request without `Idempotency-Key` |
| `INVALID_CURSOR` | 400 | History cursor that was not produced by the API |
| `UNAUTHORIZED` | 401 | Missing or wrong admin token |
| `ACCOUNT_NOT_FOUND` | 404 | Unknown, malformed or system account ID |
| `TRANSACTION_NOT_FOUND` | 404 | Unknown or malformed transaction ID |
| `ROUTE_NOT_FOUND` | 404 | No such route |
| `METHOD_NOT_ALLOWED` | 405 | Route exists, method does not |
| `IDEMPOTENCY_CONFLICT` | 409 | `Idempotency-Key` already used with a different request or endpoint |
| `INVALID_STATUS_TRANSITION` | 409 | For example `CLOSED → ACTIVE` |
| `ACCOUNT_HAS_BALANCE` | 409 | Closing an account whose balance is not zero |
| `UNSUPPORTED_MEDIA_TYPE` | 415 | Body is not `application/json` |
| `UNSUPPORTED_CURRENCY` | 422 | Currency not seeded (only `BRL` today); codes are case-sensitive |
| `ACCOUNT_NOT_ACTIVE` | 422 | An account in the movement is `SUSPENDED` or `CLOSED` |
| `CURRENCY_MISMATCH` | 422 | An account's currency differs from the request's |
| `INSUFFICIENT_FUNDS` | 422 | The debited account would go negative |
| `SAME_ACCOUNT_TRANSFER` | 422 | Source and destination are the same account |
| `INTERNAL_ERROR` | 500 | Anything unexpected; details go to the log only |
| `SERVICE_BUSY` | 503 | An account lock was not obtained in time; retry with the same key (`Retry-After: 1`) |

### History and reconciliation

The history of an account explains its balance line by line: every entry carries the signed movement (`direction` and `amount`), the `balance_after` and its `sequence`. Pages are stable while money is moving: a cursor asks for entries older than the last one read, and new entries always get higher sequences, so nothing is skipped or repeated (tested with deposits arriving while the pages are read).

Reconciliation runs six checks in a single read-only snapshot: stored balance = sum of entries, stored balance = `balance_after` of the last entry, gap-free sequences, an unbroken `balance_after` chain, every transaction balanced, and all balances of a currency summing to zero. It reports and never corrects; fixing data automatically would hide the bug that corrupted it.

```json
{
  "status": "INCONSISTENT",
  "checked_accounts": 3,
  "checked_transactions": 2,
  "issues": [
    { "check": "BALANCE_MATCHES_LEDGER", "account_id": "01a1...", "currency": "BRL",
      "expected": 700, "actual": 750, "detail": "stored balance differs from the sum of the account's entries" }
  ]
}
```

### Idempotency

Money-movement requests require an `Idempotency-Key` header (1 to 255 printable ASCII characters). Retrying with the same key and the same request is always safe:

- the first successful request returns `201 Created`;
- an identical retry returns `200 OK` with `Idempotent-Replayed: true` and the same body, without moving money again;
- the same key with a different request, or on a different endpoint, returns `409 IDEMPOTENCY_CONFLICT`;
- a request that was **rejected** (for example `INSUFFICIENT_FUNDS`) does not consume its key: a retry is evaluated again and may succeed later ([ADR 0009](docs/adr/0009-failed-requests-do-not-consume-keys.md)).

### Examples

sh:

```sh
curl -s -X POST localhost:8080/v1/accounts \
  -H 'Content-Type: application/json' \
  -d '{"owner_id":"maria","currency":"BRL"}'

curl -s localhost:8080/v1/accounts/<id>/balance

curl -s -X PATCH localhost:8080/v1/admin/accounts/<id>/status \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer dev-admin-token-change-me' \
  -d '{"status":"SUSPENDED"}'

curl -s -X POST localhost:8080/v1/admin/deposits \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer dev-admin-token-change-me' \
  -H 'Idempotency-Key: deposit-001' \
  -d '{"account_id":"<id>","amount":100000,"currency":"BRL"}'

curl -s -X POST localhost:8080/v1/transfers \
  -H 'Content-Type: application/json' \
  -H 'Idempotency-Key: transfer-001' \
  -d '{"from_account_id":"<id>","to_account_id":"<other id>","amount":20000,"currency":"BRL"}'

curl -s localhost:8080/v1/transactions/<transaction id>
```

PowerShell (use `Invoke-RestMethod`: Windows PowerShell 5.1 strips the inner double quotes when passing JSON to `curl.exe`, which then sends invalid JSON):

```powershell
$account = Invoke-RestMethod -Method Post http://localhost:8080/v1/accounts `
  -ContentType 'application/json' -Body '{"owner_id":"maria","currency":"BRL"}'

Invoke-RestMethod "http://localhost:8080/v1/accounts/$($account.id)/balance"

Invoke-RestMethod -Method Patch "http://localhost:8080/v1/admin/accounts/$($account.id)/status" `
  -ContentType 'application/json' -Body '{"status":"SUSPENDED"}' `
  -Headers @{ Authorization = 'Bearer dev-admin-token-change-me' }

Invoke-RestMethod -Method Post http://localhost:8080/v1/admin/deposits -ContentType 'application/json' `
  -Headers @{ Authorization = 'Bearer dev-admin-token-change-me'; 'Idempotency-Key' = 'deposit-001' } `
  -Body "{""account_id"":""$($account.id)"",""amount"":100000,""currency"":""BRL""}"
```

## Load testing

[k6](https://k6.io) scenarios in [tests/load/ledger.js](tests/load/ledger.js): accounts and initial deposits are created through the API, then `transfers` (random pairs of 200 accounts), `deposits` (all of them lock the WORLD row) or `mixed` (90/10). Insufficient funds and idempotent replays are valid outcomes; any other response counts as an error, and the threshold is zero. Every run uses a fresh `ledger_load` database, never `ledger_dev`.

```powershell
make load-db                       # recreate ledger_load (needs TEST_DATABASE_URL)
make load-serve                    # API against ledger_load, in its own terminal
make load-test scenario=transfers  # or deposits, mixed; k6 must be installed
make load-reconcile                # reconcile ledger_load after the run
```

### Results

Development machine: Windows 11, 12 logical CPUs, PostgreSQL 18.6, k6 and the API on the same host, `synchronous_commit=on`, 200 funded accounts. Zero unexpected responses and reconciliation `OK` after every run.

20 virtual users, 30 s, access log at INFO written to a file:

| Scenario | Requests/s | p50 | p95 | p99 |
|---|---|---|---|---|
| transfers | 1,798 | 9.8 ms | 15.2 ms | 20.8 ms |
| deposits (WORLD hot row) | 700 | 20.0 ms | 59.1 ms | 75.4 ms |
| mixed 90/10 | 1,803 | 9.7 ms | 14.7 ms | 25.7 ms |

Scaling, 20 s per run:

| Scenario | 5 VUs | 20 VUs | 50 VUs |
|---|---|---|---|
| transfers, requests/s (p95) | 1,067 (5.9 ms) | about 1,740 (15 ms) | 1,769 (32.3 ms) |
| deposits, requests/s (p95) | 604 (12.8 ms) | 582 (78.5 ms) | 531 (411.7 ms) |

Deposits do not scale: every deposit locks the same WORLD row, so more clients only lengthen the queue. Transfers spread over 200 accounts and keep scaling. See [ADR 0010](docs/adr/0010-world-hot-row.md).

**Variance, reported rather than hidden.** In 2 of 8 transfer runs at 20 VUs, throughput dropped (996 and 276 requests/s) because of stalls of about half a second. A 4-minute run traced most stalls to the synchronous access log: with the log written to a file, the API stalled for 50 of 232 seconds (one stall lasted about 18 s); with access logging off, 11 of 233 seconds, the longest about 2.5 s, at 2,223 requests/s with p95 11.4 ms. The log writes themselves stalled for reasons outside the application on this machine (antivirus or disk are the likely candidates). Two of those stalls outlasted the server write timeout; the load generator's Go HTTP client resent the cut requests automatically, and they came back as idempotent replays: 40 retries, zero money moved twice. See [ADR 0011](docs/adr/0011-synchronous-access-logging.md).

## Failure injection

| Failure | Result | Evidence |
|---|---|---|
| Database connection killed with `pg_terminate_backend` after the first entry of a posting | nothing persisted; error classified as unavailable (`503`); the pool replaces the connection; the next posting succeeds | `TestBackendKilledMidPostingLeavesNoState`, repeated runs |
| Failure injected inside the engine after the first entry | whole posting rolled back, key stays free | `TestFailureMidPostingLeavesNoState` |
| Account lock held by another transaction | `503 SERVICE_BUSY` with `Retry-After: 1` after `DB_LOCK_TIMEOUT` | `TestLockTimeoutIsServiceBusy` |
| PostgreSQL restarted during a 10-minute load run (20 VUs) | about 1,400 requests answered `503 SERVICE_BUSY` during the 2-second outage, no `500`; `/readyz` went 503 and back to 200 by itself; the API was never restarted; reconciliation OK over 819,783 transactions | `Restart-Service postgresql-x64-18` by hand, k6 counters, API log |
| Every local TCP connection aborted by Windows mid-run (unplanned, same run) | PostgreSQL did not notice and kept 5 orphaned transactions holding account locks; requests touching those accounts got `503` after `DB_LOCK_TIMEOUT` until the run ended. Fixed: the server now ends a transaction idle longer than `REQUEST_TIMEOUT` and releases its locks | `pg_stat_activity` during the run; `TestPoolIdleInTransactionTimeoutReleasesLocks`; [ADR 0012](docs/adr/0012-idle-in-transaction-timeout.md) |

## MVP checklist (SPEC 24)

- [x] With Go and a native PostgreSQL, the README steps start the API on a clean machine (verified on Windows without WSL).
- [x] Accounts can be created, read, suspended, reactivated and closed (only with zero balance).
- [x] Deposits and withdrawals go through WORLD and are admin-only.
- [x] Transfers work and reject every invalid case with the documented code.
- [x] Invariants I1 to I10 each have passing tests, including database-level tests.
- [x] Concurrency tests C1 to C6 pass repeatedly under `-race`.
- [x] Duplicate and conflicting idempotent requests behave per the spec, including concurrently.
- [x] Failed operations leave no ledger state and free the idempotency key.
- [x] History is paginated, newest first, stable under concurrent writes.
- [x] Reconciliation reports OK on valid data and detects each injected inconsistency.
- [ ] CI runs lint, unit and integration tests on every push: the workflow is written and validated with `actionlint`, but has not run yet (the repository has not been pushed).
- [x] README, ARCHITECTURE, API and ADRs complete, load-test numbers published.

## Commands
The binary reads only real environment variables. The Makefile loads `.env` for convenience; without `make`, set the variables yourself (on Windows, `$env:DATABASE_URL = "..."`).

| make | Plain command | What it does |
|---|---|---|
| `make db-setup` | Windows: `psql -U postgres -h localhost -f scripts/db-setup.sql`<br>Linux: `sudo -u postgres psql -f scripts/db-setup.sql`<br>macOS: `psql -d postgres -f scripts/db-setup.sql` | One-time roles and dev database |
| `make migrate-up` | `go run ./cmd/ledger migrate up` | Apply pending migrations |
| `make migrate-down n=1` | `go run ./cmd/ledger migrate down 1` | Revert exactly N migrations (N is required) |
| `make migrate-down-all` | `go run ./cmd/ledger migrate down --all` | Revert everything (destroys all data) |
| `make migrate-version` | `go run ./cmd/ledger migrate version` | Show the applied version |
| `make migrate-new name=x` | `migrate create -ext sql -dir db/migrations -seq -digits 6 x` | New migration pair |
| `make run` | `go run ./cmd/ledger serve` | Start the API |
| `make build` | `go build -o bin/ledger ./cmd/ledger` | Build the binary |
| `make test` | `go test ./...` | All tests; database tests skip without `TEST_DATABASE_URL` |
| `make test-unit` | `go test -short ./...` | Unit tests only, no database |
| `make test-integration` | `go test -count=1 ./...` with `TESTDB_REQUIRED=1` | All tests; fails instead of skipping without a database |
| `make lint` | `golangci-lint run` | Static analysis (includes `go vet`) |
| `make sqlc` | `sqlc generate` | Regenerate `internal/postgres/sqlc` |
| `make sqlc-diff` | `sqlc diff` | Fail if generated code is stale |
| `make demo` | `go run ./scripts/demo` | Narrated walkthrough against the running API, with raw requests and responses |
| `make reconcile` | `go run ./cmd/ledger reconcile` | Check the ledger; prints a JSON report and exits with code 1 if it is inconsistent |

The Makefile sets `CGO_ENABLED=0`: the project needs no C compiler.

### Configuration

| Variable | Used by | Default |
|---|---|---|
| `DATABASE_URL` | `serve` (role `ledger_app`) | required |
| `MIGRATE_DATABASE_URL` | `migrate` (role `ledger_owner`) | required |
| `ADMIN_TOKEN` | `serve`, admin routes | required, at least 16 characters |
| `HTTP_ADDR` | `serve` | `:8080` |
| `REQUEST_TIMEOUT` | `serve` | `5s` |
| `DB_LOCK_TIMEOUT` | `serve` | `2s`; must be shorter than `REQUEST_TIMEOUT` |
| `DB_MAX_CONNS` | `serve` | `10` |
| `LOG_LEVEL` | all | `info` |
| `LOG_FORMAT` | all | `text` (`json` for machines) |
| `TEST_DATABASE_URL` | tests (role `ledger_test`) | unset: database tests skip |

Invalid configuration fails at startup with the complete list of problems.

## Testing

```powershell
make test-unit           # no database
make test-integration    # against your local PostgreSQL
```

Integration tests run against a real PostgreSQL, never mocks. Each test gets its own database, cloned in milliseconds from a template that already has every migration applied ([ADR 0003](docs/adr/0003-content-hashed-test-template.md)). Tests connect as `ledger_app`, so they run with production privileges.

- `KEEP_TEST_DB=1` keeps a test's database after it finishes, for inspection with `psql`.
- Databases left behind by an interrupted run are dropped automatically after one hour.

### Race detector

CI always runs `go test -race` on Linux. Locally on Windows it is optional, because it needs cgo and a 64-bit C compiler:

```powershell
winget install MSYS2.MSYS2
# in the MSYS2 UCRT64 shell:
pacman -S mingw-w64-ucrt-x86_64-gcc
# then, from the project:
make test-integration RACE=1
```

With `RACE=1` on Windows, the Makefile puts `C:\msys64\ucrt64\bin` first on `PATH` for that run only, so another `gcc` earlier on the system `PATH` (for example a 32-bit MinGW) cannot get in the way. Set `MSYS2_UCRT64_BIN` in `.env` if MSYS2 is installed elsewhere.

## Continuous integration

GitHub Actions on every push and pull request ([.github/workflows/ci.yml](.github/workflows/ci.yml)), without containers:

- **lint:** `go vet`, `golangci-lint`, `sqlc diff`.
- **test:** PostgreSQL 16 and 18 installed natively from the PGDG repository, `scripts/db-setup.sql`, `go test -race`, then a migrate and build smoke test.

## Known limitations

- **No end-user authentication in the MVP.** Anyone who knows an account ID can read it and transfer from it, and anyone who knows a transaction ID can read it. This is a deliberate scope cut; administrative endpoints require a bearer token.
- Account creation is not idempotent: a retried request creates a second account. It moves no money, so this is accepted (spec 11.1).
- BRL is the only seeded currency. The schema supports more.
