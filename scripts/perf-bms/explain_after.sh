#!/usr/bin/env bash
# 修复后新 SQL 的 EXPLAIN (ANALYZE, BUFFERS)：avg(单测点 24h) / change(protection 30d) / latest-data / raw 对照。
# 用法: scripts/perf-bms/explain_after.sh [out_file]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

OUT=${1:-"$BENCH_DIR/explain-after.txt"}
mkdir -p "$BENCH_DIR"
psql_bench -v end_ts="$BENCH_END_PG" -v start24="$BENCH_START24_PG" -v start30d="$BENCH_START30D_PG" \
  < "$HERE/explain_after.sql" | tee "$OUT"
echo "[explain_after] wrote $OUT"
