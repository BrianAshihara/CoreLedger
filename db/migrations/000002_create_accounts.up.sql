BEGIN;

CREATE TABLE accounts (
    id              uuid        PRIMARY KEY,
    kind            text        NOT NULL CHECK (kind IN ('USER', 'SYSTEM')),
    owner_id        text,
    system_code     text,
    currency        char(3)     NOT NULL REFERENCES currencies (code),
    status          text        NOT NULL DEFAULT 'ACTIVE'
                                CHECK (status IN ('ACTIVE', 'SUSPENDED', 'CLOSED')),
    balance         bigint      NOT NULL DEFAULT 0,
    last_entry_seq  bigint      NOT NULL DEFAULT 0 CHECK (last_entry_seq >= 0),
    created_at      timestamptz NOT NULL DEFAULT now(),
    updated_at      timestamptz NOT NULL DEFAULT now(),

    -- Alvo das FKs compostas de ledger_entries (SPEC I2, Fase 3).
    CONSTRAINT accounts_id_currency_key UNIQUE (id, currency),

    -- Uma conta de sistema de cada tipo por moeda (por exemplo, uma WORLD por moeda).
    CONSTRAINT accounts_system_code_currency_key UNIQUE (system_code, currency),

    -- I3: conta USER nunca fica negativa. Contas SYSTEM (WORLD) podem.
    CONSTRAINT accounts_user_balance_non_negative CHECK (kind = 'SYSTEM' OR balance >= 0),

    -- USER tem dono e não tem código de sistema; SYSTEM é o contrário.
    CONSTRAINT accounts_kind_fields CHECK (
        (kind = 'USER'   AND owner_id IS NOT NULL AND system_code IS NULL) OR
        (kind = 'SYSTEM' AND owner_id IS NULL     AND system_code IS NOT NULL)
    ),

    -- Conta fechada sempre tem saldo zero.
    CONSTRAINT accounts_closed_zero_balance CHECK (status <> 'CLOSED' OR balance = 0),

    -- Mesmas regras do Go (SPEC 7.1), como segunda linha de defesa.
    CONSTRAINT accounts_owner_id_valid CHECK (owner_id IS NULL OR (
        char_length(owner_id) BETWEEN 1 AND 128
        AND owner_id !~ '[[:cntrl:]]'
        AND owner_id = btrim(owner_id)
    )),

    CONSTRAINT accounts_system_code_format CHECK (system_code IS NULL OR system_code ~ '^[A-Z][A-Z_]*$')
);

CREATE INDEX accounts_owner_idx ON accounts (owner_id) WHERE owner_id IS NOT NULL;

-- A aplicação lê e cria contas. UPDATE só nas colunas que mudam com o tempo:
-- currency, kind e owner_id nunca podem ser reescritos pela aplicação.
-- DELETE nunca é concedido.
GRANT SELECT, INSERT ON accounts TO ledger_app;
GRANT UPDATE (balance, last_entry_seq, status, updated_at) ON accounts TO ledger_app;

-- Conta WORLD de BRL (SPEC 7.5). O ID é um UUIDv7 fixo, gerado uma única vez,
-- para ser idêntico em todos os ambientes.
INSERT INTO accounts (id, kind, system_code, currency)
VALUES ('01a12684-a404-7517-a48b-d8df0d7bb98a', 'SYSTEM', 'WORLD', 'BRL');

COMMIT;
