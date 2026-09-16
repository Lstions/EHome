#!/usr/bin/env bash
# 黑盒验证 · 秘密脱敏（共享实现，**所有写容器日志的探针都必须用**）
#
# ===== 为什么需要它（一次真实泄漏事故）=====
# 2026-09-16：主控全证据目录扫描发现 **15 个文件含明文一次性初始化凭据**，且**已提交进 git**：
#   grep -rlE 'Initialization credential \(valid for 10 minutes\): [A-Za-z0-9]{20,}' \
#        deploy/blackbox/evidence/   # → 15 个文件命中
# 泄漏点：探针用 `docker logs "$WEB" > "$EVID/*.log"` **原样落盘**，
# 而容器启动日志里含一次性凭据行。
#
# 根因不是「忘了脱敏」，而是**脱敏只做在了 05-first-boot 一处**，
# 而 03-coldstart / 06-wiring 也 dump 日志却没脱敏 ——
# 作者（子代理 C）的原话：「我只把脱敏放在 05，错误假设『凭据是 05 的事』」。
#
# ⇒ 教训：**脱敏是「写日志」这个动作的属性，不是某个探针的职责**。
#   故抽成共享函数，并要求**每个产生日志文件的探针**在写完后调用。
#
# 严重性评估（如实）：泄漏的是一次性凭据，**10 分钟有效**且其承载库（ehome_bb_*）
# 已被收尾 DROP ⇒ 事后不可用。但**「已过期」不构成不修的理由**：
#   ① 若将来有人把证据目录用于演示/分享，凭据形状本身有误导性；
#   ② 判据应建立在「不写入」而非「写了但过期了」。
#
# ===== 用法 =====
#   source "$SCRIPT_DIR/_redact.sh"
#   docker logs "$WEB" > "$EVID/x.log" 2>&1 || true
#   bb_redact_file "$EVID/x.log"          # ← 写完立即脱敏
#   bb_redact_tree "$EVID"                # ← 末尾全局兜底（可选但推荐）
set -u

# 凭据形状：`<selector>.<secret>`，总长 >= 20 的 base64url 风格串。
# 两段都脱敏，避免只红一段仍可被拼接。
BB_CRED_RE='[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}'

# bb_redact_text <file> : 就地脱敏一个文本文件（幂等）。
# 用 python3 而不是 sed —— 实测 GNU sed 的 ERE `{m,n}` 方言差异会让
# 脱敏**静默失效**（报 Invalid preceding regular expression），而静默失效正是最危险的形态。
bb_redact_file() {
  local f="${1:?file required}"
  [ -f "$f" ] || return 0
  python3 - "$f" <<'PY' 2>/dev/null || return 0
import re, sys
p = sys.argv[1]
pat = re.compile(r'[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}')
try:
    s = open(p, encoding='utf-8', errors='replace').read()
except Exception:
    sys.exit(0)
new = pat.sub('<REDACTED:credential>', s)
if new != s:
    open(p, 'w', encoding='utf-8').write(new)
PY
}

# bb_redact_tree <dir> : 对目录下所有文本类文件做兜底脱敏。
bb_redact_tree() {
  local d="${1:?dir required}"
  [ -d "$d" ] || return 0
  local f
  while IFS= read -r f; do
    bb_redact_file "$f"
  done < <(find "$d" -type f \( -name '*.log' -o -name '*.txt' -o -name '*.raw' -o -name '*.json' -o -name '*.out' \) 2>/dev/null)
}

# bb_scan_credentials <dir> : 扫描残留明文凭据，返回命中文件数（0=干净）。
# 供探针自检与主控复核共用 —— **同一份判据**，避免两边不一致。
bb_scan_credentials() {
  local d="${1:?dir required}"
  [ -d "$d" ] || { echo 0; return 0; }
  grep -rlE 'Initialization credential \(valid for 10 minutes\): [A-Za-z0-9]{20,}' "$d" 2>/dev/null | wc -l | tr -d ' '
}
