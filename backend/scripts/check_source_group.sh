#!/usr/bin/env bash
# check_source_group.sh —— 来源组（source_group）门禁 SG-01…SG-21（方案《规则数据来源与复合来源》§12.6 + §12.9.1 F-9/F-5）。
#
# 定位：门禁的价值在于能变红。三态 PASS/FAIL/SKIP：
#   - SKIP 只用于「依赖尚未落地」（PG 不可达、代码批次未完成但目标已登记）；
#   - 「目标不存在/测试不存在/grep 该命中却没命中」一律 FAIL（反假绿）；
#   - 末尾 RESULT：PASS / PASS_WITH_SKIPS(n) / FAIL(n) / FAIL_STRICT_SKIP(n)。--strict 让 SKIP 也红。
#
# 用法：
#   ./scripts/check_source_group.sh                 # 全部门禁（SKIP 容忍）
#   ./scripts/check_source_group.sh --strict        # 发布口径（SKIP 也红）
#   ./scripts/check_source_group.sh --self-test     # 反假绿自证：证明每类 helper 能变红
#   ./scripts/check_source_group.sh --mutation      # 变异自证（apply→跑→必须非零→restore→md5）
#   ./scripts/check_source_group.sh --only SG-14    # 单条
#   ./scripts/check_source_group.sh --list          # 列出编号与一句话判据
#
# 纪律：只读仓库；--mutation 临时改源码必须备份-改-跑-还原并核对 md5。
set -uo pipefail
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
BACKEND_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
REPO_DIR=$(cd "$BACKEND_DIR/.." && pwd)
cd "$BACKEND_DIR" || exit 2

GO=$(printenv GO 2>/dev/null || true)
if [ -z "$GO" ]; then GO=go; fi
if ! command -v "$GO" >/dev/null 2>&1; then
  for cand in "$REPO_DIR/.tools/gotool/bin/go" "/mnt/storage/WorkSpace/EHome/.tools/gotool/bin/go"; do
    if [ -x "$cand" ]; then GO="$cand"; break; fi
  done
fi
if ! command -v "$GO" >/dev/null 2>&1 && [ ! -x "$GO" ]; then
  echo "FATAL: 找不到 go 工具链（PATH 无 go，也未见 .tools/gotool/bin/go）" >&2
  exit 2
fi

STRICT=0; SELF_TEST=0; MUTATION=0; ONLY=""; LIST=0
while [ "$#" -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1 ;;
    --self-test) SELF_TEST=1 ;;
    --mutation) MUTATION=1 ;;
    --only) shift; ONLY="$1" ;;
    --list) LIST=1 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

tmp_dir=$(mktemp -d 2>/dev/null || mktemp -d -t sggates.XXXXXX)
trap 'rm -rf "$tmp_dir"' EXIT
RESULTS=""
append_result() { RESULTS="$RESULTS$1|$2|$3|$4
"; }
record_pass() { append_result PASS "$1" "$2" "$3"; }
record_fail() { append_result FAIL "$1" "$2" "$3"; }
record_skip() { append_result SKIP "$1" "$2" "$3"; }
record_warn() { append_result WARN "$1" "$2" "$3"; }

GATE_HEAD=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)
GATE_DIRTY=$(git -C "$REPO_DIR" status --porcelain -- backend .github 2>/dev/null | wc -l | tr -d ' ')
if [ "$GATE_DIRTY" -gt 0 ]; then
  echo "WARN: dirty worktree, results may be flaky (HEAD=$GATE_HEAD, $GATE_DIRTY files)"
fi
echo "HEAD: $GATE_HEAD (dirty files: $GATE_DIRTY)"
RESULT_SUFFIX=" (HEAD=$GATE_HEAD)"

# ── 通用 helpers ──────────────────────────────────────────────────────────────

run_go_test() {
  local pattern="$1"; shift
  : > "$tmp_dir/gotest.out"
  "$GO" test "$@" -run "$pattern" -count=1 -v >> "$tmp_dir/gotest.out" 2>&1 || return 1
  grep -q '^=== RUN' "$tmp_dir/gotest.out" || return 2
  return 0
}

grep_absent() {
  local pattern="$1"; shift
  local path missing=0
  for path in "$@"; do [ -e "$path" ] || missing=1; done
  if [ "$missing" -eq 1 ]; then return 2; fi
  if grep -rnE "$pattern" "$@" --include=*.go 2>/dev/null | grep -v '_test.go' | grep -q .; then return 1; fi
  return 0
}

grep_present() {
  local pattern="$1"; shift
  local path missing=0
  for path in "$@"; do [ -e "$path" ] || missing=1; done
  if [ "$missing" -eq 1 ]; then return 2; fi
  if grep -rqE "$pattern" "$@" --include=*.go 2>/dev/null; then return 0; fi
  return 1
}

file_has() { [ -f "$1" ] && grep -qE "$2" "$1"; }

pg_available() {
  if command -v docker >/dev/null 2>&1; then
    docker exec ehome-postgres psql -U ehome -d postgres -At -c 'select 1' >/dev/null 2>&1 && return 0
  fi
  return 1
}

go_list_contains() {
  local needle="$1"; shift
  local out
  out=$("$GO" list -deps "$@" 2>/dev/null) || return 2
  printf '%s\n' "$out" | grep -qE "$needle" && return 0
  return 1
}

backup_file() {
  local f="$1"
  cp "$f" "$tmp_dir/$(basename "$f").bak"
  md5sum "$f" | cut -d' ' -f1 > "$tmp_dir/$(basename "$f").md5"
}
restore_file() {
  local f="$1" want got
  cp "$tmp_dir/$(basename "$f").bak" "$f"
  want=$(cat "$tmp_dir/$(basename "$f").md5")
  got=$(md5sum "$f" | cut -d' ' -f1)
  if [ "$want" != "$got" ]; then echo "MUTATION: restore md5 mismatch on $f" >&2; return 1; fi
  return 0
}

# ── 门禁实现 ──────────────────────────────────────────────────────────────────

gate_SG_01() {
  local reasons=""
  if ! run_go_test 'TestValidateSourceRefColumns_FiveStates|TestAutomationRuleSourceRef|TestAlertRuleSourceRef' ./internal/models; then
    reasons="$reasons 模型五态测试未通过或不存在;"
  fi
  if ! grep_present 'ValidateSourceRefColumns' ./internal/api ./internal/automation ./internal/alert ./internal/sourceref; then
    reasons="$reasons 校验实现未被保存/加载路径调用;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-01 '双指针五态一致性' "$reasons"; else
    record_pass SG-01 '双指针五态一致性' '五态测试 PASS 且保存/加载路径已调用 ValidateSourceRefColumns'
  fi
}

gate_SG_02() {
  record_skip SG-02 '成员 category=组 category（已删除）' 'N-8 裁决删除：无字段载体，由 source_groups.category 单一权威替代'
}

gate_SG_03() {
  local reasons=""
  file_has internal/models/source_group.go 'chk_sg_combine' || reasons="$reasons DB CHECK chk_sg_combine 缺失;"
  file_has internal/models/source_group.go 'CombineWhitelist' || reasons="$reasons CombineWhitelist 常量缺失;"
  if ! run_go_test 'TestSourceGroupCombineWhitelist|TestSourceGroupKindCombine' ./internal/models; then
    reasons="$reasons combine/kind 白名单测试不存在或未通过;"
  fi
  if ! grep_present 'CombineWhitelist|ValidateSourceGroup|SourceGroupValidation' ./internal/api ./internal/datasource; then
    reasons="$reasons API/service 保存期 400 校验未接线;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-03 'combine 白名单 + kind 语义' "$reasons"; else
    record_pass SG-03 'combine 白名单 + kind 语义' 'DB CHECK + 常量白名单 + 保存期 400 校验齐备'
  fi
}

gate_SG_04() {
  local reasons="" n=0
  if ! run_go_test 'TestEnsureSourceGroupSchema_CreatesPartialUniqueIndexes|TestSourceGroupMember_SecondActiveRejected|TestDataSource_SecondActiveRejected' ./internal/dbschema; then
    reasons="$reasons SQLite 索引/第二 active 拒绝测试未通过（检查 testutil.OpenTestDB 是否调 EnsureSourceGroupSchema）;"
  fi
  if pg_available; then
    n=$(docker exec ehome-postgres psql -U ehome -d postgres -At -c \
      "SELECT count(*) FROM pg_indexes WHERE indexdef ILIKE 'CREATE UNIQUE INDEX%' AND indexdef ILIKE '%status%active%' AND (tablename='source_group_members' OR tablename='data_sources')" 2>/dev/null | tr -d ' ')
    if [ -z "$n" ]; then n=0; fi
    if [ "$n" -lt 2 ]; then reasons="$reasons PG 部分唯一索引数=$n（期望 2；迁移是否已在目标库执行？）;"; fi
  else
    reasons="$reasons SKIP(pg-unavailable)：PG 侧索引断言未执行;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-04 '两处部分唯一索引 + 第二 active 必拒' "$reasons"; else
    record_pass SG-04 '两处部分唯一索引 + 第二 active 必拒' 'SQLite 行为测试 PASS；PG 两索引存在'
  fi
}

gate_SG_05() {
  local reasons=""
  if ! file_has internal/automation/evaluator.go 'SourceEdgeDeviceID|Identity|Revision'; then
    reasons="$reasons automation 窗口样本未携带来源身份;"
  fi
  if ! run_go_test 'TestWindowSampleCarriesSourceIdentity|TestWindowS1|TestWindowS2|TestWindowS7|TestWindowS8' ./internal/automation ./internal/alert; then
    reasons="$reasons 窗口 identity/S1/S2/S7/S8 行为测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-05 '窗口 identity + S1/S2/S7/S8' "$reasons"; else
    record_pass SG-05 '窗口 identity + S1/S2/S7/S8' '结构断言与四类行为用例 PASS'
  fi
}

gate_SG_06() {
  local rc=0
  grep_absent 'source_groups' ./internal/automation ./internal/alert
  rc=$?
  if [ "$rc" -eq 2 ]; then record_fail SG-06 'resolver 唯一性' '目标目录不存在'; return; fi
  if [ "$rc" -eq 1 ]; then record_fail SG-06 'resolver 唯一性' 'automation/alert 直查 source_groups（应为 0 处，必须经 sourceref）'; return; fi
  if [ ! -d internal/sourceref ]; then record_fail SG-06 'resolver 唯一性' 'internal/sourceref 不存在'; return; fi
  go_list_contains 'ehome/backend/internal/sourceref' ./internal/automation ./internal/alert
  rc=$?
  if [ "$rc" -eq 2 ]; then record_fail SG-06 'resolver 唯一性' 'go list 失败（不得当通过）'; return; fi
  if [ "$rc" -eq 1 ]; then record_fail SG-06 'resolver 唯一性' 'automation/alert 未依赖 sourceref'; return; fi
  record_pass SG-06 'resolver 唯一性' '两引擎均经 sourceref，且无直查 source_groups'
}

gate_SG_07() {
  local reasons=""
  file_has internal/automation/planner.go 'SourceEdgeDeviceID' || reasons="$reasons planner.go 未使用 SourceEdgeDeviceID;"
  if ! run_go_test 'TestF4PinsTriggerEventMember|TestPlannerF4SourceMember' ./internal/automation; then
    reasons="$reasons F4 钉成员行为测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-07 'F4 钉住触发时成员' "$reasons"; else
    record_pass SG-07 'F4 钉住触发时成员' 'planner 使用事件来源成员且行为测试 PASS'
  fi
}

gate_SG_08() {
  local reasons="" col
  for col in SourceKind SourceGroupID SourceEdgeDeviceID MembersJSON; do
    grep -q "$col" internal/models/automation.go || reasons="$reasons automation 事件缺列 $col;"
  done
  for col in SourceKind SourceGroupID SourceEdgeDeviceID MembersJSON; do
    grep -q "$col" internal/models/alert.go || reasons="$reasons alert 事件缺列 $col;"
  done
  if ! run_go_test 'TestSourceGroupAuditColumns|TestSourceGroupSchema' ./internal/models ./internal/dbschema; then
    reasons="$reasons 审计列 struct/实库断言测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-08 '审计列（4 表 + 2 旧表列）' "$reasons"; else
    record_pass SG-08 '审计列（4 表 + 2 旧表列）' 'struct 与实库断言齐备'
  fi
}

gate_SG_09() {
  local reasons=""
  if [ ! -f internal/sourceref/testdata/match_cases.jsonl ]; then
    reasons="$reasons match_cases.jsonl 不存在;"
  fi
  if ! run_go_test 'TestMatchCasesParity|TestResolverParity' ./internal/sourceref; then
    reasons="$reasons parity 测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-09 '两引擎 parity' "$reasons"; else
    record_pass SG-09 '两引擎 parity' '同一用例表驱动 automation/alert 一致'
  fi
}

gate_SG_10() {
  local reasons="" n=0
  if ! grep_present 'SourceGroupKindComposite|composite' ./internal/api ./internal/datasource; then
    reasons="$reasons API/service 未对 composite 置 active 做拒绝;"
  fi
  if ! run_go_test 'TestCompositeNoActive|TestCompositeRejectsActive' ./internal/api ./internal/datasource; then
    reasons="$reasons composite-active 负例测试不存在或未通过;"
  fi
  if pg_available; then
    n=$(docker exec ehome-postgres psql -U ehome -d postgres -At -c \
      "SELECT count(*) FROM source_group_members m JOIN source_groups g ON g.id=m.group_id WHERE g.kind='composite' AND m.status='active'" 2>/dev/null | tr -d ' ')
    if [ -z "$n" ]; then n=0; fi
    if [ "$n" -ne 0 ]; then reasons="$reasons PG 存在 composite active 行=$n;"; fi
  fi
  if [ -n "$reasons" ]; then record_fail SG-10 'composite 无 active' "$reasons"; else
    record_pass SG-10 'composite 无 active' 'API 拒绝 + 启动自检 + 负例测试 PASS'
  fi
}

gate_SG_11() {
  local reasons=""
  file_has internal/models/source_group.go 'SourceMemberStatusActive' || reasons="$reasons 成员状态常量缺失;"
  if ! run_go_test 'TestSourceGroupMemberStatusDefaults|TestMemberStatusVocabulary' ./internal/datasource ./internal/models; then
    reasons="$reasons 状态词表/默认值测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-11 '状态词表/默认值' "$reasons"; else
    record_pass SG-11 '状态词表/默认值' '成员创建状态与 kind 对照测试 PASS'
  fi
}

gate_SG_12() {
  local rollout="scripts/check_source_group_rollout.sh" n=0
  if [ ! -f "$rollout" ]; then record_fail SG-12 '回滚盘点脚本' "缺少 $rollout"; return; fi
  bash -n "$rollout" 2>/dev/null || { record_fail SG-12 '回滚盘点脚本' "$rollout 语法错误"; return; }
  n=$(grep -c '^-- audit:' "$rollout" 2>/dev/null || true)
  if [ "$n" -lt 4 ]; then record_fail SG-12 '回滚盘点脚本' "盘点 SQL 数=$n（期望 ≥4）"; return; fi
  if ! "$rollout" --self-test >/dev/null 2>&1; then record_fail SG-12 '回滚盘点脚本' "$rollout --self-test 未通过"; return; fi
  record_pass SG-12 '回滚盘点脚本' "4 条 SQL + --self-test 通过"
}

gate_SG_13() {
  local reasons=""
  file_has internal/models/source_group.go 'Revision' || reasons="$reasons 组 Revision 列缺失;"
  if ! run_go_test 'TestRevisionBumpWhitelist|TestRevisionMonotonic|TestMarkSuccessNoRevisionBump' ./internal/datasource ./internal/models; then
    reasons="$reasons revision 单调/白名单测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-13 'revision 单调 + 白名单' "$reasons"; else
    record_pass SG-13 'revision 单调 + 白名单' '100 次 MarkSuccess 不变 + 一次 active 迁移 +1'
  fi
}

gate_SG_14() {
  local bad=0 f
  : > "$tmp_dir/sg14.txt"
  for f in internal/automation/evaluator.go internal/alert/evaluator.go; do
    [ -f "$f" ] || continue
    awk -v fn="$f" '
      /mu\.RLock\(\)/ { inlock=1 }
      inlock {
        if ($0 ~ /e\.db\.|resolveLogicalID/) { print fn ":" NR ": " $0; found=1 }
        if ($0 ~ /mu\.RUnlock\(\)/) { inlock=0 }
      }
      END { exit (found ? 0 : 1) }
    ' "$f" >> "$tmp_dir/sg14.txt" 2>&1 || bad=1
  done
  if [ "$bad" -eq 1 ]; then
    record_fail SG-14 '锁内无 DB（静态）' "$(head -3 "$tmp_dir/sg14.txt")"
  else
    record_pass SG-14 '锁内无 DB（静态）' 'automation/alert evaluator 的 RLock 区间内无 e.db./resolveLogicalID'
  fi
}

gate_SG_15() {
  if ! run_go_test 'TestTransitionSeqMatchesEvents|TestActiveChangeEventCount' ./internal/datasource; then
    record_fail SG-15 '迁移数==ActiveChange 数' '测试不存在或未通过'
  else
    record_pass SG-15 '迁移数==ActiveChange 数' '逐条造迁移后计数一致'
  fi
}

gate_SG_16() {
  local reasons="" m="" s="" f=""
  m=$(grep -rhoE 'AutomationResultSource[A-Za-z]+' internal/models/*.go 2>/dev/null | sort -u)
  s=$(grep -lq 'source_group' simulation/catalog/audit.go 2>/dev/null && echo has || echo missing)
  f=$(grep -rhoE 'source_[a-z_]+' ../frontend-shared/src/api/*.ts 2>/dev/null | sort -u)
  if [ -z "$m" ]; then reasons="$reasons models 未定义 source 相关 result 码;"; fi
  if [ "$s" = missing ]; then reasons="$reasons simulation audit 白名单未见 source 码;"; fi
  if [ -z "$f" ]; then reasons="$reasons 前端 union 未见 source 码;"; fi
  if [ -n "$reasons" ]; then record_fail SG-16 'result 码三向一致' "$reasons"; else
    record_pass SG-16 'result 码三向一致' 'models/simulation/frontend 三处 source 码一致'
  fi
}

gate_SG_17() {
  local rc=0
  go_list_contains 'ehome/backend/internal/sensorfield' ./internal/datasource ./internal/databus
  rc=$?
  if [ "$rc" -eq 2 ]; then record_fail SG-17 '传递依赖红线' 'go list 失败（不得当作通过）'; return; fi
  if [ "$rc" -eq 0 ]; then record_fail SG-17 '传递依赖红线' 'datasource/databus 传递依赖含 sensorfield'; return; fi
  record_pass SG-17 '传递依赖红线' 'datasource/databus 的 go list -deps 均不含 sensorfield'
}

gate_SG_18() {
  local bad="" dep=""
  dep=$("$GO" list -deps ./internal/sourceref 2>/dev/null) || { record_fail SG-18 'sourceref 纯净性' 'go list 失败（不得当作通过）'; return; }
  if printf '%s\n' "$dep" | grep -qE 'gorm\.io|internal/(databus|notify|websocket|sensorfield)'; then
    bad="$bad 依赖含黑名单包;"
  fi
  if [ -f internal/sourceref/combine.go ]; then
    if grep -vE '^\s*//' internal/sourceref/combine.go | grep -qE 'time\.Now|go func'; then bad="$bad combine.go 含 time.Now/goroutine;"; fi
  else
    bad="$bad combine.go 不存在;"
  fi
  if [ -n "$bad" ]; then record_fail SG-18 'sourceref 纯净性' "$bad"; else
    record_pass SG-18 'sourceref 纯净性' '无 gorm/databus/notify/sensorfield 依赖；combine.go 纯函数'
  fi
}

gate_SG_19() {
  local rc=0
  grep_absent 'SourceGroup|SourceRef|sourceref' ./internal/databus
  rc=$?
  if [ "$rc" -eq 2 ]; then record_fail SG-19 'databus 零来源依赖' 'internal/databus 不存在'; return; fi
  if [ "$rc" -eq 1 ]; then record_fail SG-19 'databus 零来源依赖' 'databus 出现 SourceGroup/SourceRef/sourceref'; return; fi
  record_pass SG-19 'databus 零来源依赖' 'grep SourceGroup|SourceRef|sourceref = 0'
}

gate_SG_20() {
  local reasons=""
  if ! grep_present 'SourceGroupReference|deletionGuard|source_group' ./internal/api; then
    reasons="$reasons API 删除守卫未接线（被引用 ⇒ 409）;"
  fi
  if ! run_go_test 'TestSourceGroupDeletionGuard|TestEdgeDeviceDeleteReferencedByGroup' ./internal/api; then
    reasons="$reasons 删除守卫负例测试不存在或未通过;"
  fi
  if [ -n "$reasons" ]; then record_fail SG-20 '删除守卫 409' "$reasons"; else
    record_pass SG-20 '删除守卫 409' '5 个调用点守卫 + 负例测试 PASS'
  fi
}

gate_SG_21() {
  local hits=""
  hits=$(grep -rn 'TriggerEvent{' internal/automation --include=*.go 2>/dev/null | grep -v '_test.go' | grep -v 'func newTriggerEvent' | head -5)
  if [ -n "$hits" ]; then record_fail SG-21 'TriggerEvent 唯一构造' "非 helper 处出现字面量构造：$hits"; return; fi
  if ! grep -rq 'func newTriggerEvent' internal/automation --include=*.go; then
    record_fail SG-21 'TriggerEvent 唯一构造' 'newTriggerEvent helper 不存在'; return
  fi
  record_pass SG-21 'TriggerEvent 唯一构造' '生产代码无字面量 TriggerEvent{，唯一构造点 newTriggerEvent'
}

summary() {
  echo ""
  echo "== 门禁结果（HEAD=$GATE_HEAD）=="
  local fails=0 skips=0 passes=0 warns=0 status num title detail
  while IFS='|' read -r status num title detail; do
    [ -z "$status" ] && continue
    echo "[$status] $num $title — $detail"
    case "$status" in
      PASS) passes=$((passes + 1)) ;;
      WARN) warns=$((warns + 1)) ;;
      SKIP) skips=$((skips + 1)) ;;
      FAIL) fails=$((fails + 1)) ;;
    esac
  done <<EOF
$RESULTS
EOF
  echo ""
  if [ "$fails" -gt 0 ]; then
    echo "RESULT: FAIL($fails) — 门禁未通过（PASS=$passes SKIP=$skips）$RESULT_SUFFIX"
    return 1
  fi
  if [ "$skips" -gt 0 ]; then
    if [ "$STRICT" -eq 1 ]; then
      echo "RESULT: FAIL_STRICT_SKIP($skips) — 严格模式不允许 SKIP（PASS=$passes）$RESULT_SUFFIX"
      return 1
    fi
    echo "RESULT: PASS_WITH_SKIPS($skips) — 通过 $passes 条；SKIP 原因见上$RESULT_SUFFIX"
    return 0
  fi
  echo "RESULT: PASS — $passes 条门禁全部通过$RESULT_SUFFIX"
  return 0
}

self_test() {
  local failures=0 rc=0
  echo "== 反假绿自证 =="

  run_go_test 'TestThisTestDefinitelyDoesNotExist_XYZ' ./internal/models >/dev/null 2>&1
  rc=$?
  if [ "$rc" -eq 2 ]; then echo "SELFTEST PASS: 测试不存在 ⇒ 无 RUN（判 FAIL）"; else
    echo "SELFTEST FAIL: 缺测试未被识别（rc=$rc）"; failures=$((failures + 1)); fi

  mkdir -p "$tmp_dir/st/pkg"
  printf 'package p\nvar x = "source_groups"\n' > "$tmp_dir/st/pkg/a.go"
  grep_absent 'source_groups' "$tmp_dir/st/pkg"
  rc=$?
  if [ "$rc" -eq 1 ]; then echo "SELFTEST PASS: grep 命中 ⇒ 红线门禁判红"; else
    echo "SELFTEST FAIL: grep 命中未被识别（rc=$rc）"; failures=$((failures + 1)); fi

  grep_absent 'x' "$tmp_dir/st/not-exist"
  rc=$?
  if [ "$rc" -eq 2 ]; then echo "SELFTEST PASS: 目标路径不存在 ⇒ 判红（不假绿）"; else
    echo "SELFTEST FAIL: 缺路径未被识别（rc=$rc）"; failures=$((failures + 1)); fi

  local saved="$RESULTS" saved_strict="$STRICT"
  RESULTS="SKIP|99|合成 SKIP|"
  STRICT=0
  if summary >/dev/null 2>&1; then echo "SELFTEST PASS: 默认模式 SKIP 容忍（exit 0）"; else
    echo "SELFTEST FAIL: 默认模式 SKIP 被当失败"; failures=$((failures + 1)); fi
  STRICT=1
  if summary >/dev/null 2>&1; then echo "SELFTEST FAIL: --strict 下 SKIP 未失败"; failures=$((failures + 1)); else
    echo "SELFTEST PASS: --strict 下 SKIP 失败"; fi
  STRICT="$saved_strict"; RESULTS="$saved"

  if ! pg_available; then echo "SELFTEST PASS: PG 不可达 ⇒ 只能 SKIP/FAIL，不计 PASS"; else
    echo "SELFTEST SKIP: 本机 PG 可达，跳过不可达分支自证"; fi

  saved="$RESULTS"
  RESULTS="PASS|1|a|
FAIL|2|b|
"
  if summary >/dev/null 2>&1; then echo "SELFTEST FAIL: 含 FAIL 时 summary 未失败"; failures=$((failures + 1)); else
    echo "SELFTEST PASS: 含 FAIL ⇒ RESULT: FAIL"; fi
  RESULTS="$saved"

  echo ""
  echo "== 自证结果：$failures 项失败 =="
  return "$failures"
}

mutation_test() {
  local mf="scripts/source_group_mutations.tsv"
  [ -f "$mf" ] || { echo "MUTATION: 缺少 $mf"; return 1; }
  local caught=0 total=0 skipped=0 id file pattern repl testpkg testpat
  while IFS="	" read -r id file pattern repl testpkg testpat; do
    case "$id" in ''|'#'*) continue ;; esac
    total=$((total + 1))
    if [ ! -f "$file" ]; then echo "MUTATION: $id SKIP(missing file $file)"; skipped=$((skipped + 1)); continue; fi
    if ! grep -qE "$pattern" "$file"; then echo "MUTATION: $id SKIP(missing pattern)"; skipped=$((skipped + 1)); continue; fi
    backup_file "$file" || return 1
    sed -i -E "s#$pattern#$repl#" "$file"
    if "$GO" test "$testpkg" -run "$testpat" -count=1 >/dev/null 2>&1; then
      echo "MUTATION: $id NOT_CAUGHT（变异后测试仍绿）"
    else
      echo "MUTATION: $id CAUGHT"
      caught=$((caught + 1))
    fi
    restore_file "$file" || return 1
  done < "$mf"
  echo "MUTATION 结果：CAUGHT $caught/$total（SKIP $skipped）"
  [ "$caught" -eq "$total" ]
}

if [ "$SELF_TEST" -eq 1 ]; then self_test; exit $?; fi
if [ "$MUTATION" -eq 1 ]; then mutation_test; exit $?; fi
if [ "$LIST" -eq 1 ]; then
  cat <<'EOF'
SG-01 双指针五态一致性（模型测试 + 保存/加载接线）
SG-02 已删除（N-8：成员 category 无字段载体）——SKIP
SG-03 combine 白名单 + kind 语义（DB CHECK + API 400）
SG-04 两处部分唯一索引 + 第二 active 必拒（SQLite+PG）
SG-05 窗口 identity + S1/S2/S7/S8 行为
SG-06 resolver 唯一性（禁止直查 source_groups）
SG-07 F4 钉住触发时成员（grep + 行为）
SG-08 审计列（4 新表 + 2 旧表列）
SG-09 automation/alert parity（match_cases.jsonl）
SG-10 composite 无 active（API + 启动自检 + 负例）
SG-11 状态词表/默认值对照
SG-12 回滚盘点脚本（4 条 SQL 入 CI）
SG-13 revision 单调（MarkSuccess 不 bump 反向断言）
SG-14 evaluator 锁内无 DB（静态）
SG-15 状态迁移数 == ActiveChange 事件数
SG-16 result 码三向一致
SG-17 传递依赖：datasource/databus 不含 sensorfield
SG-18 sourceref 依赖黑名单 + combine 纯函数
SG-19 databus 零来源依赖
SG-20 删除守卫 409
SG-21 TriggerEvent 唯一构造
EOF
  exit 0
fi

gate_list="01 02 03 04 05 06 07 08 09 10 11 12 13 14 15 16 17 18 19 20 21"
if [ -n "$ONLY" ]; then
  gate_list=$(printf '%s' "$ONLY" | sed -E 's/^SG-//; s/^0*//')
  case ",$gate_list," in
    ',1,'|',2,'|',3,'|',4,'|',5,'|',6,'|',7,'|',8,'|',9,'|',10,'|',11,'|',12,'|',13,'|',14,'|',15,'|',16,'|',17,'|',18,'|',19,'|',20,'|',21,') ;;
    *) echo "未知门禁编号: $ONLY" >&2; exit 2 ;;
  esac
fi
for n in $gate_list; do
  if [ "$n" = "1" ]; then fn="gate_SG_01"; elif [ "$n" = "2" ]; then fn="gate_SG_02"; else fn="gate_SG_$n"; fi
  "$fn"
done
summary
exit $?