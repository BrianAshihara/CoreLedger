-- name: ListCurrencies :many
SELECT code, exponent
FROM currencies
ORDER BY code;

-- name: CurrencyExists :one
SELECT EXISTS (SELECT 1 FROM currencies WHERE code = $1);
