#!/usr/bin/env bash
# 黑盒验证 · 秘密脱敏（共享实现，**所有写容器日志的探针都必须用**）
#
# ===== 为什么需要它（两次真实泄漏事故）=====
# 事故① 2026-09-16：证据目录 15 个文件含明文**一次性初始化凭据**且已提交进 git。
#   根因不是「忘了脱敏」，而是**脱敏只做在 05-first-boot 一处** ——
#   「脱敏是『写日志』这个动作的属性，不是某个探针的职责」。
#
# 事故② 2026-09-16（同一天，更严重，且**真的推到了 GitHub**）：
#   00-compose-config-layered.json 是 `docker compose config` 的完整渲染结果，
#   里面是**真实生产值**：
#       "EHOME_JWT_SECRET": "ehome_jwt_secret_key_..."   ← 与本机 .env 逐字相同
#       "POSTGRES_PASSWORD": "ehome123"
#   含 37 个已跟踪文件，随 abe3a7b0 推送。
#   根因两条：(a) **判据漏掉的那一类，等于不存在** —— 原 bb_scan_credentials 只认规则①
#   （`<selector>.<secret>` 形状），键值对型秘密一个字符都扫不到，门禁在事故现场**全绿**；
#   (b) `.gitignore` 只写了 `deploy/ghcr/evidence/` 却漏了 `deploy/blackbox/evidence/`。
#   ⇒ 因此本文件里「脱敏」与「扫描」必须共用同一套规则、同一份敏感键名、**同一个文件集**。
#
# ===== 用法 =====
#   source "$SCRIPT_DIR/_redact.sh"
#   docker logs "$WEB" > "$EVID/x.log" 2>&1 || true
#   bb_redact_file "$EVID/x.log"          # ← 写完立即脱敏
#   bb_redact_tree "$EVID"                # ← 末尾全局兜底（推荐）
#   bb_scan_credentials "$EVID"           # ← 自检：0 = 干净（BB_SCAN_VERBOSE=1 打印命中文件）
set -u

# 规则① 的形状：`<selector>.<secret>`，两段各 >=20 的 base64url 风格串。
BB_CRED_RE='[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}'

# 敏感键名（大小写不敏感）。脱敏与扫描共用这一份，避免两边漂移。
# 注意：这里描述的是**键名**，不是值的形状 —— 值可以是任意字符串，
# 按形状判定必然漏（这正是事故② 的教训）。
BB_SENSITIVE_KEY='PASSWORD|PASSWD|SECRET|TOKEN|API[_-]?KEY|PRIVATE[_-]?KEY|CREDENTIAL'

# bb_text_files <dir> : 列出「可脱敏的文本产物」。
#
# **必须被 bb_redact_tree 与 bb_scan_credentials 共用** —— 本文件第三次踩同一个坑：
#   ① 事故① 只脱敏 05 一处；
#   ② 事故② 判据只认规则①；
#   ③ 本次：脱敏只处理 *.log/*.txt/*.json/*.env/*.yml，而扫描 grep 整个目录，
#      于是 .sh 里的 `secret_len=$SEC_LEN`（长度变量，不是秘密）被扫出来，
#      扫描**永远报 4，永远不可能变绿**。
#   ⇒ 一��永远红的门禁 = 一个会被收窄到失效的门禁（本仓已记载该失败模式）。
#      判据的作用域必须与实现的作用域**逐字相同**。
#
# 覆盖范围刻意包含 .env/.yml/.yaml：同一份 compose 内容换个 dump 方式就是这些后缀。
# 刻意不含 .sh/.md/.tsv：那是探针源码与结论报告，不是容器 dump；
# 改写会破坏可复现性，且它们的 SECRET 字样是断言文本。
bb_text_files() {
  local d="${1:?dir required}"
  [ -d "$d" ] || return 0
  find "$d" -type f \( -name '*.log' -o -name '*.txt' -o -name '*.raw' \
         -o -name '*.json' -o -name '*.out' -o -name '*.env' \
         -o -name '*.yml' -o -name '*.yaml' \) 2>/dev/null
}

# bb_redact_file <file> : 就地脱敏一个文本文件（**幂等**）。
# 用 python3 而不是 sed —— 实测 GNU sed 的 ERE `{m,n}` 方言差异会让
# 脱敏**静默失效**，而静默失效正是最危险的形态。
bb_redact_file() {
  local f="${1:?file required}"
  [ -f "$f" ] || return 0
  # 敏感键名走 argv 而非 env —��� 未 export 的环境变量 python 看不到，
  # 会静默退回硬编码默认值，那就又变成「两处定义、悄悄漂移」。
  python3 - "$f" "$BB_SENSITIVE_KEY" <<'PY' 2>/dev/null || return 0
import re, sys
p = sys.argv[1]
sensitive = sys.argv[2]

# 规则①：一次性初始化凭据，形态 <selector>.<secret>（base64url，两段各 >=20）。
pat = re.compile(r'[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}')

try:
    s = open(p, encoding='utf-8', errors='replace').read()
except Exception:
    sys.exit(0)

new = pat.sub('<REDACTED:credential>', s)

# 规则②：JSON 键值对 —— "SensitiveKey": "value"
# 覆盖 docker compose config / 容器 inspect 的渲染结果。
new = re.sub(
    r'("[^"]*(?:' + sensitive + r')[^"]*"[ \t]*:[ \t]*")([^"]*)(")',
    lambda m: m.group(1) + '<REDACTED:secret>' + m.group(3),
    new, flags=re.I)

# 规则③：dotenv / YAML / shell —— KEY=value 或 KEY: value
# **键名字符集必须收紧到 [A-Za-z0-9_.-]**，且必须行首锚定：
# 第一版写成 `(\s*\S*(?:SENSITIVE)\S*\s*[:=]\s*)(\S+)`，`\S*` 会跨越 `"` 与 `:`，
# 在单行 JSON 上从行首一路吃到某个 SECRET，把整行剩余内容（含无关键）都替换掉，
# 且 `<` 也是 \S ⇒ 重跑一次还会再改 ⇒ 既不精确也不幂等。
new = re.sub(
    r'(?m)^([ \t]*[A-Za-z0-9_.-]*(?:' + sensitive + r')[A-Za-z0-9_.-]*[ \t]*[:=][ \t]*)(\S+)',
    lambda m: m.group(1) + '<REDACTED:secret>',
    new, flags=re.I)

if new != s:
    open(p, 'w', encoding='utf-8').write(new)
PY
}

# bb_redact_tree <dir> : 对目录下所有可脱敏文本产物做兜底脱敏。
bb_redact_tree() {
  local d="${1:?dir required}"
  [ -d "$d" ] || return 0
  local f
  while IFS= read -r f; do
    bb_redact_file "$f"
  done < <(bb_text_files "$d")
}

# bb_scan_credentials <dir> : 扫描残留明文凭据，返回命中文件数（0=干净）。
# 供探针自检与主控复核共用 —— **同一份判据**。
#
# 判据与 bb_redact_file 的规则一一对应；**作用域与 bb_redact_tree 逐字相同**
# （用 bb_text_files），否则会报出永远修不掉的假阳性。
# 已脱敏的值以 '<' 开头，故用 [^<] 排除。BB_SCAN_VERBOSE=1 时打印命中文件名。
bb_scan_credentials() {
  local d="${1:?dir required}"
  [ -d "$d" ] || { echo 0; return 0; }
  local re
  re='Initialization credential \(valid for 10 minutes\): [A-Za-z0-9]{20,}'
  re="${re}|\"[^\"]*(${BB_SENSITIVE_KEY})[^\"]*\"[[:space:]]*:[[:space:]]*\"[^\"<]"
  re="${re}|^[[:space:]]*[A-Za-z0-9_.-]*(${BB_SENSITIVE_KEY})[A-Za-z0-9_.-]*[[:space:]]*[:=][[:space:]]*[^<[:space:]]"
  local n=0 f
  while IFS= read -r f; do
    if grep -qE "$re" "$f" 2>/dev/null; then
      n=$((n + 1))
      if [ -n "${BB_SCAN_VERBOSE:-}" ]; then echo "  HIT: $f" >&2; fi
    fi
  done < <(bb_text_files "$d")
  echo "$n"
}
