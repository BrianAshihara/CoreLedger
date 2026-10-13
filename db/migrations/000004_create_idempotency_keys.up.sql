BEGIN;

-- Uma chave de idempotência produz no máximo uma transação do ledger (I7).
-- Só pedidos lançados com sucesso ficam gravados: um pedido recusado é
-- desfeito junto com a transação de banco, e a chave continua livre (SPEC 11.3).
CREATE TABLE idempotency_keys (
    key                  text        PRIMARY KEY,
    request_method       text        NOT NULL CHECK (request_method IN ('POST')),
    request_path         text        NOT NULL CHECK (request_path LIKE '/v1/%'),
    request_fingerprint  bytea       NOT NULL CHECK (length(request_fingerprint) = 32),
    -- Adiada: a chave é reservada antes de a transação do ledger existir, na
    -- mesma transação de banco (SPEC 11.2).
    transaction_id       uuid        NOT NULL UNIQUE
                         REFERENCES ledger_transactions (id) DEFERRABLE INITIALLY DEFERRED,
    created_at           timestamptz NOT NULL DEFAULT now(),

    -- De 1 a 255 caracteres ASCII imprimíveis (SPEC 7.4).
    CONSTRAINT idempotency_keys_key_format CHECK (key ~ '^[ -~]{1,255}$')
);

-- O registro de idempotência é tão imutável quanto o ledger que ele protege.
CREATE TRIGGER idempotency_keys_immutable
    BEFORE UPDATE OR DELETE ON idempotency_keys
    FOR EACH ROW EXECUTE FUNCTION ledger_reject_mutation();
CREATE TRIGGER idempotency_keys_no_truncate
    BEFORE TRUNCATE ON idempotency_keys
    FOR EACH STATEMENT EXECUTE FUNCTION ledger_reject_mutation();

GRANT SELECT, INSERT ON idempotency_keys TO ledger_app;

COMMIT;
