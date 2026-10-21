#!/bin/bash
# D1 门禁 #9：变异自证（改坏 -> 必须红 -> 恢复 -> md5 一致）。
# 用法：bash .analysis/tools/mutation_check.sh
set -u
cd "$(dirname "$0")/../../backend" || exit 1
export PATH=/mnt/storage/WorkSpace/EHome/.tools/gotool/bin:$PATH
export GOCACHE=/tmp/eh-wt-cache
SF=internal/sensorfield
PASS=0
FAIL=0

mutate() {
  local name="$1" file="$2" from="$3" to="$4" testregex="$5"
  local before after out
  before=$(md5sum "$file" | cut -d' ' -f1)
  python3 - "$file" "$from" "$to" <<'PY'
import io, sys
path, frm, to = sys.argv[1], sys.argv[2], sys.argv[3]
src = io.open(path, encoding="utf-8").read()
if frm not in src:
    print("MISSING_MARKER")
    sys.exit(2)
io.open(path, "w", encoding="utf-8").write(src.replace(frm, to, 1))
PY
  if [ $? -ne 0 ]; then
    echo "MUTATION $name: FAILED_TO_APPLY"
    FAIL=$((FAIL+1))
    return
  fi
  out=$(go test "./$SF/" -run "$testregex" -count=1 2>&1)
  if [ $? -ne 0 ]; then
    echo "MUTATION $name: CAUGHT (测试按预期变红)"
    echo "$out" | grep -E '^--- FAIL|^    --- FAIL|^\s+[a-z_]+_test.go:[0-9]+:' | head -2 | sed 's/^/    /'
    PASS=$((PASS+1))
  else
    echo "MUTATION $name: NOT_CAUGHT (测试仍绿 = 测试空洞!)"
    FAIL=$((FAIL+1))
  fi
  git checkout -- "$file"
  after=$(md5sum "$file" | cut -d' ' -f1)
  if [ "$before" = "$after" ]; then
    echo "    restore: md5 一致 ($before)"
  else
    echo "    restore: MD5 MISMATCH ($before != $after)"
    FAIL=$((FAIL+1))
  fi
}

mutate "M1-default-aggregate-any-to-all" $SF/selector.go \
  'prefix: prefix, aggregate: AggAny}' 'prefix: prefix, aggregate: AggAll}' \
  'TestResolveFamily'

mutate "M2-remove-missing-member-detection" $SF/eval.go \
  's.missing = len(known) - s.foundKnown' 's.missing = 0' \
  'TestEvalPredicateTruthTable216'

mutate "M3-family-pure-prefix" $SF/selector.go \
  'if !isDigitASCII(c) {' 'if false && !isDigitASCII(c) {' \
  'TestFamilyDerivedAggregatesExcluded'

mutate "M4-literal-takes-last" $SF/eval.go \
  'if frame[i].Name == p.literal {' 'if frame[i].Name == p.literal && i == len(frame)-1 {' \
  'TestLiteralEvalEquivalence'

mutate "M5-remove-killswitch-eval-check" $SF/eval.go \
  'func (p *familyPlan) disabled() bool { return p.enabled != nil && !p.enabled.Load() }' \
  'func (p *familyPlan) disabled() bool { return false }' \
  'TestKillSwitch'

mutate "M6-remove-family-minmax-degradation" $SF/eval.go \
  'degradeReason = familyDegradedReason(s)' 'degradeReason = ""' \
  'TestEvalPredicateTruthTable216'

mutate "M7-virtual-sum_of-should-not-degrade" $SF/eval.go \
  'if n.fn == "sum_of" {' 'if false {' \
  'TestVirtualFamilyReduction'

echo "=== MUTATION SUMMARY: caught=$PASS failed=$FAIL ==="
echo "=== FINAL FULL SUITE ==="
go test "./$SF/" -count=1 2>&1 | tail -2
