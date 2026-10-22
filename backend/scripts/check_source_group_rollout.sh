#!/usr/bin/env bash
# check_source_group_rollout.sh —— 来源组回滚盘点（方案 N-7 / SG-12）。
#
# 定位：灰度/回滚前回答「现在有没有规则在用来源组、有没有非法残留」。
# 只读；4 条审计 SQL 全部返回 0 行才算干净（G0 灰度前置）。
#
# 用法：
#   ./scripts/check_source_group_rollout.sh             # 跑全部审计（PG）
#   ./scripts/check_source_group_rollout.sh --rollback  # 回退旧二进制前的前置检查（见下）
#   ./scripts/check_source_group_rollout.sh --self-test # 离线自证（断言审计与关键谓词齐全）
#   ./scripts/check_source_group_rollout.sh --json      # 机器可读（每行一条 JSON）
#
# --rollback 回答「能不能安全回退到旧二进制」。两处陷阱（合并可行性审查 C3 实测）：
#   ① 新索引 ux_ds_one_active 会让【旧二进制】的 Deactivate 撞 UNIQUE：
#      旧代码是「先把候选置 active，再把目标置 disabled」，同一语句序列内出现双 active。
#      生产 data_sources=0 行时无风险；但上线后若创建过 data_sources 且有 active 行，
#      回滚后手动停用活跃来源会 500。⇒ 回滚前 DROP INDEX IF EXISTS ux_ds_one_active。
#   ② 旧二进制 ConfirmEvent 不比对 source_hash：来源切换后确认会按【旧来源】执行，
#      用户看到的是 A、实际用 B 执行。⇒ 回滚前清理 result='pending_confirm' 且
#      source_hash<>'' 的事件（走确认/取消 API，或 UPDATE result='expired'）。
#   ③ 旧二进制不认 source_kind='source_group'，只会按 trigger_edge_device_id（锚点成员）
#      触发 ⇒ 若当前 active 成员 != 锚点，规则会读到非权威数据。脚本会把这类规则列出来，
#      由人逐条确认锚点成员是否仍权威。
#
# 退出码：0=全部 0 行；1=有残留；2=用法错/DB 不可达。
set -uo pipefail
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

JSON=0; SELF_TEST=0; ROLLBACK=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --json) JSON=1 ;;
    --self-test) SELF_TEST=1 ;;
    --rollback) ROLLBACK=1 ;;
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
  if declare -f rollback_check >/dev/null; then echo "SELFTEST PASS: rollback_check 已定义"; else
    echo "SELFTEST FAIL: 缺 rollback_check"; failures=$((failures + 1)); fi
  if declare -f rollback_check | grep -q 'ux_ds_one_active'; then echo "SELFTEST PASS: 回滚检查含 ux_ds_one_active 谓词"; else
    echo "SELFTEST FAIL: 回滚检查缺 ux_ds_one_active 谓词"; failures=$((failures + 1)); fi
  if declare -f rollback_check | grep -q 'source_hash'; then echo "SELFTEST PASS: 回滚检查含 source_hash 谓词"; else
    echo "SELFTEST FAIL: 回滚检查缺 source_hash 谓词"; failures=$((failures + 1)); fi
  if bash -n "$0" 2>/dev/null; then echo "SELFTEST PASS: 脚本语法可解析"; else
    echo "SELFTEST FAIL: 语法错误"; failures=$((failures + 1)); fi
  echo ""
  echo "== 自证结果：$failures 项失败 =="
  return "$failures"
}

# rollback_check 回答「现在能不能安全回退旧二进制」。只读，不改库。
rollback_check() {
  local blockers=0
  local idx active pending
  idx=$(exec_psql "SELECT count(*) FROM pg_indexes WHERE indexname='ux_ds_one_active';" 2>/dev/null | tr -d ' ')
  active=$(exec_psql "SELECT count(*) FROM data_sources WHERE status='active';" 2>/dev/null | tr -d ' ')
  # source_hash 是本次新增列：旧库没有它 ⇒ 「跨来源 pending」这个风险不可能存在，按 0 处理
  # （不能因为列不存在就报「查询失败」——那是拿着新库的判据去量旧库）。
  has_hash=$(exec_psql "SELECT count(*) FROM information_schema.columns WHERE table_name='automation_events' AND column_name='source_hash';" 2>/dev/null | tr -d ' ')
  if [ "$has_hash" = "0" ]; then
    pending=0
  else
    pending=$(exec_psql "SELECT count(*) FROM automation_events WHERE result='pending_confirm' AND COALESCE(source_hash,'') <> '';" 2>/dev/null | tr -d ' ')
  fi
  if [ -z "$idx" ] || [ -z "$active" ] || [ -z "$pending" ] || [ -z "$has_hash" ]; then
    echo "ROLLBACK: 查询失败（DB 不可达）"; return 2
  fi
  echo "== 回退旧二进制前置检查 =="
  echo "[index]   ux_ds_one_active 存在=$idx"
  echo "[data_src] data_sources active 行=$active"
  if [ "$idx" -ge 1 ] && [ "$active" -ge 1 ]; then
    echo "  ❌ 阻断：回滚后旧 Deactivate（先升新后降旧）会撞 UNIQUE 冲突。"
    echo "     处置：psql -c "DROP INDEX IF EXISTS ux_ds_one_active;" 或先清空 active 行。"
    blockers=$((blockers + 1))
  elif [ "$idx" -ge 1 ]; then
    echo "  ✅ 索引存在但 data_sources 无 active 行 ⇒ 旧 Deactivate 不会被触发，可回滚。"
  else
    echo "  ✅ 索引不存在（未跑过新迁移或无 data_sources）。"
  fi
  echo "[confirm] pending_confirm 且 source_hash<>'' 的事件=$pending"
  if [ "$pending" -ge 1 ]; then
    echo "  ❌ 阻断：旧二进制不比对 source_hash，回滚后确认会按旧来源执行。"
    echo "     处置：走确认/取消 API，或 UPDATE automation_events SET result='expired'"
    echo "           WHERE result='pending_confirm' AND COALESCE(source_hash,'') <> '';"
    blockers=$((blockers + 1))
  else
    echo "  ✅ 无跨来源 pending_confirm 事件。"
  fi
  # ③ 绑定来源组的规则：回滚后旧码只看锚点成员，需人工确认锚点是否权威。
  echo "[anchor]  绑定来源组的规则（回滚后旧码按锚点成员触发，需逐条确认锚点仍权威）："
  local rules has_kind
  # source_kind 也是新增列：旧库没有它 ⇒ 不可能有组规则。必须显式判定，
  # 否则「列不存在」会被误报成「没有组规则」（fail-open 的假绿）。
  has_kind=$(exec_psql "SELECT count(*) FROM information_schema.columns WHERE table_name='automation_rules' AND column_name='source_kind';" 2>/dev/null | tr -d ' ')
  if [ -z "$has_kind" ]; then
    echo "  ⚠ 查询失败（DB 不可达）"; return 2
  fi
  if [ "$has_kind" = "0" ]; then
    rules=""
  else
    rules=$(exec_psql "SELECT r.id, COALESCE(r.name,''), COALESCE(r.trigger_edge_device_id,0), COALESCE(r.source_group_id,0) FROM automation_rules r WHERE r.source_kind='source_group' ORDER BY r.id;" 2>/dev/null)
  fi
  if [ "$has_kind" = "0" ]; then
    echo "  ✅ 旧库无 source_kind 列 ⇒ 不可能有组规则。"
  elif [ -z "$rules" ]; then
    echo "  ✅ 无 source_kind='source_group' 规则。"
  else
    printf '%s\n' "$rules" | while IFS='|' read -r rid rname anchor gid; do
      echo "    rule=$rid name=$rname 锚点设备=$anchor 组=$gid"
    done
    echo "  ⚠ 以上规则回滚后只按锚点设备触发；若组内 active 已不是该设备，请先改绑或接受降级。"
  fi
  echo ""
  if [ "$blockers" -gt 0 ]; then
    echo "RESULT: FAIL($blockers 项阻断) — 先按上面处置，再回退旧二进制"
    return 1
  fi
  echo "RESULT: PASS — 无硬阻断；请人工确认 [anchor] 清单后执行回退"
  return 0
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

if [ "$ROLLBACK" -eq 1 ]; then rollback_check; exit $?; fi

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
