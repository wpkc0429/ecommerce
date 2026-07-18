# Loads local overrides when present (copy .env.example → .env).
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help up down dev test test-int lint migrate migrate-down migrate-gen seed seed-demo web-dev admin-dev

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up: ## Start PostgreSQL 16 + Redis 7 (waits for healthchecks)
	docker compose up -d --wait

down: ## Stop infra containers
	docker compose down

dev: up ## Start infra + run the API server
	cd api && go run ./cmd/server

test: ## API unit tests (no infra required)
	cd api && go test ./...

test-int: up ## API unit + integration tests (requires docker infra)
	cd api && INTEGRATION=1 go test -p 1 -count=1 ./...

lint: ## golangci-lint over /api
	cd api && golangci-lint run

migrate: ## Apply pending migrations to DATABASE_URL
	cd api && go run ./cmd/migrate -direction up

migrate-down: ## Roll back the most recent migration
	cd api && go run ./cmd/migrate -direction down -steps 1

migrate-gen: ## Generate a versioned migration from ent schema (make migrate-gen name=add_x)
	cd api && go run ./cmd/migrategen -name $(name)

seed: ## Seed permissions catalog, platform roles, super admin, starter theme
	cd api && go run ./cmd/seed

seed-demo: ## Seed everything plus a demo shop bound to demo.localhost
	cd api && go run ./cmd/seed -demo

web-dev: ## Run the storefront SSR dev server
	cd web && npm run dev

admin-dev: ## Run the admin SPA dev server
	cd admin && npm run dev
