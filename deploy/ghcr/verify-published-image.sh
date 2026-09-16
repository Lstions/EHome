#!/usr/bin/env bash
# 验证 GHCR 已发布镜像 —— **推送后必跑**。
#
# 为什么需要它：`git push` 后 CI 会构建并推镜像，但 **CI 绿 ≠ 镜像可用**。
# 本脚本把「CI 成功」这个弱信号，变成「镜像真能起来且功能正常」的强信号。
#
# 判据（逐条，全部可复现）：
#   1. 镜像能拉取；
#   2. **镜像 label 的 revision == 期望 commit**（证明是这次推送构建的，不是旧镜像）；
#   3. 用 deploy/ghcr/compose.ghcr.yml 起栈（**无 build 段** ⇒ 强制用远端产物）；
#   4. /health 返回 JSON（真实端点）且**不是** SPA catch-all 的 HTML；
#   5. 前端 SPA 可访问；
#   6. 首次部署全流程：凭据 → initialize 201 → 登录 200 → 重放被拒 409；
#   7. 启动日志出现 `Latest value cache warmed up` （证明镜像含启动回填接线）。
#
# 用法：
#   ./deploy/ghcr/verify-published-image.sh                 # 默认 tag=main
#   GHCR_TAG=sha-abe3a7b0 ./deploy/ghcr/verify-published-image.sh
#   EXPECT_COMMIT=abe3a7b0a726b904a5b23e11806301563051fee7 ...
#
# 退出码：0=全部通过；1=有断言失败。结束时**必定清理**栈（trap）。
set -uo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
TAG="${GHCR_TAG:-main}"
PORT="${GHCR_PORT:-18090}"
PROJECT="${GHCR_PROJECT:-ehome-ghcr}"
IMAGE="ghcr.io/lstions/ehome:${TAG}"
EXPECT_COMMIT="${EXPECT_COMMIT:-}"
COMPOSE="$REPO/deploy/ghcr/compose.ghcr.yml"
EVID="${GHCR_EVIDENCE:-$REPO/deploy/ghcr/evidence/$(date +%Y%m%d-%H%M%S)}"
mkdir -p "$EVID"

FAIL=0; PASS=0
ok()  { echo "  [PASS] $*"; PASS=$((PASS+1)); }
bad() { echo "  [FAIL] $*"; FAIL=$((FAIL+1)); }

cleanup() {
  echo ""
  echo "-- 清理 (project=$PROJECT) --"
  GHCR_PORT="$PORT" docker compose -p "$PROJECT" -f "$COMPOSE" down -v >/dev/null 2>&1 || true
  # DB 卷可能残留（down -v 已删，此处兜底核对）
  local n; n="$(docker ps -aq --filter "name=^${PROJECT}-" | wc -l | tr -d ' ')"
  echo "  清理后残留容器: $n"
}
trap cleanup EXIT

echo "== GHCR 镜像验证: $IMAGE =="
echo "   project=$PROJECT 端口=$PORT 证据=$EVID"

# 1) 拉取
if docker pull "$IMAGE" > "$EVID/pull.log" 2>&1; then ok "镜像可拉取"; else bad "镜像拉取失败"; exit 1; fi

# 2) revision label == 期望 commit（**这是「是否为最新」的判据**）
REV="$(docker inspect "$IMAGE" --format '{{index .Config.Labels "org.opencontainers.image.revision"}}' 2>/dev/null)"
echo "   revision=$REV"
if [ -z "$REV" ] || [ "$REV" = "<no value>" ]; then
  bad "镜像无 revision label（无法证明来自哪次提交）"
elif [ -n "$EXPECT_COMMIT" ] && [ "$REV" != "$EXPECT_COMMIT" ]; then
  bad "revision=$REV 与期望 $EXPECT_COMMIT **不一致** ⇒ 镜像是旧的（CI 可能还没跑完）"
else
  ok "revision label 存在${EXPECT_COMMIT:+且与期望 commit 一致}"
fi
docker inspect "$IMAGE" --format '{{json .Config.Labels}}' > "$EVID/labels.json" 2>/dev/null || true

# 3) 起栈
if GHCR_TAG="$TAG" GHCR_PORT="$PORT" GHCR_PROJECT="$PROJECT" docker compose -p "$PROJECT" -f "$COMPOSE" up -d > "$EVID/up.log" 2>&1; then
  ok "compose up 成功"
else
  bad "compose up 失败（见 $EVID/up.log）"; exit 1
fi

# 等待就绪（轮询，不 sleep 死等）
ready=0
for _ in $(seq 1 60); do
  if curl -sf "http://127.0.0.1:$PORT/health" >/dev/null 2>&1; then ready=1; break; fi
  sleep 1
done
if [ "$ready" = 1 ]; then ok "/health 就绪"; else bad "/health 超时未就绪"; fi

# 4) 真实健康端点（必须是 JSON，不是 SPA 的 HTML）
H_BODY="$(curl -s "http://127.0.0.1:$PORT/health" 2>/dev/null)"
echo "   /health -> $H_BODY"
case "$H_BODY" in
  *'"status"'*) ok "/health 返回 JSON（真实端点，非 SPA catch-all）";;
  *) bad "/health 未返回 JSON: $H_BODY";;
esac

# 5) 前端 SPA
CT="$(curl -s -o /dev/null -w '%{content_type}' "http://127.0.0.1:$PORT/" 2>/dev/null)"
case "$CT" in *text/html*) ok "SPA 可访问 ($CT)";; *) bad "SPA 不可访问 ($CT)";; esac

# 6) 首次部署全流程 + 7) 启动回填日志
python3 - "$PORT" "$PROJECT-web" "$EVID" <<'PY' || FAIL=$((FAIL+1))
import json, re, subprocess, sys, urllib.request, urllib.error
port, web, evid = sys.argv[1], sys.argv[2], sys.argv[3]
base = 'http://127.0.0.1:' + port

def call(path, data=None, token=None):
    h = {'Content-Type': 'application/json'}
    if token: h['Authorization'] = 'Bearer ' + token
    body = json.dumps(data).encode() if data is not None else None
    req = urllib.request.Request(base + path, data=body, headers=h)
    try:
        with urllib.request.urlopen(req, timeout=20) as r: return r.status, json.loads(r.read().decode())
    except urllib.error.HTTPError as e:
        try: return e.code, json.loads(e.read().decode())
        except Exception: return e.code, None

fails = 0
logs = subprocess.run(['docker','logs',web], capture_output=True, text=True)
blob = (logs.stdout or '') + (logs.stderr or '')

# 7) 启动回填接线（本会话新增）
if 'Latest value cache warmed up' in blob:
    print('  [PASS] 启动日志含 Latest value cache warmed up（镜像含回填接线）')
else:
    print('  [FAIL] 启动日志无回填行 —— 镜像可能不含该接线'); fails += 1

# 无 panic/fatal
bad_lines = [l for l in blob.splitlines() if re.search(r'panic|FATAL', l)]
if not bad_lines:
    print('  [PASS] 启动日志无 panic/FATAL')
else:
    print('  [FAIL] 启动日志含 panic/FATAL: %s' % bad_lines[:3]); fails += 1

# 6) 首次部署流程
m = re.search(r'Initialization credential \(valid for 10 minutes\): (\S+)', blob)
if not m:
    print('  [FAIL] 冷启动日志未见一次性凭据'); fails += 1
else:
    print('  [PASS] 一次性凭据出现（值不落证据文件）')
    cred = m.group(1)
    st, b = call('/api/v1/auth/initialize', {'credential': cred, 'username':'ghcr','password':'GhcrVerify123!'})
    if st == 201:
        print('  [PASS] initialize => 201')
    else:
        print('  [FAIL] initialize => %s' % st); fails += 1
    st, b = call('/api/v1/auth/login', {'username':'ghcr','password':'GhcrVerify123!'})
    tok = (b or {}).get('data',{}).get('token')
    if st == 200 and tok:
        print('  [PASS] login => 200 (token len=%d)' % len(tok))
    else:
        print('  [FAIL] login => %s' % st); fails += 1
    st, _ = call('/api/v1/auth/initialize', {'credential': cred, 'username':'evil','password':'Evil123456!'})
    if st == 409:
        print('  [PASS] 凭据重放被拒 => 409')
    else:
        print('  [FAIL] 重放 => %s' % st); fails += 1

passes = 4 - fails  # 该段共 4 条断言（回填行/无 panic/凭据+init+login+replay 计 3）
open(evid + '/functional.txt','w').write('passes=%d fails=%d\n' % (passes, fails))
print('  __PY_PASSES=%d __PY_FAILS=%d' % (passes, fails))
sys.exit(1 if fails else 0)
PY

echo ""
PY_PASSES="$(grep -o '__PY_PASSES=[0-9]*' "$EVID/up.log" 2>/dev/null | head -1 | cut -d= -f2)"
PY_FAILS="$(grep -o '__PY_FAILS=[0-9]*' "$EVID/up.log" 2>/dev/null | head -1 | cut -d= -f2)"
# python 段直接打到 stdout（被 tail 捕获），这里从证据文件取更可靠
if [ -f "$EVID/functional.txt" ]; then
  PY_PASSES="$(sed -n 's/.*passes=\([0-9]*\).*/\1/p' "$EVID/functional.txt")"
  PY_FAILS="$(sed -n 's/.*fails=\([0-9]*\).*/\1/p' "$EVID/functional.txt")"
fi
PASS=$((PASS + ${PY_PASSES:-0}))
FAIL=$((FAIL + ${PY_FAILS:-0}))
echo "== 汇总: PASS=$PASS FAIL=$FAIL =="
echo "   证据: $EVID"
[ "$FAIL" -eq 0 ] && exit 0 || exit 1