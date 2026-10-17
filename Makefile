SHELL := /bin/bash
-include .env
export

GOOSE_DIR := server/internal/db/migrations

.PHONY: up down migrate seed migrate-status sqlc gen lint test run-api run-admin run-worker build

up:            ; docker compose up -d
down:          ; docker compose down

migrate:       ; cd server && go run github.com/pressly/goose/v3/cmd/goose@latest -dir internal/db/migrations postgres "$(MIGRATE_DATABASE_URL)" up
seed:          ; psql "$(MIGRATE_DATABASE_URL)" -f server/scripts/seed_dev.sql
migrate-status:; cd server && go run github.com/pressly/goose/v3/cmd/goose@latest -dir internal/db/migrations postgres "$(MIGRATE_DATABASE_URL)" status

sqlc:          ; cd server && go run github.com/sqlc-dev/sqlc/cmd/sqlc@latest generate

# OpenAPI → Go 服务端接口（oapi-codegen）与 Dart 客户端（openapi-generator，需本机安装）
gen: gen-go gen-dart
gen-go:
	cd server && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest -config oapi-codegen.app.yaml ../api/openapi/app.yaml
	cd server && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest -config oapi-codegen.node.yaml ../api/openapi/node.yaml
	cd server && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@latest -config oapi-codegen.admin.yaml ../api/openapi/admin.yaml
gen-dart:
	openapi-generator generate -i api/openapi/app.yaml -g dart-dio -o apps/mobile/lib/core/network/gen --additional-properties=pubName=yuhai_api

lint:
	cd server && go vet ./... && test -z "$$(gofmt -l .)"
	npx --yes @redocly/cli lint api/openapi/app.yaml api/openapi/node.yaml api/openapi/admin.yaml

test:          ; cd server && go test -race ./...
build:         ; cd server && mkdir -p bin && go build -o bin/ ./cmd/...

run-api:       ; cd server && go run ./cmd/api
run-admin:     ; cd server && go run ./cmd/admin-api
run-worker:    ; cd server && go run ./cmd/worker

# 仅使用独立测试数据库，已迁移并含 MOCK 收款地址及种子数据。
.PHONY: test-integration
test-integration:
	@test -n "$(TEST_DATABASE_URL)" || (echo "请设置独立 TEST_DATABASE_URL"; exit 1)
	cd server && go test -race ./internal/account -run Integration -v
