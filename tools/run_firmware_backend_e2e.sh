#!/usr/bin/env bash
# run_firmware_backend_e2e.sh -- 跨语言对锚：**真实固件 C** ↔ **真实后端 Go**。
#
# ## 为什么需要一个脚本（而不是只写一个 Go 测试）
#
# 后端那个测试需要固件侧客户端的**可执行文件路径**。若没有这个脚本：
#   - 没人构建固件客户端 ⇒ 测试 Skip ⇒ **静默不跑**（假绿，本项目最忌讳的形态）；
#   - 或者有人手工设了环境变量、路径却过期 ⇒ 得到的是"上一次的二进制"的结论。
# 所以：**构建与运行必须绑在一起**，且缺任何一半都要**响亮地失败**。
#
# ## 这个脚本证明什么、不证明什么
#   证明：两端**各自的真实实现**（固件 session/rx_pump/wire；后端 protoframe/device_op 编解码）
#         能在**真实 socket 流**上互相理解，包括**逐字节切分**（D-09 那一类）。
#   不证明：TLS、真实 ESP32 硬件、WiFi。理由见 backend/cmd/server/device_e2e_firmware_test.go 的头部说明。
set -uo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
FW="$ROOT/esp32-collector"
BE="$ROOT/backend"

CMAKE_BIN="/mnt/storage/WorkSpace/EHome/.tools/cmake-3.30.5-linux-x86_64/bin"
GO_BIN="/mnt/storage/WorkSpace/EHome/.tools/gotool/bin"
# ⚠ 默认 build 目录**带 pid**: 两人（或 CI 的两个 job）同时跑本脚本时,
# 若共用同一个目录会并发 cmake 同一个构建树 ⇒ 目标文件被互相覆盖,
# 症状是"莫名其妙的编译失败/测试跑的是别人的二进制"。
# 这是实测踩到的（v3-firmware 与 Lead 同时跑过本脚本）。
BUILD_DIR="${EHOME_FW_E2E_BUILD:-/tmp/v3-fw-e2e-build-$$}"
CLIENT_NAME="firmware_tcp_e2e_client"

fail() { echo "FAIL: $*" >&2; exit 1; }

[ -d "$CMAKE_BIN" ] || fail "找不到 cmake：$CMAKE_BIN（本脚本假定本机既有环境；详见 3.0 实施设计 §113.7）"
[ -d "$GO_BIN" ]    || fail "找不到 go：$GO_BIN"
export PATH="$CMAKE_BIN:$GO_BIN:$PATH"

echo "==> 1/3 构建固件侧客户端目标 $CLIENT_NAME"
cmake -S "$FW/host_tests" -B "$BUILD_DIR" >/dev/null 2>&1 \
  || fail "cmake configure 失败（$BUILD_DIR）；先单独跑一次看输出"
if ! cmake --build "$BUILD_DIR" --target "$CLIENT_NAME" -j8 >/tmp/fw-e2e-build.log 2>&1; then
  echo "--- 构建输出尾部 ---" >&2
  tail -25 /tmp/fw-e2e-build.log >&2
  fail "目标 $CLIENT_NAME 构建失败（日志 /tmp/fw-e2e-build.log）"
fi

CLIENT="$BUILD_DIR/$CLIENT_NAME"
[ -x "$CLIENT" ] || fail "目标构建成功但找不到可执行文件：$CLIENT"

echo "==> 2/3 运行跨语言测试（固件=$CLIENT）"
cd "$BE" || fail "cd $BE 失败"
# `-timeout` 是必须的：本测试会 Wait 子进程，若哪一侧挂住，
# **没有它就会永久挂住 CI**，而挂住看起来像"还在跑"，不是失败。
# 加了之后 go test 会 panic 并打印**全部 goroutine 栈** —— 定位"卡在哪一行"的唯一有效手段
# （本次实测就是靠 SIGQUIT 的栈找到 nil-channel 死锁的）。
EHOME_FW_E2E_CLIENT="$CLIENT" go test -count=1 -v -timeout 60s \
  -run 'TestCrossLanguageFirmwareClientOverSocket|TestSharedVectorBytesMatchConstants' \
  ./cmd/server/ 
rc=$?

echo "==> 3/3 结论"
if [ $rc -eq 0 ]; then
  echo "PASS 跨语言对锚通过（固件 C ↔ 后端 Go，真实 socket；TLS/真机不在范围内，见测试头注释）"
else
  echo "FAIL 跨语言对锚未通过（go test rc=$rc）" >&2
fi
exit $rc
