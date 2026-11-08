#!/usr/bin/env bash
# 跑测试并输出可复现的结果头部：sha + 树状态 + 命令 + 结果
#
# 为什么需要它：今天的教训是「报红/报绿必须绑定 commit sha」。
# 「工作树脏」这个描述不够 —— 它没说脏在哪，而 sha 是唯一可复现的基准点。
#
# 用法：bash scripts/test_with_sha.sh [go test 参数...]
#   例：bash scripts/test_with_sha.sh -count=1 ./internal/api/
#
#
# ⚠ 需要 PATH 里有 `go`。本机（开发机）没有 go —— 标准跑法是 docker 容器，
#   所以本脚本在开发机上不能直接跑测试，只能用来打印基准点头部。
#   要在容器里跑，用 scripts/gow.sh（它转发 EHOME_* 环境变量）。
#   为什么不支持 GO="docker run ..." 覆盖：多词命令经 $GO 展开会破坏
#   引号（实测 `-v "$PWD:/ws"` 被拆成两个词，docker 报 invalid volume name）。
#   要覆盖执行器就写一个只含单个可执行文件的包装脚本，放进 PATH。
set -uo pipefail
cd "$(dirname "$0")/.." || exit 2

SHA="$(git rev-parse HEAD)"
SHORT="$(git rev-parse --short HEAD)"
BRANCH="$(git rev-parse --abbrev-ref HEAD)"
TRACKED_DIRTY="$(git status --porcelain --untracked-files=no | wc -l | tr -d " ")"

# 未跟踪**源码**文件：排除 .tmp-* / .analysis 等临时产物后再数。
#
# 两个坑（都已踩过）：
#  ① git 对含空格的路径输出带双引号 => 过滤前必须先剥掉引号，否则 `^\.tmp-` 匹配不到；
#  ② 本仓有 2 万+ 个临时产物 => 不排除的话「未跟踪 N 个」这个数字失去意义。
SRC_UNTRACKED_LIST="$(git status --porcelain --untracked-files=all \
  | grep "^?? " \
  | sed "s/^?? //" \
  | sed "s/^\"//; s/\"$//" \
  | grep -vE "^\.(tmp-|analysis)" \
  || true)"

echo "================================================================================"
echo " 测试基准点"
echo "   branch        : $BRANCH"
echo "   HEAD          : $SHA"
echo "   已跟踪改动     : $TRACKED_DIRTY 个文件"
echo "   未跟踪源码文件 : $(printf "%s" "$SRC_UNTRACKED_LIST" | grep -c . || true) 个（已排除 .tmp-* / .analysis）"
echo "   时间           : $(date -u +%Y-%m-%dT%H:%M:%SZ)"
echo "   命令           : go test $*"
echo "================================================================================"
if [ "$TRACKED_DIRTY" -ne 0 ]; then
  echo "⚠ 工作树有 $TRACKED_DIRTY 个已跟踪文件被改动 —— 本次结果**不代表 HEAD $SHORT**"
  echo "   改动清单："
  git status --porcelain --untracked-files=no | sed "s/^/     /"
  echo ""
fi
if printf "%s" "$SRC_UNTRACKED_LIST" | grep -q .; then
  echo "   未跟踪源码文件（这些不在 HEAD $SHORT 里）："
  printf "%s\n" "$SRC_UNTRACKED_LIST" | sed "s/^/     + /"
  echo ""
fi
echo ""

if ! command -v go >/dev/null 2>&1; then
  echo "✗ PATH 里没有 go。" >&2
  echo "  本机是开发机（无 go）：用 scripts/gow.sh 在容器里跑，" >&2
  echo "  或在装有 go 的环境里跑本脚本。基准点头部已打印在上面。" >&2
  exit 127
fi
exec go test "$@"