# Makefile do CoreLedger (SPEC 15.2 e 15.3).
#
# Portável de propósito: cada receita é uma única chamada de `go`, `psql`,
# `sqlc`, `golangci-lint` ou `migrate`, então roda igual via sh e cmd.exe.
# Cada target também está documentado no README como comando avulso.

# Configuração local, se existir (cp .env.example .env).
-include .env
export

GO ?= go
LEDGER := $(GO) run ./cmd/ledger

# O projeto não precisa de cgo: binários estáticos e nenhuma surpresa com o
# compilador C que estiver no PATH. A exceção é o race detector.
CGO_ENABLED := 0
RACE_FLAG :=
ifeq ($(RACE),1)
CGO_ENABLED := 1
RACE_FLAG := -race
endif
export CGO_ENABLED

ifeq ($(OS),Windows_NT)
EXE := .exe
DB_SETUP := psql -U postgres -h localhost -v ON_ERROR_STOP=1 -f scripts/db-setup.sql
ifeq ($(RACE),1)
# O race detector precisa de um gcc de 64 bits. Coloca o do MSYS2 na frente do
# PATH só nesta execução, para não depender da ordem do PATH do sistema (outro
# gcc, como um MinGW de 32 bits, pode vir antes). Ajustável pelo .env.
MSYS2_UCRT64_BIN ?= C:\msys64\ucrt64\bin
export PATH := $(MSYS2_UCRT64_BIN);$(PATH)
endif
else
EXE :=
UNAME := $(shell uname -s)
ifeq ($(UNAME),Darwin)
DB_SETUP := psql -d postgres -v ON_ERROR_STOP=1 -f scripts/db-setup.sql
else
DB_SETUP := sudo -u postgres psql -v ON_ERROR_STOP=1 -f scripts/db-setup.sql
endif
endif

.DEFAULT_GOAL := help

.PHONY: help db-setup run build test test-unit test-integration lint sqlc sqlc-diff \
        migrate-up migrate-down migrate-down-all migrate-version migrate-new reconcile demo \
        load-db load-serve load-reconcile load-test

help: ## Lista os targets
	@$(GO) run ./scripts/makehelp Makefile

db-setup: ## Uma vez só: cria os papéis e o banco de dev (pede a senha do postgres)
	$(DB_SETUP)

run: ## Sobe a API (exige banco migrado)
	$(LEDGER) serve

build: ## Gera bin/ledger
	$(GO) build -o bin/ledger$(EXE) ./cmd/ledger

test: ## Todos os testes; os de banco pulam sem TEST_DATABASE_URL (RACE=1 liga o -race)
	$(GO) test $(RACE_FLAG) ./...

test-unit: ## Só testes unitários, sem banco
	$(GO) test -short ./...

test-integration: export TESTDB_REQUIRED := 1
test-integration: ## Todos os testes, falhando (sem pular) se faltar TEST_DATABASE_URL (RACE=1 liga o -race)
	$(GO) test $(RACE_FLAG) -count=1 ./...

lint: ## golangci-lint (inclui go vet)
	golangci-lint run

sqlc: ## Regenera internal/postgres/sqlc a partir de db/queries
	sqlc generate

sqlc-diff: ## Falha se o código gerado estiver desatualizado
	sqlc diff

migrate-up: ## Aplica as migrations pendentes
	$(LEDGER) migrate up

migrate-down: ## Reverte n migrations: make migrate-down n=1
	$(LEDGER) migrate down $(n)

migrate-down-all: ## Reverte TODAS as migrations (apaga todos os dados)
	$(LEDGER) migrate down --all

migrate-version: ## Mostra a versão de migration aplicada
	$(LEDGER) migrate version

reconcile: ## Confere o ledger contra os saldos; sai com código 1 se houver inconsistência
	$(LEDGER) reconcile

demo: ## Roteiro narrado contra a API em execução (suba com make run em outro terminal)
	$(GO) run ./scripts/demo

migrate-new: ## Cria um par de migrations: make migrate-new name=create_accounts
	migrate create -ext sql -dir db/migrations -seq -digits 6 $(name)

# Teste de carga (SPEC Fase 6). Usa um banco próprio, ledger_load, recriado a
# cada execução; o ledger_dev nunca recebe dados de carga.
LOAD_DATABASE_URL ?= postgres://ledger_app:ledger_app@localhost:5432/ledger_load?sslmode=disable
scenario ?= mixed

load-db: ## Recria o banco ledger_load do zero (usa TEST_DATABASE_URL)
	$(GO) run ./tests/load/resetdb

load-serve: export DATABASE_URL := $(LOAD_DATABASE_URL)
load-serve: ## Sobe a API apontando para o ledger_load
	$(LEDGER) serve

load-reconcile: export DATABASE_URL := $(LOAD_DATABASE_URL)
load-reconcile: ## Reconcilia o ledger_load depois de uma carga
	$(LEDGER) reconcile

load-test: ## Roda o k6 contra a API: make load-test scenario=transfers|deposits|mixed
	k6 run -e SCENARIO=$(scenario) -e ADMIN_TOKEN=$(ADMIN_TOKEN) --summary-export tests/load/results/$(scenario).json tests/load/ledger.js
