# ============================================================
# EHomeSystem Makefile
# ============================================================
# 统一环境：本地开发（本机 Go/Vite）与生产共用同一套容器基础设施
# （docker-compose.yml：postgres/redis/emqx）。无独立 dev 栈。
#
# Usage:
#   make up             - 确保统一基础设施运行 + 启动本机前后端（默认目标）
#   make dev            - make up 的别名（历史兼容）
#   make down           - 停止本机前后端（统一基础设施保持运行，手动 docker compose down 停止）
#   make restart        - 重启本机前后端（基础设施不动）
#   make infra          - 确保统一基础设施（PG/Redis/EMQX）运行
#   make infra-down     - 停止统一基础设施
#   make auth-bootstrap - 初始化统一数据库并生成管理员设置凭据
#   make backend        - 仅启动本机后端（连统一基础设施）
#   make frontend       - 仅启动本机前端
#   make e2e            - 运行 Playwright E2E 测试
#   make test           - 运行全部测试 (backend + frontend)
#   make test-backend   - 运行 Go 后端单元测试 (SQLite)
#   make test-frontend  - 运行前端 vitest 单元测试
#   make test-integration - 运行 Go 后端集成测试 (统一 PostgreSQL ehome_test)
#   make test-coverage  - 运行全部测试并生成覆盖率报告
#   make lint-backend   - Go vet 静态检查
#   make lint-frontend  - 前端 TypeScript 类型检查
#   make lint           - 运行全部 lint
#   make status         - 查看服务状态
#   make logs           - 查看后端日志 (LOGS=frontend 查看前端)
#   make clean          - 停止本机前后端并清理日志（不删统一基础设施/数据卷）
#
# 说明：
#   - 统一基础设施 = docker-compose.yml 的 postgres/redis/emqx，与生产共用。
#     生产数据卷（ehome-pgdata 等）不受 make down/clean 影响。
#   - 生产 web 服务（ehome）由 docker compose 直接管理，不受本 Makefile 控制。
#   - 本地开发端口：后端 :8082、前端 :5174；
#     统一基础设施主机端口：PG :5432、Redis :6379、EMQX :1883（仅绑定 127.0.0.1）。
# ============================================================

.DEFAULT_GOAL := up

# ---- 端口配置 (可覆盖) ----
POSTGRES_USER   ?= ehome
POSTGRES_PASSWORD ?= ehome123
POSTGRES_DB     ?= ehome
BACKEND_PORT    ?= 8082
FRONTEND_PORT   ?= 5174

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

# 统一基础设施 Compose：与生产共用 docker-compose.yml（只操作 postgres/redis/emqx）
COMPOSE := docker compose -f $(ROOT)/docker-compose.yml

# 按端口杀进程（可靠，不依赖 PID 文件）
define kill_port
	@lsof -ti :$(1) 2>/dev/null | xargs -r kill 2>/dev/null; \
	lsof -ti :$(1) 2>/dev/null | xargs -r kill -9 2>/dev/null
endef

# ---- 覆盖率阈值 (当前基线，逐步提高) ----
BACKEND_COVERAGE_THRESHOLD  ?= 35
FRONTEND_COVERAGE_THRESHOLD ?= 25

.PHONY: dev up down restart infra infra-down auth-bootstrap backend frontend e2e \
        test test-backend test-frontend test-integration test-coverage \
        lint lint-backend lint-frontend \
        test-infra test-infra-down \
        status logs clean help

# ---- 一键启动统一环境 ----
dev: up ## 启动统一环境（历史兼容别名）

up: infra auth-bootstrap ## 确保基础设施运行 + 启动本机前后端
	@mkdir -p $(LOG_DIR)
	@echo "==> Starting local backend (port $(BACKEND_PORT), unified infra)..."
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=127.0.0.1:6379 \
		MQTT_BROKER=tcp://127.0.0.1:1883 \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=* \
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
	@echo "    Postgres: 127.0.0.1:5432 (统一基础设施)"
	@echo "    EMQX:     127.0.0.1:1883 (dashboard: 127.0.0.1:18083)"
	@echo "    Redis:    127.0.0.1:6379"

# ---- 首次运行认证引导 ----
auth-bootstrap: ## 初始化统一数据库并生成一次性管理员设置凭据
	@echo "==> Checking first-run authentication..."
	@cd $(BACKEND) && \
		if EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		go run ./cmd/ehomectl auth bootstrap-database >/dev/null 2>&1; then \
			echo "    Empty database bootstrapped."; \
		fi
	@cd $(BACKEND) && \
		if credential=$$(EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		go run ./cmd/ehomectl auth create-initialization-token 2>/dev/null); then \
			echo "    First-run setup: http://localhost:$(FRONTEND_PORT)/login"; \
			echo "    Initialization credential (valid for 10 minutes):"; \
			echo "    $$credential"; \
			echo "    Paste this credential into the setup screen."; \
		else \
			echo "    Authentication already initialized or requires migration."; \
		fi

# ---- 停止本机前后端 ----
down: ## 停止本机前后端（统一基础设施保持运行）
	@echo "==> Stopping local backend (port $(BACKEND_PORT))..."
	$(call kill_port,$(BACKEND_PORT))
	@echo "==> Stopping local frontend (port $(FRONTEND_PORT))..."
	$(call kill_port,$(FRONTEND_PORT))
	@echo "==> Local services stopped (unified infra still running: docker compose ps)"

# ---- 重启本机前后端（基础设施不动） ----
restart: auth-bootstrap ## 重启本机前后端
	@echo "==> Stopping local services..."
	$(call kill_port,$(BACKEND_PORT))
	$(call kill_port,$(FRONTEND_PORT))
	@sleep 1
	@mkdir -p $(LOG_DIR)
	@echo "==> Starting local backend..."
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=127.0.0.1:6379 \
		MQTT_BROKER=tcp://127.0.0.1:1883 \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=* \
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

# ---- 统一基础设施 ----
infra: ## 确保统一基础设施 (PG/Redis/EMQX) 运行
	@echo "==> Ensuring unified infrastructure running (docker-compose.yml)..."
	@$(COMPOSE) up -d --wait postgres redis emqx
	@$(COMPOSE) exec -T postgres psql -U $(POSTGRES_USER) -d postgres -tAc \
		"SELECT 1 FROM pg_database WHERE datname = 'ehome_test'" | grep -q 1 || \
		$(COMPOSE) exec -T postgres createdb -U $(POSTGRES_USER) ehome_test
	@echo "==> Unified infrastructure ready (PG:5432 Redis:6379 EMQX:1883)"

infra-down: ## 停止统一基础设施
	@echo "==> Stopping unified infrastructure..."
	@$(COMPOSE) down --remove-orphans

# ---- 单独启动 ----
backend: auth-bootstrap ## 仅启动本机后端（连统一基础设施）
	@mkdir -p $(LOG_DIR)
	$(call kill_port,$(BACKEND_PORT))
	@cd $(BACKEND) && \
		EHOME_SERVER_ADDR=:$(BACKEND_PORT) \
		EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
		EHOME_DB_USER=$(POSTGRES_USER) \
		EHOME_DB_PASSWORD=$(POSTGRES_PASSWORD) \
		EHOME_DB_NAME=$(POSTGRES_DB) \
		REDIS_ADDR=127.0.0.1:6379 \
		MQTT_BROKER=tcp://127.0.0.1:1883 \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=$(EHOME_EXTERNAL_HOST) \
		EHOME_ENV=development \
		EHOME_JWT_SECRET=ehome-dev-jwt-secret-not-for-production \
		EHOME_ALLOWED_ORIGINS=* \
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

frontend: ## 仅启动本机前端
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

test-integration: infra ## 使用统一 PostgreSQL 的 ehome_test 数据库运行集成测试
	@echo "==> Running Go integration tests (PostgreSQL)..."
	@cd $(BACKEND) && EHOME_TEST_DB=postgres \
		EHOME_DB_HOST=127.0.0.1 \
		EHOME_DB_PORT=5432 \
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

# 测试与本地开发共用同一套统一基础设施；保留旧目标名作为兼容别名。
test-infra: infra ## 确保统一基础设施已启动

test-infra-down: infra-down ## 停止统一基础设施

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
	@echo "==> Unified infrastructure (docker-compose.yml postgres/redis/emqx):"
	@$(COMPOSE) ps postgres redis emqx
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
clean: ## 停止本机前后端并清理日志（不删除统一基础设施/数据卷）
	@echo "==> Stopping local backend/frontend and removing .logs..."
	$(call kill_port,$(BACKEND_PORT))
	$(call kill_port,$(FRONTEND_PORT))
	rm -rf $(ROOT)/.logs
	@echo "==> Cleaned up. (Unified infra & production data untouched; stop via 'make infra-down')"

# ---- Help ----
help: ## Show this help
	@awk 'BEGIN {FS = ":.*## "}; /^[a-zA-Z0-9_-]+:.*## / {printf "  \033[36m%-16s\033[0m %s\n", $$1, $$2}' $(MAKEFILE_LIST)
