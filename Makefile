# ============================================================
# EHomeSystem Development Makefile
# ============================================================
# 本地开发：前后端跑在本机，基础设施使用独立的 docker-compose.dev.yml
#
# Usage:
#   make dev             - 启动独立开发环境（默认目标）
#   make up              - make dev 的别名
#   make down            - 停止基础设施 + 本地前后端
#   make restart         - 重启本地前后端（基础设施不动）
#   make infra           - 仅启动基础设施
#   make infra-down      - 仅停止基础设施
#   make backend         - 仅启动本地后端
#   make frontend        - 仅启动本地前端
#   make e2e             - 运行 Playwright E2E 测试
#   make test            - 运行全部测试 (backend + frontend)
#   make test-backend    - 运行 Go 后端单元测试 (SQLite)
#   make test-frontend   - 运行前端 vitest 单元测试
#   make test-integration- 运行 Go 后端集成测试 (PostgreSQL)
#   make test-coverage   - 运行全部测试并生成覆盖率报告
#   make lint-backend    - Go vet 静态检查
#   make lint-frontend   - 前端 TypeScript 类型检查
#   make lint            - 运行全部 lint
#   make status          - 查看服务状态
#   make logs            - 查看后端日志 (LOGS=frontend 查看前端)
#   make clean           - 删除所有容器和数据卷
#
# 生产环境只能直接使用 docker compose，不受本 Makefile 管理:
#   docker compose up -d          # 启动全部生产服务
#   docker compose down           # 停止
# ============================================================

.DEFAULT_GOAL := dev

# Make 与 Compose 共用同一份开发配置，命令行变量仍可覆盖。
-include .env.dev

# ---- 端口配置 (可覆盖) ----
POSTGRES_USER   ?= ehome
POSTGRES_PASSWORD ?= ehome123
POSTGRES_DB     ?= ehome
POSTGRES_PORT   ?= 5435
BACKEND_PORT    ?= 8082
FRONTEND_PORT   ?= 5174
EMQX_MQTT_PORT  ?= 1884
EMQX_WS_PORT    ?= 8084
EMQX_DASHBOARD_PORT ?= 18084
REDIS_PORT      ?= 6380
DEV_PROJECT     ?= ehome-dev

# ---- OTA external host (ESP32 reaches backend here, not localhost) ----
# Auto-detect the IP that external devices can reach; override with EHOME_EXTERNAL_HOST=ip:port
_WSL_IP := $(shell ip route get 1 2>/dev/null | awk '{print $$7; exit}')
ifeq ($(EHOME_EXTERNAL_HOST),)
  ifneq ($(_WSL_IP),)
    EHOME_EXTERNAL_HOST := $(_WSL_IP):$(BACKEND_PORT)
  else
    EHOME_EXTERNAL_HOST := localhost:$(BACKEND_PORT)
  endif
endif

# ---- 路径 ----
ROOT     := $(shell pwd)
BACKEND  := $(ROOT)/backend
FRONTEND := $(ROOT)/frontend-shared
LOG_DIR  := $(ROOT)/.logs
DEV_COMPOSE := docker compose --project-name $(DEV_PROJECT) --env-file $(ROOT)/.env.dev -f $(ROOT)/docker-compose.dev.yml

# 按端口杀进程（可靠，不依赖 PID 文件）
define kill_port
	@lsof -ti :$(1) 2>/dev/null | xargs -r kill 2>/dev/null; \
	lsof -ti :$(1) 2>/dev/null | xargs -r kill -9 2>/dev/null
endef

# ---- 覆盖率阈值 (当前基线，逐步提高) ----
BACKEND_COVERAGE_THRESHOLD  ?= 35
FRONTEND_COVERAGE_THRESHOLD ?= 25

.PHONY: dev up down restart infra infra-down backend frontend e2e \
        test test-backend test-frontend test-integration test-coverage \
        lint lint-backend lint-frontend \
        test-infra test-infra-down \
        status logs clean help

# ---- 一键启动开发环境 ----
dev: up ## 启动独立开发环境（默认）

up: infra ## 启动基础设施 + 本地前后端
	@mkdir -p $(LOG_DIR)
	@echo "==> Starting local backend (port $(BACKEND_PORT))..."
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_MQTT_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=http://localhost:$(FRONTEND_PORT) \
		LOG_LEVEL=debug \
		GIN_MODE=debug \
		nohup go run ./cmd/server/ > $(LOG_DIR)/backend.log 2>&1 &
	@echo "==> Starting local frontend (port $(FRONTEND_PORT))..."
	@cd $(FRONTEND) && \
		VITE_API_TARGET=http://localhost:$(BACKEND_PORT) \
		nohup pnpm dev --port $(FRONTEND_PORT) --strictPort > $(LOG_DIR)/frontend.log 2>&1 &
	@echo "==> Waiting for services..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		b=0; f=0; \
		lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1 && b=1; \
		lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1 && f=1; \
		[ "$$b" = "1" ] && [ "$$f" = "1" ] && break; \
		sleep 2; \
	done
	@if lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1; then \
		echo "    Backend:  http://localhost:$(BACKEND_PORT) ✓"; \
	else echo "    Backend:  FAILED — check $(LOG_DIR)/backend.log"; fi
	@if lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1; then \
		echo "    Frontend: http://localhost:$(FRONTEND_PORT) ✓"; \
	else echo "    Frontend: FAILED — check $(LOG_DIR)/frontend.log"; fi
	@echo "    Postgres: localhost:$(POSTGRES_PORT)"
	@echo "    EMQX:     localhost:$(EMQX_MQTT_PORT) (dashboard: $(EMQX_DASHBOARD_PORT))"
	@echo "    Redis:    localhost:$(REDIS_PORT)"

# ---- 停止全部 ----
down: ## 停止基础设施 + 本地前后端
	@echo "==> Stopping local backend (port $(BACKEND_PORT))..."
	$(call kill_port,$(BACKEND_PORT))
	@echo "==> Stopping local frontend (port $(FRONTEND_PORT))..."
	$(call kill_port,$(FRONTEND_PORT))
	@$(MAKE) infra-down

# ---- 重启本地前后端（基础设施不动） ----
restart: ## 重启本地前后端
	@echo "==> Stopping local services..."
	$(call kill_port,$(BACKEND_PORT))
	$(call kill_port,$(FRONTEND_PORT))
	@sleep 1
	@mkdir -p $(LOG_DIR)
	@echo "==> Starting local backend..."
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_MQTT_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=http://localhost:$(FRONTEND_PORT) \
		LOG_LEVEL=debug \
		GIN_MODE=debug \
		nohup go run ./cmd/server/ > $(LOG_DIR)/backend.log 2>&1 &
	@echo "==> Starting local frontend..."
	@cd $(FRONTEND) && \
		VITE_API_TARGET=http://localhost:$(BACKEND_PORT) \
		nohup pnpm dev --port $(FRONTEND_PORT) --strictPort > $(LOG_DIR)/frontend.log 2>&1 &
	@echo "==> Waiting for services..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		b=0; f=0; \
		lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1 && b=1; \
		lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1 && f=1; \
		[ "$$b" = "1" ] && [ "$$f" = "1" ] && break; \
		sleep 2; \
	done
	@echo "==> Restarted!"

# ---- 基础设施 ----
infra: ## 仅启动基础设施 (PG/Redis/EMQX)
	@echo "==> Starting infrastructure..."
	@POSTGRES_PORT=$(POSTGRES_PORT) REDIS_PORT=$(REDIS_PORT) \
		EMQX_MQTT_PORT=$(EMQX_MQTT_PORT) EMQX_WS_PORT=$(EMQX_WS_PORT) \
		EMQX_DASHBOARD_PORT=$(EMQX_DASHBOARD_PORT) \
		$(DEV_COMPOSE) up -d --wait postgres redis emqx
	@$(DEV_COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d postgres -tAc \
		"SELECT 1 FROM pg_database WHERE datname = 'ehome_test'" | grep -q 1 || \
		$(DEV_COMPOSE) exec -T postgres createdb -U $(POSTGRES_USER) ehome_test
	@echo "==> Infrastructure ready (PG:$(POSTGRES_PORT) Redis:$(REDIS_PORT) EMQX:$(EMQX_MQTT_PORT))"

infra-down: ## 仅停止基础设施
	@echo "==> Stopping infrastructure..."
	$(DEV_COMPOSE) down --remove-orphans

# ---- 单独启动 ----
backend: ## 仅启动本地后端
	@mkdir -p $(LOG_DIR)
	$(call kill_port,$(BACKEND_PORT))
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_MQTT_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=http://localhost:$(FRONTEND_PORT) \
		LOG_LEVEL=debug \
		GIN_MODE=debug \
		nohup go run ./cmd/server/ > $(LOG_DIR)/backend.log 2>&1 &
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1 && break; \
		sleep 2; \
	done
	@if lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1; then \
		echo "==> Backend started (http://localhost:$(BACKEND_PORT)) ✓"; \
	else echo "==> Backend FAILED — check $(LOG_DIR)/backend.log"; fi

frontend: ## 仅启动本地前端
	@mkdir -p $(LOG_DIR)
	$(call kill_port,$(FRONTEND_PORT))
	@cd $(FRONTEND) && \
		VITE_API_TARGET=http://localhost:$(BACKEND_PORT) \
		nohup pnpm dev --port $(FRONTEND_PORT) --strictPort > $(LOG_DIR)/frontend.log 2>&1 &
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1 && break; \
		sleep 2; \
	done
	@if lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1; then \
		echo "==> Frontend started (http://localhost:$(FRONTEND_PORT)) ✓"; \
	else echo "==> Frontend FAILED — check $(LOG_DIR)/frontend.log"; fi

# ---- 测试 ----
test: test-backend test-frontend ## 运行全部测试 (backend + frontend)

test-backend: ## 运行 Go 后端单元测试 (SQLite, 带覆盖率)
	@echo "==> Running Go unit tests (SQLite)..."
	@cd $(BACKEND) && go test -race -count=1 -coverprofile=coverage.out -covermode=atomic ./...
	@cd $(BACKEND) && go tool cover -func=coverage.out | tail -1
	@COV=$$(cd $(BACKEND) && go tool cover -func=coverage.out | grep total | awk '{print $$3}' | sed 's/%//') && \
		echo "    Coverage: $${COV}% (threshold: $(BACKEND_COVERAGE_THRESHOLD)%)" && \
		if [ "$$(echo "$$COV < $(BACKEND_COVERAGE_THRESHOLD)" | bc -l)" -eq 1 ]; then \
			echo "❌ Coverage $${COV}% below $(BACKEND_COVERAGE_THRESHOLD)% threshold!"; exit 1; \
		else echo "✅ Coverage gate passed"; fi

test-frontend: ## 运行前端 vitest 单元测试 (带覆盖率)
	@echo "==> Running frontend unit tests..."
	@cd $(FRONTEND) && pnpm test:coverage
	@echo "    Coverage report: $(FRONTEND)/coverage/index.html"

test-integration: infra ## 使用开发环境的 ehome_test 数据库运行集成测试
	@echo "==> Running Go integration tests (PostgreSQL)..."
	@cd $(BACKEND) && EHOME_TEST_DB=postgres \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=ehome_test \
		go test -race -count=1 -tags=integration ./...
	@echo "✅ Integration tests passed"

test-coverage: ## 运行全部测试并生成覆盖率报告
	@echo "==> Running all tests with coverage..."
	@$(MAKE) test-backend
	@$(MAKE) test-frontend
	@echo ""
	@echo "==> Coverage reports:"
	@echo "    Backend:  $(BACKEND)/coverage.out (go tool cover -html=coverage.out)"
	@echo "    Frontend: $(FRONTEND)/coverage/index.html"

# 测试与开发共用同一套基础设施；保留旧目标名作为兼容别名。
test-infra: infra ## 确保开发/测试共用基础设施已启动

test-infra-down: infra-down ## 停止开发/测试共用基础设施

# ---- Lint ----
lint: lint-backend lint-frontend ## 运行全部 lint

lint-backend: ## Go vet 静态检查
	@echo "==> Running go vet..."
	@cd $(BACKEND) && go vet ./...
	@echo "✅ go vet passed"

lint-frontend: ## 前端 TypeScript 类型检查
	@echo "==> Running TypeScript type check..."
	@cd $(FRONTEND) && pnpm typecheck
	@echo "✅ TypeScript passed"

# ---- E2E 测试 ----
e2e: ## Run Playwright E2E tests (run make up first)
	@if ! lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1; then \
		echo "❌ Frontend is not running on port $(FRONTEND_PORT). Run 'make up' first."; \
		exit 1; \
	fi
	@echo "==> Running Playwright E2E tests against http://localhost:$(FRONTEND_PORT)..."
	@cd $(FRONTEND) && npx playwright test

# ---- 状态 ----
status: ## 查看服务状态
	@echo "==> Development infrastructure ($(DEV_PROJECT)):"
	@$(DEV_COMPOSE) ps
	@echo ""
	@echo "==> Local services:"
	@if lsof -ti :$(BACKEND_PORT) >/dev/null 2>&1; then \
		echo "  backend  running (http://localhost:$(BACKEND_PORT))"; \
	else echo "  backend  stopped"; fi
	@if lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1; then \
		echo "  frontend running (http://localhost:$(FRONTEND_PORT))"; \
	else echo "  frontend stopped"; fi

# ---- 日志 ----
logs: ## 查看后端日志 (LOGS=frontend 查看前端)
	@tail -f $(LOG_DIR)/$(or $(LOGS),backend).log

# ---- 清理 ----
clean: ## 删除开发环境容器、数据卷和本地进程（不影响生产）
	@echo "==> WARNING: This will delete DEVELOPMENT data only (database, redis, emqx)!"
	@read -p "    Are you sure? [y/N] " confirm && [ "$$confirm" = "y" ] || exit 1
	$(call kill_port,$(BACKEND_PORT))
	$(call kill_port,$(FRONTEND_PORT))
	$(DEV_COMPOSE) down -v --remove-orphans
	rm -rf $(ROOT)/.logs
	@echo "==> Cleaned up."

# ---- Help ----
help: ## Show this help
	@grep -E '^[a-zA-Z0-9_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}'
