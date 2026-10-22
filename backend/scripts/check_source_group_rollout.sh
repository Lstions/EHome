#!/usr/bin/env bash
# check_source_group_rollout.sh —— 来源组回滚盘点（方案 N-7 / SG-12）。
#
# 定位：灰度/回滚前回答「现在有没有规则在用来源组、有没有非法残留」。
# 只读；4 条审计 SQL 全部返回 0 行才算干净（G0 灰度前置）。
#
# 用法：
#   ./scripts/check_source_group_rollout.sh             # 跑全部审计（PG）
#   ./scripts/check_source_group_rollout.sh --self-test # 离线自证（断言 4 条审计与关键谓词齐全）
#   ./scripts/check_source_group_rollout.sh --json      # 机器可读（每行一条 JSON）
#
# 退出码：0=全部 0 行；1=有残留；2=用法错/DB 不可达。
set -uo pipefail
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

JSON=0; SELF_TEST=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --json) JSON=1 ;;
    --self-test) SELF_TEST=1 ;;
    -h|--help) sed -n '2,18p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

# 4 条审计（SG-12）：每条给出「残留 = 必须处理」的语义。
# audit: rules_source_binding  规则双指针矛盾（五态非法组合）
# audit: group_without_member 启用但无成员的组（规则会恒 unbound）
# audit: duplicate_active     同组/同 data_sources 组键出现多条 active
# audit: composite_active     composite 组出现 active 成员（违反 I5）
audits() {
cat <<'SQL'
-- audit: rules_source_binding
SELECT 'rules_source_binding' AS audit, count(*) AS rows FROM automation_rules
 WHERE (COALESCE(source_kind,'edge_device')='edge_device' AND source_group_id IS NOT NULL)
    OR (source_kind='source_group' AND (source_group_id IS NULL OR source_group_id=0))
    OR (source_kind NOT IN ('edge_device','source_group') AND source_kind IS NOT NULL);
-- audit: group_without_member
SELECT 'group_without_member' AS audit, count(*) AS rows FROM source_groups g
 WHERE g.enabled = true AND NOT EXISTS (SELECT 1 FROM source_group_members m WHERE m.group_id = g.id);
-- audit: duplicate_active
SELECT 'duplicate_active' AS audit, count(*) AS rows FROM (
  SELECT group_id FROM source_group_members WHERE status='active' GROUP BY group_id HAVING count(*) > 1
) d;
-- audit: composite_active
SELECT 'composite_active' AS audit, count(*) AS rows FROM source_group_members m
 JOIN source_groups g ON g.id = m.group_id
 WHERE g.kind='composite' AND m.status='active';
SQL
}

self_test() {
  local failures=0 n
  echo "== 回滚盘点自证 =="
  n=$(audits | grep -c '^-- audit:')
  if [ "$n" -eq 4 ]; then echo "SELFTEST PASS: 4 条审计齐备（N-7）"; else
    echo "SELFTEST FAIL: 审计数=$n（期望 4）"; failures=$((failures + 1)); fi
  if audits | grep -q "COALESCE(source_kind,'edge_device')"; then echo "SELFTEST PASS: 五态矛盾谓词存在"; else
    echo "SELFTEST FAIL: 缺五态矛盾谓词"; failures=$((failures + 1)); fi
  if audits | grep -q 'HAVING count(\*) > 1'; then echo "SELFTEST PASS: 重复 active 谓词存在"; else
    echo "SELFTEST FAIL: 缺重复 active 谓词"; failures=$((failures + 1)); fi
  if audits | grep -q "kind='composite'"; then echo "SELFTEST PASS: composite-active 谓词存在"; else
    echo "SELFTEST FAIL: 缺 composite-active 谓词"; failures=$((failures + 1)); fi
  if bash -n "$0" 2>/dev/null; then echo "SELFTEST PASS: 脚本语法可解析"; else
    echo "SELFTEST FAIL: 语法错误"; failures=$((failures + 1)); fi
  echo ""
  echo "== 自证结果：$failures 项失败 =="
  return "$failures"
}

if [ "$SELF_TEST" -eq 1 ]; then self_test; exit $?; fi

# DB 连接口径：与 e2e 一致（EHOME_TEST_DB=postgres + EHOME_DB_*），否则退回本机 docker postgres。
PGHOST=$(printenv EHOME_DB_HOST || true); [ -z "$PGHOST" ] && PGHOST=127.0.0.1
PGPORT=$(printenv EHOME_DB_PORT || true); [ -z "$PGPORT" ] && PGPORT=5432
PGUSER=$(printenv EHOME_DB_USER || true); [ -z "$PGUSER" ] && PGUSER=ehome
PGPASSWORD=$(printenv EHOME_DB_PASSWORD || true); [ -z "$PGPASSWORD" ] && PGPASSWORD=ehome123
PGDATABASE=$(printenv EHOME_DB_NAME || true); [ -z "$PGDATABASE" ] && PGDATABASE=ehome
export PGHOST PGPORT PGUSER PGPASSWORD PGDATABASE

if command -v psql >/dev/null 2>&1; then
  exec_psql() { psql -At -F '|' -c "$1"; }
elif command -v docker >/dev/null 2>&1 && docker exec ehome-postgres psql -U ehome -d ehome -At -c 'select 1' >/dev/null 2>&1; then
  exec_psql() { docker exec ehome-postgres psql -U ehome -d ehome -At -F '|' -c "$1"; }
else
  echo "ROLLOUT: DB 不可达（psql/docker 均不可用）"; exit 2
fi

total=0; dirty=0
while IFS= read -r block; do
  audit=$(printf '%s\n' "$block" | sed -n '1p' | sed 's/^-- audit: //')
  sql=$(printf '%s\n' "$block" | sed -n '2,$p')
  rows=$(exec_psql "$sql" 2>/dev/null | awk -F'|' '{print $2}' | tr -d ' ')
  if [ -z "$rows" ]; then echo "ROLLOUT: $audit 查询失败"; exit 2; fi
  total=$((total + 1))
  if [ "$JSON" -eq 1 ]; then printf '{"audit":"%s","rows":%s}\n' "$audit" "$rows"
  else echo "[$audit] rows=$rows"; fi
  [ "$rows" -ne 0 ] && dirty=$((dirty + 1))
done < <(audits | awk '/^-- audit:/{if (b!="") print b; b=$0; next} {b=b"\n"$0} END{if (b!="") print b}')

echo ""
if [ "$dirty" -gt 0 ]; then
  echo "RESULT: FAIL($dirty/$total audits 有残留) — 回滚/灰度前置未满足"
  exit 1
fi
echo "RESULT: PASS — $total 条审计全部 0 行（可回滚/可灰度）"
