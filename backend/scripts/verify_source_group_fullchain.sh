#!/usr/bin/env bash
# 来源组全链路验收（方案《规则数据来源与复合来源》v0.3 §12.9.3 R-1…R-10）
#
# 为什么需要这个脚本：单测绿 ≠ 全链路通。本脚本按「分层递进」顺序跑，任一层红即停，
# 并把每层的**分母**打印出来（防止「0 个用例也算绿」的假绿）。
#
# 用法：
#   bash backend/scripts/verify_source_group_fullchain.sh [--fast]
#   --fast  跳过 PG / -race / 前端（本地快速回归）
set -uo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"
export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH
export GOCACHE=${GOCACHE:-/tmp/eh-sg-cache}

FAST=0
[ "${1:-}" = "--fast" ] && FAST=1

PASS=0; FAIL=0; SKIP=0
RESULT_LINES=()
ok()   { PASS=$((PASS+1)); RESULT_LINES+=("PASS  $1"); printf '\033[32mPASS\033[0m  %s\n' "$1"; }
bad()  { FAIL=$((FAIL+1)); RESULT_LINES+=("FAIL  $1"); printf '\033[31mFAIL\033[0m  %s\n' "$1"; }
skip() { SKIP=$((SKIP+1)); RESULT_LINES+=("SKIP  $1"); printf '\033[33mSKIP\033[0m  %s\n' "$1"; }

step() { printf '\n\033[1m── %s ──\033[0m\n' "$1"; }

# 0) 编译
step "0. 编译";
if go build ./... 2>/tmp/build.log; then ok "go build ./..."; else bad "go build ./..."; sed -n '1,20p' /tmp/build.log; fi

# 1) 静态检查
step "1. go vet";
if go vet ./internal/... ./cmd/... 2>/tmp/vet.log; then ok "go vet"; else bad "go vet"; sed -n '1,20p' /tmp/vet.log; fi

# 2) 单元/集成测试（打印每包用例数，防「0 用例假绿」）
step "2. 单元测试";
UNIT_PKGS="./internal/models/ ./internal/dbschema/ ./internal/sourceref/ ./internal/datasource/ ./internal/automation/ ./internal/alert/ ./internal/api/"
if go test $UNIT_PKGS -count=1 2>/tmp/unit.log; then
  CASES=$(go test $UNIT_PKGS -list '.+' 2>/dev/null | grep -c '^Test')
  ok "单元测试（$CASES 个 Test）"
else
  bad "单元测试"; grep -E '^(--- FAIL|FAIL\s)' /tmp/unit.log | head -20
fi

# 3) 门禁脚本
step "3. 门禁 SG-01…SG-21";
if [ -x backend/scripts/check_source_group.sh ]; then
  if bash backend/scripts/check_source_group.sh --strict 2>/tmp/gate.log; then ok "check_source_group.sh --strict";
  else bad "check_source_group.sh --strict"; tail -30 /tmp/gate.log; fi
else
  bad "check_source_group.sh 不存在";
fi

# 4) e2e（SQLite 快速）
step "4. e2e SQLite";
if go test -tags=e2e ./internal/e2e/ -count=1 -v 2>/tmp/e2e_sqlite.log; then
  N=$(grep -c '^=== RUN   Test' /tmp/e2e_sqlite.log || true)
  if [ "$N" -eq 0 ]; then bad "e2e SQLite（0 个用例 = 假绿：构建标签/筛选写错？）"
  else ok "e2e SQLite（$N 个用例）"; fi
else bad "e2e SQLite"; grep -E '^(--- FAIL|FAIL\s)' /tmp/e2e_sqlite.log | head -20; fi

if [ "$FAST" = "1" ]; then
  printf '\n\033[33m--fast：跳过 PG / -race / 前端\033[0m\n'
else
  # 5) e2e（PG + -race）
  step "5. e2e PostgreSQL (+ -race)"
  PG_DB=ehome_sg_verify
  if docker exec ehome-postgres psql -U ehome -d postgres -q -c "DROP DATABASE IF EXISTS $PG_DB;" -c "CREATE DATABASE $PG_DB;" >/dev/null 2>&1; then
    if EHOME_TEST_DB=postgres EHOME_DB_HOST=127.0.0.1 EHOME_DB_PORT=5432 EHOME_DB_USER=ehome EHOME_DB_PASSWORD=ehome123 EHOME_DB_NAME=$PG_DB \
      go test -tags=e2e ./internal/e2e/ -count=1 -race -v 2>/tmp/e2e_pg.log; then
      N=$(grep -c '^=== RUN   Test' /tmp/e2e_pg.log || true)
      if [ "$N" -eq 0 ]; then bad "e2e PG -race（0 个用例 = 假绿）"; else ok "e2e PG -race（$N 个用例）"; fi
    else
      bad "e2e PG -race"
      grep -E '^(--- FAIL|FAIL[[:space:]]|WARNING: DATA RACE)' /tmp/e2e_pg.log | head -20
    fi
    docker exec ehome-postgres psql -U ehome -d postgres -q -c "DROP DATABASE IF EXISTS $PG_DB;" >/dev/null 2>&1 || true
  else
    skip "e2e PG（docker ehome-postgres 不可达）"
  fi

  # 6) 前端单元测试（相关子集）
  step "6. 前端单测";
  if [ -d ../frontend-shared/node_modules ] || [ -d ../frontend-shared/src ]; then
    FE=../frontend-shared
    if [ -x "$FE/node_modules/.bin/vitest" ] || [ -d "$FE/node_modules" ]; then
      ( cd "$FE" && timeout 600s npx vitest run --maxWorkers=2 --testTimeout=30000 >/tmp/fe_unit.log 2>&1 ) \
        && ok "前端 vitest" \
        || { bad "前端 vitest"; grep -E 'FAIL |Test Files' /tmp/fe_unit.log | head -10; }
    else
      skip "前端 vitest（node_modules 未安装）"
    fi
  else
    skip "前端 vitest（目录不存在）"
  fi

  # 7) 前端渲染契约（真实构建产物 + 注入 API）
  step "7. 前端渲染契约";
  FE=../frontend-shared
  CHROME=/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome
  if [ -x "$CHROME" ] && [ -f "$FE/playwright.render.config.ts" ]; then
    ( cd "$FE" && npx vite build --outDir dist >/tmp/fe_build.log 2>&1 ) || bad "前端构建"
    if ( cd "$FE" && CHROME_BIN="$CHROME" timeout 600s npx playwright test -c playwright.render.config.ts >/tmp/fe_render.log 2>&1 ); then
      ok "前端渲染契约"
    else
      bad "前端渲染契约"; tail -20 /tmp/fe_render.log
    fi
  else
    skip "前端渲染契约（CHROME_BIN 或配置文件缺失）"
  fi
fi

printf '\n\033[1m=== 全链路验收汇总 ===\033[0m\n'
for line in "${RESULT_LINES[@]+${RESULT_LINES[@]}}"; do printf '%s\n' "$line"; done
printf 'PASS=%d FAIL=%d SKIP=%d\n' "$PASS" "$FAIL" "$SKIP"
if [ "$FAIL" -gt 0 ]; then
  printf 'RESULT: FAIL\n'
  exit 1
fi
if [ "$SKIP" -gt 0 ]; then
  printf 'RESULT: PASS_WITH_SKIP\n'
  exit 0
fi
printf 'RESULT: PASS\n'
