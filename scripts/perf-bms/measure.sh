#!/usr/bin/env bash
# 对 bench 库测 5 个页面历史请求 + latest-data：预热 1 轮，正式 3 轮，报中位数。
# 用法: scripts/perf-bms/measure.sh [label]     # label 默认 main（旧代码基线）
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)
source "$HERE/lib.sh"

LABEL=${1:-main}
ROUNDS=${ROUNDS:-3}
RUN_ID=$(date +%Y%m%d-%H%M%S)
TSV="$BENCH_DIR/baseline-$LABEL-$RUN_ID.tsv"
OUT_JSON="$BENCH_DIR/baseline-$LABEL-$RUN_ID.json"
OUT_MD="$BENCH_DIR/baseline-$LABEL-$RUN_ID.md"
JWT=$(mint_jwt)

mkdir -p "$BENCH_DIR"
if ! curl -fsS "$BASE_URL/health" >/dev/null 2>&1; then
  echo "[measure] backend not healthy at $BASE_URL — start it first (scripts/perf-bms/start_backend.sh)" >&2
  exit 1
fi
: > "$TSV"

do_req() { # name round url
  local name=$1 round=$2 url=$3 res code
  res=$(curl -s -o /dev/null -w '%{http_code}|%{time_starttransfer}|%{time_total}|%{size_download}' \
        -H "Authorization: Bearer $JWT" "$url")
  echo "$name|$round|$res" >> "$TSV"
  code=${res%%|*}
  if [ "$code" != "200" ]; then
    echo "[measure] WARN $name round=$round http=$res" >&2
  fi
}

one_round() { # round
  local r=$1
  do_req cells16_24h     "$r" "$(hist_url "$CELLS16" "$BENCH_START24_RFC" "$BENCH_END_RFC" 500)"
  do_req metrics9_24h    "$r" "$(hist_url "$METRICS9" "$BENCH_START24_RFC" "$BENCH_END_RFC" 500)"
  do_req protection_30d  "$r" "$(hist_url "$PROTECTION" "$BENCH_START30D_RFC" "$BENCH_END_RFC" 500)"
  do_req temp3_24h       "$r" "$(hist_url "$TEMP3" "$BENCH_START24_RFC" "$BENCH_END_RFC" 500)"
  do_req soc_current_24h "$r" "$(hist_url "$SOC2" "$BENCH_START24_RFC" "$BENCH_END_RFC" 500)"
  do_req latest_data     "$r" "$BASE_URL/api/v1/edge-devices/14/latest-data"
}

echo "[measure] label=$LABEL run=$RUN_ID rounds=$ROUNDS warmup=1"
echo "[measure] warmup round ..."
one_round warmup
for r in $(seq 1 "$ROUNDS"); do
  echo "[measure] round $r ..."
  one_round "r$r"
done

# 环境与数据版本记录（与计时同批，可复现）。
{
  echo "# bench env @ $RUN_ID label=$LABEL"
  echo "## windows"
  echo "end=$BENCH_END_RFC start24=$BENCH_START24_RFC start30d=$BENCH_START30D_RFC db=$BENCH_DB"
  echo "## pg_settings"
  psql_bench -tA -c "SELECT name||'='||setting||coalesce(' '||unit,'') FROM pg_settings WHERE name IN ('shared_buffers','work_mem','maintenance_work_mem','effective_cache_size','max_parallel_workers','max_parallel_workers_per_gather','max_connections','track_io_timing','random_page_cost','seq_page_cost','jit') ORDER BY name"
  echo "## row counts"
  psql_bench -tA -c "SELECT 'unified_data_total='||count(*) FROM unified_data"
  psql_bench -tA -c "SELECT 'unified_24h='||count(*) FROM unified_data WHERE \"timestamp\" >= '$BENCH_START24_PG'::timestamptz"
  psql_bench -tA -c "SELECT 'protection_30d='||count(*) FROM unified_data WHERE sensor_name='protection_status' AND \"timestamp\" >= '$BENCH_START30D_PG'::timestamptz"
  psql_bench -tA -c "SELECT 'device_data_total='||count(*) FROM device_data"
  echo "## sizes"
  psql_bench -tA -c "SELECT relname||' heap='||pg_size_pretty(pg_table_size(c.oid))||' idx='||pg_size_pretty(pg_indexes_size(c.oid))||' total='||pg_size_pretty(pg_total_relation_size(c.oid)) FROM pg_class c WHERE relname IN ('unified_data','unified_data_202610','device_data','device_data_202610') ORDER BY relname"
} > "$BENCH_DIR/baseline-env-$LABEL-$RUN_ID.txt" 2>&1

python3 - "$TSV" "$OUT_JSON" "$OUT_MD" "$LABEL" "$RUN_ID" "$BENCH_END_RFC" "$ROUNDS" <<'PY'
import json, statistics, sys
tsv, out_json, out_md, label, run_id, end_ts, rounds = sys.argv[1:8]
rounds = int(rounds)
rows = []
for line in open(tsv, encoding="utf-8"):
    line = line.strip()
    if not line:
        continue
    name, rnd, rest = line.split("|", 2)
    code, ttfb, total, size = rest.split("|")
    rows.append({"name": name, "round": rnd, "http": int(code),
                 "ttfb_s": float(ttfb), "total_s": float(total), "bytes": int(size)})
order = []
for r in rows:
    if r["name"] not in order:
        order.append(r["name"])
summary = {}
for name in order:
    warm = [r for r in rows if r["name"] == name and r["round"] == "warmup"]
    timed = [r for r in rows if r["name"] == name and r["round"] != "warmup"]
    total = [r["total_s"] for r in timed]
    ttfb = [r["ttfb_s"] for r in timed]
    sizes = [r["bytes"] for r in timed]
    summary[name] = {
        "warmup": warm[0] if warm else None,
        "rounds": timed,
        "median_total_s": statistics.median(total),
        "median_ttfb_s": statistics.median(ttfb),
        "bytes": sizes[0] if len(set(sizes)) == 1 else sizes,
        "http": sorted({r["http"] for r in timed}),
    }
doc = {"label": label, "run_id": run_id, "end_ts": end_ts, "rounds": rounds,
       "params": {"device_pk": 14, "max_points": 500,
                  "scope": "logical_device_id=14 (OR device_id fallback form)"},
       "order": order, "summary": summary}
with open(out_json, "w", encoding="utf-8") as f:
    json.dump(doc, f, ensure_ascii=False, indent=2)
lines = []
lines.append("# BMS 基线计时 — label=%s run=%s" % (label, run_id))
lines.append("")
lines.append("固定窗口 end=%s，device_pk=14，max_points=500；环境/行数/体积见 baseline-env-%s-%s.txt。" % (end_ts, label, run_id))
lines.append("")
lines.append("| 请求 | median total (s) | median TTFB (s) | 体积 (B) | HTTP |")
lines.append("|---|---:|---:|---:|---|")
for name in order:
    s = summary[name]
    lines.append("| %s | %.3f | %.3f | %s | %s |" % (
        name, s["median_total_s"], s["median_ttfb_s"], s["bytes"],
        ",".join(str(c) for c in s["http"])))
lines.append("")
lines.append("## 每轮明细")
lines.append("")
lines.append("| 请求 | 轮次 | total (s) | TTFB (s) | 体积 (B) |")
lines.append("|---|---|---:|---:|---:|")
for r in rows:
    lines.append("| %s | %s | %.3f | %.3f | %d |" % (
        r["name"], r["round"], r["total_s"], r["ttfb_s"], r["bytes"]))
open(out_md, "w", encoding="utf-8").write("\n".join(lines) + "\n")
print("\n".join(lines))
print("")
print("[measure] wrote:")
print("  " + out_json)
print("  " + out_md)
PY
