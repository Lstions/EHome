# ============================================================
# EHomeSystem Development Makefile
# ============================================================
# 本地开发：前后端跑在本机，基础设施（PG/Redis/EMQX）复用生产 compose
#
# Usage:
#   make up          - 启动基础设施 (PG/Redis/EMQX) + 本地前后端
#   make down        - 停止基础设施 + 本地前后端
#   make restart     - 重启本地前后端（基础设施不动）
#   make infra       - 仅启动基础设施
#   make infra-down  - 仅停止基础设施
#   make backend     - 仅启动本地后端
#   make frontend    - 仅启动本地前端
#   make status      - 查看服务状态
#   make logs        - 查看后端日志 (LOGS=frontend 查看前端)
#   make clean       - 删除所有容器和数据卷
#
# 生产环境直接用 docker compose:
#   docker compose up -d          # 启动全部生产服务
#   docker compose down           # 停止
# ============================================================

# ---- 端口配置 (可覆盖) ----
POSTGRES_PORT   ?= 5434
BACKEND_PORT    ?= 8080
FRONTEND_PORT   ?= 5174
EMQX_PORT       ?= 1883
REDIS_PORT      ?= 6379

# ---- 路径 ----
ROOT     := $(shell pwd)
BACKEND  := $(ROOT)/backend
FRONTEND := $(ROOT)/frontend-shared
LOG_DIR  := $(ROOT)/.logs

# 按端口杀进程（可靠，不依赖 PID 文件）
define kill_port
	@lsof -ti :$(1) 2>/dev/null | xargs -r kill 2>/dev/null; \
	lsof -ti :$(1) 2>/dev/null | xargs -r kill -9 2>/dev/null
endef

.PHONY: up down restart infra infra-down backend frontend status logs clean help

# ---- 一键启动开发环境 ----
up: infra ## 启动基础设施 + 本地前后端
	@mkdir -p $(LOG_DIR)
	@echo "==> Starting local backend (port $(BACKEND_PORT))..."
	@cd $(BACKEND) && \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=ehome \
		EHOME_DB_PASSWORD=ehome123 \
		EHOME_DB_NAME=ehome \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=localhost:$(BACKEND_PORT) \
		LOG_LEVEL=debug \
		nohup go run ./cmd/server/ > $(LOG_DIR)/backend.log 2>&1 &
	@echo "==> Starting local frontend (port $(FRONTEND_PORT))..."
	@cd $(FRONTEND) && \
		nohup pnpm dev > $(LOG_DIR)/frontend.log 2>&1 &
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
	@echo "    EMQX:     localhost:$(EMQX_PORT) (dashboard: 18083)"
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
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=ehome \
		EHOME_DB_PASSWORD=ehome123 \
		EHOME_DB_NAME=ehome \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=localhost:$(BACKEND_PORT) \
		LOG_LEVEL=debug \
		nohup go run ./cmd/server/ > $(LOG_DIR)/backend.log 2>&1 &
	@echo "==> Starting local frontend..."
	@cd $(FRONTEND) && \
		nohup pnpm dev > $(LOG_DIR)/frontend.log 2>&1 &
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
	POSTGRES_PORT=$(POSTGRES_PORT) EMQX_MQTT_PORT=$(EMQX_PORT) REDIS_PORT=$(REDIS_PORT) \
		docker compose up -d postgres redis emqx
	@echo "==> Infrastructure ready (PG:$(POSTGRES_PORT) Redis:$(REDIS_PORT) EMQX:$(EMQX_PORT))"

infra-down: ## 仅停止基础设施
	@echo "==> Stopping infrastructure..."
	docker compose down --remove-orphans

# ---- 单独启动 ----
backend: ## 仅启动本地后端
	@mkdir -p $(LOG_DIR)
	$(call kill_port,$(BACKEND_PORT))
	@cd $(BACKEND) && \
		EHOME_DB_HOST=localhost \
		EHOME_DB_PORT=$(POSTGRES_PORT) \
		EHOME_DB_USER=ehome \
		EHOME_DB_PASSWORD=ehome123 \
		EHOME_DB_NAME=ehome \
		REDIS_ADDR=localhost:$(REDIS_PORT) \
		MQTT_BROKER=tcp://localhost:$(EMQX_PORT) \
		EHOME_MQTT_CLIENT_ID=ehome-backend-dev \
		EHOME_EXTERNAL_HOST=localhost:$(BACKEND_PORT) \
		LOG_LEVEL=debug \
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
		nohup pnpm dev > $(LOG_DIR)/frontend.log 2>&1 &
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
		lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1 && break; \
		sleep 2; \
	done
	@if lsof -ti :$(FRONTEND_PORT) >/dev/null 2>&1; then \
		echo "==> Frontend started (http://localhost:$(FRONTEND_PORT)) ✓"; \
	else echo "==> Frontend FAILED — check $(LOG_DIR)/frontend.log"; fi

# ---- 状态 ----
status: ## 查看服务状态
	@echo "==> Infrastructure:"
	@docker ps --format 'table {{.Names}}\t{{.Status}}\t{{.Ports}}' | grep ehome || echo "  (none running)"
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
clean: ## 删除所有容器、数据卷和本地进程
	@echo "==> WARNING: This will delete all data (database, redis, emqx)!"
	@read -p "    Are you sure? [y/N] " confirm && [ "$$confirm" = "y" ] || exit 1
	$(call kill_port,$(BACKEND_PORT))
	$(call kill_port,$(FRONTEND_PORT))
	docker compose down -v --remove-orphans
	rm -rf $(ROOT)/.logs
	@echo "==> Cleaned up."

# ---- Help ----
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
