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
#
# 纪律：只读仓库/DB；门禁 9 的变异临时改被测源文件，必须备份-改-跑-还原并核对 md5。
set -uo pipefail   # 不用 -e（逐门禁收集结果）；pipefail 防「管道失败即绿」。

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
BACKEND_DIR=$(cd "$SCRIPT_DIR/.." && pwd)
REPO_DIR=$(cd "$BACKEND_DIR/.." && pwd)

GO=$(printenv GO 2>/dev/null || true)
if [ -z "$GO" ]; then GO="go"; fi
if ! command -v "$GO" >/dev/null 2>&1; then
  if [ -x "$REPO_DIR/.tools/gotool/bin/go" ]; then GO="$REPO_DIR/.tools/gotool/bin/go"; fi
fi
BENCH_BASELINE="$BACKEND_DIR/internal/sensorfield/testdata/bench_baseline.txt"
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

tmp_dir=$(mktemp -d)
trap 'rm -rf "$tmp_dir"' EXIT

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
  grep -c '^Test' "$out" || true
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
# 门禁 10：性能基准。无 benchmark 必须 FAIL（防假绿）；有 bench 无基线 ⇒ SKIP。
# ─────────────────────────────────────────────────────────────────────────────
gate10() {
  local cnt
  cnt=$(go_test_has_cases ./internal/sensorfield '^BenchmarkLiteralEval$' | head -1)
  if [ "$cnt" -eq 0 ]; then
    record_fail 10 "性能基准" "无 BenchmarkLiteralEval —— §12.3#10 要求此时必须 FAIL 而不是 PASS（防假绿）"
    return
  fi
  local raw="$tmp_dir/bench.txt"
  if ! run_in_backend "$raw" "$tmp_dir/bench.err" "$GO" test ./internal/sensorfield -run '^$' -bench '^BenchmarkLiteralEval$' -benchmem -count=5 -benchtime=200ms; then
    record_fail 10 "性能基准" "bench 执行失败：$(head -1 "$tmp_dir/bench.err")"
    return
  fi
  if ! grep -q '^BenchmarkLiteralEval' "$raw"; then
    record_fail 10 "性能基准" "输出无 'BenchmarkLiteralEval' 基准行（假绿形态）"
    return
  fi
  if [ "$RECORD_BENCH" -eq 1 ]; then
    mkdir -p "$(dirname "$BENCH_BASELINE")"
    {
      echo "# recorded_by: scripts/check_field_semantics.sh --record-bench-baseline"
      echo "# commit: $(cd "$REPO_DIR" && git rev-parse --short HEAD 2>/dev/null || echo unknown)"
      echo "# date: $(date -Is)"
      cat "$raw"
    } > "$BENCH_BASELINE"
    record_pass 10 "性能基准" "已记录基线到 $BENCH_BASELINE（基线须在 main 上产生）"
    return
  fi
  if [ ! -f "$BENCH_BASELINE" ]; then
    record_skip 10 "性能基准" "SKIP(baseline-missing)：$BENCH_BASELINE 不存在；先在 main 上 --record-bench-baseline"
    return
  fi
  if ! command -v benchstat >/dev/null 2>&1; then
    record_skip 10 "性能基准" "SKIP(benchstat-missing)：未安装 benchstat（CI 需 go install golang.org/x/perf/cmd/benchstat@latest）"
    return
  fi
  local cmp="$tmp_dir/benchstat.out"
  if benchstat -fail "$BENCH_BASELINE" "$raw" > "$cmp" 2>&1; then
    record_pass 10 "性能基准" "benchstat -fail 通过（≤基线+5%）"
  else
    record_fail 10 "性能基准" "benchstat 判定退化 >5%：$(grep -m1 'p=' "$cmp" || tail -2 "$cmp" | tr '\n' ' ')"
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
  if run_in_backend "$tmp_dir/st.out" "$tmp_dir/st.err" "$GO" run ./scripts/fieldsem -drivers "$ast_dir/drivers" -mode decl; then
    echo "SELFTEST FAIL: 幽灵声明未变红"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 幽灵声明 ⇒ 门禁红"
  fi
  {
    echo 'package drivers'
    echo 'func (d *T) ParseData2(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "ghost_field", Value: 2}}, nil'
    echo '}'
  } > "$ast_dir/drivers/b.go"
  if run_in_backend "$tmp_dir/st2.out" "$tmp_dir/st2.err" "$GO" run ./scripts/fieldsem -drivers "$ast_dir/drivers" -mode decl; then
    echo "SELFTEST PASS: 有产出 ⇒ 门禁绿"
  else
    echo "SELFTEST FAIL: 有产出仍红"; failures=$((failures + 1))
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
  if run_in_backend "$tmp_dir/st3.out" "$tmp_dir/st3.err" "$GO" run ./scripts/fieldsem -drivers "$name_dir/drivers" -mode name; then
    echo "SELFTEST FAIL: 非法产出名未变红"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 非法产出名 ⇒ 门禁红"
  fi
  {
    echo 'package drivers'
    echo 'type SensorData struct { Name string; Value float64; Unit string }'
    echo 'type T struct{}'
    echo 'func (d *T) ParseData(raw []byte) ([]SensorData, error) {'
    echo '  return []SensorData{{Name: "write_ack", Value: 1}}, nil'
    echo '}'
  } > "$name_dir/drivers/c.go"
  if run_in_backend "$tmp_dir/st4.out" "$tmp_dir/st4.err" "$GO" run ./scripts/fieldsem -drivers "$name_dir/drivers" -mode name; then
    echo "SELFTEST PASS: *_ack 例外 ⇒ 门禁绿"
  else
    echo "SELFTEST FAIL: *_ack 例外被判红（应列入例外清单）"; failures=$((failures + 1))
  fi

  printf 'ok  	ehome/backend/internal/sensorfield	0.001s\n' > "$tmp_dir/fake8.out"
  if grep -q '^=== RUN' "$tmp_dir/fake8.out"; then
    echo "SELFTEST FAIL: 空输出被当成有 RUN 行"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 无 '=== RUN' ⇒ 门禁 #8 判红（反假绿）"
  fi

  printf 'no benchmarks found\n' > "$tmp_dir/fake10.out"
  if grep -q '^BenchmarkLiteralEval' "$tmp_dir/fake10.out"; then
    echo "SELFTEST FAIL: 无 bench 输出被判有"; failures=$((failures + 1))
  else
    echo "SELFTEST PASS: 无 benchmark ⇒ 门禁 #10 判红（不是 PASS）"
  fi
  if [ ! -f "$BENCH_BASELINE" ]; then
    echo "SELFTEST PASS: 基线缺失 ⇒ 门禁 #10 走 SKIP(baseline-missing)"
  else
    echo "SELFTEST WARN: 基线已存在，跳过 baseline-missing 断言"
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
  local fails=0 skips=0 passes=0 status num title detail
  while IFS='|' read -r status num title detail; do
    case "$status" in
      PASS) passes=$((passes + 1)) ;;
      FAIL) fails=$((fails + 1)) ;;
      SKIP) skips=$((skips + 1)) ;;
    esac
  done <<EOF
$RESULTS
EOF
  echo ""
  echo "== 门禁结果 =="
  while IFS='|' read -r status num title detail; do
    if [ -n "$status" ]; then
      printf '[%s] #%s %s — %s\n' "$status" "$num" "$title" "$detail"
    fi
  done <<EOF
$RESULTS
EOF
  echo ""
  if [ "$fails" -gt 0 ]; then
    echo "RESULT: FAIL($fails) — 门禁未通过（PASS=$passes SKIP=$skips）"
    return 1
  fi
  if [ "$skips" -gt 0 ]; then
    if [ "$STRICT" -eq 1 ]; then
      echo "RESULT: FAIL_STRICT_SKIP($skips) — 严格模式不允许 SKIP（PASS=$passes）"
      return 1
    fi
    echo "RESULT: PASS_WITH_SKIPS($skips) — 通过 $passes 条；SKIP 原因见上（D6 完成后用 --strict）"
    return 0
  fi
  echo "RESULT: PASS — $passes 条门禁全部通过"
  return 0
}

run_gate() {
  case "$1" in
    1) gate1 ;; 2) gate2 ;; 3) gate3 ;; 4) gate4 ;; 5) gate5 ;;
    6) gate6 ;; 7) gate7 ;; 8) gate8 ;; 9) gate9 ;; 10) gate10 ;; 11) gate11 ;;
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
