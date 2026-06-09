#!/usr/bin/env bash
# ============================================================
# EHomeSystem 环境管理脚本
# 用法: ./scripts/env.sh <env> <command> [args...]
#
#   <env>:     dev | prod | all
#   <command>: start | stop | restart | status | logs | clean
#
# ⚠️  dev 和 prod 不能同时启动(共享基础设施容器名)
#     切换环境用: ./scripts/env.sh <old> stop && ./scripts/env.sh <new> start
# ============================================================
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "$0")/.." && pwd)"
COMPOSE_BASE="$PROJECT_DIR/docker-compose.yml"
COMPOSE_DEV="$PROJECT_DIR/docker-compose.dev.yml"

# ── 颜色 ──
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
BOLD='\033[1m'
NC='\033[0m'

# ── 端口配置 ──
DEV_PORTS=(
    "POSTGRES_PORT=5433"
    "REDIS_PORT=6380"
    "EMQX_MQTT_PORT=1884"
    "EMQX_WS_PORT=8084"
    "EMQX_DASHBOARD_PORT=18084"
    "FRONTEND_PORT=80"
    "FRONTEND_DEV_PORT=5174"
    "BACKEND_PORT=8082"
    "POSTGRES_USER=ehome"
    "POSTGRES_PASSWORD=ehome123"
    "POSTGRES_DB=ehome"
    "EHOME_EXTERNAL_HOST=192.168.20.3:8082"
)

PROD_PORTS=(
    "POSTGRES_PORT=5434"
    "REDIS_PORT=6379"
    "EMQX_MQTT_PORT=1883"
    "EMQX_WS_PORT=8083"
    "EMQX_DASHBOARD_PORT=18083"
    "FRONTEND_PORT=80"
    "BACKEND_PORT=8080"
    "POSTGRES_USER=ehome"
    "POSTGRES_PASSWORD=ehome123"
    "POSTGRES_DB=ehome"
)

usage() {
    cat << 'EOF'
 🏠 EHomeSystem 环境管理

用法: ./scripts/env.sh <env> <command> [args...]

环境:
  dev     开发环境 (air热重载, Vite HMR, 端口偏移)
  prod    生产/演示环境
  all     显示所有容器状态

命令:
  start       启动
  stop        停止
  restart     重启
  status      查看状态
  logs [svc]  查看日志 (可指定服务名)
  clean       停止并删除容器+数据卷

示例:
  ./scripts/env.sh dev start
  ./scripts/env.sh dev logs backend
  ./scripts/env.sh prod restart
  ./scripts/env.sh all status

端口参考:
               DEV         PROD
  PostgreSQL   5433        5434
  Redis        6380        6379
  EMQX MQTT    1884        1883
  EMQX WS      8084        8083
  EMQX Dash    18084       18083
  Backend      8082        8080
  Frontend     80/5174     80

⚠️  dev 和 prod 共享基础设施, 不能同时运行
EOF
}

# ── 内部: 检测当前运行的是哪个环境 ──
_detect_running() {
    local pg_port
    pg_port=$(docker inspect ehome-postgres --format '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' 2>/dev/null || echo "")
    if [[ "$pg_port" == "5433" ]]; then
        echo "dev"
    elif [[ "$pg_port" == "5434" ]]; then
        echo "prod"
    else
        echo "none"
    fi
}

# ── 内部: compose 执行 ──
_compose() {
    local env="$1"; shift
    cd "$PROJECT_DIR"
    if [[ "$env" == "dev" ]]; then
        env "${DEV_PORTS[@]}" docker compose -f "$COMPOSE_BASE" -f "$COMPOSE_DEV" "$@"
    else
        env "${PROD_PORTS[@]}" docker compose -f "$COMPOSE_BASE" "$@"
    fi
}

# ── 命令实现 ──

cmd_start() {
    local env="$1"
    local running; running=$(_detect_running)

    if [[ "$running" != "none" && "$running" != "$env" ]]; then
        echo -e "${RED}❌ ${running} 环境正在运行，不能同时启动 ${env}${NC}"
        echo -e "${YELLOW}   先执行: $0 ${running} stop${NC}"
        exit 1
    fi

    echo -e "${GREEN}${BOLD}▶ 启动 ${env} 环境${NC}"
    _compose "$env" up -d --wait
    echo
    cmd_status "$env"
}

cmd_stop() {
    local env="$1"
    echo -e "${YELLOW}${BOLD}■ 停止 ${env} 环境${NC}"
    _compose "$env" down
    echo -e "${GREEN}  已停止${NC}"
}

cmd_restart() {
    local env="$1"
    echo -e "${YELLOW}${BOLD}↻ 重启 ${env} 环境${NC}"
    _compose "$env" down
    _compose "$env" up -d --wait
    echo
    cmd_status "$env"
}

cmd_status() {
    local env="$1"
    local running; running=$(_detect_running)
    local marker=""

    if [[ "$env" == "all" ]]; then
        echo -e "${CYAN}${BOLD}═══ EHomeSystem 容器状态 ═══${NC}"
        local current; current=$(_detect_running)
        echo -e "${YELLOW}当前环境: ${current}${NC}"
        echo
    else
        if [[ "$running" == "$env" ]]; then
            marker=" ${GREEN}● active${NC}"
        elif [[ "$running" != "none" ]]; then
            marker=" ${YELLOW}(running: ${running})${NC}"
        fi
        echo -e "${CYAN}${BOLD}=== ${env} 环境${marker}${NC}"
    fi

    local containers
    containers=$(docker ps --filter "name=ehome-" --format "table {{.Names}}\t{{.Status}}\t{{.Ports}}" 2>/dev/null)

    if [[ -z "$containers" ]]; then
        echo -e "${RED}  无运行中的 EHomeSystem 容器${NC}"
        return
    fi

    # 显示所有 ehome 容器，但标注属于哪个环境
    while IFS= read -r line; do
        if [[ "$line" == NAMES* ]]; then
            printf "  %-22s %-28s %s\n" "NAME" "STATUS" "PORTS"
            continue
        fi
        local name; name=$(echo "$line" | awk '{print $1}')
        local status; status=$(echo "$line" | awk '{print $2, $3, $4}')
        local ports; ports=$(echo "$line" | awk '{for(i=5;i<=NF;i++) printf "%s ", $i; print ""}')

        # 判断是 dev 还是 prod 容器
        local tag=""
        case "$name" in
            *-dev) tag="${YELLOW}[dev]${NC}" ;;
            ehome-postgres|ehome-redis|ehome-emqx)
                if docker inspect "$name" --format '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' 2>/dev/null | grep -q "5433"; then
                    tag="${YELLOW}[dev]${NC}"
                elif docker inspect "$name" --format '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' 2>/dev/null | grep -q "5434"; then
                    tag="${GREEN}[prod]${NC}"
                else
                    tag="${YELLOW}[dev]${NC}"
                fi
                ;;
            *) tag="${GREEN}[prod]${NC}" ;;
        esac
        printf "  %-30b %-28s %s\n" "$name $tag" "$status" "$ports"
    done <<< "$containers"
    echo

    # 后端日志
    local check_name
    if [[ "$env" == "dev" ]]; then
        check_name="ehome-backend-dev"
    elif [[ "$env" == "prod" ]]; then
        check_name="ehome-backend"
    else
        # all: 显示当前活跃的后端
        if [[ "$running" == "dev" ]]; then
            check_name="ehome-backend-dev"
        elif [[ "$running" == "prod" ]]; then
            check_name="ehome-backend"
        else
            return
        fi
    fi

    if docker ps --filter "name=$check_name" --format "{{.Names}}" 2>/dev/null | grep -q .; then
        echo -e "${YELLOW}${check_name} 日志 (最近5行):${NC}"
        docker logs --tail 5 "$check_name" 2>/dev/null | while IFS= read -r l; do echo "  $l"; done
    fi
}

cmd_logs() {
    local env="$1"
    local svc="${2:-}"
    echo -e "${CYAN}${BOLD}=== ${env} 日志 ===${NC}"
    if [[ -n "$svc" ]]; then
        _compose "$env" logs --tail=100 -f "$svc"
    else
        _compose "$env" logs --tail=100 -f
    fi
}

cmd_clean() {
    local env="$1"
    echo -e "${RED}${BOLD}⚠️  将删除 ${env} 环境所有容器和数据卷${NC}"
    if [[ "$env" == "dev" ]]; then
        echo -e "${RED}   包括: ehome-postgres, ehome-redis, ehome-emqx 数据${NC}"
    fi
    echo -ne "${RED}   确认? (输入 yes 继续): ${NC}"
    read -r confirm
    if [[ "$confirm" != "yes" ]]; then
        echo "已取消"
        exit 0
    fi
    _compose "$env" down -v
    echo -e "${GREEN}已清理${NC}"
}

# ── 主入口 ──

main() {
    local env="${1:-}"
    local cmd="${2:-}"

    if [[ -z "$env" ]]; then
        usage
        exit 0
    fi

    case "$env" in
        dev|prod) ;;
        all)
            cmd_status "all"
            exit 0
            ;;
        *)
            echo -e "${RED}无效环境: $env (有效值: dev, prod, all)${NC}"
            usage
            exit 1
            ;;
    esac

    case "${cmd:-status}" in
        start)   cmd_start "$env" ;;
        stop)    cmd_stop "$env" ;;
        restart) cmd_restart "$env" ;;
        status)  cmd_status "$env" ;;
        logs)    cmd_logs "$env" "${3:-}" ;;
        clean)   cmd_clean "$env" ;;
        *)
            echo -e "${RED}无效命令: $cmd${NC}"
            usage
            exit 1
            ;;
    esac
}

main "$@"
