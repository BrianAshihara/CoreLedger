BEGIN;

-- Uma transação do ledger é um fato financeiro: só existe se foi lançada por
-- inteiro. Não tem coluna de status (SPEC 7.2).
CREATE TABLE ledger_transactions (
    id          uuid        PRIMARY KEY,
    type        text        NOT NULL CHECK (type IN ('DEPOSIT', 'WITHDRAWAL', 'TRANSFER')),
    currency    char(3)     NOT NULL REFERENCES currencies (code),
    created_at  timestamptz NOT NULL DEFAULT now(),

    -- Alvo da FK composta das pernas (I2).
    CONSTRAINT ledger_transactions_id_currency_key UNIQUE (id, currency)
);

-- Cada linha é uma perna: um valor com sinal aplicado a uma conta.
-- Negativo é débito, positivo é crédito.
CREATE TABLE ledger_entries (
    id              uuid        PRIMARY KEY,
    transaction_id  uuid        NOT NULL,
    account_id      uuid        NOT NULL,
    currency        char(3)     NOT NULL,
    amount          bigint      NOT NULL,
    account_seq     bigint      NOT NULL CHECK (account_seq > 0),
    balance_after   bigint      NOT NULL,
    created_at      timestamptz NOT NULL DEFAULT now(),

    -- I10: nunca zero e nunca acima do MAX_AMOUNT (SPEC 5) em valor absoluto.
    CONSTRAINT ledger_entries_amount_valid CHECK (
        amount <> 0 AND amount BETWEEN -1000000000000 AND 1000000000000
    ),

    -- I2: a moeda da perna é a da transação e a da conta, garantido pelo banco.
    CONSTRAINT ledger_entries_transaction_fkey
        FOREIGN KEY (transaction_id, currency) REFERENCES ledger_transactions (id, currency),
    CONSTRAINT ledger_entries_account_fkey
        FOREIGN KEY (account_id, currency) REFERENCES accounts (id, currency),

    -- I6: cada conta tem uma sequência sem repetição. Também é o índice do histórico.
    CONSTRAINT ledger_entries_account_seq_key UNIQUE (account_id, account_seq),

    -- Uma perna por conta em cada transação (SPEC 7.3). Também indexa a
    -- busca das pernas de uma transação.
    CONSTRAINT ledger_entries_transaction_account_key UNIQUE (transaction_id, account_id)
);

-- I1: toda transação tem pelo menos 2 pernas e elas somam exatamente zero.
-- Checado no COMMIT (constraint trigger adiado), então a aplicação pode
-- inserir as pernas uma a uma. A soma é feita em numeric para não estourar.
CREATE FUNCTION ledger_check_transaction_balanced(txn_id uuid) RETURNS void
LANGUAGE plpgsql AS $$
DECLARE
    n integer;
    s numeric;
BEGIN
    SELECT count(*), coalesce(sum(amount), 0) INTO n, s
    FROM ledger_entries
    WHERE transaction_id = txn_id;

    IF n < 2 OR s <> 0 THEN
        RAISE EXCEPTION 'ledger transaction % is unbalanced (entries=%, sum=%)', txn_id, n, s
            USING ERRCODE = 'check_violation';
    END IF;
END $$;

CREATE FUNCTION ledger_entries_balanced_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM ledger_check_transaction_balanced(NEW.transaction_id);
    RETURN NULL;
END $$;

CREATE FUNCTION ledger_transactions_balanced_trigger() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    PERFORM ledger_check_transaction_balanced(NEW.id);
    RETURN NULL;
END $$;

CREATE CONSTRAINT TRIGGER ledger_entries_balanced
    AFTER INSERT ON ledger_entries
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_entries_balanced_trigger();

-- Pega a transação sem nenhuma perna, que o trigger acima nunca veria.
CREATE CONSTRAINT TRIGGER ledger_transactions_balanced
    AFTER INSERT ON ledger_transactions
    DEFERRABLE INITIALLY DEFERRED
    FOR EACH ROW EXECUTE FUNCTION ledger_transactions_balanced_trigger();

-- I4: o ledger só cresce. UPDATE, DELETE e TRUNCATE são recusados para todos,
-- inclusive superusuários. Correções são feitas com transações novas.
CREATE FUNCTION ledger_reject_mutation() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    RAISE EXCEPTION '% on % is not allowed: the ledger is append-only', TG_OP, TG_TABLE_NAME
        USING ERRCODE = 'restrict_violation';
END $$;

CREATE TRIGGER ledger_transactions_immutable
    BEFORE UPDATE OR DELETE ON ledger_transactions
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
CREATE TRIGGER ledger_transactions_no_truncate
    BEFORE TRUNCATE ON ledger_transactions
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

CREATE TRIGGER ledger_entries_immutable
    BEFORE UPDATE OR DELETE ON ledger_entries
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
CREATE TRIGGER ledger_entries_no_truncate
    BEFORE TRUNCATE ON ledger_entries
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

-- Tabelas só de acréscimo: a aplicação lê e insere, nunca altera nem apaga.
GRANT SELECT, INSERT ON ledger_transactions TO ledger_app;
GRANT SELECT, INSERT ON ledger_entries TO ledger_app;

COMMIT;
