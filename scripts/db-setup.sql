-- CoreLedger: setup único do banco para máquinas de desenvolvimento e teste
-- (SPEC 15.1.1). Rode como superusuário do PostgreSQL:
--
--   Windows:  psql -U postgres -h localhost -f scripts/db-setup.sql
--   Linux:    sudo -u postgres psql -f scripts/db-setup.sql
--   macOS:    psql -d postgres -f scripts/db-setup.sql
--
-- O script só cria os papéis e o banco de desenvolvimento. Todos os
-- privilégios dentro do banco são concedidos pelas migrations, então dev,
-- testes e CI seguem o mesmo caminho.
--
-- Não pode ser rodado duas vezes. Para começar do zero:
--   DROP DATABASE ledger_dev;
--   DROP ROLE ledger_app, ledger_owner, ledger_test;
--
-- As senhas abaixo servem só para desenvolvimento local e batem com o
-- .env.example.

\set ON_ERROR_STOP on

-- Dono do schema; roda as migrations.
CREATE ROLE ledger_owner LOGIN PASSWORD 'ledger_owner';

-- Usado pela API em tempo de execução. Não é dono de nada e só recebe o que
-- as migrations concedem.
CREATE ROLE ledger_app LOGIN PASSWORD 'ledger_app';

-- Usado só pelos testes: cria e remove um banco por teste, e pode desligar
-- triggers para injetar inconsistências nos testes de reconciliação.
CREATE ROLE ledger_test LOGIN SUPERUSER PASSWORD 'ledger_test';

CREATE DATABASE ledger_dev OWNER ledger_owner;
