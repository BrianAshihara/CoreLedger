-- name: InsertAccount :one
INSERT INTO accounts (id, kind, owner_id, system_code, currency)
VALUES ($1, $2, $3, $4, $5)
RETURNING *;

-- Conta de sistema por código e moeda (por exemplo, a WORLD de BRL).
-- name: GetSystemAccount :one
SELECT *
FROM accounts
WHERE system_code = $1 AND currency = $2;

-- name: GetAccount :one
SELECT *
FROM accounts
WHERE id = $1;

-- Trava a linha até o fim da transação. Mudanças de status usam o mesmo lock
-- que os lançamentos, então nunca correm em paralelo com movimentação de
-- dinheiro (SPEC 7.1).
-- name: GetAccountForUpdate :one
SELECT *
FROM accounts
WHERE id = $1
FOR UPDATE;

-- name: UpdateAccountStatus :one
UPDATE accounts
SET status = $2, updated_at = now()
WHERE id = $1
RETURNING *;
