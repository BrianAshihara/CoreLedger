BEGIN;

DROP TABLE currencies;

REVOKE SELECT ON schema_migrations FROM ledger_app;
REVOKE USAGE ON SCHEMA public FROM ledger_app;

-- Restaura o padrão do PostgreSQL 15+ para o schema public.
GRANT USAGE ON SCHEMA public TO PUBLIC;

COMMIT;
