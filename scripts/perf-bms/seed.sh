#!/usr/bin/env bash
# 建立 ehome_bench 生产量级基线库（可重复执行：每次 DROP 重造）。
# 步骤：建库 -> 起后端完成 schema 迁移 -> 写 bench 鉴权/设备元数据 -> 造数 -> ANALYZE。
# 用法: scripts/perf-bms/seed.sh [backend_binary]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

BIN=${1:-"$ROOT/.bench/bin/ehome-server-main"}
mkdir -p "$BENCH_DIR"
LOG="$BENCH_DIR/seed-migrate.log"

echo "[seed] DB=$BENCH_DB container=$PG_CONTAINER bin=$BIN"
echo "[seed] 24h window: $BENCH_START24_RFC .. $BENCH_END_RFC"
echo "[seed] 30d window: $BENCH_START30D_RFC .. $BENCH_END_RFC"

echo "[seed] (1/5) drop/create database"
docker exec "$PG_CONTAINER" psql -U ehome -d postgres -v ON_ERROR_STOP=1   -c "DROP DATABASE IF EXISTS $BENCH_DB WITH (FORCE)"   -c "CREATE DATABASE $BENCH_DB OWNER ehome" >/dev/null

echo "[seed] (2/5) start backend to migrate schema, log=$LOG"
: > "$LOG"
"$HERE/start_backend.sh" "$BIN" >>"$LOG" 2>&1 &
SERVER_PID=$!
cleanup() {
  if kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
    wait "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT
OK=0
for _ in $(seq 1 180); do
  if ! kill -0 "$SERVER_PID" 2>/dev/null; then break; fi
  if curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then OK=1; break; fi
  sleep 1
done
if [ "$OK" != "1" ]; then
  echo "[seed] backend failed to become healthy; tail of $LOG:"
  tail -50 "$LOG" || true
  exit 1
fi
echo "[seed] schema ready"

echo "[seed] (3/5) bench auth + device metadata"
psql_bench < "$HERE/seed_meta.sql"

echo "[seed] (4/5) seed production-scale rows (24h 2,501,279 + history 19,964,868 unified, 2,000,000 device_data)"
psql_bench -v end_ts="$BENCH_END_PG" -v start24="$BENCH_START24_PG" -v start30d="$BENCH_START30D_PG" \
  < "$HERE/seed_data.sql"

echo "[seed] (5/5) sizes after seed"
psql_bench -c "SELECT c.relname AS rel, c.reltuples::bigint AS est_rows, pg_size_pretty(pg_table_size(c.oid)) AS heap, pg_size_pretty(pg_indexes_size(c.oid)) AS indexes, pg_size_pretty(pg_total_relation_size(c.oid)) AS total FROM pg_class c WHERE c.relname IN ('unified_data','unified_data_202610','device_data','device_data_202610') ORDER BY c.relname"

cleanup
trap - EXIT
echo "[seed] done (backend stopped). Next: scripts/perf-bms/start_backend.sh as background job."
