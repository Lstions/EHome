#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")"

if [ ! -f .env ]; then
  ./gen-env.sh
fi
set -a
# shellcheck disable=SC1091
. ./.env
set +a

BASE="http://127.0.0.1:${LOCAL_WEB_PORT:-18080}"
API="$BASE/api/v1"

echo "等待 ehome-local 就绪（$BASE/readyz）..."
for _ in $(seq 1 150); do
  if curl -fsS "$BASE/readyz" >/dev/null 2>&1; then break; fi
  sleep 2
done
if ! curl -fsS "$BASE/readyz" >/dev/null 2>&1; then
  echo "服务未就绪：最近日志" >&2
  docker logs --tail 30 ehome-local-web >&2 || true
  exit 1
fi

state=$(curl -fsS "$API/auth/initialization" | jq -r '.data.state')
if [ "$state" != "initialized" ]; then
  credential=$(docker logs ehome-local-web 2>&1 \
    | grep -oE 'Initialization credential.*' \
    | tail -1 \
    | sed 's/.*: //' \
    | tr -d '\r')
  if [ -z "$credential" ]; then
    echo "数据库未初始化，但日志里没有初始化凭据；请查看：docker logs ehome-local-web" >&2
    exit 1
  fi
  body=$(jq -n \
    --arg credential "$credential" \
    --arg username "$LOCAL_ADMIN_USERNAME" \
    --arg password "$LOCAL_ADMIN_PASSWORD" \
    --arg email "$LOCAL_ADMIN_EMAIL" \
    '{credential:$credential,username:$username,password:$password,email:$email}')
  code=$(curl -sS -o /tmp/ehome-init-resp.json -w '%{http_code}' \
    -X POST "$API/auth/initialize" -H 'Content-Type: application/json' -d "$body")
  if [ "$code" != "201" ]; then
    echo "初始化失败（HTTP $code）：" >&2
    cat /tmp/ehome-init-resp.json >&2
    exit 1
  fi
  echo "管理员已创建：$LOCAL_ADMIN_USERNAME"
else
  echo "系统已初始化，跳过初始化凭据步骤"
fi

login=$(jq -n --arg username "$LOCAL_ADMIN_USERNAME" --arg password "$LOCAL_ADMIN_PASSWORD" \
  '{username:$username,password:$password}')
token=$(curl -fsS -X POST "$API/auth/login" -H 'Content-Type: application/json' -d "$login" | jq -r '.data.token')
if [ -z "$token" ] || [ "$token" = "null" ]; then
  echo "登录失败，请检查 .env 中的 LOCAL_ADMIN_PASSWORD 是否与已初始化账号一致" >&2
  exit 1
fi

echo "登录验证通过"
curl -fsS "$API/nodes?page=1&page_size=50" -H "Authorization: Bearer $token" \
  | jq -r '"节点数：\(.data.total // (.data.items|length))\(.data.items // [] | map("  - \(.node_id) \(.status) fw=\(.firmware_version // "-")") | join("\n"))"'
echo
echo "访问：$BASE"
echo "账号：$LOCAL_ADMIN_USERNAME / $LOCAL_ADMIN_PASSWORD"
