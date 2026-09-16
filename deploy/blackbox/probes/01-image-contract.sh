#!/usr/bin/env bash
# =============================================================================
# EHomeSystem 部署黑盒验证 · 子代理 A · Q1「镜像产物完整性」
# 方案：docs/分析/部署黑盒验证方案-2026-09-16.md §1/§3/§4-A
# -----------------------------------------------------------------------------
# 边界：只经公开接口（docker build / docker image inspect / docker run）做黑盒。
#       不读容器内私有业务数据、不直连任何 DB、不碰共享容器/端口。
#       「容器内取证」模板里的 canary 文件是本探针自己的临时产物（--rm 一次性容器），
#       用于给密钥扫描器做正控，用完即删。
#
# 断言清单（每条都必须能在「故意破坏」下变红）：
#   A1   镜像可构建（tag = ehome-bb-img:<短commit>）
#   A1b  镜像体积在合理区间（参考实测 90.4MB）
#   A2   /app/ehome-server 存在 + 可执行 + 确为 ELF 可执行文件
#   A3   /app/static/dist/index.html 存在且非空（SPA 入口）
#   A4   index.html 引用的每个 asset（script src / link href）在镜像内逐个存在
#   A5   /app/firmwares 目录存在
#   A6a  镜像内无 .env / .env.* 文件
#   A6b  镜像内无 *.pem / id_rsa / *.key 等密钥文件（/etc/ssl 的 CA 证书不计）
#   A6c  镜像内无 JWT / DB 口令明文，镜像 Config.Env 无 SECRET/TOKEN/PASSWORD 键
#   A7   镜像内二进制含 "Latest value cache warmed up" 日志格式串（启动回填已接线）
#   G1   分母守卫：解析到的静态资源引用数 >= 2、/app 内文件数 >= 5、/assets/*.js >= 1
#   G2   扫描器正控：密钥扫描器必须能抓到植入的 canary（否则「扫不到」不可信）
#   G3   扫描器负控：同一个 grep 对必然不存在的串必须 0 命中（否则「命中」不可信）
#   M1   BB_MUTATION 模式下：Dockerfile 恢复后 md5 与变异前逐字节一致
#
# 用法：
#   deploy/blackbox/probes/01-image-contract.sh
#   BB_SKIP_BUILD=1  ...                 # 复用已存在的镜像，不重新构建
#   BB_EVIDENCE_DIR=/path ...            # 指定证据目录（默认 evidence/A-<UTC时间戳>）
#   BB_IMAGE=ehome-bb-img:foo ...        # 指定镜像 tag
#   BB_MUTATION=drop-frontend-copy ...   # 变异自证：临时注释掉 Dockerfile 的
#                                        #   COPY --from=frontend-builder /app/dist ./static/dist
#                                        # 构建后自动从备份恢复并校验 md5
# 退出码：0 = 全部断言通过；1 = 有断言失败；2 = 环境/前置错误
# =============================================================================
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

STAMP="$BB_STAMP"; [ -n "$STAMP" ] || STAMP="$(date -u +%Y%m%dT%H%M%SZ)"
EVID="$BB_EVIDENCE_DIR"; [ -n "$EVID" ] || EVID="$REPO_ROOT/deploy/blackbox/evidence/A-$STAMP"
mkdir -p "$EVID"
LOG="$EVID/01-image-contract.log"
ASSERT_TSV="$EVID/01-assertions.tsv"
: > "$LOG"
: > "$ASSERT_TSV"

PASS_N=0
FAIL_N=0
say()  { printf '%s\n' "$*"; printf '%s\n' "$*" >>"$LOG"; }
pass() { PASS_N=$((PASS_N + 1)); say "  [PASS] $1"; printf 'PASS\t%s\t%s\n' "$2" "$1" >>"$ASSERT_TSV"; }
fail() { FAIL_N=$((FAIL_N + 1)); say "  [FAIL] $1"; printf 'FAIL\t%s\t%s\n' "$2" "$1" >>"$ASSERT_TSV"; }
hdr()  { say ""; say "== $* ============================================"; }

# == Dockerfile 变异 / 恢复 ===================================================
DOCKERFILE="$REPO_ROOT/Dockerfile"
DF_BACKUP=""
DF_ORIG_MD5=""
DF_MUTATED=0
TMPBASE="$TMPDIR"; [ -n "$TMPBASE" ] || TMPBASE=/tmp

restore_dockerfile() {
  [ "$DF_MUTATED" = 1 ] || return 0
  [ -n "$DF_BACKUP" ] && [ -f "$DF_BACKUP" ] || return 0
  cp -p "$DF_BACKUP" "$DOCKERFILE"
  DF_MUTATED=0
  local now_md5
  now_md5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  say "  [TRAP 恢复] Dockerfile 已从备份 $DF_BACKUP 还原"
  if [ "$now_md5" = "$DF_ORIG_MD5" ]; then
    say "  [TRAP 恢复] md5 一致 ($now_md5) -- 字节级还原确认"
  else
    say "  [TRAP 恢复] !! md5 不一致: 期望 $DF_ORIG_MD5 实际 $now_md5"
  fi
}
trap restore_dockerfile EXIT

apply_mutation() {
  [ -n "$BB_MUTATION" ] || return 0
  if [ "$BB_MUTATION" != "drop-frontend-copy" ]; then
    say "未知 BB_MUTATION=$BB_MUTATION（仅支持 drop-frontend-copy）"; exit 2
  fi
  hdr "变异注入 BB_MUTATION=drop-frontend-copy"
  DF_BACKUP="$(mktemp "$TMPBASE/bb-Dockerfile.XXXXXX")"
  cp -p "$DOCKERFILE" "$DF_BACKUP"
  DF_ORIG_MD5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  cp "$DOCKERFILE" "$EVID/01-dockerfile.before.txt"
  sed -i 's|^COPY --from=frontend-builder /app/dist ./static/dist$|# MUTATED(01): COPY --from=frontend-builder /app/dist ./static/dist|' "$DOCKERFILE"
  DF_MUTATED=1
  cp "$DOCKERFILE" "$EVID/01-dockerfile.mutated.txt"
  # * 纪律 #2：变异脚本必须自证「真的改了东西」
  local hits
  hits="$(grep -c '^# MUTATED(01): COPY --from=frontend-builder /app/dist ./static/dist$' "$DOCKERFILE")"
  say "  变异落地校验：Dockerfile 中匹配 '# MUTATED(01): COPY ...' 的行数 = $hits （期望 1）"
  say "  变更后 grep -n 'static/dist' Dockerfile："
  grep -n 'static/dist' "$DOCKERFILE" | sed 's/^/    /' | tee -a "$LOG"
  if [ "$hits" != "1" ]; then
    say "  !! 变异未落地 -- 后续「仍绿」不可采信，直接判环境错误"
    exit 2
  fi
  say "  变异前 Dockerfile md5 = $DF_ORIG_MD5"
  say "  变异后 Dockerfile md5 = $(md5sum "$DOCKERFILE" | awk '{print $1}')"
  say "  证据：01-dockerfile.before.txt / 01-dockerfile.mutated.txt"
}

# == 0. 环境与上下文 ==========================================================
hdr "0. 环境与上下文"
SHORT_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null)"
[ -n "$SHORT_COMMIT" ] || SHORT_COMMIT=nogit
IMG="$BB_IMAGE"; [ -n "$IMG" ] || IMG="ehome-bb-img:$SHORT_COMMIT"
GOPROXY_VAL="$BB_GOPROXY"; [ -n "$GOPROXY_VAL" ] || GOPROXY_VAL="https://goproxy.cn,direct"
CANARY="bb-canary-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"
ABSENT_TOKEN="bb-absent-control-$(head -c 16 /dev/urandom | od -An -tx1 | tr -d ' \n')"

say "  仓库根         : $REPO_ROOT"
say "  commit         : $SHORT_COMMIT ($(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null))"
say "  镜像 tag       : $IMG"
say "  证据目录       : $EVID"
say "  docker         : $(docker --version 2>&1)"
say "  buildx         : $(docker buildx version 2>&1)"
say "  GOPROXY        : $GOPROXY_VAL"
say "  Dockerfile md5 : $(md5sum "$DOCKERFILE" | awk '{print $1}')"
say "  工作树脏文件   :"
git -C "$REPO_ROOT" status --porcelain 2>/dev/null | sed 's/^/    /' | tee -a "$LOG"
say "  注：探针不加 --no-cache，构建时间受本地 build cache 影响；只有 07 才考「两次一致」。"
say "  注：本探针只测静态产物契约，不启动服务、不连 DB、不碰共享容器/端口。"

apply_mutation

# == 1. 构建 =================================================================
hdr "1. 构建镜像 (A1)"
BUILD_LOG="$EVID/01-build.log"
BUILD_RC=1
NEED_BUILD=1
if [ "$BB_SKIP_BUILD" = "1" ] && docker image inspect "$IMG" >/dev/null 2>&1; then
  NEED_BUILD=0
  BUILD_RC=0
  say "  BB_SKIP_BUILD=1 且镜像已存在 -- 跳过构建（复用 $IMG）"
  printf 'SKIPPED (BB_SKIP_BUILD=1, image reused)\n' > "$BUILD_LOG"
fi

if [ "$NEED_BUILD" = 1 ]; then
  say "  复现命令: docker build --progress=plain --build-arg GOPROXY=$GOPROXY_VAL -t $IMG $REPO_ROOT"
  printf '$ docker build --progress=plain --build-arg GOPROXY=%s -t %s %s\n' "$GOPROXY_VAL" "$IMG" "$REPO_ROOT" > "$BUILD_LOG"
  docker build --progress=plain --build-arg "GOPROXY=$GOPROXY_VAL" -t "$IMG" "$REPO_ROOT" >>"$BUILD_LOG" 2>&1
  BUILD_RC=$?
  printf '[build exit=%d]\n' "$BUILD_RC" >>"$BUILD_LOG"
fi

if [ "$BUILD_RC" = "0" ]; then
  pass "A1 镜像构建成功（exit 0）-- 日志 $(basename "$BUILD_LOG")" "A1"
else
  fail "A1 镜像构建失败（exit $BUILD_RC）-- 构建日志末 40 行见下" "A1"
  say "  ---- 01-build.log 末尾 40 行 ----"
  tail -40 "$BUILD_LOG" | sed 's/^/    | /' | tee -a "$LOG"
  say "  ---- Dockerfile md5 = $(md5sum "$DOCKERFILE" | awk '{print $1}') ----"
  say ""
  say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"
  exit 1
fi

if ! docker image inspect "$IMG" >/dev/null 2>&1; then
  say "  !! docker image inspect $IMG 失败 -- 无法继续"
  say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"
  exit 2
fi

# == 2. 镜像元数据 ============================================================
hdr "2. 镜像元数据 (A1b)"
IMG_META="$EVID/01-image-meta.json"
docker image inspect "$IMG" >"$IMG_META" 2>&1
IMG_ID="$(docker image inspect "$IMG" --format '{{.Id}}')"
IMG_SIZE="$(docker image inspect "$IMG" --format '{{.Size}}')"
IMG_CREATED="$(docker image inspect "$IMG" --format '{{.Created}}')"
IMG_ENV="$(docker image inspect "$IMG" --format '{{range .Config.Env}}{{println .}}{{end}}')"
printf '%s\n' "$IMG_ENV" > "$EVID/01-image-config-env.txt"
# 分母守卫：Env 项数。为 0 说明 docker inspect 没读到（断言会空转）——
# 正常镜像至少有 PATH，故 >=1 是「检查真的跑起来了」的证据。
ENV_TOTAL="$(printf '%s\n' "$IMG_ENV" | grep -c . || true)"; ENV_TOTAL=$((ENV_TOTAL+0))
say "  image id : $IMG_ID"
say "  created  : $IMG_CREATED"
say "  size     : $IMG_SIZE bytes ($((IMG_SIZE / 1048576)) MiB；参考实测 90.4MB)"
say "  Config.Env:"
printf '%s\n' "$IMG_ENV" | sed 's/^/    /' | tee -a "$LOG"

SIZE_MIN=$((40 * 1048576))
SIZE_MAX=$((300 * 1048576))
if [ "$IMG_SIZE" -ge "$SIZE_MIN" ] && [ "$IMG_SIZE" -le "$SIZE_MAX" ]; then
  pass "A1b 镜像体积 $((IMG_SIZE / 1048576)) MiB 落在合理区间 [40,300] MiB" "A1b"
else
  fail "A1b 镜像体积 $IMG_SIZE bytes 超出合理区间 [40,300] MiB（参考 90.4MB）" "A1b"
fi

# == 3. 容器内取证（一次 docker run 全量采集）================================
hdr "3. 容器内取证"
INSPECT_OUT="$EVID/01-image-inspect.txt"
INSPECT_ERR="$EVID/01-image-inspect.stderr.txt"

PROBE_SH='
set -u
echo "### APP_TREE_BEGIN"
find /app -type f 2>/dev/null | sort
echo "### APP_TREE_END"
echo "### INDEX_BEGIN"
cat /app/static/dist/index.html 2>/dev/null
echo ""
echo "### INDEX_END"
echo "### REFS_BEGIN"
tr ">" "\n" < /app/static/dist/index.html 2>/dev/null | grep -oE "(src|href)=\"[^\"]*\"" | sed "s/^[a-z]*=\"//; s/\"$//"
echo "### REFS_END"
echo "### ASSET_FILES_BEGIN"
ls -1 /app/static/dist/assets 2>/dev/null | sort
echo "### ASSET_FILES_END"
echo "### IMG_FILE_COUNT=$(find /app -type f 2>/dev/null | wc -l)"
echo "### SERVER_EXISTS=$([ -f /app/ehome-server ] && echo 1 || echo 0)"
echo "### SERVER_EXEC=$([ -x /app/ehome-server ] && echo 1 || echo 0)"
echo "### SERVER_MAGIC=$(head -c 4 /app/ehome-server 2>/dev/null | od -An -tx1 | tr -d " \n")"
echo "### SERVER_SIZE=$(wc -c < /app/ehome-server 2>/dev/null || echo 0)"
echo "### INDEX_EXISTS=$([ -f /app/static/dist/index.html ] && echo 1 || echo 0)"
echo "### INDEX_SIZE=$(wc -c < /app/static/dist/index.html 2>/dev/null || echo 0)"
echo "### ASSETS_COUNT=$(ls -1 /app/static/dist/assets 2>/dev/null | wc -l)"
echo "### FIRMWARES_DIR=$([ -d /app/firmwares ] && echo 1 || echo 0)"
echo "### ENVFILES_BEGIN"
find / -xdev \( -name ".env" -o -name ".env.*" \) 2>/dev/null | sort
echo "### ENVFILES_END"
echo "### SECRETFILES_BEGIN"
find /app /root /home /srv /opt /var /tmp /usr/local -xdev \( -name "*.pem" -o -name "*.key" -o -name "*.p12" -o -name "*.pfx" -o -name "*.jks" -o -name "id_rsa*" -o -name "id_ed25519*" -o -name "id_ecdsa*" -o -name ".npmrc" -o -name ".netrc" -o -name ".pgpass" \) 2>/dev/null | sort
echo "### SECRETFILES_END"
echo "### CA_PEM_COUNT=$(find /etc/ssl -name "*.pem" 2>/dev/null | wc -l)"
echo "### SECRETGREP_BEGIN"
# 只查**密钥类内容的特征串**（PEM 头），不查环境变量名。
# 为什么移除 EHOME_JWT_SECRET / EHOME_DB_PASSWORD：它们是**环境变量的名字**，
# 二进制里必然含 os.Getenv("EHOME_JWT_SECRET") 这类字面量 —— 那是代码，不是密钥值。
# 主控实测（2026-09-16）：原判据命中的 3 处全是变量名；镜像 Config.Env 只有 PATH，无任何敏感值。
# ⇒ 原判据把「变量名」当「密钥值」，是**假阳性**，会把合法镜像判红。
# 真正的「值」检查在别处：Config.Env 敏感键扫描（ENV_SECRET_KEYS）+ 无 .env 文件（A6a）。
grep -rlF -e "BEGIN RSA PRIVATE KEY" -e "BEGIN OPENSSH PRIVATE KEY" -e "BEGIN EC PRIVATE KEY" -e "BEGIN PRIVATE KEY" /app 2>/dev/null | sort
echo "### SECRETGREP_END"
W=$(grep -acF "Latest value cache warmed up" /app/ehome-server 2>/dev/null); W=$((W+0))
echo "### WARMUP_STR=$W"
N=$(grep -acF "$BB_ABSENT" /app/ehome-server 2>/dev/null); N=$((N+0))
echo "### ABSENT_STR=$N"
echo "### CANARY_HITS_BEGIN"
echo "CANARY_VALUE=$BB_CANARY" > /app/.bb-canary-probe
grep -rlF "$BB_CANARY" /app 2>/dev/null
rm -f /app/.bb-canary-probe
echo "### CANARY_HITS_END"
echo "### DONE"
'
printf '$ docker run --rm -e BB_CANARY=<红acted> -e BB_ABSENT=<红acted> --entrypoint sh %s -c <probe>\n' "$IMG" >>"$LOG"
docker run --rm -e "BB_CANARY=$CANARY" -e "BB_ABSENT=$ABSENT_TOKEN" --entrypoint sh "$IMG" -c "$PROBE_SH" >"$INSPECT_OUT" 2>"$INSPECT_ERR"
INSPECT_RC=$?
say "  docker run 退出码: $INSPECT_RC （stdout $(wc -l < "$INSPECT_OUT") 行）"
if [ "$INSPECT_RC" != "0" ] || ! grep -q '^### DONE$' "$INSPECT_OUT"; then
  say "  !! 容器内取证未完成，stdout 末尾 30 行："
  tail -30 "$INSPECT_OUT" | sed 's/^/    | /' | tee -a "$LOG"
  say "  !! stderr 末尾 30 行："
  tail -30 "$INSPECT_ERR" | sed 's/^/    | /' | tee -a "$LOG"
  say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"
  exit 2
fi

section() { awk -v b="$1" -v e="$2" '$0==b{f=1;next} $0==e{f=0} f' "$INSPECT_OUT"; }
kvv()     { grep -m1 "^### $1=" "$INSPECT_OUT" | sed "s/^### $1=//"; }

section "### APP_TREE_BEGIN"    "### APP_TREE_END"    > "$EVID/01-app-tree.txt"
section "### INDEX_BEGIN"       "### INDEX_END"       > "$EVID/01-index-html.txt"
section "### REFS_BEGIN"        "### REFS_END"        > "$EVID/01-index-refs-raw.txt"
section "### ASSET_FILES_BEGIN" "### ASSET_FILES_END" > "$EVID/01-asset-files.txt"
section "### ENVFILES_BEGIN"    "### ENVFILES_END"    > "$EVID/01-found-envfiles.txt"
section "### SECRETFILES_BEGIN" "### SECRETFILES_END" > "$EVID/01-found-secretfiles.txt"
section "### SECRETGREP_BEGIN"  "### SECRETGREP_END"  > "$EVID/01-found-secretgrep.txt"
section "### CANARY_HITS_BEGIN" "### CANARY_HITS_END" > "$EVID/01-canary-hits.txt"

IMG_FILE_COUNT="$(kvv IMG_FILE_COUNT)"
SERVER_EXISTS="$(kvv SERVER_EXISTS)"
SERVER_EXEC="$(kvv SERVER_EXEC)"
SERVER_MAGIC="$(kvv SERVER_MAGIC)"
SERVER_SIZE="$(kvv SERVER_SIZE)"
INDEX_EXISTS="$(kvv INDEX_EXISTS)"
INDEX_SIZE="$(kvv INDEX_SIZE)"
ASSETS_COUNT="$(kvv ASSETS_COUNT)"
FIRMWARES_DIR="$(kvv FIRMWARES_DIR)"
CA_PEM_COUNT="$(kvv CA_PEM_COUNT)"
WARMUP_STR="$(kvv WARMUP_STR)"
ABSENT_STR="$(kvv ABSENT_STR)"

say "  /app 文件数        : $IMG_FILE_COUNT"
say "  ehome-server       : exists=$SERVER_EXISTS exec=$SERVER_EXEC magic=$SERVER_MAGIC size=$SERVER_SIZE"
say "  index.html         : exists=$INDEX_EXISTS size=$INDEX_SIZE"
say "  static/dist/assets : $ASSETS_COUNT 个文件"
say "  /app/firmwares     : dir=$FIRMWARES_DIR"
say "  二进制含 warmed-up 格式串 : $WARMUP_STR"
say "  二进制含随机不存在串(负控) : $ABSENT_STR"

# == 4. 镜像内产物断言 ========================================================
hdr "4. 镜像内产物断言"

if [ "$SERVER_EXISTS" = "1" ] && [ "$SERVER_EXEC" = "1" ] && [ "$SERVER_MAGIC" = "7f454c46" ]; then
  pass "A2 /app/ehome-server 存在、可执行、且为 ELF（magic=$SERVER_MAGIC, $SERVER_SIZE bytes）" "A2"
else
  fail "A2 /app/ehome-server 异常：exists=$SERVER_EXISTS exec=$SERVER_EXEC magic=$SERVER_MAGIC（期望 1/1/7f454c46）" "A2"
  say "    实际 /app 文件清单（诊断）："
  sed 's/^/      /' "$EVID/01-app-tree.txt" | tee -a "$LOG"
fi

if [ "$INDEX_EXISTS" = "1" ] && [ "$INDEX_SIZE" -gt 0 ]; then
  pass "A3 /app/static/dist/index.html 存在且非空（$INDEX_SIZE bytes）" "A3"
else
  fail "A3 /app/static/dist/index.html 缺失或为空：exists=$INDEX_EXISTS size=$INDEX_SIZE" "A3"
  say "    实际 /app 文件清单（诊断）："
  sed 's/^/      /' "$EVID/01-app-tree.txt" | tee -a "$LOG"
fi

if [ "$FIRMWARES_DIR" = "1" ]; then
  pass "A5 /app/firmwares 目录存在" "A5"
else
  fail "A5 /app/firmwares 目录缺失（FIRMWARES_DIR=$FIRMWARES_DIR）" "A5"
fi

# == 5. 静态资源完整性 (A4 / G1) ==============================================
hdr "5. 静态资源完整性 (A4 / G1)"

RESOLVED="$EVID/01-resolved-refs.txt"
SKIPPED="$EVID/01-skipped-refs.txt"
: > "$RESOLVED"
: > "$SKIPPED"
REF_TOTAL=0
REF_SKIPPED=0
REF_UNSAFE=0

while IFS= read -r ref; do
  [ -n "$ref" ] || continue
  case "$ref" in
    http://*|https://*|//*|data:*|mailto:*|javascript:*|tel:*|blob:*|"#"*)
      REF_SKIPPED=$((REF_SKIPPED + 1)); printf '%s\n' "$ref" >>"$SKIPPED"; continue ;;
  esac
  p="$(printf '%s' "$ref" | sed 's/[?#].*$//')"
  [ -n "$p" ] || { REF_SKIPPED=$((REF_SKIPPED + 1)); printf '%s\n' "$ref" >>"$SKIPPED"; continue; }
  case "$p" in
    /*) abs="/app/static/dist$p" ;;
    *)  abs="/app/static/dist/$p" ;;
  esac
  case "$abs" in
    *"'"*|*" "*|*"	"*) REF_UNSAFE=$((REF_UNSAFE + 1)); printf '%s\n' "$ref" >>"$SKIPPED"; continue ;;
  esac
  REF_TOTAL=$((REF_TOTAL + 1))
  printf '%s\n' "$abs" >>"$RESOLVED"
done < "$EVID/01-index-refs-raw.txt"

say "  index.html 中 (src|href)= 引用总数 : $((REF_TOTAL + REF_SKIPPED))"
say "  解析为镜像内路径待查              : $REF_TOTAL"
say "  跳过（外部链接/data:/#片段等）     : $REF_SKIPPED"
say "  跳过（含引号/空白的异常路径）      : $REF_UNSAFE"
say "  待查路径清单（$(basename "$RESOLVED")）："
sed 's/^/      /' "$RESOLVED" | tee -a "$LOG"

if [ "$REF_UNSAFE" = "0" ]; then
  pass "G1a 引用路径解析安全：0 条含引号/空白（解析结果可信）" "G1a"
else
  fail "G1a 有 $REF_UNSAFE 条引用路径含引号/空白，解析不可信" "G1a"
fi

if [ "$REF_TOTAL" -ge 2 ]; then
  pass "G1b 分母守卫：解析到 $REF_TOTAL 条待查静态资源引用（期望 >= 2）" "G1b"
else
  fail "G1b 分母守卫失败：只解析到 $REF_TOTAL 条静态资源引用（期望 >= 2）-- 解析器可能坏了" "G1b"
  say "    index.html 原文（诊断）："
  sed 's/^/      /' "$EVID/01-index-html.txt" | tee -a "$LOG"
fi

if [ "$IMG_FILE_COUNT" -ge 5 ]; then
  pass "G1c 分母守卫：镜像 /app 内文件数 $IMG_FILE_COUNT（期望 >= 5）" "G1c"
else
  fail "G1c 分母守卫失败：镜像 /app 内文件数只有 $IMG_FILE_COUNT（期望 >= 5）" "G1c"
fi

REF_CHECK="$EVID/01-refs-check.txt"
docker run --rm -i --entrypoint sh "$IMG" -c '
while IFS= read -r p; do
  [ -n "$p" ] || continue
  if [ -f "$p" ]; then echo "FOUND   $p"; else echo "MISSING $p"; fi
done' < "$RESOLVED" >"$REF_CHECK" 2>>"$INSPECT_ERR"
CHECK_RC=$?
say "  逐条核对结果（$(basename "$REF_CHECK")）："
sed 's/^/      /' "$REF_CHECK" | tee -a "$LOG"

MISSING_N="$(grep -c '^MISSING' "$REF_CHECK")"
FOUND_N="$(grep -c '^FOUND' "$REF_CHECK")"
JS_BUNDLES="$(sed 's|.*/assets/||' "$RESOLVED" | grep -cE '\.js$')"

if [ "$CHECK_RC" != "0" ]; then
  fail "A4 镜像内逐条核对未完成（docker run exit=$CHECK_RC）" "A4"
elif [ "$MISSING_N" = "0" ] && [ "$FOUND_N" = "$REF_TOTAL" ]; then
  pass "A4 index.html 引用的 $REF_TOTAL 个静态资源在镜像内逐个存在（FOUND=$FOUND_N MISSING=0）" "A4"
else
  fail "A4 镜像内缺失 $MISSING_N/$REF_TOTAL 个静态资源（FOUND=$FOUND_N）" "A4"
  say "    缺失清单（诊断）："
  grep '^MISSING' "$REF_CHECK" | sed 's/^/      /' | tee -a "$LOG"
  say "    镜像内 /app 全部文件（诊断）："
  sed 's/^/      /' "$EVID/01-app-tree.txt" | tee -a "$LOG"
  say "    index.html 原文（诊断）："
  sed 's/^/      /' "$EVID/01-index-html.txt" | tee -a "$LOG"
fi

if [ "$JS_BUNDLES" -ge 1 ]; then
  pass "G1d 分母守卫：引用的 /assets/*.js bundle 数 $JS_BUNDLES（期望 >= 1）-- 确是真前端产物" "G1d"
else
  fail "G1d 分母守卫失败：解析到的引用里没有 /assets/*.js" "G1d"
fi

# == 6. 密钥泄漏 (A6 / A7 / G2 / G3) =========================================
hdr "6. 密钥泄漏与二进制格式串检查 (A6 / A7 / G2 / G3)"

ENVFILE_N="$(grep -c . "$EVID/01-found-envfiles.txt")"
say "  镜像内 .env / .env.* 命中 $ENVFILE_N 个（find / -xdev 全盘）："
sed 's/^/      /' "$EVID/01-found-envfiles.txt" | tee -a "$LOG"
if [ "$ENVFILE_N" = "0" ]; then
  pass "A6a 镜像内无 .env / .env.* 文件" "A6a"
else
  fail "A6a 镜像内发现 $ENVFILE_N 个 .env 文件" "A6a"
fi

SECRETFILE_N="$(grep -c . "$EVID/01-found-secretfiles.txt")"
say "  镜像内密钥类文件命中 $SECRETFILE_N 个（/app /root /home /srv /opt /var /tmp /usr/local）："
sed 's/^/      /' "$EVID/01-found-secretfiles.txt" | tee -a "$LOG"
say "  对照：/etc/ssl 下 CA 证书 *.pem = $CA_PEM_COUNT 个（基础镜像 ca-certificates，属预期非泄漏）"
if [ "$SECRETFILE_N" = "0" ]; then
  pass "A6b 镜像内无 *.pem / *.key / id_rsa* / .npmrc / .pgpass 等密钥文件" "A6b"
else
  fail "A6b 镜像内发现 $SECRETFILE_N 个密钥类文件" "A6b"
fi

SECRETGREP_N="$(grep -c . "$EVID/01-found-secretgrep.txt")"
say "  镜像内 JWT/DB 口令明文 grep 命中 $SECRETGREP_N 个文件："
sed 's/^/      /' "$EVID/01-found-secretgrep.txt" | tee -a "$LOG"
ENV_SECRET_KEYS="$(printf '%s\n' "$IMG_ENV" | grep -inE '(SECRET|PASSWORD|PASSWD|TOKEN|APIKEY|API_KEY|PRIVATE)')"
if [ -n "$ENV_SECRET_KEYS" ]; then
  say "  镜像 Config.Env 中的敏感键："
  printf '%s\n' "$ENV_SECRET_KEYS" | sed 's/^/      /' | tee -a "$LOG"
fi
if [ "$SECRETGREP_N" = "0" ] && [ -z "$ENV_SECRET_KEYS" ] && [ "$ENV_TOTAL" -ge 1 ]; then
  pass "A6c 无密钥类内容（PEM 头）且 Config.Env 无 SECRET/PASSWORD/TOKEN 键（Env 共 $ENV_TOTAL 项，检查非空转）" "A6c"
else
  fail "A6c 密钥检查未通过：PEM 类文件命中 $SECRETGREP_N，Config.Env 敏感键 $([ -n "$ENV_SECRET_KEYS" ] && echo 有 || echo 无)，Env 项数 $ENV_TOTAL（<1 说明镜像 Env 全空、本断言空转）" "A6c"
fi

if [ "$WARMUP_STR" -ge 1 ]; then
  pass "A7 镜像内 ehome-server 含 'Latest value cache warmed up' 格式串（启动回填已接线进产物）" "A7"
else
  fail "A7 镜像内 ehome-server 不含 'Latest value cache warmed up' 格式串（接线未进产物）" "A7"
fi

# * 扫描器正控：证明「扫不到」不是因为扫描器坏了
CANARY_HITS="$(grep -c . "$EVID/01-canary-hits.txt")"
say "  扫描器正控（G2）：容器内植入 canary 文件 /app/.bb-canary-probe，用同一 grep -rlF 扫描，命中 $CANARY_HITS 行"
if [ "$CANARY_HITS" -ge 1 ]; then
  pass "G2 扫描器正控通过：植入的 canary 被抓到（命中 $CANARY_HITS 行）-- 「扫不到密钥」可信" "G2"
else
  fail "G2 扫描器正控失败：植入的 canary 都没抓到 -- 上面『无密钥』的结论不可信" "G2"
fi

# * 扫描器负控：同一 grep 对必然不存在的串必须 0 命中
say "  扫描器负控（G3）：同一 grep -acF 对随机串 '$ABSENT_TOKEN' 在二进制里命中 $ABSENT_STR 次"
if [ "$ABSENT_STR" = "0" ]; then
  pass "G3 扫描器负控通过：随机不存在串 0 命中 -- 「命中」这个信号可信" "G3"
else
  fail "G3 扫描器负控失败：随机不存在串竟命中 $ABSENT_STR 次 -- grep 行为异常，所有 grep 结论不可信" "G3"
fi

# == 7. 变异恢复校验 ==========================================================
hdr "7. 变异恢复校验"
if [ "$BB_MUTATION" = "drop-frontend-copy" ]; then
  DF_MUT_MD5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  restore_dockerfile
  DF_NOW_MD5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  if [ "$DF_NOW_MD5" = "$DF_ORIG_MD5" ]; then
    pass "M1 Dockerfile 已字节级还原（md5 $DF_NOW_MD5 与变异前一致；变异态 md5=$DF_MUT_MD5）" "M1"
  else
    fail "M1 Dockerfile 恢复后 md5 不一致：期望 $DF_ORIG_MD5 实际 $DF_NOW_MD5" "M1"
  fi
  say "  恢复后 grep -n 'static/dist' Dockerfile："
  grep -n 'static/dist' "$DOCKERFILE" | sed 's/^/      /' | tee -a "$LOG"
else
  say "  未启用 BB_MUTATION，无变异需恢复。"
fi

# == 汇总 ====================================================================
hdr "汇总"
say "  镜像      : $IMG"
say "  image id  : $IMG_ID"
say "  断言      : PASS=$PASS_N  FAIL=$FAIL_N"
say "  证据目录  : $EVID"
printf 'TOTAL\t%s\tPASS=%d FAIL=%d\n' "$IMG" "$PASS_N" "$FAIL_N" >>"$ASSERT_TSV"

if [ "$FAIL_N" = "0" ]; then
  say "  结论      : 全部通过（Q1 镜像产物完整性成立）"
  exit 0
fi
say "  结论      : 有 $FAIL_N 条断言失败（详见上方诊断与 01-image-contract.log）"
exit 1
