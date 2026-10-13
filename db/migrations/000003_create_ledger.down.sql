BEGIN;

DROP TABLE ledger_entries;
DROP TABLE ledger_transactions;

DROP FUNCTION ledger_reject_mutation();
DROP FUNCTION ledger_transactions_balanced_trigger();
DROP FUNCTION ledger_entries_balanced_trigger();
DROP FUNCTION ledger_check_transaction_balanced(uuid);

COMMIT;
