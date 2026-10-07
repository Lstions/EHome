#!/bin/bash
# run_backend_golden.sh —— 跑后端（Go）侧的 S0 对锚测试
#
# 为什么单独一个脚本：本仓的 Go 工具链是【自包含】放在 <repo>/.tools/gotool 的
# （与 .tools/cmake-3.30.5-linux-x86_64 同一约定），不在系统 PATH 里。
# 直接 go test 会因为找不到 go 而"看起来后端没有测试"。
#
# 注意：worktree（.worktrees/v3）共享【主仓的】.tools —— 所以要多处查找，
# 不能只按"脚本所在目录上溯"推断（第一次就是这么写错的）。
#
# 用法：tools/run_backend_golden.sh [额外 go test 参数...]
set -u

HERE="$(cd "$(dirname "$0")" && pwd)"
WORKTREE="$(cd "$HERE/../.." && pwd)"                 # esp32-collector -> worktree 根
MAINREPO="$(cd "$WORKTREE/../.." 2>/dev/null && pwd)" # .worktrees/v3 -> 主仓根

find_go() {
    for c in \
        "$MAINREPO/.tools/gotool/bin/go" \
        "$WORKTREE/../.tools/gotool/bin/go" \
        "$WORKTREE/.tools/gotool/bin/go" \
        "$HOME/.local/share/go/bin/go" \
        "/usr/local/go/bin/go" \
        "/usr/lib/go/bin/go" ; do
        [ -n "$c" ] && [ -x "$c" ] && { echo "$c"; return 0; }
    done
    # 最后才依赖 PATH
    command -v go 2>/dev/null && return 0
    return 1
}

GO="$(find_go)" || {
    echo "FAIL 找不到 Go 工具链。已尝试："
    echo "       $MAINREPO/.tools/gotool/bin/go"
    echo "       $WORKTREE/../.tools/gotool/bin/go"
    echo "       /usr/local/go/bin/go  等"
    echo "安装（自包含，不动系统）："
    echo "  curl -sSLo /tmp/go.tgz https://go.dev/dl/go1.27.1.linux-amd64.tar.gz"
    echo "  curl -sS https://dl.google.com/go/go1.27.1.linux-amd64.tar.gz.sha256   # 必须一致"
    echo "  mkdir -p $MAINREPO/.tools/gotool && tar -C $MAINREPO/.tools/gotool --strip-components=1 -xzf /tmp/go.tgz"
    exit 2
}

echo "使用 Go: $GO  ($("$GO" version))"
export PATH="$(dirname "$GO"):$PATH"

cd "$WORKTREE/backend" || exit 2

# -count=1 关掉测试缓存：否则改了实现/向量后可能拿到上一次的"PASS"，
# 让门禁变成假绿（本次开发中确实踩到过：日志显示 "(cached)"）。
# ⚠ 必须把【所有】消费共享向量的包都跑上。
# 原来只跑 ./pkg/frame/（2.6 的 protobuf TLV 向量），
# 于是 3.0 的 12 B 定界头（./pkg/protoframe/）**从来没被这个脚本跑过** ——
# 门禁覆盖不到新包，等于新包没有门禁。
#
# 这里用显式列表而不是 ./... ：
#   - ./... 会带上大量依赖 DB/网络的包，在没有环境的机器上必然红，
#     结果就是"门禁常年红 ⇒ 没人看"（比没有门禁更糟）；
#   - 显式列表让"哪个包受 S0 契约约束"这件事**可见**。
PACKAGES="./pkg/frame/ ./pkg/protoframe/"

echo "运行后端 S0 对锚：go test -count=1 $PACKAGES $*"
exec "$GO" test -count=1 $PACKAGES "$@"
