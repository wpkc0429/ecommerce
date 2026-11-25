# Loads local overrides when present (copy .env.example → .env).
ifneq (,$(wildcard .env))
include .env
export
endif

.PHONY: help up down dev test test-int lint migrate migrate-down migrate-gen ecpay-sim seed seed-demo sanitize-content purge-applications purge-content-audit purge-design-history design-sweep content-scheduler admin-dev openapi openapi-mock openapi-ui

help: ## List targets
	@grep -E '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

up: ## Start PostgreSQL 16 + Redis 7 + MinIO (waits for healthchecks)
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

# ── API 契約（change openapi-contract）──────────────────────────────────
# 這三個 target 都是「給人用的捷徑」。CI 的保證來自既有的 `go test ./...`：
# 四道檢查是普通的 Go 測試，不需要任何額外接線（design D2）。
#
# 守著「生產環境是不是真的這樣接」的另外三道（change contract-router-completeness）
# 在 internal/app/wiring_integration_test.go，刻意不掛在這個 target 上：它們守的
# 不是契約文件而是接線本身。它們由 `make test-int`（＝CI 跑的那一行）執行。

openapi: ## Run the four contract checks (routes / money units / responses / v1 surface)
	cd api && INTEGRATION=1 go test ./internal/httpapi/ -run TestOpenAPI -count=1 -v

openapi-mock: ## Mock server from the contract, no DB/Redis needed (npx, :4010)
	npx --yes @stoplight/prism-cli mock api/openapi/openapi.yaml

openapi-ui: ## Browsable API docs (npx, :8081)
	npx --yes @redocly/cli preview-docs api/openapi/openapi.yaml --port 8081

migrate: ## Apply pending migrations to DATABASE_URL
	cd api && go run ./cmd/migrate -direction up

migrate-down: ## Roll back the most recent migration
	cd api && go run ./cmd/migrate -direction down -steps 1

migrate-gen: ## Generate a versioned migration from ent schema (make migrate-gen name=add_x)
	cd api && go run ./cmd/migrategen -name $(name)

ecpay-sim: ## Local stand-in for 綠界's cashier (:8090); point ECPAY_CHECKOUT_URL at it
	cd api && go run ./cmd/ecpay-sim

seed: ## Seed permissions catalog, platform roles, super admin, starter theme
	cd api && go run ./cmd/seed

seed-demo: ## Seed everything plus a demo shop (publishable key pk_demoseedstorefrontkeynotsecret)
	cd api && go run ./cmd/seed -demo

sanitize-content: ## DRY RUN: report CMS rich text that needs re-sanitizing (writes nothing; add -apply manually)
	cd api && go run ./cmd/sanitize-content

purge-applications: ## DRY RUN: report merchant applications past their retention (writes nothing; add -apply manually)
	cd api && go run ./cmd/purge-applications
purge-content-audit: ## DRY RUN: report content audit rows and finished schedules past their retention (add -apply manually)
	cd api && go run ./cmd/purge-content-audit
purge-design-history: ## DRY RUN: report design history past its retention (add -apply manually; needs cron)
	cd api && go run ./cmd/purge-design-history
design-sweep: ## DRY RUN: report stuck design requests; -verify checks the event hash chains (add -apply to fail them)
	cd api && go run ./cmd/design-sweep
content-scheduler: ## Publish due scheduled content documents once (run every minute from cron / Task Scheduler)
	cd api && go run ./cmd/content-scheduler

admin-dev: ## Run the admin SPA dev server
	cd admin && npm run dev
