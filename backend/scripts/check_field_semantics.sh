#!/usr/bin/env bash
# check_field_semantics.sh —— 数据字段解析层方案 §12.3 门禁 1..11（D6 交付物）。
#
# 定位：「门禁的价值在于能变红」。每个门禁区分 PASS / FAIL / SKIP：
#   - SKIP 只用于「依赖尚未落地」（alias 配置/测试、bench 基线、DB 不可达）；
#   - 「目标不存在」一律 FAIL（如 bench 函数不存在、go list 失败、无测试用例）；
#   - 末尾 RESULT：PASS / PASS_WITH_SKIPS(n) / FAIL(n) / FAIL_STRICT_SKIP(n)。
#   - --strict：SKIP 也判失败（D6 完成后 CI 应切到严格模式）。
#
# 用法：
#   ./scripts/check_field_semantics.sh                 # 默认：SKIP 容忍
#   ./scripts/check_field_semantics.sh --strict        # 发布门禁（SKIP 也红）
#   ./scripts/check_field_semantics.sh --self-test     # 反假绿自证
#   ./scripts/check_field_semantics.sh --only 8        # 只跑第 8 条
#   ./scripts/check_field_semantics.sh --mutation      # 打开门禁 9 的变异自证
#   ./scripts/check_field_semantics.sh --record-bench-baseline
#     录两份参考基线：bench_baseline_prod.txt（10a 生产链）/ bench_baseline_layer.txt（10b 新层成本）。
#     main d406dc2d 无集成层 ⇒ 无法在 main 录；在首个含 literal 快速路径的 commit 上录，文件头记录 commit 与局限。
#
# 纪律：只读仓库/DB；门禁 9 的变异临时改被测源文件，必须备份-改-跑-还原并核对 md5。
set -uo pipefail   # 不用 -e（逐门禁收集结果）；pipefail 防「管道失败即绿」。

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
BACKEND_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
REPO_DIR=$(cd "$BACKEND_DIR/.." && pwd)

GO=$(printenv GO 2>/dev/null || true)
if [ -z "$GO" ]; then GO="go"; fi
if ! command -v "$GO" >/dev/null 2>&1; then
  # 工具链可能只存在于主仓 .tools（git worktree 里没有 .tools/）。
  # 先试 worktree 根，再用 git-common-dir 推出主仓根，最后试规范路径；都没有则明确失败。
  for cand in "$REPO_DIR/.tools/gotool/bin/go" "/mnt/storage/WorkSpace/EHome/.tools/gotool/bin/go"; do
    if [ -x "$cand" ]; then GO="$cand"; break; fi
  done
  if ! command -v "$GO" >/dev/null 2>&1 && [ ! -x "$GO" ]; then
    common=$(git -C "$REPO_DIR" rev-parse --path-format=absolute --git-common-dir 2>/dev/null || true)
    if [ -n "$common" ]; then
      cand=$(dirname "$common")/.tools/gotool/bin/go
      if [ -x "$cand" ]; then GO="$cand"; fi
    fi
  fi
fi
if ! command -v "$GO" >/dev/null 2>&1 && [ ! -x "$GO" ]; then
  echo "FATAL: 找不到 go 工具链（PATH 无 go；也未见 .tools/gotool/bin/go）。" >&2
  echo "       请 export PATH=<repo>/.tools/gotool/bin:$PATH 后重跑。" >&2
  exit 2
fi
BENCH_BASELINE_PROD="$BACKEND_DIR/internal/sensorfield/testdata/bench_baseline_prod.txt"
BENCH_BASELINE_LAYER="$BACKEND_DIR/internal/sensorfield/testdata/bench_baseline_layer.txt"
BENCH_BASELINE_E2E="$BACKEND_DIR/internal/sensorfield/testdata/bench_baseline_e2e.txt"
MUTATION_MANIFEST="$BACKEND_DIR/scripts/field_semantics_mutations.tsv"

STRICT=0
ONLY=""
SELF_TEST=0
RECORD_BENCH=0
MUTATION=0

while [ $# -gt 0 ]; do
  case "$1" in
    --strict) STRICT=1 ;;
    --self-test) SELF_TEST=1 ;;
    --mutation) MUTATION=1 ;;
    --record-bench-baseline) RECORD_BENCH=1 ;;
    --only) shift; ONLY="$1" ;;
    -h|--help) sed -n '2,22p' "$0"; exit 0 ;;
    *) echo "未知参数: $1" >&2; exit 2 ;;
  esac
  shift
done

# RESULTS 是「每行一条 PASS|N|title|detail」的累加串（不用数组，避免 shell 参数展开）。
RESULTS=""
append_result() { RESULTS="$RESULTS$1|$2|$3|$4
"; }
record_pass() { append_result PASS "$1" "$2" "$3"; }
record_fail() { append_result FAIL "$1" "$2" "$3"; }
record_skip() { append_result SKIP "$1" "$2" "$3"; }
record_warn() { append_result WARN "$1" "$2" "$3"; }

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

# ── 测量前提（Lead 2026-10-21 纪律，D1 验收要求）────────────────────────────
# 门禁/基准必须在**干净工作树**上跑，并记录 HEAD：impl-core 编辑 selector.go 的中间态
# 曾让同一门禁三次给出互相矛盾的结果（no tests to run / 计数非 0 / 通过）。
#   · 始终打印 HEAD 与脏文件数；
#   · dirty 时打印 WARN，并在 RESULT 行标注 DIRTY；
#   · 基准类门禁（#10a-1a 确定性、#10a-2 端到端）dirty 时降级 SKIP(dirty-worktree)，
#     不用别人的中间态判 PASS/FAIL（1b 仅记录，不受影响）。
GATE_HEAD=$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || echo unknown)
# 只统计**代码面**（backend/、.github/）的未提交改动：.analysis/ 下的评审/分析产物
# 不参与任何编译或基准，把它们算进 dirty 会让"别人写文档期间基准门禁一律 SKIP"。
# 反过来，backend/ 内任何未提交改动（含 _test.go 中间态）都必须让基准类门禁 SKIP。
GATE_DIRTY_COUNT=$(git -C "$REPO_DIR" status --porcelain -- backend .github 2>/dev/null | wc -l | tr -d ' ')
GATE_DIRTY=0
if [ "$GATE_DIRTY_COUNT" -gt 0 ]; then
  GATE_DIRTY=1
  echo "WARN: dirty worktree, results may be flaky (HEAD=$GATE_HEAD, $GATE_DIRTY_COUNT files)"
fi
echo "HEAD: $GATE_HEAD (dirty files: $GATE_DIRTY_COUNT)"
DIRTY_MARK=""
if [ "$GATE_DIRTY" -eq 1 ]; then DIRTY_MARK=", DIRTY=$GATE_DIRTY_COUNT"; fi
RESULT_SUFFIX=" (HEAD=$GATE_HEAD$DIRTY_MARK)"


# Go 构建缓存：无 GOCACHE 或默认缓存不可写时，退回临时目录（沙箱/CI 只读 $HOME 场景）。
if [ -z "$(printenv GOCACHE 2>/dev/null || true)" ]; then
  export GOCACHE="$tmp_dir/gocache"
  mkdir -p "$GOCACHE"
fi

run_in_backend() {
  local out="$1" err="$2"; shift 2
  ( cd "$BACKEND_DIR" && "$@" ) >"$out" 2>"$err"
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 1：数据面零 import。go list 失败必须 FAIL（旧写法会「失败即绿」）。
# ─────────────────────────────────────────────────────────────────────────────
gate1() {
  local out="$tmp_dir/g1.out" err="$tmp_dir/g1.err"
  if ! run_in_backend "$out" "$err" "$GO" list -deps ./internal/databus; then
    record_fail 1 "数据面零 import" "go list -deps ./internal/databus 失败（不是无命中）：$(head -1 "$err")"
    return
  fi
  if grep -q '^ehome/backend/internal/sensorfield$' "$out"; then
    record_fail 1 "数据面零 import" "databus 传递闭包包含 sensorfield（违反红线 2）"
  else
    record_pass 1 "数据面零 import" "闭包 $(wc -l < "$out") 行；sensorfield 行锚定命中 0"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 2：领域包零 internal 依赖（与冻结契约 doc.go 红线 2 等价）
# ^ehome/backend/ 只允许 pkg/logger、pkg/metrics（自身除外）。
# ─────────────────────────────────────────────────────────────────────────────
gate2() {
  local out="$tmp_dir/g2.out" err="$tmp_dir/g2.err"
  if ! run_in_backend "$out" "$err" "$GO" list -deps ./internal/sensorfield; then
    record_fail 2 "领域包零 internal 依赖" "go list -deps ./internal/sensorfield 失败（包不可编译/不存在）：$(head -1 "$err")"
    return
  fi
  local bad="$tmp_dir/g2.bad"
  grep '^ehome/backend/' "$out" \
    | grep -vx 'ehome/backend/internal/sensorfield' \
    | grep -vx 'ehome/backend/pkg/logger' \
    | grep -vx 'ehome/backend/pkg/metrics' > "$bad"
  if [ -s "$bad" ]; then
    record_fail 2 "领域包零 internal 依赖" "不允许的依赖：$(tr '\n' ' ' < "$bad")"
  else
    record_pass 2 "领域包零 internal 依赖" "ehome/backend/ 仅自身/pkgs 白名单（共 $(grep -c '^ehome/backend/' "$out") 行）"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 3：包级默认拒绝。允许 import sensorfield 的包 ONLY：
# internal/automation、internal/alert、internal/api、cmd/server；其余出现即红。
# ─────────────────────────────────────────────────────────────────────────────
gate3() {
  local importers="$tmp_dir/g3.importers"
  # 只扫生产布局（internal/、cmd/）：simulation/、scripts/、testutil/ 不属于「包级默认
  # 拒绝」对象（它们是测试/工具代码；仿真场景依赖领域类型是设计允许项）。
  grep -rl --include='*.go' -F '"ehome/backend/internal/sensorfield"' \
    "$BACKEND_DIR/internal" "$BACKEND_DIR/cmd" 2>/dev/null \
    | while IFS= read -r f; do
        rel=$(printf '%s' "$f" | sed "s#^$BACKEND_DIR/##")
        dirname "$rel"
      done | sort -u > "$importers"
  if [ ! -s "$importers" ]; then
    record_pass 3 "包级默认拒绝" "当前 0 个包 import（尚未接线；默认拒绝生效）"
    return
  fi
  local bad="$tmp_dir/g3.bad"
  : > "$bad"
  while IFS= read -r dir; do
    case "$dir" in
      internal/automation|internal/alert|internal/api|cmd/server) ;;
      *) printf '%s\n' "$dir" >> "$bad" ;;
    esac
  done < "$importers"
  if [ -s "$bad" ]; then
    record_fail 3 "包级默认拒绝" "越界包：$(sort -u "$bad" | tr '\n' ' ')"
  else
    record_pass 3 "包级默认拒绝" "import 包：$(sort -u "$importers" | tr '\n' ' ')"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 4/5：AST 判据（scripts/fieldsem）。grep 实测只抓到 13/104 字面量，
# 必须 go/ast；不写死分母，数字由脚本推导并打印。
# ─────────────────────────────────────────────────────────────────────────────
gate4() {
  local out="$tmp_dir/g4.out" err="$tmp_dir/g4.err" rc=0
  run_in_backend "$out" "$err" "$GO" run ./scripts/fieldsem -mode decl || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -q '^VIOLATION=' "$out"; then
      record_fail 4 "声明自洽（AST）" "$(grep -c '^VIOLATION=' "$out") 条违例；首条：$(grep -m1 '^VIOLATION=' "$out")"
    else
      record_fail 4 "声明自洽（AST）" "fieldsem 执行失败 rc=$rc：$(head -1 "$err")"
    fi
    return
  fi
  record_pass 4 "声明自洽（AST）" "声明 $(sed -n 's/^DECLARED=//p' "$out") 个 ⊆ 可达（字面量 $(sed -n 's/^REACHABLE_LITERALS=//p' "$out") ∪ 族 $(sed -n 's/^FAMILY_PATTERNS=//p' "$out") ∪ 拼接后缀 $(sed -n 's/^CONCAT_SUFFIXES=//p' "$out")）；动作参数名 $(sed -n 's/^ACTION_PARAM_NAMES=//p' "$out") 个仅报告"
}

gate5() {
  local out="$tmp_dir/g5.out" err="$tmp_dir/g5.err" rc=0
  run_in_backend "$out" "$err" "$GO" run ./scripts/fieldsem -mode name || rc=$?
  if [ "$rc" -ne 0 ]; then
    if grep -q '^VIOLATION=' "$out"; then
      record_fail 5 "产出可寻址（AST）" "$(grep -c '^VIOLATION=' "$out") 条违例；首条：$(grep -m1 '^VIOLATION=' "$out")"
    else
      record_fail 5 "产出可寻址（AST）" "fieldsem 执行失败 rc=$rc：$(head -1 "$err")"
    fi
    return
  fi
  record_pass 5 "产出可寻址（AST）" "*_ack 例外 $(sed -n 's/^ACK_NAMES=//p' "$out") 个；动态名（静态不可判，已列 WARN）$(grep -c '^WARN=' "$out" || true) 处"
}

# go_test_has_cases 输出用例数（无测试时 go test exit=0，不能当 PASS）。
go_test_has_cases() {
  local pkg="$1" pattern="$2"
  local out="$tmp_dir/list.out" err="$tmp_dir/list.err"
  if ! run_in_backend "$out" "$err" "$GO" test "$pkg" -list "$pattern"; then
    echo 0
    return
  fi
  # 兼容 Test / Benchmark / Example 三类。原实现只数 ^Test，导致以 Benchmark 命名的
  # 门禁（#10）在基准存在时仍判 0 ⇒ 永远 FAIL（Lead 独立复核发现并修正）。
  grep -cE '^(Test|Benchmark|Example)' "$out" || true
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 6：别名一致（配置可加载：冲突/单位类/格式；候选在声明/观测中只 WARN）
# ─────────────────────────────────────────────────────────────────────────────
gate6() {
  local cfg=""
  local c
  for c in "$REPO_DIR/config/field_aliases.yaml" "$BACKEND_DIR/config/field_aliases.yaml"; do
    if [ -f "$c" ]; then cfg="$c"; break; fi
  done
  local cnt
  cnt=$(go_test_has_cases ./internal/sensorfield '^TestAlias' | head -1)
  if [ -f "$cfg" ] && [ "$cnt" -gt 0 ]; then
    local out="$tmp_dir/g6.out" err="$tmp_dir/g6.err"
    if ! run_in_backend "$out" "$err" "$GO" test ./internal/sensorfield -run '^TestAlias' -v -count=1; then
      record_fail 6 "别名一致（可加载）" "别名测试失败：$(tail -2 "$out" | tr '\n' ' ')"
      return
    fi
    if ! grep -q '^=== RUN' "$out"; then
      record_fail 6 "别名一致（可加载）" "go test 退出 0 但输出无 '=== RUN'（假绿形态）"
      return
    fi
    record_pass 6 "别名一致（可加载）" "配置 $cfg 可加载；执行 $(grep -c '^=== RUN' "$out") 个用例"
    return
  fi
  if [ -f "$cfg" ]; then
    record_skip 6 "别名一致（可加载）" "配置存在但无 TestAlias 加载用例（D3 未落地）"
  else
    record_skip 6 "别名一致（可加载）" "config/field_aliases.yaml 不存在且无 TestAlias（D3 未落地）"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 7：派生无环（拓扑排序、参数个数/类型）——由同包测试承担同一校验函数。
# ─────────────────────────────────────────────────────────────────────────────
gate7() {
  local cnt
  cnt=$(go_test_has_cases ./internal/sensorfield '^TestVirtual' | head -1)
  if [ "$cnt" -eq 0 ]; then
    record_skip 7 "派生无环" "无 TestVirtual 用例（D3 未落地；不得以无测试=通过代替）"
    return
  fi
  local out="$tmp_dir/g7.out" err="$tmp_dir/g7.err"
  if ! run_in_backend "$out" "$err" "$GO" test ./internal/sensorfield -run '^TestVirtual' -v -count=1; then
    record_fail 7 "派生无环" "virtual 测试失败：$(tail -2 "$out" | tr '\n' ' ')"
    return
  fi
  if ! grep -q '^=== RUN' "$out"; then
    record_fail 7 "派生无环" "输出无 '=== RUN'（假绿形态）"
    return
  fi
  record_pass 7 "派生无环" "执行 $(grep -c '^=== RUN' "$out") 个用例"
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 8：语法表驱动。反假绿：必须断言输出含 '=== RUN' 且用例数>0。
# ─────────────────────────────────────────────────────────────────────────────
gate8() {
  local cnt
  cnt=$(go_test_has_cases ./internal/sensorfield '^TestSelector' | head -1)
  if [ "$cnt" -eq 0 ]; then
    record_fail 8 "语法表驱动" "go test -list '^TestSelector' 无用例（无测试文件时 go test 也会 exit=0 —— 不得算 PASS）"
    return
  fi
  local out="$tmp_dir/g8.out" err="$tmp_dir/g8.err"
  if ! run_in_backend "$out" "$err" "$GO" test ./internal/sensorfield -run '^TestSelector' -v -count=1; then
    record_fail 8 "语法表驱动" "TestSelector 失败：$(tail -3 "$out" | tr '\n' ' ')"
    return
  fi
  if ! grep -q '^=== RUN' "$out"; then
    record_fail 8 "语法表驱动" "go test 退出 0 但输出无 '=== RUN'（假绿形态）"
    return
  fi
  record_pass 8 "语法表驱动" "执行 $(grep -c '^=== RUN' "$out") 个用例，全部通过"
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 9：变异自证（apply→run→expect red→restore→md5 核对）
# 清单：scripts/field_semantics_mutations.tsv；默认 SKIP，--mutation 启用。
# ─────────────────────────────────────────────────────────────────────────────
gate9() {
  if [ "$MUTATION" -ne 1 ]; then
    record_skip 9 "变异自证" "默认不执行（会临时改源码）；用 --mutation 启用；清单 scripts/field_semantics_mutations.tsv"
    return
  fi
  if [ ! -f "$MUTATION_MANIFEST" ]; then
    record_fail 9 "变异自证" "缺少变异清单 $MUTATION_MANIFEST"
    return
  fi
  local total=0 caught=0 skipped=0 failed=0 detail=""
  local id glob pattern replacement testpat target cand backup md5a md5b rc
  while IFS='	' read -r id glob pattern replacement testpat; do
    case "$id" in ''|'#'*) continue ;; esac
    total=$((total + 1))
    target=""
    for cand in "$BACKEND_DIR"/$glob; do
      if [ -f "$cand" ] && grep -q "$pattern" "$cand"; then target="$cand"; break; fi
    done
    if [ -z "$target" ]; then
      skipped=$((skipped + 1)); detail="$detail $id=SKIP(missing)"
      continue
    fi
    backup="$tmp_dir/mut.$id.bak"
    cp "$target" "$backup"
    md5a=$(md5sum "$target" | cut -d' ' -f1)
    sed -i "s/$pattern/$replacement/g" "$target"
    rc=0
    ( cd "$BACKEND_DIR" && "$GO" test ./internal/sensorfield -run "$testpat" -count=1 ) >"$tmp_dir/mut.$id.out" 2>&1 || rc=$?
    cp "$backup" "$target"
    md5b=$(md5sum "$target" | cut -d' ' -f1)
    if [ "$md5a" != "$md5b" ]; then
      record_fail 9 "变异自证" "变异 $id 还原失败（md5 不一致）—— 工作树可能被污染，停止"
      return
    fi
    if [ "$rc" -ne 0 ]; then
      caught=$((caught + 1)); detail="$detail $id=CAUGHT"
    else
      failed=$((failed + 1)); detail="$detail $id=NOT_CAUGHT"
    fi
  done < "$MUTATION_MANIFEST"
  if [ "$failed" -gt 0 ]; then
    record_fail 9 "变异自证" "未捕获变异 $failed/$total：$detail"
  elif [ "$caught" -eq 0 ]; then
    record_skip 9 "变异自证" "清单 $total 条全部目标未命中（代码尚未落地/清单待更新）：$detail"
  else
    record_pass 9 "变异自证" "捕获 $caught/$total（目标未命中 $skipped 条）：$detail"
  fi
}

# ─────────────────────────────────────────────────────────────────────────────
# 门禁 10（Lead 2026-10-21 裁决：拆两条，只有 10a 参与 PASS/FAIL）
#
#   10a 生产求值链路（硬判据）：BenchmarkAutomationLiteralPath / BenchmarkAlertLiteralPath
#       相对 main 基线 ≤ +5%。两个专用 bench 尚未落地时，用 sensorfield 包内
#       BenchmarkLiteralEvalBaselineMatchField（逐字节拷贝 main 的 matchField）作 proxy，
#       并在输出里明确标注 proxy（可见，不伪装成生产 bench）。
#   10b 新层抽象成本（观测，不阻断）：BenchmarkLiteralEval ≤10ns 且 0 alloc ⇒ PASS；
#       否则 WARN。理由：新层 +428% 但生产链 literal 仍走 legacy matchField（G-6），
#       可感知回归 0%，把它当硬门禁会逼实现者优化一条不承载流量的路径。
#
# 基线两份：bench_baseline_prod.txt（10a）/ bench_baseline_layer.txt（10b）。
# ⚠ 下面这句必须打印（防后人误读「新层更慢=线上更慢」）：
BENCH_WORDING="生产链 literal 走 legacy matchField（G-6），新层成本不计入回归"

# bench_median_ns 取同名基准 ns/op 中位数（benchstat 缺失时的兜底；go test 会附加 -<GOMAXPROCS> 后缀）。
bench_median_ns() {
  local file="$1" name="$2"
  grep -E "^$name(-[0-9]+)?[[:space:]]" "$file" 2>/dev/null \
    | awk '{for (i=1;i<=NF;i++) if ($i=="ns/op") print $(i-1)}' \
    | sort -n \
    | awk '{a[NR]=$1} END{
        if (NR==0) exit 2;
        if (NR%2==1) { print a[(NR+1)/2] }
        else { printf "%.6f", (a[NR/2]+a[NR/2+1])/2 }
      }'
}

# bench_alloc_nonzero 统计 allocs/op 非 0 的行数（0 表示全部无分配）。
bench_alloc_nonzero() {
  local file="$1" name="$2"
  grep -E "^$name(-[0-9]+)?[[:space:]]" "$file" 2>/dev/null \
    | awk '{for (i=1;i<=NF;i++) if ($i=="allocs/op") { if ($(i-1)+0 != 0) n++ }} END{print n+0}'
}

# bench_gate_verdict 三态纯函数（--self-test 断言不回归）：
#   has_bench=0 -> fail；record=1 -> record；baseline_present=0 -> skip-baseline；否则 compare
bench_gate_verdict() {
  if [ "$1" -eq 0 ]; then echo fail; return 0; fi
  if [ "$3" -eq 1 ]; then echo record; return 0; fi
  if [ "$2" -eq 0 ]; then echo skip-baseline; return 0; fi
  echo compare
}

# bench_resolve_name <pkg> <候选名...>：在包内 -list 结果里找第一个存在的基准名。
bench_resolve_name() {
  local pkg="$1"; shift
  local out="$tmp_dir/bench.list.out" err="$tmp_dir/bench.list.err"
  if ! run_in_backend "$out" "$err" "$GO" test "$pkg" -list '^Benchmark'; then return 0; fi
  local cand
  for cand in "$@"; do
    if grep -qx "$cand" "$out"; then echo "$cand"; return 0; fi
  done
  return 0
}

# bench_measure <pkg> <name> <outfile>：跑 5 轮 benchmem，写原始输出。
bench_measure() {
  run_in_backend "$3" "$tmp_dir/bench.err" "$GO" test "$1" -run '^$' \
    -bench "^$2$" -benchmem -count=5 -benchtime=200ms
}

# bench_compare <name> <baseline-file> <raw-file>：pass/fail/skip（benchstat 优先，中位数兜底）。
bench_compare() {
  local name="$1" baseline="$2" raw="$3"
  if [ ! -s "$baseline" ]; then echo skip; return 0; fi
  local filtered="$tmp_dir/cmp.base"
  grep -v '^#' "$baseline" > "$filtered"
  if command -v benchstat >/dev/null 2>&1; then
    if benchstat -fail "$filtered" "$raw" > "$tmp_dir/benchstat.out" 2>&1; then echo pass; else echo fail; fi
    return 0
  fi
  local b c
  b=$(bench_median_ns "$filtered" "$name")
  c=$(bench_median_ns "$raw" "$name")
  if [ -z "$b" ] || [ -z "$c" ]; then echo skip; return 0; fi
  if awk -v b="$b" -v c="$c" 'BEGIN{exit (c<=b*1.05)?0:1}'; then echo pass; else echo fail; fi
}

# bench_record <file> <pkg> <name> <label> <note>：录参考基线（带 header 与 commit）。
bench_record() {
  local file="$1" pkg="$2" name="$3" label="$4" note="$5"
  local raw="$tmp_dir/bench.record.txt"
  if ! bench_measure "$pkg" "$name" "$raw"; then
    return 1
  fi
  mkdir -p "$(dirname "$file")"
  local c s
  c=$(cd "$REPO_DIR" && git rev-parse --short HEAD 2>/dev/null || echo unknown)
  s=$(cd "$REPO_DIR" && git log -1 --format=%s "$c" 2>/dev/null || echo unknown)
  {
    echo "# ===== $label（REFERENCE BASELINE，非 main 基线）====="
    echo "# bench: $name (pkg=$pkg)"
    echo "# note: $note"
    echo "# 为什么不是 main：main d406dc2d 上不存在 sensorfield/selector 集成，无法在 main 录基线。"
    echo "# 局限（诚实边界）：本机（AMD Ryzen 5 5600）与并行负载下录制，单次波动大；"
    echo "#   ±5% 阈值受机器/负载影响，是 D1 门槛的近似判据，不是发布级 SLA。"
    echo "# commit: $c ($s)"
    echo "# date: $(date -Is)"
    echo "# recorded_by: scripts/check_field_semantics.sh --record-bench-baseline"
    grep -E '^(Benchmark|goos|goarch|pkg|cpu)' "$raw"
  } > "$file"
  return 0
}

# bench_pair_exists <pkg> <prod> <baseline>：两个 bench 都必须存在（成对才可比）。
bench_pair_exists() {
  local pkg="$1" prod="$2" base="$3"
  local out="$tmp_dir/bench.pair.out" err="$tmp_dir/bench.pair.err"
  if ! run_in_backend "$out" "$err" "$GO" test "$pkg" -list "^Benchmark"; then return 1; fi
  grep -qx "$prod" "$out" || return 1
  grep -qx "$base" "$out" || return 1
  return 0
}

# bench_ratio_verdict <prod_ns> <base_ns>：同一次 run 的比值判据（机器无关）。
#   prod 或 base 为空/非数 -> skip；ratio <= 1.05 -> pass；否则 fail。
bench_ratio_verdict() {
  local prod="$1" base="$2"
  if [ -z "$prod" ] || [ -z "$base" ]; then echo skip; return 0; fi
  if ! awk -v b="$base" 'BEGIN{exit (b>0)?0:1}'; then echo skip; return 0; fi
  if awk -v p="$prod" -v b="$base" 'BEGIN{exit (p <= b*1.05) ? 0 : 1}'; then echo pass; else echo fail; fi
}

# bench_abs_delta_verdict <prod_ns> <base_ns> <budget_ns>：绝对预算判据（Lead 2026-10-21 裁决 10a-①）。
#   空值 -> skip；prod-base <= budget -> pass；否则 fail。
bench_abs_delta_verdict() {
  local prod="$1" base="$2" budget="$3"
  if [ -z "$prod" ] || [ -z "$base" ] || [ -z "$budget" ]; then echo skip; return 0; fi
  if awk -v p="$prod" -v b="$base" -v d="$budget" 'BEGIN{exit (p-b <= d) ? 0 : 1}'; then echo pass; else echo fail; fi
}

# literal_count_verdict <has_run> <rc>：确定性判据三态（--self-test 断言「计数非 0 ⇒ 红」）。
literal_count_verdict() {
  if [ "$1" -eq 0 ]; then echo fail; return 0; fi
  if [ "$2" -ne 0 ]; then echo fail; return 0; fi
  echo pass
}

# bench_ratio_only <prod_ns> <base_ns>：仅记录（1b），不参与判定。
bench_ratio_only() {
  local p="$1" b="$2"
  if [ -z "$p" ] || [ -z "$b" ]; then echo ""; return 0; fi
  awk -v p="$p" -v b="$b" 'BEGIN{ if (b>0) printf "%.2f", p/b }'
}

# e2e_verdict <m1> <m2> <baseline>：端到端判据稳健化（并行负载下 5% 阈值会被噪声击穿）。
# 两次独立采样各取中位数：任一 ≤ baseline*1.05 即 pass；两次都 >1.05 才 fail。
e2e_verdict() {
  local m1="$1" m2="$2" b="$3"
  if [ -z "$m1" ] || [ -z "$m2" ] || [ -z "$b" ]; then echo skip; return 0; fi
  if ! awk -v b="$b" 'BEGIN{exit (b>0)?0:1}'; then echo skip; return 0; fi
  if awk -v m="$m1" -v b="$b" 'BEGIN{exit (m<=b*1.05)?0:1}' || \
     awk -v m="$m2" -v b="$b" 'BEGIN{exit (m<=b*1.05)?0:1}'; then
    echo pass
  else
    echo fail
  fi
}

e2e_min_ratio() {
  awk -v a="$1" -v b="$2" -v base="$3" 'BEGIN{ if (base>0) { m=(a<b?a:b); printf "%.2fx", m/base } }'
}

gate10a() {
  # 三层判据（Lead 2026-10-21 最终裁决）：
  #   1a 确定性（硬）：TestLiteralPathSkipsProviderAndCompile 必须真实执行且通过——
  #      注入计数型 provider，断言纯字面量路径 provider snapshot/compile 调用数 == 0，
  #      特殊 selector 调用数 > 0（证明计数器非恒 0）。这是「红线 9 不为统一入口付税」的直接证明。
  #   1b 观测（软）：同 run 比值只记录；并行负载下噪声可达 4–5×，不参与判定。
  #   2  端到端（硬）：BenchmarkEvaluateFrame 相对参考基线 ≤+5%；未落地则 SKIP（不得 PASS）。
  echo "  判据：#10a-1a 是确定性判据（provider/compile 调用数 == 0）；#10a-1b 仅记录，不用于判定"
  echo "  口径：literal 生产路径不得为统一入口付税（红线 9）"
  echo "  口径：$BENCH_WORDING"

  # ── 10a-1a：确定性结构断言 ──
  if [ "$GATE_DIRTY" -eq 1 ] && [ "$RECORD_BENCH" -eq 0 ]; then
    record_skip 10a-1a "literal 路径结构断言（确定性）" "SKIP(dirty-worktree)：工作树有未提交改动（HEAD=$GATE_HEAD, $GATE_DIRTY_COUNT files），基准类门禁不用别人的中间态判 PASS/FAIL"
  else
  local out_1a="$tmp_dir/bench10a1a.out" rc_1a=0
  run_in_backend "$out_1a" "$tmp_dir/bench10a1a.err" "$GO" test ./internal/automation \
    -run '^TestLiteralPathSkipsProviderAndCompile$' -v -count=1 || rc_1a=$?
  local has_run_1a=0
  if grep -q '^=== RUN   TestLiteralPathSkipsProviderAndCompile' "$out_1a"; then has_run_1a=1; fi
  local v_1a
  v_1a=$(literal_count_verdict "$has_run_1a" "$rc_1a")
  if [ "$v_1a" = "pass" ]; then
    record_pass 10a-1a "literal 路径结构断言（确定性）" "TestLiteralPathSkipsProviderAndCompile 真实执行且通过（provider/compile 调用数 == 0）"
  elif [ "$has_run_1a" -eq 0 ]; then
    if grep -q "no tests to run" "$out_1a" 2>/dev/null; then
      record_fail 10a-1a "literal 路径结构断言（确定性）" "测试未落地（go test 输出 [no tests to run] 且 exit=0 是假绿，判 FAIL）；请 impl-core 落地 TestLiteralPathSkipsProviderAndCompile"
    else
      record_fail 10a-1a "literal 路径结构断言（确定性）" "包不可构建/测试未跑成（不能判定计数）：$(tail -2 "$tmp_dir/bench10a1a.err" | tr '\n' ' ')"
    fi
  else
    record_fail 10a-1a "literal 路径结构断言（确定性）" "测试失败（rc=$rc_1a）⇒ 纯字面量路径仍在调用 provider/compile（计数非 0），红线 9 被违反"
  fi
  fi

  # ── 10a-1b：同 run 比值，仅记录（>5× 只 WARN） ──
  local prod="" base="" pkg=""
  if bench_pair_exists ./internal/automation BenchmarkAutomationLiteralPath BenchmarkAutomationLiteralPathBaseline; then
    prod=BenchmarkAutomationLiteralPath; base=BenchmarkAutomationLiteralPathBaseline; pkg=./internal/automation
  elif bench_pair_exists ./internal/alert BenchmarkAlertLiteralPath BenchmarkAlertLiteralPathBaseline; then
    prod=BenchmarkAlertLiteralPath; base=BenchmarkAlertLiteralPathBaseline; pkg=./internal/alert
  fi
  if [ -z "$prod" ]; then
    record_skip 10a-1b "literal/基线 同 run 比值（仅记录）" "SKIP(benchmark-missing)：未找到成对 Benchmark{Automation,Alert}LiteralPath(+Baseline)"
  else
    local raw="$tmp_dir/bench10a1b.txt"
    if ! run_in_backend "$raw" "$tmp_dir/bench.err" "$GO" test "$pkg" -run '^$' \
        -bench "^$prod$|^$base$" -benchmem -count=10 -benchtime=300ms; then
      record_warn 10a-1b "literal/基线 同 run 比值（仅记录）" "WARN(不阻断)：bench 执行失败：$(head -1 "$tmp_dir/bench.err") [pkg=$pkg]"
    else
      local pm bm ratio
      pm=$(bench_median_ns "$raw" "$prod")
      bm=$(bench_median_ns "$raw" "$base")
      ratio=$(bench_ratio_only "$pm" "$bm")
      if [ -n "$ratio" ] && awk -v r="$ratio" 'BEGIN{exit (r>5.0)?0:1}'; then
        record_warn 10a-1b "literal/基线 同 run 比值（仅记录）" "WARN(不阻断)：比值 "$ratio"x > 5x（并行负载噪声可达 5×，不用于判定；literal="$pm"ns baseline="$bm"ns）"
      else
        record_pass 10a-1b "literal/基线 同 run 比值（仅记录）" "比值 "$ratio"x（literal="$pm"ns baseline="$bm"ns）；仅记录，不参与判定"
      fi
    fi
  fi

  # ── 10a-2：端到端（硬） ──
  local e2e="" e2epkg="" e2e_build_err=""
  for cand in ./internal/automation ./internal/sensorfield ./internal/alert ./cmd/server; do
    local lout="$tmp_dir/e2e.list.out" lerr="$tmp_dir/e2e.list.err" lrc=0
    run_in_backend "$lout" "$lerr" "$GO" test "$cand" -list '^Benchmark' || lrc=$?
    if [ "$lrc" -ne 0 ]; then
      e2e_build_err="$cand: $(tail -1 "$lerr")"
      continue
    fi
    if grep -qx BenchmarkEvaluateFrame "$lout"; then e2e=BenchmarkEvaluateFrame; e2epkg="$cand"; break; fi
  done
  if [ -z "$e2e" ] && [ -n "$e2e_build_err" ]; then
    record_fail 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "包不可构建，无法判定端到端判据（不是 bench 缺失）：$e2e_build_err"
    return
  fi
  if [ -z "$e2e" ]; then
    record_skip 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "SKIP(e2e-bench-missing)：BenchmarkEvaluateFrame 未落地（一帧 8 字段 3 规则）；不得用绝对预算替代，不得 PASS"
    return
  fi
  if [ "$GATE_DIRTY" -eq 1 ] && [ "$RECORD_BENCH" -eq 0 ]; then
    record_skip 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "SKIP(dirty-worktree)：工作树有未提交改动（HEAD=$GATE_HEAD, $GATE_DIRTY_COUNT files），基准类门禁不用别人的中间态判 PASS/FAIL"
    return
  fi
  local eraw="$tmp_dir/bench10a-e2e.txt" eraw2="$tmp_dir/bench10a-e2e2.txt"
  if ! run_in_backend "$eraw" "$tmp_dir/bench.err" "$GO" test "$e2epkg" -run '^$' \
      -bench "^$e2e$" -benchmem -count=5 -benchtime=1s; then
    record_fail 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "bench 执行失败：$(head -1 "$tmp_dir/bench.err") [pkg=$e2epkg]"
    return
  fi
  run_in_backend "$eraw2" "$tmp_dir/bench.err" "$GO" test "$e2epkg" -run '^$' \
      -bench "^$e2e$" -benchmem -count=5 -benchtime=1s || true
  if [ "$RECORD_BENCH" -eq 1 ]; then
    if bench_record "$BENCH_BASELINE_E2E" "$e2epkg" "$e2e" "端到端求值基线（10a-2，参考记录）" "一帧 8 字段 3 规则；相对基线 ≤+5%（参考基线非 main 基线）"; then
      record_pass 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "已记录参考基线 $BENCH_BASELINE_E2E [$e2e]"
    else
      record_fail 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "录制基线失败"
    fi
    return
  fi
  local bfilter="$tmp_dir/e2e.base.raw" bmed m1 m2 ev ratio
  if [ ! -s "$BENCH_BASELINE_E2E" ]; then
    record_skip 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "SKIP(e2e-baseline-missing)：$BENCH_BASELINE_E2E 不存在；用 --record-bench-baseline 录参考基线"
    return
  fi
  grep -v '^#' "$BENCH_BASELINE_E2E" > "$bfilter"
  bmed=$(bench_median_ns "$bfilter" "$e2e")
  m1=$(bench_median_ns "$eraw" "$e2e")
  m2=$(bench_median_ns "$eraw2" "$e2e")
  ev=$(e2e_verdict "$m1" "$m2" "$bmed")
  ratio=$(e2e_min_ratio "$m1" "$m2" "$bmed")
  case "$ev" in
    pass) record_pass 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "两次采样 "$m1"ns/"$m2"ns vs 基线 "$bmed"ns ⇒ 更优比 "$ratio" ≤ 1.05 [pkg=$e2epkg]" ;;
    fail) record_fail 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "两次采样 "$m1"ns/"$m2"ns vs 基线 "$bmed"ns ⇒ 两次都超 1.05（更优比 "$ratio"）[pkg=$e2epkg]" ;;
    *) record_skip 10a-2 "端到端求值开销（相对基线 ≤+5%，硬）" "SKIP(e2e-baseline-unusable)：基线或采样无法解析（基线="$bmed" m1="$m1" m2="$m2"）" ;;
  esac
}

gate10b() {
  local name
  name=$(bench_resolve_name ./internal/sensorfield BenchmarkLiteralEval)
  if [ -z "$name" ]; then
    record_fail 10b "新层抽象成本（观测）" "无 BenchmarkLiteralEval —— 目标不存在必须 FAIL（防假绿）"
    return
  fi
  local raw="$tmp_dir/bench10b.txt"
  if ! bench_measure ./internal/sensorfield "$name" "$raw"; then
    record_fail 10b "新层抽象成本（观测）" "bench 执行失败：$(head -1 "$tmp_dir/bench.err")"
    return
  fi
  if ! grep -qE "^$name(-[0-9]+)?[[:space:]]" "$raw"; then
    record_fail 10b "新层抽象成本（观测）" "输出无基准行（假绿形态）"
    return
  fi
  local verdict
  verdict=$(bench_gate_verdict 1 "$([ -s "$BENCH_BASELINE_LAYER" ] && echo 1 || echo 0)" "$RECORD_BENCH")
  if [ "$verdict" = "record" ]; then
    if bench_record "$BENCH_BASELINE_LAYER" ./internal/sensorfield "$name" "新层抽象成本基线（10b，观测）" "$BENCH_WORDING"; then
      record_pass 10b "新层抽象成本（观测）" "已记录参考基线 $BENCH_BASELINE_LAYER [$BENCH_WORDING]"
    else
      record_fail 10b "新层抽象成本（观测）" "录制基线失败"
    fi
    return
  fi
  local med allocs
  med=$(bench_median_ns "$raw" "$name")
  allocs=$(bench_alloc_nonzero "$raw" "$name")
  if [ -z "$med" ]; then
    record_skip 10b "新层抽象成本（观测）" "无法解析 ns/op"
    return
  fi
  if awk -v m="$med" 'BEGIN{exit (m<=10)?0:1}' && [ "$allocs" -eq 0 ]; then
    record_pass 10b "新层抽象成本（观测）" "median "$med"ns ≤10ns 且 0 alloc（$BENCH_WORDING）"
  else
    record_warn 10b "新层抽象成本（观测）" "WARN(不阻断)：median "$med"ns / alloc 非0 行数="$allocs"（阈值 ≤10ns 且 0 alloc；$BENCH_WORDING）"
  fi
}
# ─────────────────────────────────────────────────────────────────────────────
# 门禁 11：回滚盘点（无该脚本不得执行代码回退）
# 退出码：0=无非字面量；1=存在（禁止回退）；77=DB 不可达（SKIP）。
# ─────────────────────────────────────────────────────────────────────────────
gate11() {
  local script="$BACKEND_DIR/scripts/check_field_selectors.sh"
  if [ ! -x "$script" ]; then
    record_fail 11 "回滚盘点" "缺少/不可执行 $script（§12.3#11 交付物）"
    return
  fi
  local out="$tmp_dir/g11.out" rc=0
  "$script" --check > "$out" 2>&1 || rc=$?
  case "$rc" in
    0) record_pass 11 "回滚盘点" "$(grep -m1 '^RESULT:' "$out" || echo '无非字面量取值')" ;;
    1) record_fail 11 "回滚盘点" "存在非字面量 selector（代码回退被阻止）：$(grep -m1 '^RESULT:' "$out" || tail -1 "$out")" ;;
    77) record_skip 11 "回滚盘点" "SKIP(db-unavailable)：$(grep -m1 '^SKIP:' "$out" || tail -1 "$out")" ;;
    *) record_fail 11 "回滚盘点" "盘点脚本异常退出 rc=$rc：$(tail -1 "$out")" ;;
  esac
}

# ─────────────────────────────────────────────────────────────────────────────
# 反假绿自证：把判据作用于合成输入，断言它会红/会 SKIP。
# ─────────────────────────────────────────────────────────────────────────────
# selftest_fieldsem <out> <err> <fieldsem args...>：跑 AST 工具，返回其退出码。
selftest_fieldsem() {
  local out="$1" err="$2"; shift 2
  run_in_backend "$out" "$err" "$GO" run ./scripts/fieldsem "$@"
}

# selftest_retry_fieldsem <out> <err> <args...>：最多重试 3 次（GOCACHE 并发/抖动）。
# 返回 0 表示命令成功，非 0 表示最后一次退出码（可能是判据红，也可能是构建失败）。
selftest_retry_fieldsem() {
  local out="$1" err="$2"; shift 2
  local attempt=0 rc=1
  while [ "$attempt" -lt 3 ]; do
    rc=0
    selftest_fieldsem "$out" "$err" "$@" || rc=$?
    if [ "$rc" -eq 0 ]; then return 0; fi
    if grep -q '^VIOLATION=' "$out"; then return "$rc"; fi   # 真判据红，不必重试
    attempt=$((attempt + 1))
  done
  return "$rc"
}

self_test() {
  local failures=0
  echo "== 反假绿自证 =="
  local ast_dir="$tmp_dir/ast"
  mkdir -p "$ast_dir/drivers"
  {
    echo 'package drivers'
    echo 'type SensorData struct { Name string; Value float64; Unit string }'
    echo 'type T struct{}'
    echo 'func (d *T) GetSensorDefinitions() []SensorData {'
    echo '  return []SensorData{{Name: "rsoc", Unit: "%"}, {Name: "ghost_field", Unit: ""}}'
    echo '}'
    echo 'func (d *T) ParseData(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "rsoc", Value: 1}}, nil'
    echo '}'
  } > "$ast_dir/drivers/a.go"
  selftest_retry_fieldsem "$tmp_dir/st.out" "$tmp_dir/st.err" -drivers "$ast_dir/drivers" -mode decl || true
  if [ -s "$tmp_dir/st.out" ] && grep -q '^VIOLATION=' "$tmp_dir/st.out"; then
    echo "SELFTEST PASS: 幽灵声明 ⇒ 门禁红（VIOLATION 行存在）"
  else
    echo "SELFTEST FAIL: 幽灵声明未产生 VIOLATION 行（rc=0 或工具构建失败都会落这里；stderr=$(tail -1 "$tmp_dir/st.err" 2>/dev/null)）"
    failures=$((failures + 1))
  fi
  {
    echo 'package drivers'
    echo 'func (d *T) ParseData2(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "ghost_field", Value: 2}}, nil'
    echo '}'
  } > "$ast_dir/drivers/b.go"
  if selftest_retry_fieldsem "$tmp_dir/st2.out" "$tmp_dir/st2.err" -drivers "$ast_dir/drivers" -mode decl; then
    echo "SELFTEST PASS: 有产出 ⇒ 门禁绿"
  else
    if grep -q '^VIOLATION=' "$tmp_dir/st2.out"; then
      echo "SELFTEST FAIL: 有产出仍红：$(grep -m1 '^VIOLATION=' "$tmp_dir/st2.out")"
    else
      echo "SELFTEST ERROR: 工具未跑成（构建/缓存失败，非判据结论）：$(tail -1 "$tmp_dir/st2.err" 2>/dev/null)"
    fi
    failures=$((failures + 1))
  fi

  local name_dir="$tmp_dir/name"
  mkdir -p "$name_dir/drivers"
  {
    echo 'package drivers'
    echo 'type SensorData struct { Name string; Value float64; Unit string }'
    echo 'type T struct{}'
    echo 'func (d *T) ParseData(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "Props", Value: 1}}, nil'
    echo '}'
  } > "$name_dir/drivers/c.go"
  selftest_retry_fieldsem "$tmp_dir/st3.out" "$tmp_dir/st3.err" -drivers "$name_dir/drivers" -mode name || true
  if [ -s "$tmp_dir/st3.out" ] && grep -q '^VIOLATION=' "$tmp_dir/st3.out"; then
    echo "SELFTEST PASS: 非法产出名 ⇒ 门禁红（VIOLATION 行存在）"
  else
    echo "SELFTEST FAIL: 非法产出名未产生 VIOLATION 行（不是判据红）：$(tail -1 "$tmp_dir/st3.err" 2>/dev/null)"
    failures=$((failures + 1))
  fi
  {
    echo 'package drivers'
    echo 'type SensorData struct { Name string; Value float64; Unit string }'
    echo 'type T struct{}'
    echo 'func (d *T) ParseData(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "write_ack", Value: 1}}, nil'
    echo '}'
  } > "$name_dir/drivers/c.go"
  if selftest_retry_fieldsem "$tmp_dir/st4.out" "$tmp_dir/st4.err" -drivers "$name_dir/drivers" -mode name; then
    echo "SELFTEST PASS: *_ack 例外 ⇒ 门禁绿"
  else
    echo "SELFTEST FAIL: *_ack 例外被判红（应列入例外清单）：$(grep -m1 '^VIOLATION=' "$tmp_dir/st4.out" 2>/dev/null)"
    failures=$((failures + 1))
  fi

  printf 'ok  	ehome/backend/internal/sensorfield	0.001s\n' > "$tmp_dir/fake8.out"
  if grep -q '^=== RUN' "$tmp_dir/fake8.out"; then
    echo "SELFTEST FAIL: 空输出被当成有 RUN 行"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 无 '=== RUN' ⇒ 门禁 #8 判红（反假绿）"
  fi

  # #10 三态判定：把 2026-10-21 的真实回归（存在 Benchmark 却被计数成 0 ⇒ 误判 FAIL）
  # 钉成用例 —— 计数必须能数到 Benchmark 行，且"有基准+无基线"必须是 SKIP 不是 FAIL。
  printf 'BenchmarkLiteralEval\nTestFoo\nExampleBar\n' > "$tmp_dir/fake10.list"
  local names
  names=$(grep -cE '^(Test|Benchmark|Example)' "$tmp_dir/fake10.list" || true)
  if [ "$names" -ne 3 ]; then
    echo "SELFTEST FAIL: 用例计数 $names，期望 3（含 Benchmark 行；回归 bug 会让它为 0）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: go test -list 计数含 Benchmark/Example（#10 不会把存在的基准判成 0）"
  fi
  if [ "$(bench_gate_verdict 1 0 0)" != "skip-baseline" ]; then
    echo "SELFTEST FAIL: 有 benchmark 但无基线 -> $(bench_gate_verdict 1 0 0)，期望 skip-baseline（不得 FAIL）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 存在 Benchmark ⇒ #10 = SKIP(baseline-missing)（不是 FAIL）"
  fi
  if [ "$(bench_gate_verdict 0 0 0)" != "fail" ]; then
    echo "SELFTEST FAIL: 无 benchmark 未判 fail（假绿风险）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 无 benchmark ⇒ #10 FAIL（判据未被放宽）"
  fi
  # #10a-1a 确定性判据：计数非 0（测试失败）或测试未执行 ⇒ 必须红。
  if [ "$(literal_count_verdict 1 1)" != "fail" ]; then
    echo "SELFTEST FAIL: 计数非 0（测试失败 rc=1）应判 fail"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-1a 计数非 0 ⇒ 红（确定性判据能抓住付税）"
  fi
  if [ "$(literal_count_verdict 0 0)" != "fail" ]; then
    echo "SELFTEST FAIL: 测试未执行（no tests to run, rc=0）必须判 fail 而不是 pass（假绿）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-1a 测试未执行 ⇒ fail（go test 无测试 exit=0 不当 PASS）"
  fi
  if [ "$(literal_count_verdict 1 0)" != "pass" ]; then
    echo "SELFTEST FAIL: 测试真实执行且通过应判 pass"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-1a 测试真实执行且通过 ⇒ pass"
  fi
  # #10a-1b 仅记录：比值再大也不能阻断。
  if [ "$(bench_ratio_only 20.0 1.0)" != "20.00" ]; then
    echo "SELFTEST FAIL: 1b 比值计算异常（期望 20.00，实得 $(bench_ratio_only 20.0 1.0)）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-1b 比值仅记录（20x 不参与判定）"
  fi
  # #10a-2 端到端：两次采样都超 ⇒ 红；一次干净 ⇒ pass。
  if [ "$(e2e_verdict 1.10 1.02 1.0)" != "pass" ]; then
    echo "SELFTEST FAIL: 两次采样中一次 ≤1.05 应判 pass"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-2 稳健化：1.10/1.02 ⇒ pass（一次干净测量）"
  fi
  if [ "$(e2e_verdict 1.10 1.08 1.0)" != "fail" ]; then
    echo "SELFTEST FAIL: 两次采样都 >1.05 应判 fail（真回归）"
    failures=$((failures + 1))
  else
    echo "SELFTEST PASS: #10a-2 稳健化：1.10/1.08 ⇒ fail（两次都超，抓真回归）"
  fi

  local saved="$RESULTS"
  RESULTS="SKIP|99|合成 SKIP|
"
  STRICT=0
  if summary >/dev/null 2>&1; then
    echo "SELFTEST PASS: 默认模式 SKIP 容忍（exit 0）"
  else
    echo "SELFTEST FAIL: 默认模式 SKIP 不应失败"; failures=$((failures + 1))
  fi
  STRICT=1
  if summary >/dev/null 2>&1; then
    echo "SELFTEST FAIL: --strict 下 SKIP 应失败"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: --strict 下 SKIP 失败（发布门禁语义）"
  fi
  STRICT=0
  RESULTS="$saved"

  echo "== 自证结果：$failures 项失败 =="
  if [ "$failures" -gt 0 ]; then return 1; fi
  return 0
}

summary() {
  local fails=0 skips=0 passes=0 warns=0 status num title detail
  while IFS='|' read -r status num title detail; do
    case "$status" in
      PASS) passes=$((passes + 1)) ;;
      WARN) warns=$((warns + 1)) ;;
      FAIL) fails=$((fails + 1)) ;;
      SKIP) skips=$((skips + 1)) ;;
    esac
  done <<EOF
$RESULTS
EOF
  echo ""
  echo "== 门禁结果（HEAD=$GATE_HEAD$DIRTY_MARK）=="
  while IFS='|' read -r status num title detail; do
    if [ -n "$status" ]; then
      printf '[%s] #%s %s — %s\n' "$status" "$num" "$title" "$detail"
    fi
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
    echo "RESULT: PASS_WITH_SKIPS($skips) — 通过 $passes 条；SKIP 原因见上（D6 完成后用 --strict）$RESULT_SUFFIX"
    return 0
  fi
  if [ "$warns" -gt 0 ]; then
    echo "RESULT: PASS（含 $warns 条 WARN，不阻断）— $passes 条 PASS / $skips 条 SKIP$RESULT_SUFFIX"
  else
    echo "RESULT: PASS — $passes 条门禁全部通过$RESULT_SUFFIX"
  fi
  return 0
}

run_gate() {
  case "$1" in
    1) gate1 ;; 2) gate2 ;; 3) gate3 ;; 4) gate4 ;; 5) gate5 ;;
    6) gate6 ;; 7) gate7 ;; 8) gate8 ;; 9) gate9 ;;
    10) gate10a; gate10b ;; 10a) gate10a ;; 10b) gate10b ;;
    11) gate11 ;;
    *) echo "未知门禁编号: $1" >&2; exit 2 ;;
  esac
}

if [ "$SELF_TEST" -eq 1 ]; then
  self_test
  exit $?
fi
if [ -z "$ONLY" ]; then
  for i in 1 2 3 4 5 6 7 8 9 10 11; do run_gate "$i"; done
else
  run_gate "$ONLY"
fi
summary
exit $?