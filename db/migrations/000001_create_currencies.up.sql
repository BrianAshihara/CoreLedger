BEGIN;

-- Privilégios do schema (SPEC 15.1.1). O papel da aplicação pode usar o
-- schema, mas nunca criar objetos nele.
REVOKE ALL ON SCHEMA public FROM PUBLIC;
GRANT USAGE ON SCHEMA public TO ledger_app;

-- Leitura do estado das migrations, usada pela checagem de schema do `serve`.
GRANT SELECT ON schema_migrations TO ledger_app;

CREATE TABLE currencies (
    code     char(3)  PRIMARY KEY CHECK (code ~ '^[A-Z]{3}$'),
    exponent smallint NOT NULL CHECK (exponent BETWEEN 0 AND 4)
);

INSERT INTO currencies (code, exponent) VALUES ('BRL', 2);

-- Dado de referência: a aplicação só lê.
GRANT SELECT ON currencies TO ledger_app;

COMMIT;
