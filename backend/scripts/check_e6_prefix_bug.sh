#!/usr/bin/env bash
# check_e6_prefix_bug.sh —— 门禁：证明「F4 在逻辑 ID 分歧下错源」这个 bug 在基线**确实复现**，
# 修复后**确实消失**。方案 v0.3 §7.2 F-4 / F-6 的放行条件之一。
#
# 为什么需要这个脚本（而不是只留一个 e2e 用例）：
#   「修复后可证明」与「修复前可复现」是两件事。只测后者（绿灯）无法区分：
#     (a) bug 真的被修好了；
#     (b) 测试压根没摸到 bug（例如夹具里逻辑 ID 恰好不分歧）。
#   本脚本用**同一份夹具**在基线提交上跑，要求它必须红 —— 基线不红 = 测试无效。
#
# 用法：
#   ./scripts/check_e6_prefix_bug.sh            # 全流程（会建临时 worktree）
#   ./scripts/check_e6_prefix_bug.sh --self-test # 只验证脚本自身逻辑（不跑 go）
#
# 判据：
#   基线 worktree 上 E6 用例 => 必须 FAIL（证明 bug 存在且用例能摸到它）
#   当前分支    上 E6 用例 => 必须 PASS（证明修复有效）
#   两者都满足 => PASS；否则 FAIL。
set -uo pipefail

BACKEND_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REPO_ROOT="$(cd "$BACKEND_DIR/.." && pwd)"
BASE_COMMIT="7bc63e3f"
TEST_NAME="TestE6_F4PinsTriggerMemberOnLogicalIDDivergence"
SELF_TEST=0
[ "${1:-}" = "--self-test" ] && SELF_TEST=1

fail() { echo "[FAIL] $*"; exit 1; }
pass() { echo "[PASS] $*"; }

echo "=== E6 基线红对照（F4 逻辑 ID 分歧错源）==="
echo "repo=$REPO_ROOT base=$BASE_COMMIT test=$TEST_NAME"

# ── 1. 夹具自检：E6 用例必须真实制造「逻辑 ID 分歧」────────────────────────────
# 若夹具里 edge_devices.logical_device_id 与 edge_devices.id 恰好相同，
# 那么「用逻辑 ID 前缀找设备」与「用真实设备 ID 找设备」得到同一答案 ——
# bug 不会被触发，用例变成永远绿的空转。这是本门禁存在的第二个理由。
check_fixture_diverges() {
  local f="$1"
  [ -f "$f" ] || return 1
  # 夹具必须显式让 logical_device_id != edge_device_id
  grep -qE 'LogicalDeviceID.*!=|logical.*diverg|分歧' "$f" && return 0
  return 1
}

# ── 2. 在指定 commit 上跑 E6，返回退出码 ─────────────────────────────────────
run_e6_at() {
  local dir="$1"
  ( cd "$dir/backend" && \
    export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH && \
    export GOCACHE="${GOCACHE:-/tmp/eh-e6-cache}" && \
    go test -tags=e2e ./internal/e2e/ -run "^$TEST_NAME$" -count=1 2>&1 | tail -20 )
}

if [ "$SELF_TEST" = "1" ]; then
  check_fixture_diverges "$BACKEND_DIR/internal/e2e/source_group_e2e_test.go" \
    && pass "self-test: 夹具自检能找到分歧标记" \
    || fail "self-test: 夹具自检找不到分歧标记"
  exit 0
fi

# ── 3. 当前分支：必须 PASS ───────────────────────────────────────────────────
echo
echo "--- [1/3] 当前分支跑 $TEST_NAME（期望 PASS）---"
cur_out=$(run_e6_at "$REPO_ROOT")
cur_rc=$?
echo "$cur_out"
if [ $cur_rc -ne 0 ]; then
  fail "当前分支上 $TEST_NAME 失败 —— 修复无效或用例本身坏了"
fi
pass "当前分支：$TEST_NAME PASS"

# ── 4. 基线 worktree：必须 FAIL ──────────────────────────────────────────────
echo
echo "--- [2/3] 基线 $BASE_COMMIT 建临时 worktree（期望 FAIL）---"
TMP_WT="$(mktemp -d)/e6base"
if ! git -C "$REPO_ROOT" worktree add -q --detach "$TMP_WT" "$BASE_COMMIT" 2>/dev/null; then
  fail "无法创建基线 worktree（$BASE_COMMIT 不存在？）"
fi
cleanup() { git -C "$REPO_ROOT" worktree remove --force "$TMP_WT" >/dev/null 2>&1 || true; }
trap cleanup EXIT

# 基线还没有 source_group_e2e_test.go（本批新增）⇒ 把 E6 用例及其夹具拷过去。
# 这是本门禁的**关键设计**：用当前写的用例去打旧代码，才叫「证明 bug 存在」。
cp "$BACKEND_DIR/internal/e2e/source_group_e2e_test.go" "$TMP_WT/backend/internal/e2e/" 2>/dev/null \
  || fail "拷贝 E6 用例到基线失败"

if ! check_fixture_diverges "$TMP_WT/backend/internal/e2e/source_group_e2e_test.go"; then
  fail "基线夹具未制造逻辑 ID 分歧 —— 用例在基线上也不会红，无法证明 bug"
fi

base_out=$(run_e6_at "$TMP_WT")
base_rc=$?
echo "$base_out"
if [ $base_rc -eq 0 ]; then
  # 基线也绿 = 用例没摸到 bug（或 bug 已在上游被修），门禁无效。
  fail "基线 $BASE_COMMIT 上 $TEST_NAME **也通过了** ⇒ 用例没有摸到 F4 错源 bug，此用例无证明力"
fi
pass "基线：$TEST_NAME FAIL（bug 确实存在且用例能摸到）"

# ── 5. 结论 ────────────────────────────────────────────────────────────────
echo
echo "=== 结论 ==="
echo "  基线 $BASE_COMMIT : FAIL（bug 存在，用例有效）"
echo "  当前分支          : PASS（修复有效）"
echo "RESULT: PASS — E6 基线红/修复绿 双向证明成立"
exit 0
