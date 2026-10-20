#!/usr/bin/env bash
# BMS 页慢修复 — 本机性能基线（ehome_bench）共享常量与工具函数。
# 约定：bench 库固定 ehome_bench，后端固定 127.0.0.1:18091；
#       固定基准时刻保证任意时间重跑可比。
set -euo pipefail

HERE=$(cd "$(dirname "$0")" && pwd)
ROOT=$(cd "$HERE/../.." && pwd)
BENCH_DIR=${BENCH_DIR:-"$ROOT/.bench"}
BENCH_DB=${BENCH_DB:-ehome_bench}
PG_CONTAINER=${PG_CONTAINER:-ehome-postgres}
BASE_URL=${BASE_URL:-http://127.0.0.1:18091}
# 本地开发用 JWT secret（生产用另一个；本目录脚本不接触生产）。
BENCH_JWT_SECRET=${BENCH_JWT_SECRET:-bench-local-secret-0123456789abcdef}

# 固定基准时刻（UTC）。24h 窗口 = END-24h .. END；30d 窗口 = END-30d .. END。
# 与生产取证同一天（2026-10-20），保证 24h 数据落在 unified_data_202610。
BENCH_END_TS=${BENCH_END_TS:-"2026-10-20 14:00:00+00"}

BENCH_END_RFC=$(date -u -d "$BENCH_END_TS" +%Y-%m-%dT%H:%M:%SZ)
BENCH_START24_RFC=$(date -u -d "$BENCH_END_TS - 24 hours" +%Y-%m-%dT%H:%M:%SZ)
BENCH_START30D_RFC=$(date -u -d "$BENCH_END_TS - 30 days" +%Y-%m-%dT%H:%M:%SZ)
BENCH_END_PG=$(date -u -d "$BENCH_END_TS" "+%Y-%m-%d %H:%M:%S+00")
BENCH_START24_PG=$(date -u -d "$BENCH_END_TS - 24 hours" "+%Y-%m-%d %H:%M:%S+00")
BENCH_START30D_PG=$(date -u -d "$BENCH_END_TS - 30 days" "+%Y-%m-%d %H:%M:%S+00")

# 5 个页面历史请求的 category 清单（与前端 BmsChartsPanel / diag 卡一致）。
CELLS16=$(python3 -c "print(','.join('cell_voltage_%d' % i for i in range(1, 17)))")
METRICS9="total_voltage,current,rsoc,remaining_capacity,temperature_1,temperature_2,temperature_3,cell_voltage_max,cell_voltage_min"
TEMP3="temperature_1,temperature_2,temperature_3"
SOC2="rsoc,current"
PROTECTION="protection_status"

# 通过 docker exec 在 PG 容器内执行 psql（stdin 可用，便于 < file.sql）。
psql_bench() {
  docker exec -i "$PG_CONTAINER" psql -U ehome -d "$BENCH_DB" -v ON_ERROR_STOP=1 "$@"
}

# 铸造本地 bench JWT：算法与生产探针 .tmp-verify/prod_api.sh 相同（HS256），
# sv 必须等于 bench 库 users.session_version（seed_meta.sql 固定为 2）。
mint_jwt() {
  BENCH_JWT_SECRET="$BENCH_JWT_SECRET" python3 - <<'PY'
import base64, hmac, hashlib, json, os, time, uuid
secret = os.environ["BENCH_JWT_SECRET"].encode()
def b64(d):
    return base64.urlsafe_b64encode(d).rstrip(b"=").decode()
now = int(time.time())
hdr = {"alg": "HS256", "typ": "JWT"}
pl = {"sv": 2, "iss": "ehome", "aud": ["ehome-api"], "sub": "1",
      "exp": now + 7200, "iat": now, "jti": str(uuid.uuid4())}
s = b64(json.dumps(hdr, separators=(",", ":")).encode()) + "." + b64(json.dumps(pl, separators=(",", ":")).encode())
print(s + "." + b64(hmac.new(secret, s.encode(), hashlib.sha256).digest()))
PY
}

# history batch URL 构造：hist_url <categories> <start_rfc3339> <end_rfc3339> <max_points>
hist_url() {
  printf '%s/api/v1/unified-data/historical-batch?device_pk=14&categories=%s&start_time=%s&end_time=%s&max_points=%s' \
    "$BASE_URL" "$1" "$2" "$3" "$4"
}
