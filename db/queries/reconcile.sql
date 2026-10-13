-- Checagens de reconciliação (SPEC 13). Cada query devolve só as linhas
-- inconsistentes: num banco saudável, todas voltam vazias.

-- name: CountAccounts :one
SELECT count(*) FROM accounts;

-- name: CountLedgerTransactions :one
SELECT count(*) FROM ledger_transactions;

-- 1. I5: saldo guardado = soma das pernas.
-- name: ReconcileBalanceMatchesLedger :many
SELECT a.id, a.currency, a.balance AS stored_balance,
       coalesce(sum(e.amount), 0)::bigint AS ledger_balance
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.id, a.currency, a.balance
HAVING a.balance <> coalesce(sum(e.amount), 0)
ORDER BY a.id;

-- 2. Saldo guardado = balance_after da última perna (ou 0 sem pernas).
-- name: ReconcileLastEntryMatchesBalance :many
SELECT a.id, a.currency, a.balance AS stored_balance, a.last_entry_seq,
       e.balance_after AS last_balance_after
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id AND e.account_seq = a.last_entry_seq
WHERE (a.last_entry_seq = 0 AND a.balance <> 0)
   OR (a.last_entry_seq > 0 AND (e.id IS NULL OR e.balance_after <> a.balance))
ORDER BY a.id;

-- 3. I6: as sequências de cada conta são exatamente 1..last_entry_seq. Como
-- (account_id, account_seq) é único e positivo, contagem = máximo = último
-- prova que não há buraco.
-- name: ReconcileSequenceGapFree :many
SELECT a.id, a.currency, a.last_entry_seq,
       count(e.id) AS entry_count,
       coalesce(max(e.account_seq), 0)::bigint AS max_seq
FROM accounts a
LEFT JOIN ledger_entries e ON e.account_id = a.id
GROUP BY a.id, a.currency, a.last_entry_seq
HAVING count(e.id) <> a.last_entry_seq OR coalesce(max(e.account_seq), 0) <> a.last_entry_seq
ORDER BY a.id;

-- 4. I6: cada balance_after = balance_after anterior + amount.
-- name: ReconcileBalanceAfterChain :many
SELECT account_id, account_seq, amount, balance_after, previous_balance_after
FROM (
    SELECT account_id, account_seq, amount, balance_after,
           (lag(balance_after, 1, 0::bigint) OVER (PARTITION BY account_id ORDER BY account_seq))::bigint AS previous_balance_after
    FROM ledger_entries
) chain
WHERE balance_after <> previous_balance_after + amount
ORDER BY account_id, account_seq;

-- 5. I1 (defesa em profundidade): toda transação tem 2+ pernas que somam zero.
-- name: ReconcileTransactionsBalanced :many
SELECT t.id, t.currency, count(e.id) AS entry_count,
       coalesce(sum(e.amount), 0)::bigint AS entries_sum
FROM ledger_transactions t
LEFT JOIN ledger_entries e ON e.transaction_id = t.id
GROUP BY t.id, t.currency
HAVING count(e.id) < 2 OR coalesce(sum(e.amount), 0) <> 0
ORDER BY t.id;

-- 6. I9: por moeda, a soma de todos os saldos é zero.
-- name: ReconcileCurrencyZeroSum :many
SELECT currency, sum(balance)::bigint AS balance_sum
FROM accounts
GROUP BY currency
HAVING sum(balance) <> 0
ORDER BY currency;
