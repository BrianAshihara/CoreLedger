-- Reserva a chave. Se outra transação já a reservou e ainda não terminou,
-- este INSERT espera no índice único até ela confirmar ou desfazer (SPEC
-- 11.2). Sem linha de volta significa que a chave já estava em uso.
-- name: ClaimIdempotencyKey :one
INSERT INTO idempotency_keys (key, request_method, request_path, request_fingerprint, transaction_id)
VALUES ($1, $2, $3, $4, $5)
ON CONFLICT (key) DO NOTHING
RETURNING key;

-- name: GetIdempotencyKey :one
SELECT key, request_method, request_path, request_fingerprint, transaction_id
FROM idempotency_keys
WHERE key = $1;
