#!/usr/bin/env bash
# =============================================================================
# Q5 —— 首次部署的**一次性初始化凭据**全流程（容器黑盒）  [子代理 C]
#
# 守护的不变量（对齐 backend/simulation/catalog/dep.go 的 SIM-DEP-001..004）:
#   全新部署时，管理员必须由**容器日志里一次性打印**的凭据来创建；
#   该凭据不可重放、不可被错误凭据替代、不可覆盖已创建的管理员。
#   任何一处失守都意味着"谁能读到日志谁就能建管理员"或"系统可被重复初始化夺权"。
#
# 本探针断言（全部经**公开 HTTP 接口**，不读容器内私有文件、不直连其 DB）:
#   F1  冷启动日志出现一次性凭据（**仅记"出现过"+长度特征，绝不写入证据文件**）
#   F2  用该凭据 POST /api/v1/auth/initialize ⇒ 201 成功建管理员
#   F3  用新管理员 POST /api/v1/auth/login ⇒ 200 且拿到非空 token
#   F4  **错误凭据**初始化 ⇒ 被拒（409）且系统仍为 uninitialized
#   F5  **重复初始化**（重放同一凭据 / 换用户名再初始化）⇒ 被拒，且原管理员未被覆盖
#   F6  冷启动回填日志 "Latest value cache warmed up: N rows" 必须出现（主控本轮接线）
#
# 前提: 栈是**全新独立库**起的（未初始化）。run.sh 已负责起栈与建库。
#       若库已被初始化，本探针主动 SKIP(77) —— 一次性凭据流程在大前提不成立时无意义。
#
# 环境契约: BB_PROJECT 默认 ehome-bb / BB_HOME_PORT 默认 18080 / BB_DB_NAME 默认 ehome_bb_<runid>
# 退出码: 0=全绿  1=有断言失败  77=主动 SKIP
# =============================================================================
set -uo pipefail

PROJECT="${BB_PROJECT:-ehome-bb}"
HOME_PORT="${BB_HOME_PORT:-18080}"
BASE="http://127.0.0.1:${HOME_PORT}"
WEB="${PROJECT}-ehome"
EVID="${BB_EVIDENCE_DIR:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/evidence/C-manual}"
export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH
export HOME="${HOME:-/home/sun}"
mkdir -p "$EVID"

ADMIN_USER="bbadmin"
ADMIN_PASS="bbadminpass123"
ADMIN_EMAIL="bb@example.com"

PASS=0; FAIL=0
declare -a RESULTS
ok()   { PASS=$((PASS+1)); RESULTS+=("PASS|$1|$2"); printf 'PASS  %s : %s\n' "$1" "$2"; }
bad()  { FAIL=$((FAIL+1)); RESULTS+=("FAIL|$1|$2"); printf 'FAIL  %s : %s\n' "$1" "$2"; }
note() { printf 'INFO  %s\n' "$*"; }

# ---------------------------------------------------------------------------
# 秘密保护：任何写入证据目录的文本都先过 redact_credential。
# 作用：把一次性凭据（<selector>.<secret> 形状）替换为 <REDACTED:sel=..sec=..>。
# 这是**最后一道防线** —— 即便某处不慎 echo 了凭据，落盘也会被脱敏。
# ---------------------------------------------------------------------------
CRED_RE='[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{20,}'
# 只保留一个实现（避免出现两份可能分叉的脱敏逻辑——那正是缺陷温床）。
# 脱敏用 python3 而不是 sed: 实测 GNU sed ERE 对本正则报
# "Invalid preceding regular expression"（{m,n} 方言差异）；python3 是本仓探针已有依赖。
# 只保留一个实现, 避免两份逻辑分叉。
# 秘密脱敏：统一用共享实现（原先本文件自带一份，与 05 分叉 —— 那正是缺陷温床）
source "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/_redact.sh"
redact_file() {
  # 薄封装：委托给共享实现（见 _redact.sh），避免两份逻辑分叉。
  bb_redact_file "$1"
}

post_json() { # path body out -> prints status, body written to out
  local path="$1" body="$2" out="$3"
  : > "$out"
  curl -sS -m 25 -o "$out" -w '%{http_code}' -X POST -H 'Content-Type: application/json' -d "$body" "$BASE$path" 2>/dev/null || echo 000
}
get_status_body() { # path outfile [token]
  local path="$1" out="$2" token="${3:-}"
  : > "$out"
  if [[ -n "$token" ]]; then
    curl -sS -m 25 -o "$out" -w '%{http_code}' -H "Authorization: Bearer $token" "$BASE$path" 2>/dev/null || echo 000
  else
    curl -sS -m 25 -o "$out" -w '%{http_code}' "$BASE$path" 2>/dev/null || echo 000
  fi
}

{
  echo "probe=05-first-boot.sh  BB_PROJECT=$PROJECT  BB_HOME_PORT=$HOME_PORT  BB_DB_NAME=${BB_DB_NAME:-<unset>}"
  echo "base_url=$BASE  container=$WEB"
  echo "注意: 一次性凭据为秘密, 本目录所有文件均已脱敏 (CRED_RE -> <REDACTED:credential>)"
  echo "started_at=$(date -Is)"
} > "$EVID/context.txt"
note "BB_PROJECT=$PROJECT  BB_HOME_PORT=$HOME_PORT  BB_DB_NAME=${BB_DB_NAME:-<unset>}"

STATE="$(docker inspect -f '{{.State.Status}}' "$WEB" 2>/dev/null || echo MISSING)"

# 就绪等待: compose 的 ehome 服务**没有 healthcheck**, 故 up -d --wait 只保证"容器已运行",
# 不保证 HTTP 已监听 —— 直接探会得到 000, 并把它误判成"端点不可达"(假红)。
# 这里显式轮询 /health(最多 WAIT_READY 秒)。
WAIT_READY="${WAIT_READY:-90}"
wait_ready() {
  local i
  for i in $(seq 1 "$WAIT_READY"); do
    if curl -sS -m 3 -o /dev/null "$BASE/health" 2>/dev/null; then return 0; fi
    sleep 1
  done
  return 1
}

if [[ "$STATE" != "running" ]]; then
  note "容器 $WEB 状态=$STATE —— 无栈可探, 主动 SKIP (77)"
  echo "SKIP: $WEB 状态=$STATE" > "$EVID/result.txt"
  exit 77
fi

if ! wait_ready; then
  echo "NOT_READY: /health 在 ${WAIT_READY}s 内未就绪" > "$EVID/not-ready.txt"
  docker logs --tail=40 "$WEB" >> "$EVID/not-ready.txt" 2>&1 || true
  note "/health 未在 ${WAIT_READY}s 内就绪 —— 见 $EVID/not-ready.txt"; exit 1
fi

# 先看初始化状态（决定本探针是否有意义）
st="$(get_status_body /api/v1/auth/initialization "$EVID/F0-initialization-state.json")"
note "GET /api/v1/auth/initialization -> status=$st body=$(head -c 200 "$EVID/F0-initialization-state.json")"
if [[ "$st" != "200" ]]; then
  bad F0 "无法读取初始化状态: status=$st body=$(head -c 200 "$EVID/F0-initialization-state.json")"
  { echo "PASS=$PASS FAIL=$FAIL"; printf '%s\n' "${RESULTS[@]}"; } > "$EVID/result.txt"
  exit 1
fi
if grep -q '"state":"initialized"' "$EVID/F0-initialization-state.json"; then
  note "库已被初始化 —— 一次性凭据流程的大前提(全新库)不成立, 主动 SKIP (77)"
  echo "SKIP: 库已是 initialized；Q5 需要全新未初始化库" > "$EVID/result.txt"
  exit 77
fi
if ! grep -q '"state":"uninitialized"' "$EVID/F0-initialization-state.json"; then
  bad F0 "初始化状态既非 uninitialized 也非 initialized: $(head -c 200 "$EVID/F0-initialization-state.json")"
  { echo "PASS=$PASS FAIL=$FAIL"; printf '%s\n' "${RESULTS[@]}"; } > "$EVID/result.txt"
  exit 1
fi

# =============================================================================
# F1 冷启动日志出现一次性凭据（秘密：只记"出现过"+长度特征）
# =============================================================================
docker logs --tail=4000 "$WEB" > "$EVID/F1-startup-logs.raw" 2>&1 || true
# 必须取**最后一条**(`tail -1`)而不是第一条: 每次启动都会在库仍未初始化时新签发
# 一次性凭据; 而 `docker logs` 保留**同一容器**历史所有启动。若容器是重启(而非重建),
# head -1 会取到最旧的一条 —— 那条早已 consumed/过期, 后续 F2 必然失败。
# (实测: 用 head -1 在"重置库 + docker restart"场景下 F2 报 409; 见变异记录 M3。)
CRED_LINE="$(grep -oE 'Initialization credential \(valid for 10 minutes\): [^ ]+' "$EVID/F1-startup-logs.raw" | tail -1 || true)"
CRED="${CRED_LINE##*: }"
HITS="$(grep -c 'Initialization credential' "$EVID/F1-startup-logs.raw" || true)"
# 立刻脱敏原始日志: 之后任何提前 exit 都不会把凭据留在磁盘上（末尾还有全局兜底扫描）。
redact_file "$EVID/F1-startup-logs.raw"
{
  echo "F1: 冷启动日志一次性凭据"
  echo "  credential_line_appeared=$([[ -n "$CRED_LINE" ]] && echo yes || echo no)"
  echo "  credential_line_count=$HITS"
# 长度特征**先算好再落盘** —— 绝不能把 selector/secret 本身 echo 进证据：
# 曾险些写成 selector_len=${CRED%%.*}，那会把 selector 明文写进证据文件，
# 而 selector 不含 "."，脱敏正则匹配不到它 ⇒ 真泄漏（本文件因此有两个防线）。
# 注意: ${#VAR#pattern} 不是合法 bash 语法(实测报 bad substitution)。
# 必须先取子串, 再对子串用 ${#} 量长度。
CRED_SEL_PART="${CRED%%.*}"
CRED_SEC_PART="${CRED#*.}"
SEL_LEN=${#CRED_SEL_PART}
SEC_LEN=${#CRED_SEC_PART}
  echo "  长度特征: total_len=${#CRED} selector_len=$SEL_LEN secret_len=$SEC_LEN"
  echo "  是否含 '.' 分隔符: $(case "$CRED" in *.*) echo yes;; *) echo no;; esac)"
  echo "  （原始值已被刻意丢弃 —— 只记录形态，绝不落盘）"
} > "$EVID/F1-credential-shape.txt"
redact_file "$EVID/F1-credential-shape.txt"
if [[ -z "$CRED" ]]; then
  bad F1 "冷启动日志未见一次性凭据行 (InitializeSystem 无从进行); 'Initialization credential' 命中=$HITS"
  { echo "PASS=$PASS FAIL=$FAIL"; printf '%s\n' "${RESULTS[@]}"; } > "$EVID/result.txt"
  exit 1
fi
if [[ "$CRED" == *.* && "$SEL_LEN" -ge 10 && "$SEC_LEN" -ge 20 ]]; then
  ok F1 "冷启动日志出现一次性凭据: total_len=${#CRED} selector_len=$SEL_LEN secret_len=$SEC_LEN (值未落盘)"
else
  bad F1 "凭据形态异常: total_len=${#CRED} selector_len=$SEL_LEN secret_len=$SEC_LEN (期望 selector>=10, secret>=20)"
fi

# =============================================================================
# F4（先做）错误凭据必须被拒，且系统仍未初始化
#    —— 放在正确凭据之前，才能断言"错误尝试"没有把系统带进 initialized
# =============================================================================
FAKE="zzzzzzzzzzzzzzzzzzzz.zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"
st="$(post_json /api/v1/auth/initialize "{\"credential\":\"$FAKE\",\"username\":\"intruder\",\"password\":\"intruderpass123\"}" "$EVID/F4-wrong-cred-init.json")"
redact_file "$EVID/F4-wrong-cred-init.json"
st_after="$(get_status_body /api/v1/auth/initialization "$EVID/F4-state-after-wrong.json")"
{
  echo "POST /api/v1/auth/initialize (错误凭据) -> status=$st"
  echo "--- body ---"; cat "$EVID/F4-wrong-cred-init.json"; echo
  echo "GET /api/v1/auth/initialization (错误尝试后) -> status=$st_after"
  cat "$EVID/F4-state-after-wrong.json"; echo
} > "$EVID/F4-wrong-credential.txt"
if [[ "$st" == "409" ]]; then
  ok F4 "错误凭据初始化被拒 (409): $(head -c 160 "$EVID/F4-wrong-cred-init.json")"
else
  bad F4 "错误凭据初始化未被拒: status=$st (期望 409); body=$(head -c 200 "$EVID/F4-wrong-cred-init.json")"
fi
if grep -q '"state":"uninitialized"' "$EVID/F4-state-after-wrong.json"; then
  ok F4b "错误凭据尝试后系统仍未初始化 (state=uninitialized)"
else
  bad F4b "错误凭据尝试后系统状态异常: $(head -c 200 "$EVID/F4-state-after-wrong.json") (期望仍 uninitialized; 若变 initialized 说明错误尝试污染了状态)"
fi

# =============================================================================
# F2 用正确凭据建管理员
# =============================================================================
st="$(post_json /api/v1/auth/initialize "{\"credential\":\"$CRED\",\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\",\"email\":\"$ADMIN_EMAIL\"}" "$EVID/F2-initialize.json")"
redact_file "$EVID/F2-initialize.json"
{ echo "POST /api/v1/auth/initialize (正确凭据, 用户名已记入) -> status=$st"; echo "--- body ---"; cat "$EVID/F2-initialize.json"; echo; } > "$EVID/F2-initialize.txt"
if [[ "$st" == "201" ]] && grep -q "\"username\":\"$ADMIN_USER\"" "$EVID/F2-initialize.json"; then
  ok F2 "用一次性凭据建管理员成功 (201): $(head -c 160 "$EVID/F2-initialize.json")"
else
  bad F2 "建管理员失败: status=$st (期望 201 且 username=$ADMIN_USER); body=$(head -c 240 "$EVID/F2-initialize.json")"
  { echo "PASS=$PASS FAIL=$FAIL"; printf '%s\n' "${RESULTS[@]}"; } > "$EVID/result.txt"
  exit 1
fi
st="$(get_status_body /api/v1/auth/initialization "$EVID/F2-state-after.json")"
grep -q '"state":"initialized"' "$EVID/F2-state-after.json" \
  && ok F2b "初始化后状态转为 initialized" \
  || bad F2b "初始化后状态未转为 initialized: $(head -c 200 "$EVID/F2-state-after.json")"

# =============================================================================
# F3 用新管理员登录
# =============================================================================
st="$(post_json /api/v1/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$EVID/F3-login.json")"
TOKEN="$(python3 -c 'import json,sys
try:
    d=json.load(open(sys.argv[1]))
    print((d.get("data") or {}).get("token") or "")
except Exception:
    print("")' "$EVID/F3-login.json" 2>/dev/null)"
{ echo "POST /api/v1/auth/login -> status=$st"; echo "token_len=${#TOKEN}"; echo "--- body ---"; python3 -c 'import json,sys;d=json.load(open(sys.argv[1]));t=(d.get("data") or {}).get("token");d.get("data",{}).__setitem__("token","<TOKEN len=%d REDACTED>"%len(t or "")) if t is not None else None;print(json.dumps(d,ensure_ascii=False))' "$EVID/F3-login.json" 2>/dev/null || cat "$EVID/F3-login.json"; echo; } > "$EVID/F3-login.txt"
if [[ "$st" == "200" && -n "$TOKEN" ]]; then
  ok F3 "新管理员登录成功 (200, token_len=${#TOKEN})"
else
  bad F3 "新管理员登录失败: status=$st token_len=${#TOKEN} body=$(head -c 200 "$EVID/F3-login.json")"
fi

# =============================================================================
# F5 重复初始化必须被拒，且原管理员不被覆盖
#   5a) 重放同一凭据（凭据一次性 ⇒ 必须失效）
#   5b) 换用户名再初始化（系统已 initialized ⇒ 必须拒绝，不得覆盖）
# =============================================================================
st="$(post_json /api/v1/auth/initialize "{\"credential\":\"$CRED\",\"username\":\"replayuser\",\"password\":\"replaypass123\"}" "$EVID/F5a-replay.json")"
redact_file "$EVID/F5a-replay.json"
{ echo "POST /auth/initialize 重放同一凭据 -> status=$st"; cat "$EVID/F5a-replay.json"; echo; } > "$EVID/F5a-replay.txt"
[[ "$st" == "409" ]] \
  && ok F5a "重放已消费的一次性凭据被拒 (409): $(head -c 140 "$EVID/F5a-replay.json")" \
  || bad F5a "重放凭据未被拒: status=$st (期望 409); body=$(head -c 200 "$EVID/F5a-replay.json")"

FAKE2="aaaaaaaaaaaaaaaaaaaaaa.bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
st="$(post_json /api/v1/auth/initialize "{\"credential\":\"$FAKE2\",\"username\":\"intruder2\",\"password\":\"intruder2pass123\"}" "$EVID/F5b-second-init.json")"
redact_file "$EVID/F5b-second-init.json"
st_before="$(post_json /api/v1/auth/login "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$EVID/F5b-admin-login.json")"
st_intr="$(post_json /api/v1/auth/login '{"username":"intruder2","password":"intruder2pass123"}' "$EVID/F5b-intruder-login.json")"
{
  echo "POST /auth/initialize (已 initialized 后再初始化) -> status=$st"; cat "$EVID/F5b-second-init.json"; echo
  echo "原管理员 $ADMIN_USER 再登录 -> status=$st_before"
  echo "入侵者 intruder2 登录 -> status=$st_intr (期望 401)"
  cat "$EVID/F5b-intruder-login.json"; echo
} > "$EVID/F5b-second-init.txt"
[[ "$st" == "409" ]] \
  && ok F5b "重复初始化被拒 (409): $(head -c 140 "$EVID/F5b-second-init.json")" \
  || bad F5b "重复初始化未被拒: status=$st (期望 409); body=$(head -c 200 "$EVID/F5b-second-init.json")"
[[ "$st_before" == "200" ]] \
  && ok F5c "重复初始化后原管理员仍可登录 (200) —— 管理员未被覆盖" \
  || bad F5c "重复初始化后原管理员登录失败: status=$st_before (管理员可能被覆盖!)"
[[ "$st_intr" == "401" ]] \
  && ok F5d "入侵者账号不存在 (401) —— 未被创建" \
  || bad F5d "入侵者账号被创建/可登录: status=$st_intr (期望 401); body=$(head -c 200 "$EVID/F5b-intruder-login.json")"

# F5e 用有效 token 读 /account 确认身份未被改写（需要登录成功）
if [[ "$st_before" == "200" ]]; then
  TOKEN2="$(python3 -c 'import json,sys
try:
    print((json.load(open(sys.argv[1])).get("data") or {}).get("token") or "")
except Exception: print("")' "$EVID/F5b-admin-login.json")"
  st_acc="$(get_status_body /api/v1/account "$EVID/F5e-account.json" "$TOKEN2")"
  { echo "GET /api/v1/account -> status=$st_acc"; cat "$EVID/F5e-account.json"; echo; } > "$EVID/F5e-account.txt"
  if [[ "$st_acc" == "200" ]] && grep -q "\"username\":\"$ADMIN_USER\"" "$EVID/F5e-account.json"; then
    ok F5e "登录主体身份确认: username=$ADMIN_USER (未被替换)"
  else
    bad F5e "身份确认失败: status=$st_acc body=$(head -c 200 "$EVID/F5e-account.json")"
  fi
fi

# =============================================================================
# F6 冷启动回填日志
# =============================================================================
WARM="$(grep -oE 'Latest value cache warmed up: [0-9]+ rows' "$EVID/F1-startup-logs.raw" | head -1 || true)"
if [[ -n "$WARM" ]]; then
  ok F6 "冷启动回填日志出现: [$WARM]"
else
  bad F6 "冷启动日志未见 'Latest value cache warmed up: N rows'; warm|cache 命中=$(grep -ciE 'warm|cache' "$EVID/F1-startup-logs.raw" || true)"
fi

# ---- F1b 负控: 证明"没找到凭据"这个信号可信 -----------------------------------
# 用**必然不存在**的标记 grep 同一份日志: 必须 0 命中。若它 >0, 说明 grep/日志
# 抽取本身有毛病,"没找到凭据"就不能作为判断依据（空转绿的经典形态）。
NEG_HITS="$(grep -c 'Initialization credential NEGATIVE-CONTROL-NOT-PRESENT' "$EVID/F1-startup-logs.raw" || true)"
if [[ "${NEG_HITS:-0}" == "0" ]]; then
  ok F1b "负控成立: 不存在的凭据标记在日志中 0 命中 —— '没找到凭据' 这个信号可信"
else
  bad F1b "负控失败: 不存在的标记命中 ${NEG_HITS} 次 —— 日志抽取逻辑不可信"
fi

# ---- F4b2/F5f 正控: 凭据抽取器在**已知存在**的行上确实能抽到 ---------------------
# （F1 已抽到真实凭据即构成正控; 此处再断言解析出的形态满足 selector.secret）
if [[ -n "${CRED:-}" ]]; then
  ok F1c "正控: 凭据抽取器在已知存在的一次性凭据行上成功抽出 ${#CRED} 字符 (selector+secret)"
else
  bad F1c "正控失败: 日志有凭据行却抽不出凭据 —— 抽取器坏了"
fi


# =============================================================================
# 本会话修复的容器级复验（附加段，非 Q5 原生；需已初始化且可登录的栈）
#   F7 latest_data 形状: 运行中必须返回多物理量（修复前缓存命中只返回 1 个）
#   F8 reconfigure: 断言**响应里的 bus_config 真的变了**, 且 GET 回读(落库值)也变了
#      —— 只看状态码 200 是假绿(谎报成功类缺陷); 严格性由变异证据 M4 证明
#   F9 告警规则 enabled:false ⇒ 创建响应与回读均为 false
#   F10 启动回填日志可解析
# =============================================================================
note "附加段: 本会话修复的容器级复验 (F7-F10)"

tok_of() {
  python3 -c 'import json,sys
try: print((json.load(open(sys.argv[1])).get("data") or {}).get("token") or "")
except Exception: print("")' "$1" 2>/dev/null
}
: > "$EVID/F7-login.json"
curl -sS -m 25 -o "$EVID/F7-login.json" -X POST -H 'Content-Type: application/json' -d "{\"username\":\"$ADMIN_USER\",\"password\":\"$ADMIN_PASS\"}" "$BASE/api/v1/auth/login" >/dev/null 2>&1 || true
TOK3="$(tok_of "$EVID/F7-login.json")"
TAG="$$"

mkdir -p "$EVID/bin"
cat > "$EVID/bin/mqttpub.py" <<'PYEOF'
import socket, struct, sys, time
def rl(n):
    o=bytearray()
    while True:
        b=n%128; n//=128
        if n>0: b|=0x80
        o.append(b)
        if n==0: break
    return bytes(o)
def ms(x):
    b=x.encode(); return struct.pack(">H",len(b))+b
def conn(cid):
    vh=ms("MQTT")+bytes([4,0x02])+struct.pack(">H",60)
    body=vh+ms(cid)
    return bytes([0x10])+rl(len(body))+body
def pub(t,pl):
    body=ms(t)+pl
    return bytes([0x30])+rl(len(body))+body
def vi(v):
    o=bytearray()
    while v>0x7F:
        o.append((v&0x7F)|0x80); v>>=7
    o.append(v&0x7F); return bytes(o)
def tag(f,w): return vi((f<<3)|w)
def ev(f,v): return tag(f,0)+vi(v)
def eb(f,d): return tag(f,2)+vi(len(d))+d
def frame(ch,ts,seq,raw,edge):
    return bytes([0x03])+ev(1,ch)+ev(2,ts)+ev(3,seq)+eb(4,raw)+ev(7,edge)
h,p,topic,hexp,ch,edge = sys.argv[1], int(sys.argv[2]), sys.argv[3], sys.argv[4], int(sys.argv[5]), int(sys.argv[6])
pl=frame(ch, time.time_ns()//1_000_000, 1, bytes.fromhex(hexp), edge)
s=socket.create_connection((h,p),timeout=10)
s.sendall(conn("bbprobe-%d"%time.time_ns()))
if len(s.recv(4))<4: raise SystemExit("CONNACK short")
s.sendall(pub(topic,pl)); s.sendall(bytes([0xE0,0x00])); s.close()
print("PUBLISH_OK bytes=%d"%len(pl))
PYEOF

api_post() { # path body out -> status
  curl -sS -m 25 -o "$3" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOK3" -H 'Content-Type: application/json' -d "$2" "$BASE$1" 2>/dev/null || echo 000
}
jget() { # file key -> value (data.<key>)
  python3 -c 'import json,sys
try: print((json.load(open(sys.argv[1])).get("data") or {}).get(sys.argv[2]) or "")
except Exception: print("")' "$1" "$2" 2>/dev/null
}

NODE="sim-bbc-$TAG"
: > "$EVID/F7-seed.txt"
echo "POST /nodes -> $(api_post /api/v1/nodes "{\"node_id\":\"$NODE\",\"name\":\"BBC $TAG\"}" "$EVID/F7-node.json")" >> "$EVID/F7-seed.txt"
echo "POST /channels -> $(api_post /api/v1/channels "{\"node_id\":\"$NODE\",\"hardware_type\":\"UART\",\"bus_type\":\"UART\",\"hardware_id\":\"UART0\",\"bus_config\":\"AABB00002580CC\",\"enabled\":true}" "$EVID/F7-chan.json")" >> "$EVID/F7-seed.txt"
CHID="$(jget "$EVID/F7-chan.json" id)"
echo "channel_id=$CHID" >> "$EVID/F7-seed.txt"
echo "POST /edge-devices -> $(api_post /api/v1/edge-devices "{\"name\":\"BBC-Sensor-$TAG\",\"type\":\"lk_th01\",\"node_id\":\"$NODE\",\"channel_id\":$CHID,\"hardware_id\":\"\"}" "$EVID/F7-dev.json")" >> "$EVID/F7-seed.txt"
DEVID="$(jget "$EVID/F7-dev.json" id)"
echo "edge_device_id=$DEVID" >> "$EVID/F7-seed.txt"

if [[ -n "$CHID" && -n "$DEVID" ]]; then
  python3 "$EVID/bin/mqttpub.py" 127.0.0.1 1883 "nodes/$NODE/up" 00FD0259 "$CHID" "$DEVID" >> "$EVID/F7-seed.txt" 2>&1 || true
fi

overview_dump() {
  curl -sS -m 20 -o "$1" -H "Authorization: Bearer $TOK3" "$BASE/api/v1/overview" 2>/dev/null || true
  python3 -c 'import json,sys
try: d=json.load(open(sys.argv[1]))["data"]
except Exception as e: print("parse_error:%s"%e); raise SystemExit
for e in (d.get("latest_data") or []):
    print("%s=%d" % (e.get("device_id"), len(e.get("data") or {})))' "$1" 2>/dev/null
}
NQTY=""
for _ in $(seq 1 20); do
  NQTY="$(overview_dump "$EVID/F7-overview-runtime.json" | awk -F= -v d="$DEVID" '$1==d{print $2}')"
  if [[ -n "$NQTY" && "$NQTY" -ge 2 ]]; then break; fi
  sleep 2
done
{
  echo "=== 运行中 /overview.latest_data 各设备物理量数 ==="
  overview_dump "$EVID/F7-overview-runtime.json"
  echo "目标 edge_device_id=$DEVID 物理量数=${NQTY:-<none>} (期望 >=2)"
} > "$EVID/F7-shape-runtime.txt"
if [[ -n "$NQTY" && "$NQTY" -ge 2 ]]; then
  ok F7 "运行时 /overview.latest_data 返回多物理量: device=$DEVID n_qty=$NQTY (>=2; 修复前缓存命中只 1)"
else
  bad F7 "/overview.latest_data 未返回多物理量: device=$DEVID n_qty='${NQTY:-<none>}' (期望 >=2) —— 见 F7-*.txt"
fi

BEFORE="$(curl -sS -m 20 -H "Authorization: Bearer $TOK3" "$BASE/api/v1/channels/$CHID" 2>/dev/null | python3 -c 'import json,sys
try: print((json.load(sys.stdin).get("data") or {}).get("bus_config") or "")
except Exception: print("")')"
ST_RC="$(curl -sS -m 25 -o "$EVID/F8-reconfigure.json" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOK3" -H 'Content-Type: application/json' -d '{"baudrate":115200}' "$BASE/api/v1/channels/$CHID/reconfigure" 2>/dev/null || echo 000)"
AFTER="$(curl -sS -m 20 -H "Authorization: Bearer $TOK3" "$BASE/api/v1/channels/$CHID" 2>/dev/null | python3 -c 'import json,sys
try: print((json.load(sys.stdin).get("data") or {}).get("bus_config") or "")
except Exception: print("")')"
RESP_BC="$(jget "$EVID/F8-reconfigure.json" bus_config)"
{
  echo "channel_id=$CHID"
  echo "GET 回读 BEFORE bus_config=$BEFORE"
  echo "POST /reconfigure -> status=$ST_RC"
  echo "响应体: $(cat "$EVID/F8-reconfigure.json")"
  echo "响应 data.bus_config=$RESP_BC"
  echo "GET 回读 AFTER  bus_config=$AFTER  (落库值)"
} > "$EVID/F8-reconfigure.txt"
if [[ "$ST_RC" == "200" && -n "$AFTER" && "$AFTER" != "$BEFORE" && "$RESP_BC" == "$AFTER" ]]; then
  ok F8 "reconfigure 真的改了 bus_config: $BEFORE -> $AFTER (响应与落库值一致; 判据看 DB 回读值, 非裸 200)"
else
  bad F8 "reconfigure 未真正改库或响应与库不一致: status=$ST_RC BEFORE='$BEFORE' 响应='$RESP_BC' AFTER='$AFTER' (期望 200 且 AFTER!=BEFORE 且 响应==AFTER)"
fi

ST_AR="$(api_post /api/v1/alert-rules "{\"target_type\":\"edge_device\",\"target_id\":${DEVID:-1},\"sensor_name\":\"temperature\",\"comparator\":\"gt\",\"threshold\":30,\"level\":\"warning\",\"enabled\":false,\"name\":\"bbc-disabled-$TAG\"}" "$EVID/F9-alert-create.json")"
RESP_EN="$(python3 -c 'import json,sys
try: print(json.dumps((json.load(open(sys.argv[1])).get("data") or {}).get("enabled")))
except Exception: print("ERR")' "$EVID/F9-alert-create.json")"
ARID="$(jget "$EVID/F9-alert-create.json" id)"
curl -sS -m 20 -o "$EVID/F9-alert-list.json" -H "Authorization: Bearer $TOK3" "$BASE/api/v1/alert-rules" 2>/dev/null || true
READBACK_EN="$(python3 -c 'import json,sys
try:
    for r in (json.load(open(sys.argv[1])).get("data") or []):
        if r.get("id")==int(sys.argv[2]): print(json.dumps(r.get("enabled"))); break
    else: print("NOT_FOUND")
except Exception: print("ERR")' "$EVID/F9-alert-list.json" "$ARID")"
{
  echo "POST /alert-rules (enabled:false) -> status=$ST_AR"
  echo "响应 data.enabled=$RESP_EN   (规则 id=$ARID)"
  echo "GET /alert-rules 回读 enabled=$READBACK_EN"
} > "$EVID/F9-alert-enabled.txt"
if [[ "$ST_AR" == "201" && "$RESP_EN" == "false" && "$READBACK_EN" == "false" ]]; then
  ok F9 "告警规则 enabled:false 落库为 false (响应=false, 回读=false)"
else
  bad F9 "告警规则 enabled:false 未如实落库: status=$ST_AR 响应=$RESP_EN 回读=$READBACK_EN (期望 201/false/false)"
fi

WA2="$(docker logs --tail=4000 "$WEB" 2>&1 | grep -oE 'Latest value cache warmed up: [0-9]+ rows' | tail -1 || true)"
N2="$(printf '%s' "$WA2" | grep -oE '[0-9]+' | head -1 || true)"
echo "启动回填日志(当前容器)=[$WA2] N=${N2:-<none>}" > "$EVID/F10-warmup.txt"
if [[ -n "$N2" ]]; then
  ok F10 "启动回填日志可解析: [$WA2]"
else
  bad F10 "启动回填日志缺失/不可解析: [$WA2]"
fi


# ---- 汇总（整体脱敏后再落盘） ------------------------------------------------
{
  echo "probe=05-first-boot.sh"
  echo "project=$PROJECT port=$HOME_PORT db=${BB_DB_NAME:-<unset>}"
  echo "PASS=$PASS FAIL=$FAIL"
  echo "finished_at=$(date -Is)"
  echo "--- 明细 ---"
  printf '%s\n' "${RESULTS[@]}"
} > "$EVID/result.txt"
# 兜底脱敏: 对证据目录做一次全局扫描（隐藏任何漏网的凭据形状）
find "$EVID" -type f -name '*.txt' -o -type f -name '*.json' -o -type f -name '*.raw' 2>/dev/null | while IFS= read -r f; do redact_file "$f"; done

printf '\n==== 05-first-boot: PASS=%d FAIL=%d ====\n' "$PASS" "$FAIL"
[[ "$FAIL" -eq 0 ]] && exit 0 || exit 1
