#!/usr/bin/env bash
# 秘密脱敏门禁（回归锁）：**凡 dump 容器日志的探针，必须脱敏**。
#
# ===== 为什么需要它（一次真实泄漏事故）=====
# 2026-09-16 主控全证据扫描发现 **15 个文件含明文一次性初始化凭据且已入 git**。
# 根因：脱敏只做在 05-first-boot 一处，而 03-coldstart / 06-wiring 也 dump 容器日志却没脱敏。
# 作者原话：「我只把脱敏放在 05，错误假设『凭据是 05 的事』」。
#
# ⇒ 已抽共享实现 `_redact.sh`；本门禁负责**防止将来新增的探针再犯**。
#
# ===== 判据 =====
# 对每个 probes/0*.sh：
#   · 若它把 `docker logs` 重定向到文件（含 `>` 落盘）⇒ 该文件内必须出现脱敏调用；
#   · 否则报红并指出该文件。
# 另：
#   · **source 行必须能真正解析**（主控实测教训：曾把 `_redact.sh` 的 source 路径写错，
#     而当时门禁只 grep「有没有调用」—— 路径坏掉也能通过 ⇒ 假绿。故必须**实际 source 一次**）。
#   · 分母守卫：必须真的扫到 >= 4 个探针（否则门禁空转）；
#   · 全证据目录扫描：不得存在明文凭据（用与探针同一份判据 `bb_scan_credentials`）。
#
# ===== 本门禁查不了什么（诚实声明）=====
#   · 查不了「凭据以外」的其它秘密（如 API key、密码）—— 那需要各自的形状定义；
#   · 查不了脱敏函数**是否真的生效**（行为验证在 _redact.sh 的自检与各探针里）；
#   · ~~不覆盖 run.sh 自己写的日志~~ **2026-09-16 已纳入**：run.sh 也 dump 容器日志
#     （S1 的 logs-excerpt.txt），实测确实泄漏过 ⇒ 必须一并检。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BB_ROOT="$(cd "$HERE/../../.." && pwd)"
source "$HERE/_redact.sh"

FAIL=0
SCANNED=0

echo "== 秘密脱敏门禁 =="
for f in "$HERE"/0*.sh; do
  [ -f "$f" ] || continue
  SCANNED=$((SCANNED+1))
  base="$(basename "$f")"
  # 该探针是否把 docker logs 落盘？（允许 `docker logs ... > file` 或 `docker logs ... | tee file`）
  if grep -qE 'docker logs[^|]*>[^&]' "$f" || grep -qE 'docker logs.*tee ' "$f"; then
    if grep -qE 'bb_redact_(file|tree)|redact_file' "$f"; then
      # 附加：**实际 source 一次**，验证路径可解析（主控曾把路径写错而门禁仍绿）
      src_line="$(grep -m1 '_redact\.sh' "$f" || true)"
      if bash -c "cd '$HERE'; $src_line; declare -F bb_redact_file >/dev/null" 2>/dev/null; then
        echo "  [PASS] $base: dump 容器日志且已脱敏（source 路径可解析）"
      else
        echo "  [FAIL] $base: 有脱敏调用，但 **source 行无法解析** ⇒ 脱敏不会生效（假绿）"
        echo "         源行: $src_line"
        FAIL=$((FAIL+1))
      fi
    else
      echo "  [FAIL] $base: **dump 容器日志但未脱敏** —— 会把一次性凭据写入证据（2026-09-16 真实事故）"
      FAIL=$((FAIL+1))
    fi
  else
    echo "  [SKIP] $base: 未 dump 容器日志，无需脱敏"
  fi
done

# 分母守卫：防止扫描器空转
if [ "$SCANNED" -lt 4 ]; then
  echo "  [FAIL] 分母守卫: 只扫到 $SCANNED 个探针（期望 >=4）—— 门禁形同虚设"
  FAIL=$((FAIL+1))
else
  echo "  [PASS] 分母守卫: 扫描 $SCANNED 个探针（>=4）"
fi

# run.sh 自己也写容器日志 ⇒ 同样必须脱敏（实测泄漏过）
RUN_SH="$HERE/../run.sh"
if [ -f "$RUN_SH" ] && grep -qE 'docker logs' "$RUN_SH"; then
  if grep -qE 'bb_redact_(file|tree)' "$RUN_SH"; then
    echo "  [PASS] run.sh: 也写容器日志且已脱敏"
  else
    echo "  [FAIL] run.sh: **也写容器日志但未脱敏** —— S1 的 logs-excerpt.txt 曾泄漏凭据"
    FAIL=$((FAIL+1))
  fi
fi

# 全证据目录：不得含明文凭据
EVID_DIR="$BB_ROOT/deploy/blackbox/evidence"
if [ -d "$EVID_DIR" ]; then
  N="$(bb_scan_credentials "$EVID_DIR")"
  if [ "$N" = "0" ]; then
    echo "  [PASS] 证据目录无明文一次性凭据"
  else
    echo "  [FAIL] 证据目录仍有 $N 个文件含明文凭据:"
    grep -rlE 'Initialization credential \(valid for 10 minutes\): [A-Za-z0-9]{20,}' "$EVID_DIR" 2>/dev/null | sed 's/^/         /' | head -10
    FAIL=$((FAIL+1))
  fi
fi

echo "== 汇总: FAIL=$FAIL =="
[ "$FAIL" -eq 0 ] && exit 0 || exit 1