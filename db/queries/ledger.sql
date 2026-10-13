-- name: GetLedgerTransaction :one
SELECT id, type, currency, created_at
FROM ledger_transactions
WHERE id = $1;

-- Pernas de uma transação, com o tipo e o código de sistema da conta, para
-- que a API mostre a WORLD como "system_code" e não pelo ID.
-- name: ListLedgerTransactionEntries :many
SELECT e.id, e.account_id, e.amount, e.account_seq, e.balance_after, e.created_at,
       a.kind AS account_kind, a.system_code
FROM ledger_entries e
JOIN accounts a ON a.id = e.account_id
WHERE e.transaction_id = $1
ORDER BY e.account_id;

-- Histórico de uma conta, do mais novo para o mais antigo, a partir de um
-- cursor (SPEC 12.3). "account_seq < before_seq" mantém as páginas estáveis:
-- pernas novas sempre ganham sequência maior, então nunca entram no meio de
-- uma página já lida. Usa o índice único (account_id, account_seq).
-- name: ListAccountEntries :many
SELECT e.id, e.transaction_id, t.type, e.amount, e.balance_after, e.account_seq, e.created_at
FROM ledger_entries e
JOIN ledger_transactions t ON t.id = e.transaction_id
WHERE e.account_id = @account_id AND e.account_seq < @before_seq
ORDER BY e.account_seq DESC
LIMIT @row_limit;

-- name: InsertLedgerTransaction :one
INSERT INTO ledger_transactions (id, type, currency)
VALUES ($1, $2, $3)
RETURNING created_at;

-- name: InsertLedgerEntry :one
INSERT INTO ledger_entries (id, transaction_id, account_id, currency, amount, account_seq, balance_after)
VALUES ($1, $2, $3, $4, $5, $6, $7)
RETURNING created_at;

-- Grava o novo saldo e a nova sequência. A condição em last_entry_seq é uma
-- defesa extra: com o lock da linha ela sempre bate; se não bater, alguém
-- escreveu na conta sem o lock, e o motor aborta em vez de seguir.
-- name: ApplyAccountPosting :execrows
UPDATE accounts
SET balance = @balance, last_entry_seq = @new_seq, updated_at = now()
WHERE id = @id AND last_entry_seq = @expected_seq;
