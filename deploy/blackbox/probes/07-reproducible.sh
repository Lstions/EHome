#!/usr/bin/env bash
# =============================================================================
# EHomeSystem 部署黑盒验证 · 子代理 A · Q7「镜像可重复构建」
# 方案：docs/分析/部署黑盒验证方案-2026-09-16.md §1/§3/§4-A
# 契约：deploy/blackbox/CONTRACT.md（project/端口/库名可覆盖，默认=契约值）
# -----------------------------------------------------------------------------
# 命题：同一 commit 连续构建两次（tag rep1/rep2），index.html 所引用的
#       asset 文件名集合必然一致。不一致 ⇒ 构建不确定，如实报告，不放宽断言。
#
# 断言：
#   R1   两次构建 index.html 引用的 asset 文件名集合一致（核心）
#   R1b  两次构建 /app/static/dist 整棵目录文件名集合一致（含懒加载 chunk，
#        防止「入口同名但分片变了」漏检）
#   R1c  两次构建 index.html 字节 sha256 一致
#   R2   两次构建镜像体积（info，仅记录不判失败）
#   G1a  分母守卫：asset 引用数 >= 2（两次）
#   G1b  分母守卫：dist 文件数 >= 5（两次）
#   G1c  两次 index.html 均存在
#   G2a  **非空转自证**：把 asset 清单清空（模拟产物缺失），比较器必须报差异
#   G2b  **非空转自证**：index sha 置为 FILE_REMOVED，必须被判出变化
#   G3   幂等性自证：同一清单自比较必须判一致（防比较器恒报不一致）
#   G4   正控：注入已知不同清单必须被判出差异
#   R3   变异模式：删掉前端 COPY ⇒ 输出必须出现可测差异（证明破坏真的改变被测量）
#   M1   变异模式：Dockerfile 恢复后 md5 与变异前逐字节一致
#
# ★ 为什么必须有 G2：若被测产物整体缺失，两次构建都缺、两份清单都空，
#   只做「两次对比」的探针会得出「一致 ⇒ 绿」的**假绿**。必须先证明
#   比较器对「产物缺失」这一情形会红，R1 的绿才可信。
#
# 边界：只 build / image inspect / run；不启动服务、不连 DB、不碰共享
#       容器/端口/库（本脚本不创建任何 container、不创建任何 database）。
#
# 用法（开发自测请用独立 project，见 CONTRACT 规则 B）：
#   deploy/blackbox/probes/07-reproducible.sh
#   BB_PROJECT=ehome-bb-dev-a5 BB_HOME_PORT=18091 BB_DB=ehome_bbdev_a5_1 \
#     deploy/blackbox/probes/07-reproducible.sh
#   BB_MUTATION=drop-frontend-copy ...   # 变异自证（只构建一次变异树）
#   BB_NO_BUILD=1 ...                    # 复用已存在的 rep1/rep2（仅调试）
#   BB_KEEP_IMAGES=1 ...                 # 保留临时镜像（默认结束即删）
# 退出码：0 = 全部通过 / 1 = 有断言失败 / 2 = 环境或前置错误
# =============================================================================
set -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# -- 契约值（全部可覆盖；默认值 = 契约值）------------------------------------
RUNID="$BB_RUNID";         [ -n "$RUNID" ] || RUNID="$(date -u +%Y%m%dT%H%M%SZ)"
PROJECT="$BB_PROJECT";     [ -n "$PROJECT" ] || PROJECT="ehome-bb"
HOME_PORT="$BB_HOME_PORT"; [ -n "$HOME_PORT" ] || HOME_PORT="18080"
DB_NAME="$BB_DB";          [ -n "$DB_NAME" ] || DB_NAME="ehome_bb_$RUNID"
IMG_REPO="$BB_IMG_REPO";   [ -n "$IMG_REPO" ] || IMG_REPO="ehome-bb-img"
TAG1="$BB_TAG1";           [ -n "$TAG1" ] || TAG1="$IMG_REPO:rep1"
TAG2="$BB_TAG2";           [ -n "$TAG2" ] || TAG2="$IMG_REPO:rep2"
TAG_MUT="$BB_TAG_MUT";     [ -n "$TAG_MUT" ] || TAG_MUT="$IMG_REPO:rep-mut"
GOPROXY_VAL="$BB_GOPROXY"; [ -n "$GOPROXY_VAL" ] || GOPROXY_VAL="https://goproxy.cn,direct"
MUT="$BB_MUTATION";        [ -n "$MUT" ] || MUT="none"
NO_BUILD="$BB_NO_BUILD";   [ -n "$NO_BUILD" ] || NO_BUILD=0
DEEP="$BB_DEEP";           [ -n "$DEEP" ] || DEEP=0
KEEP_IMAGES="$BB_KEEP_IMAGES"; [ -n "$KEEP_IMAGES" ] || KEEP_IMAGES=0
DOCKERFILE="$REPO_ROOT/Dockerfile"
TMPBASE="$TMPDIR"; [ -n "$TMPBASE" ] || TMPBASE=/tmp

EVID="$BB_EVIDENCE_DIR"; [ -n "$EVID" ] || EVID="$REPO_ROOT/deploy/blackbox/evidence/A-$RUNID"
mkdir -p "$EVID"
LOG="$EVID/07-reproducible.log"
TSV="$EVID/07-assertions.tsv"
: > "$LOG"; : > "$TSV"

PASS_N=0; FAIL_N=0
say()  { printf '%s\n' "$*"; printf '%s\n' "$*" >>"$LOG"; }
pass() { PASS_N=$((PASS_N+1)); say "  [PASS] $1"; printf 'PASS\t%s\t%s\n' "$2" "$1" >>"$TSV"; }
fail() { FAIL_N=$((FAIL_N+1)); say "  [FAIL] $1"; printf 'FAIL\t%s\t%s\n' "$2" "$1" >>"$TSV"; }
hdr()  { say ""; say "== $* ============================================"; }
need() { command -v "$1" >/dev/null 2>&1 || { say "缺少依赖: $1"; exit 2; }; }
sec()  { awk -v b="$1" -v e="$2" '$0==b{f=1;next} $0==e{f=0} f' "$3"; }
getkv(){ grep -m1 "^$2=" "$1" 2>/dev/null | sed "s/^$2=//"; }

# -- 清理（异常也执行）--------------------------------------------------------
DF_BACKUP=""; DF_ORIG_MD5=""; DF_MUTATED=0
PROBE_SH_FILE=""
cleanup() {
  if [ "$DF_MUTATED" = 1 ] && [ -n "$DF_BACKUP" ] && [ -f "$DF_BACKUP" ]; then
    cp -p "$DF_BACKUP" "$DOCKERFILE"; DF_MUTATED=0
    nm="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
    say "  [TRAP 恢复] Dockerfile 已还原 md5=$nm （原 $DF_ORIG_MD5）"
  fi
  if [ -n "$DF_BACKUP" ] && [ -f "$DF_BACKUP" ]; then rm -f "$DF_BACKUP"; fi
  if [ -n "$PROBE_SH_FILE" ] && [ -f "$PROBE_SH_FILE" ]; then rm -f "$PROBE_SH_FILE"; fi
  if [ "$KEEP_IMAGES" != "1" ]; then
    for t in "$TAG1" "$TAG2" "$TAG_MUT"; do
      if docker image inspect "$t" >/dev/null 2>&1; then
        if docker rmi -f "$t" >/dev/null 2>&1; then say "  [清理] 已删除临时镜像 $t"; else say "  [清理] !! 删除 $t 失败"; fi
      fi
    done
  else
    say "  [清理] BB_KEEP_IMAGES=1 -- 保留临时镜像"
  fi
  return 0
}
trap cleanup EXIT

PROBE_SH_FILE="$(mktemp "$TMPBASE/bb-07-probe.XXXXXX")"
cat >"$PROBE_SH_FILE" <<'PROBE_EOF'
img="$1"
echo "### IMG_ID=$(docker image inspect "$img" --format '{{.Id}}' 2>/dev/null)"
echo "### IMG_SIZE=$(docker image inspect "$img" --format '{{.Size}}' 2>/dev/null)"
docker run --rm -e BB_DEEP="$BB_DEEP" --entrypoint sh "$img" -c '
if [ -f /app/static/dist/index.html ]; then echo "### INDEX_PRESENT=1"; else echo "### INDEX_PRESENT=0"; fi
echo "### INDEX_SHA=$(sha256sum /app/static/dist/index.html 2>/dev/null | cut -d" " -f1)"
echo "### INDEX_SIZE=$(wc -c < /app/static/dist/index.html 2>/dev/null || echo 0)"
echo "### APP_FILE_COUNT=$(find /app -type f 2>/dev/null | wc -l)"
echo "### DIST_TREE_BEGIN"
find /app/static/dist -type f 2>/dev/null | sort
echo "### DIST_TREE_END"
echo "### ASSET_FILES_BEGIN"
ls -1 /app/static/dist/assets 2>/dev/null | sort
echo "### ASSET_FILES_END"
echo "### INDEX_REFS_BEGIN"
tr ">" "\n" < /app/static/dist/index.html 2>/dev/null | grep -oE "(src|href)=\"[^\"]*\"" | sed "s/^[a-z]*=\"//; s/\"$//" | xargs -r -n1 basename 2>/dev/null | sort -u
echo "### INDEX_REFS_END"
if [ "$BB_DEEP" = "1" ]; then
  echo "### APP_SHA_BEGIN"
  find /app -type f 2>/dev/null | sort | xargs -r sha256sum 2>/dev/null
  echo "### APP_SHA_END"
fi
echo "### DONE"
'
PROBE_EOF

# -- 0. 环境 ---------------------------------------------------------------
hdr "0. 环境与前置"
need docker; need awk; need md5sum; need diff; need cmp

SHORT_COMMIT="$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null)"
[ -n "$SHORT_COMMIT" ] || SHORT_COMMIT=nogit
FULL_COMMIT="$(git -C "$REPO_ROOT" rev-parse HEAD 2>/dev/null)"
TREE_MD5="$(git -C "$REPO_ROOT" ls-files -s backend frontend-shared Dockerfile 2>/dev/null | md5sum | awk '{print $1}')"
DF_ORIG_MD5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"

say "  BB_PROJECT   : $PROJECT"
say "  BB_HOME_PORT : $HOME_PORT"
say "  BB_DB_NAME   : $DB_NAME"
say "  证据目录     : $EVID"
say "  commit       : $SHORT_COMMIT ($FULL_COMMIT)"
say "  被测内容指纹 (git ls-files -s backend frontend-shared Dockerfile | md5): $TREE_MD5"
say "  Dockerfile md5: $DF_ORIG_MD5"
say "  docker       : $(docker --version 2>&1)"
say "  GOPROXY      : $GOPROXY_VAL"
say "  BB_MUTATION  : $MUT"
say "  临时镜像 tag : $TAG1 / $TAG2 / $TAG_MUT"
say "  工作树脏文件 :"
git -C "$REPO_ROOT" status --porcelain 2>/dev/null | sed 's/^/    /' | tee -a "$LOG"
say "  注：本脚本不创建任何容器/数据库/网络；不碰共享 PG/EMQX、端口 3080/8082/80。"

if [ -n "$BB_BASELINE_TREE_MD5" ] && [ "$BB_BASELINE_TREE_MD5" != "$TREE_MD5" ]; then
  fail "P0 被测内容指纹与本轮基线不一致（期望 $BB_BASELINE_TREE_MD5 实际 $TREE_MD5）-- 违反 CONTRACT 规则 A，停止下结论" "P0"
  say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"
  exit 2
fi
if [ "$TAG1" = "$TAG2" ]; then say "  !! TAG1 与 TAG2 相同，无法比较"; exit 2; fi

# -- 变异注入 ---------------------------------------------------------------
if [ "$MUT" = "drop-frontend-copy" ]; then
  hdr "变异注入 BB_MUTATION=drop-frontend-copy"
  DF_BACKUP="$(mktemp "$TMPBASE/bb-07-Dockerfile.XXXXXX")"
  cp -p "$DOCKERFILE" "$DF_BACKUP"
  cp "$DOCKERFILE" "$EVID/07-dockerfile.before.txt"
  sed -i 's|^COPY --from=frontend-builder /app/dist ./static/dist$|# MUTATED(07): COPY --from=frontend-builder /app/dist ./static/dist|' "$DOCKERFILE"
  DF_MUTATED=1
  cp "$DOCKERFILE" "$EVID/07-dockerfile.mutated.txt"
  hits="$(grep -c '^# MUTATED(07): COPY --from=frontend-builder /app/dist ./static/dist$' "$DOCKERFILE")"
  say "  变异落地校验：匹配 '# MUTATED(07): COPY ...' 的行数 = $hits （期望 1）"
  grep -n 'static/dist' "$DOCKERFILE" | sed 's/^/    /' | tee -a "$LOG"
  if [ "$hits" != "1" ]; then say "  !! 变异未落地 -- 后续结论不可采信，判环境错误"; exit 2; fi
  say "  变异前 md5=$DF_ORIG_MD5  变异后 md5=$(md5sum "$DOCKERFILE" | awk '{print $1}')"
fi

# -- 构建 -------------------------------------------------------------------
build_img() {  # $1=tag  $2=logfile
  if [ "$NO_BUILD" = "1" ] && docker image inspect "$1" >/dev/null 2>&1; then
    say "  BB_NO_BUILD=1 且 $1 已存在 -- 复用，不重新构建"
    printf 'SKIPPED (reused)\n' >"$2"; return 0
  fi
  printf '$ docker build --progress=plain --no-cache --build-arg GOPROXY=%s -t %s %s\n' "$GOPROXY_VAL" "$1" "$REPO_ROOT" >"$2"
  docker build --progress=plain --no-cache --build-arg "GOPROXY=$GOPROXY_VAL" -t "$1" "$REPO_ROOT" >>"$2" 2>&1
  return $?
}

hdr "1. 构建 (A1)"
if [ "$MUT" = "drop-frontend-copy" ]; then
  say "  [变异模式] 只构建变异树，用于证明「破坏被测产物 ⇒ 输出出现可测差异」"
  say "  复现命令见 07-build-mut.log 首行"
  build_img "$TAG_MUT" "$EVID/07-build-mut.log"
  MUT_RC=$?
  if [ "$MUT_RC" = "0" ]; then
    pass "A1m 变异树构建成功（tag $TAG_MUT，--no-cache）" "A1m"
  else
    fail "A1m 变异树构建失败（exit $MUT_RC）-- 见 07-build-mut.log 末 30 行" "A1m"
    tail -30 "$EVID/07-build-mut.log" | sed 's/^/    | /' | tee -a "$LOG"
  fi
else
  say "  构建 #1: tag $TAG1 （--no-cache，全量重建）"
  build_img "$TAG1" "$EVID/07-build-rep1.log"; RC1=$?
  say "  构建 #2: tag $TAG2 （--no-cache，全量重建）"
  build_img "$TAG2" "$EVID/07-build-rep2.log"; RC2=$?
  if [ "$RC1" = "0" ] && [ "$RC2" = "0" ]; then
    pass "A1 连续两次构建均成功（rep1 exit=0, rep2 exit=0）" "A1"
  else
    fail "A1 构建失败：rep1 exit=$RC1, rep2 exit=$RC2" "A1"
    for f in 07-build-rep1.log 07-build-rep2.log; do
      say "    ---- $f 末尾 30 行 ----"
      tail -30 "$EVID/$f" | sed 's/^/    | /' | tee -a "$LOG"
    done
  fi
fi

# -- 取证 -------------------------------------------------------------------
collect() {  # $1=tag  $2=前缀
  img="$1"; pf="$2"
  out="$EVID/$pf-inspect.txt"
  sh "$PROBE_SH_FILE" "$img" >"$out" 2>"$EVID/$pf-inspect.err"
  rc=$?
  if [ "$rc" != "0" ] || ! grep -q '^### DONE$' "$out"; then
    say "  !! 取证失败 $img (rc=$rc)；stdout/stderr 末尾："
    tail -20 "$out" | sed 's/^/    | /' | tee -a "$LOG"
    tail -20 "$EVID/$pf-inspect.err" | sed 's/^/    | /' | tee -a "$LOG"
    return 1
  fi
  sec "### DIST_TREE_BEGIN"  "### DIST_TREE_END"  "$out" >"$EVID/$pf-dist-tree.txt"
  sec "### ASSET_FILES_BEGIN" "### ASSET_FILES_END" "$out" >"$EVID/$pf-asset-files.txt"
  sec "### INDEX_REFS_BEGIN"  "### INDEX_REFS_END"  "$out" >"$EVID/$pf-index-refs.txt"
  sec "### APP_SHA_BEGIN"      "### APP_SHA_END"      "$out" >"$EVID/$pf-app-sha.txt"
  grep '^### ' "$out" | sed 's/^### //' >"$EVID/$pf-kv.txt"
  return 0
}

hdr "2. 产物取证"
if [ "$MUT" = "drop-frontend-copy" ]; then
  collect "$TAG_MUT" mut || { say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"; exit 2; }
  MUT_PRESENT="$(getkv "$EVID/mut-kv.txt" INDEX_PRESENT)"
  MUT_SHA="$(getkv "$EVID/mut-kv.txt" INDEX_SHA)"
  MUT_REFS="$(grep -c . "$EVID/mut-index-refs.txt")"
  MUT_TREE="$(grep -c . "$EVID/mut-dist-tree.txt")"
  say "  变异镜像：INDEX_PRESENT=$MUT_PRESENT INDEX_SHA=$MUT_SHA asset引用数=$MUT_REFS dist文件数=$MUT_TREE"
  say "  判据：删掉前端 COPY 后，被测量必须出现可测差异（index.html 缺失 / 引用集合变化）。"
  if [ "$MUT_PRESENT" = "0" ] || [ -z "$MUT_SHA" ] || [ "$MUT_REFS" = "0" ]; then
    pass "R3 变异产生可测差异：变异镜像 index.html 缺失或引用为空（PRESENT=$MUT_PRESENT refs=$MUT_REFS）-- 破坏确实改变了被测量" "R3"
  else
    fail "R3 变异未产生可测差异：变异镜像仍有 index.html（sha=$MUT_SHA refs=$MUT_REFS）-- 破坏无效或存在比较盲区" "R3"
  fi
  say ""
  say "  ★ 参考（不构成 07 的失败条件）：完整的『变异 ⇒ ASSERTION 必红』证据由"
  say "    probes/01-image-contract.sh BB_MUTATION=drop-frontend-copy 提供（其 A3/A4 变红）。"
  say "    07 此处只证明「该破坏真的改变了被测量」，与 G2 一起排除比较器空转。"
else
  collect "$TAG1" rep1 || { say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"; exit 2; }
  collect "$TAG2" rep2 || { say "汇总: PASS=$PASS_N FAIL=$FAIL_N 证据: $EVID"; exit 2; }

  R1_ID="$(getkv "$EVID/rep1-kv.txt" IMG_ID)";   R2_ID="$(getkv "$EVID/rep2-kv.txt" IMG_ID)"
  R1_SZ="$(getkv "$EVID/rep1-kv.txt" IMG_SIZE)"; R2_SZ="$(getkv "$EVID/rep2-kv.txt" IMG_SIZE)"
  R1_PR="$(getkv "$EVID/rep1-kv.txt" INDEX_PRESENT)"
  R2_PR="$(getkv "$EVID/rep2-kv.txt" INDEX_PRESENT)"
  R1_SH="$(getkv "$EVID/rep1-kv.txt" INDEX_SHA)"; R2_SH="$(getkv "$EVID/rep2-kv.txt" INDEX_SHA)"
  R1_IS="$(getkv "$EVID/rep1-kv.txt" INDEX_SIZE)"; R2_IS="$(getkv "$EVID/rep2-kv.txt" INDEX_SIZE)"
  R1_AF="$(getkv "$EVID/rep1-kv.txt" APP_FILE_COUNT)"; R2_AF="$(getkv "$EVID/rep2-kv.txt" APP_FILE_COUNT)"
  A1_N="$(grep -c . "$EVID/rep1-index-refs.txt")"; A2_N="$(grep -c . "$EVID/rep2-index-refs.txt")"
  T1_N="$(grep -c . "$EVID/rep1-dist-tree.txt")";  T2_N="$(grep -c . "$EVID/rep2-dist-tree.txt")"

  say "  rep1: id=$R1_ID size=$R1_SZ present=$R1_PR sha=$R1_SH index=$R1_IS /app文件=$R1_AF"
  say "  rep2: id=$R2_ID size=$R2_SZ present=$R2_PR sha=$R2_SH index=$R2_IS /app文件=$R2_AF"
  say "  asset 引用数: rep1=$A1_N rep2=$A2_N ；dist 文件数: rep1=$T1_N rep2=$T2_N"

  # -- 3. 分母守卫 + 非空转自证 --------------------------------------------
  hdr "3. 分母守卫与非空转自证 (G1 / G2 / G3 / G4)"
  if [ "$A1_N" -ge 2 ] && [ "$A2_N" -ge 2 ]; then
    pass "G1a 分母守卫：两次都解析到 >=2 个 asset 引用（rep1=$A1_N, rep2=$A2_N）" "G1a"
  else
    fail "G1a 分母守卫失败：asset 引用数 rep1=$A1_N rep2=$A2_N（期望均 >=2）-- 解析器或产物有问题" "G1a"
  fi
  if [ "$T1_N" -ge 5 ] && [ "$T2_N" -ge 5 ]; then
    pass "G1b 分母守卫：两次 dist 文件数 >=5（rep1=$T1_N, rep2=$T2_N）" "G1b"
  else
    fail "G1b 分母守卫失败：dist 文件数 rep1=$T1_N rep2=$T2_N（期望均 >=5）" "G1b"
  fi
  if [ "$R1_PR" = "1" ] && [ "$R2_PR" = "1" ]; then
    pass "G1c 两次 index.html 均存在（rep1=$R1_IS bytes, rep2=$R2_IS bytes）" "G1c"
  else
    fail "G1c 有构建缺 index.html：rep1_present=$R1_PR rep2_present=$R2_PR" "G1c"
  fi

  DEST="$EVID/07-destroyed-assets.txt"; : > "$DEST"
  printf 'FILE_REMOVED\n' >"$EVID/07-destroyed-sha.txt"
  say "  G2 非空转自证：构造「产物缺失」场景（asset 清单清空 + index sha=FILE_REMOVED），"
  say "     再用**完全相同的比较逻辑**（cmp/diff）判断，看它是否真的报差异。"
  if cmp -s "$DEST" "$EVID/rep1-index-refs.txt"; then
    fail "G2a 非空转自证失败：asset 清单清空后比较器仍判「与 rep1 一致」-- 比较器空转，R1 的绿不可信" "G2a"
  else
    pass "G2a 非空转自证通过：清空清单后被判出差异（diff 行数 $(diff "$EVID/rep1-index-refs.txt" "$DEST" | grep -c '^[<>]')）" "G2a"
  fi
  printf '%s\n' "$R1_SH" >"$EVID/07-rep1-sha.txt"
  if cmp -s "$EVID/07-destroyed-sha.txt" "$EVID/07-rep1-sha.txt"; then
    fail "G2b 非空转自证失败：index sha 被改为 FILE_REMOVED 后仍判一致" "G2b"
  else
    pass "G2b 非空转自证通过：sha 变化被检出（$R1_SH vs FILE_REMOVED）" "G2b"
  fi
  if cmp -s "$EVID/rep1-index-refs.txt" "$EVID/rep1-index-refs.txt"; then
    pass "G3 幂等性自证通过：同一清单自比较判一致（防比较器恒报不一致）" "G3"
  else
    fail "G3 幂等性自证失败：同一清单自比较竟判不一致" "G3"
  fi
  printf 'zzz-injected-control.js\n' >"$EVID/07-injected-control.txt"
  if cmp -s "$EVID/07-injected-control.txt" "$EVID/rep1-index-refs.txt"; then
    fail "G4 正控失败：注入的已知不同清单仍判一致" "G4"
  else
    pass "G4 正控通过：注入的已知不同清单被判出差异" "G4"
  fi

  # -- 4. 核心断言 --------------------------------------------------------
  hdr "4. 核心断言：两次构建产物集合一致 (R1 / R1b / R1c / R2)"
  cp "$EVID/rep1-index-refs.txt" "$EVID/07-rep1-assets.txt"
  cp "$EVID/rep2-index-refs.txt" "$EVID/07-rep2-assets.txt"
  say "  rep1 引用 asset（$A1_N 个）："; sed 's/^/      /' "$EVID/07-rep1-assets.txt" | tee -a "$LOG"
  say "  rep2 引用 asset（$A2_N 个）："; sed 's/^/      /' "$EVID/07-rep2-assets.txt" | tee -a "$LOG"

  diff "$EVID/07-rep1-assets.txt" "$EVID/07-rep2-assets.txt" >"$EVID/07-assets.diff" 2>&1
  if [ "$?" = "0" ]; then
    pass "R1 两次构建 index.html 引用的 asset 文件名集合**一致**（各 $A1_N 个，diff 为空）" "R1"
  else
    fail "R1 两次构建 asset 文件名集合**不一致** ⇒ 构建不确定（见 07-assets.diff）" "R1"
    say "    ---- 差异明细（< 仅 rep1 / > 仅 rep2）----"
    sed 's/^/      /' "$EVID/07-assets.diff" | tee -a "$LOG"
  fi

  diff "$EVID/rep1-dist-tree.txt" "$EVID/rep2-dist-tree.txt" >"$EVID/07-dist-tree.diff" 2>&1
  if [ "$?" = "0" ]; then
    pass "R1b 两次构建 /app/static/dist 整棵目录文件名集合一致（各 $T1_N 个文件，含全部懒加载 chunk）" "R1b"
  else
    fail "R1b 两次构建 dist 目录文件名集合不一致 ⇒ 存在额外不确定产物" "R1b"
    say "    ---- dist tree diff（前 40 行）----"
    head -40 "$EVID/07-dist-tree.diff" | sed 's/^/      /' | tee -a "$LOG"
  fi

  if [ -n "$R1_SH" ] && [ "$R1_SH" = "$R2_SH" ]; then
    pass "R1c 两次构建 index.html 字节级 sha256 一致（$R1_SH）" "R1c"
  else
    fail "R1c 两次构建 index.html sha256 不一致：rep1=$R1_SH rep2=$R2_SH" "R1c"
  fi

  if [ "$DEEP" = "1" ]; then
    D1_N="$(grep -c . "$EVID/rep1-app-sha.txt")"; D2_N="$(grep -c . "$EVID/rep2-app-sha.txt")"
    say "  深比对（BB_DEEP=1）：逐文件 sha256，rep1=$D1_N 个 / rep2=$D2_N 个"
    if [ "$D1_N" -ge 5 ] && [ "$D2_N" -ge 5 ]; then
      pass "G1d 深比对分母守卫：两次各取到 >=5 个文件摘要（rep1=$D1_N, rep2=$D2_N）" "G1d"
    else
      fail "G1d 深比对分母守卫失败：rep1=$D1_N, rep2=$D2_N（期望均 >=5）" "G1d"
    fi
    diff "$EVID/rep1-app-sha.txt" "$EVID/rep2-app-sha.txt" >"$EVID/07-app-sha.diff" 2>&1
    if [ "$?" = "0" ]; then
      pass "R1d 深比对：两次构建 /app 内**每个文件的 sha256 全部一致**（共 $D1_N 个文件）⇒ 产物字节级确定" "R1d"
    else
      CHANGED="$(grep -c '^[<>]' "$EVID/07-app-sha.diff")"
      fail "R1d 深比对：两次构建有文件内容不同（$CHANGED 行差异）⇒ 产物**非**字节级确定" "R1d"
      say "    ---- app sha diff（前 40 行，< rep1 / > rep2）----"
      head -40 "$EVID/07-app-sha.diff" | sed 's/^/      /' | tee -a "$LOG"
    fi
  else
    say "  （BB_DEEP=0：跳过逐文件 sha 深比对；如需字节级结论请加 BB_DEEP=1）"
  fi

  if [ -n "$R1_SZ" ] && [ "$R1_SZ" = "$R2_SZ" ]; then
    pass "R2 两次构建镜像体积一致（$R1_SZ bytes）" "R2"
  else
    say "  [info] R2 镜像体积不同：rep1=$R1_SZ rep2=$R2_SZ（仅记录，不判失败）"
  fi
fi

# -- 5. 变异恢复校验 --------------------------------------------------------
hdr "5. 变异恢复校验"
if [ "$MUT" = "drop-frontend-copy" ]; then
  MUT_MD5_NOW="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  cp "$DF_BACKUP" "$DOCKERFILE"; DF_MUTATED=0
  NOW_MD5="$(md5sum "$DOCKERFILE" | awk '{print $1}')"
  if [ "$NOW_MD5" = "$DF_ORIG_MD5" ]; then
    pass "M1 Dockerfile 已字节级还原（md5 $NOW_MD5 == 变异前 $DF_ORIG_MD5；变异态 $MUT_MD5_NOW）" "M1"
  else
    fail "M1 Dockerfile 恢复后 md5 不一致：期望 $DF_ORIG_MD5 实际 $NOW_MD5" "M1"
  fi
  say "  恢复后 grep -n 'static/dist' Dockerfile："
  grep -n 'static/dist' "$DOCKERFILE" | sed 's/^/      /' | tee -a "$LOG"
else
  say "  未启用变异，无需恢复（Dockerfile md5=$DF_ORIG_MD5 未变）"
fi

# -- 6. 清理与残留核对 ------------------------------------------------------
hdr "6. 清理与残留核对"
cleanup
LEFT=""
for t in "$TAG1" "$TAG2" "$TAG_MUT"; do
  if docker image inspect "$t" >/dev/null 2>&1; then LEFT="$LEFT $t"; fi
done
if [ "$KEEP_IMAGES" = "1" ]; then
  say "  [skip] C1：BB_KEEP_IMAGES=1（调试用），按设计保留临时镜像，不做零残留断言"
elif [ -z "$LEFT" ]; then
  pass "C1 临时镜像零残留（$TAG1 / $TAG2 / $TAG_MUT 均已删除）" "C1"
else
  fail "C1 临时镜像残留：$LEFT" "C1"
fi
say "  当前 $IMG_REPO 系列镜像（可能含 01 探针的镜像，非本脚本产物）："
docker images "$IMG_REPO" --format '{{.Repository}}:{{.Tag}} {{.ID}}' 2>/dev/null | sed 's/^/      /' | tee -a "$LOG"
say "  容器残留核对（本脚本不创建容器，应为空）："
LEFT_CT="$(docker ps -a --format '{{.Names}}' 2>/dev/null | grep -cF "$PROJECT" || true)"
if [ "$LEFT_CT" = "0" ]; then
  say "      (无 $PROJECT 前缀容器，符合预期)"
else
  docker ps -a --format '{{.Names}}' 2>/dev/null | grep -F "$PROJECT" | sed 's/^/      /' | tee -a "$LOG"
  fail "C2 发现 $LEFT_CT 个 $PROJECT 前缀容器残留（本脚本不应创建容器）" "C2"
fi

# -- 汇总 -------------------------------------------------------------------
hdr "汇总"
say "  BB_PROJECT=$PROJECT  BB_HOME_PORT=$HOME_PORT  BB_DB_NAME=$DB_NAME"
say "  commit=$SHORT_COMMIT  被测内容指纹=$TREE_MD5"
say "  断言：PASS=$PASS_N  FAIL=$FAIL_N"
say "  证据目录：$EVID"
printf 'TOTAL\t%s\tPASS=%d FAIL=%d\n' "$TAG1" "$PASS_N" "$FAIL_N" >>"$TSV"

if [ "$FAIL_N" = "0" ]; then
  if [ "$MUT" = "drop-frontend-copy" ]; then
    say "  结论：变异自证通过（破坏被测产物 ⇒ 输出出现可测差异；Dockerfile 已字节级还原）"
  else
    say "  结论：Q7 通过 -- 同 commit 两次构建的 asset 文件名集合一致，构建在本断言范围内**确定性**"
  fi
  exit 0
fi
say "  结论：有 $FAIL_N 条断言失败（详见 $LOG）"
exit 1
