#!/usr/bin/env bash
# 用指定后端二进制在 127.0.0.1:18091 起服务（前台阻塞；由后台任务托管）。
# 用法: scripts/perf-bms/start_backend.sh [backend_binary]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

BIN=${1:-"$ROOT/.bench/bin/ehome-server-main"}
if [ ! -x "$BIN" ]; then echo "backend binary not found: $BIN" >&2; exit 1; fi

export EHOME_DB_HOST=127.0.0.1
export EHOME_DB_PORT=5432
export EHOME_DB_USER=ehome
export EHOME_DB_PASSWORD=ehome123
export EHOME_DB_NAME="$BENCH_DB"
export EHOME_SERVER_ADDR=127.0.0.1:${PORT:-18091}
export GIN_MODE=release
export EHOME_JWT_SECRET="$BENCH_JWT_SECRET"
export EHOME_DEVICE_ENABLED=false
export LOG_LEVEL=${LOG_LEVEL:-info}
export CONFIG_PATH=/nonexistent/config.yaml
# 前端构建产物（E2E 阶段用；目录不存在时后端只是不提供静态文件）。
export EHOME_STATIC_DIR=${EHOME_STATIC_DIR:-"$ROOT/frontend-shared/dist"}
mkdir -p "$BENCH_DIR"
echo "[start_backend] bin=$BIN db=$BENCH_DB url=127.0.0.1:${PORT:-18091} static=$EHOME_STATIC_DIR"
exec "$BIN"