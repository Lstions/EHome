#!/usr/bin/env bash
# 旧代码实际 SQL 的 EXPLAIN (ANALYZE, BUFFERS) 成本记录（rows / shared hit+read / execution time）。
# 用法: scripts/perf-bms/explain.sh [out_file]
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

OUT=${1:-"$BENCH_DIR/explain-before.txt"}
mkdir -p "$BENCH_DIR"
psql_bench -v end_ts="$BENCH_END_PG" -v start24="$BENCH_START24_PG" -v start30d="$BENCH_START30D_PG" \
  < "$HERE/explain.sql" | tee "$OUT"
echo "[explain] wrote $OUT"
