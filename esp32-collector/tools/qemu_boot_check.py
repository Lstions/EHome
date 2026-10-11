#!/usr/bin/env python3
"""校验：真实固件镜像**能不能启动**（QEMU 跑真产物，不是宿主重实现）。

## 为什么需要（2026-10-07，round 90）

此前"验证"只有两类，而它们**都答不了"这个 .bin 真的能启动吗"**：

  * 宿主测试 —— 编的是**同一份源码**，但换了一套胶水（#ifndef *_HOST_TEST），
    而且**从不经过 bootloader、不读分区表、不碰真实 flash 布局**；
  * build_firmware.sh —— 只证明"能编出来"、体积/内存门禁通过。

**没有任何一处在检查最终产物能不能跑起来。** 而下面这些失败模式恰恰只能这样发现：
  - 分区表与 app 的实际偏移重叠 / app 超过分区大小；
  - 镜像头、bootloader 与 app 的 IDF 版本不匹配；
  - 早期初始化 panic（NVS 打不开、堆不足、断言失败）；
  - 合并 flash 镜像这一步本身出错。

> 一个能通过全部宿主测试、全部构建门禁、却**根本起不来**的固件，
> 在加入本工具之前是不会被任何检查拦住的。

## 能证明什么（这是本工具的**全部**价值，不要外推）
1. ROM 能引导；2. 二级 bootloader 能加载；3. 分区表 md5 校验通过；
4. 应用能到 app_main；5. APP_STATE / CRASH_DIAG 早期初始化完成；
6. 期间**没有** panic / abort / assert / 看门狗复位。

## ⚠ 不能证明什么（必须写在这里，否则会被当成"链路验证过了"）
  * **不能证明 3.0 链路可连** —— QEMU 不模拟 WiFi，而 3.0 是 TCP+TLS over WiFi。
    这是本项目最核心的目标，所以 **QEMU 不能替代真机联调**；
  * **到不了"正常初始化"** —— QEMU 的 esp32s3 机器只暴露 strap_mode 一个 GPIO 属性，
    无法把 GPIO0 置高；固件读到 GPIO0=0 就（**正确地**）进入 BOOT 键下载模式，
    跳过"normal init"。这不是缺陷，是 QEMU 的能力上限；
  * **C6 跑不了** —— QEMU 9.2.2 没有 esp32c6 机器类型；
  * 不能验证任何运行期行为（网络、GPIO、传感器采样）。

## ⚠ 名字刻意**不叫** check_*.py（这是一个有意的取舍，不是遗漏）
门禁自检按 `glob("check_*.py")` 收集，且会**要求每条门禁都有自检配方**。
本工具没有进那个集合，原因：它需要 IDF 环境 + QEMU + 一次完整固件构建
（实测 40~90 秒），进 sweep 会让每次门禁巡检多花好几分钟。

**但"不进 sweep"绝不能变成"没人跑"** —— 那正是本仓反复出现过的
"从未运行的门禁"（见 check_baseline_sync.py 头部 D-27 的记录）。
所以要求：
  * 在**六 profile 构建之后**、或在任何"要交付镜像"的动作之前手工跑一次；
  * 已实测会咬：把 app 区抹成 0 ⇒ rc=1 且指出缺哪些里程碑；
    破坏分区表 ⇒ rc=1 且指出 'Failed to verify partition table'；
    还原后 md5 逐字节一致并复绿。

## 用法
    qemu_boot_check.py                    # s3-n16，自己构建
    qemu_boot_check.py --profile s3-n8
    qemu_boot_check.py --skip-build --build-root /tmp/xxx/s3-n16   # 复用已有产物

退出码 0 = 启动里程碑齐全且无 panic；1 = 失败（缺里程碑 / 有 panic）；
2 = 无法运行（缺 qemu / 构建失败 / 环境不完整）—— "无法判定"不得当成通过。
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import subprocess
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)              # esp32-collector
WT = os.path.dirname(ROOT)                 # worktree root

QEMU_CANDIDATES = [
    "qemu-system-xtensa",
    os.path.expanduser("~/.espressif/tools/qemu-xtensa/"
                       "esp_develop_9.2.2_20260417/qemu/bin/qemu-system-xtensa"),
]

# 必须按顺序出现的里程碑（顺序即启动顺序 —— 用顺序断言可以在"卡在哪一步"时定位）。
#
# ⚠ 顺序是**实测**出来的，不是推测的：我第一版把"分区表校验"放在 app_main 之前，
# 结果门禁对着一份**完全正常**的启动日志报 FAIL（缺"到达 app_main"）。
# 实测行号（/tmp 日志）：ROM=1, 二级 bootloader=10, app_main=61,
# 分区表 MD5 校验=66, State initialized=75 ——
# "Partition table MD5 verified" 是**应用**打的（我们的固件自己读分区表并校验），
# 不是二级 bootloader 打的。⇒ 差点让门禁把正常启动判成失败。
# 这条也说明：**门禁自己的判据同样需要被实测校准**。
MILESTONES = [
    ("ROM 引导",        re.compile(r"ESP-ROM:esp32s3")),
    ("二级 bootloader", re.compile(r"boot: ESP-IDF v")),
    ("到达 app_main",   re.compile(r"Calling app_main\(\)")),
    ("分区表校验",      re.compile(r"Partition table MD5 verified")),
    ("早期初始化",      re.compile(r"APP_STATE: State initialized")),
]

# 出现即判失败（真实故障信号，不是"任何 error 字样"）
FATAL = [
    ("panic",        re.compile(r"panic|Guru Meditation", re.I)),
    ("abort",        re.compile(r"abort\(\) was called", re.I)),
    ("断言失败",     re.compile(r"assert failed", re.I)),
    ("看门狗复位",   re.compile(r"rst:0x[0-9a-f]+ \((?!POWERON)[A-Z_]+\)")),
    ("栈溢出",       re.compile(r"Stack canary|stack overflow", re.I)),
]


def find_qemu():
    for c in QEMU_CANDIDATES:
        p = shutil.which(c) if os.sep not in c else (c if os.path.exists(c) else None)
        if p:
            return p
    return None


def run(cmd, **kw):
    return subprocess.run(cmd, capture_output=True, text=True, **kw)


def build_variant(build_root, profile):
    """构建"控制台走 UART"的变体。产品控制台在 USB Serial JTAG，QEMU 不模拟它。"""
    env = dict(os.environ)
    env["BUILD_ROOT"] = build_root
    env["EXTRA_SDKCONFIG_DEFAULTS"] = "tools/qemu-console-uart.defaults"
    # LD_LIBRARY_PATH：qemu 需要宿主的 libslirp（见本文件"安装陷阱"说明）
    env["LD_LIBRARY_PATH"] = os.path.expanduser("~/.local/lib") + ":" + \
        env.get("LD_LIBRARY_PATH", "")
    r = run(["./build_firmware.sh", profile], cwd=ROOT, env=env)
    return r.returncode, (r.stdout or "") + (r.stderr or "")


def gen_images(build_dir, project):
    """用 idf.py qemu 生成 qemu_flash.bin / qemu_efuse.bin（它会自己 merge-bin）。"""
    # 它会把 QEMU 跑起来；我们只等镜像出现就杀掉。--qemu-extra-args 给个立即退出的招不行
    # （机器需要一直跑），所以用超时 —— 超时是**预期**的，不是错误。
    try:
        subprocess.run(
            ["idf.py", "-B", build_dir,
             "-D", "SDKCONFIG=" + os.path.join(build_dir, "sdkconfig"),
             "qemu", "--qemu-extra-args=-nographic"],
            cwd=project, env=os.environ,
            capture_output=True, text=True, timeout=25)
    except subprocess.TimeoutExpired:
        pass
    flash = os.path.join(build_dir, "qemu_flash.bin")
    efuse = os.path.join(build_dir, "qemu_efuse.bin")
    return (flash if os.path.exists(flash) else None,
            efuse if os.path.exists(efuse) else None)


def boot(qemu, flash, efuse, timeout_s):
    args = [
        qemu, "-M", "esp32s3", "-m", "32M",
        "-drive", "file=%s,if=mtd,format=raw" % flash,
    ]
    if efuse:
        args += ["-drive", "file=%s,if=none,format=raw,id=efuse" % efuse,
                 "-global", "driver=nvram.esp32s3.efuse,property=drive,value=efuse"]
    args += [
        "-global", "driver=timer.esp32s3.timg,property=wdt_disable,value=true",
        # ⚠ strap_mode=0x04：让 ROM 从 SPI flash 启动。
        #   其他取值实测：0x06/0x07 => SPI_DOWNLOAD_BOOT（wait uart0 download）；
        #   0x01/0x03/0x05 => 连日志都没有。0x04 是唯一"确实进到 app"的。
        "-global", "driver=esp32s3.gpio,property=strap_mode,value=0x04",
        "-nographic",
    ]
    try:
        r = subprocess.run(args, capture_output=True, text=True, timeout=timeout_s)
        out = (r.stdout or "") + (r.stderr or "")
    except subprocess.TimeoutExpired as e:
        # 预期：固件跑完后进入等待（下载模式），由超时结束。
        out = ((e.stdout or b"").decode("utf-8", "replace") if isinstance(e.stdout, bytes)
               else (e.stdout or "")) + \
              ((e.stderr or b"").decode("utf-8", "replace") if isinstance(e.stderr, bytes)
               else (e.stderr or ""))
    return out.replace("\r", "")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--profile", default="s3-n16")
    ap.add_argument("--build-root", default="/tmp/ehome-qemu-boot")
    ap.add_argument("--timeout", type=int, default=25)
    ap.add_argument("--skip-build", action="store_true")
    ap.add_argument("--save-log", default=None, help="把原始启动日志写到该路径")
    args = ap.parse_args()

    qemu = find_qemu()
    if not qemu:
        print("SKIP 找不到 qemu-system-xtensa ⇒ 无法判定（不得当成通过）")
        print("     安装：python3 $IDF_PATH/tools/idf_tools.py install qemu-xtensa")
        print("     ⚠ 它会因宿主缺 libslirp.so.0 失败并**删掉已解包目录**，报错看起来像 QEMU 坏了。")
        print("     无 root 解法：apt-get download libslirp0 && dpkg-deb -x libslirp0_*.deb ./root")
        print("     cp root/usr/lib/x86_64-linux-gnu/libslirp.so.0* ~/.local/lib/ && export LD_LIBRARY_PATH=~/.local/lib")
        return 2

    build_dir = os.path.join(args.build_root, args.profile)
    if not args.skip_build:
        print("==> 构建 QEMU 变体（控制台走 UART；产品是 USB Serial JTAG，QEMU 不模拟它）")
        rc, out = build_variant(args.build_root, args.profile)
        if rc != 0:
            print("FAIL 构建失败 rc=%d" % rc)
            for line in out.splitlines()[-12:]:
                print("     | " + line)
            return 2
        print("    构建 rc=0")

    flash = os.path.join(build_dir, "qemu_flash.bin")
    efuse = os.path.join(build_dir, "qemu_efuse.bin")
    if not os.path.exists(flash):
        print("==> 生成 flash/efuse 镜像（idf.py qemu 会自己 merge-bin；超时是预期的）")
        flash, efuse = gen_images(build_dir, ROOT)
    if not flash:
        print("FAIL 找不到 qemu_flash.bin —— 无法判定")
        return 2

    print("==> 启动真实镜像：%s" % os.path.basename(flash))
    log = boot(qemu, flash, efuse if os.path.exists(efuse) else None, args.timeout)
    if args.save_log:
        with open(args.save_log, "w", encoding="utf-8") as f:
            f.write(log)

    lines = log.splitlines()
    if len(lines) < 5:
        print("FAIL 串口只有 %d 行 —— 镜像没有真正跑起来（或控制台通道不对）" % len(lines))
        for line in lines[:10]:
            print("     | " + line)
        return 1

    # 1) 里程碑按**顺序**检查（这样"卡在哪一步"能直接看出来）
    pos = 0
    missing = []
    for name, rx in MILESTONES:
        found = -1
        for i in range(pos, len(lines)):
            if rx.search(lines[i]):
                found = i
                break
        if found < 0:
            missing.append(name)
        else:
            pos = found + 1
    if missing:
        print("FAIL 启动里程碑缺失（按启动顺序，卡在：%s）" % missing[0])
        for m in missing:
            print("     缺: %s" % m)
        print("     ---- 串口尾部 ----")
        for line in lines[-14:]:
            print("     | " + line)
        return 1

    # 2) 致命信号
    fatal = []
    for name, rx in FATAL:
        for line in lines:
            if rx.search(line):
                fatal.append((name, line.strip()[:110]))
                break
    if fatal:
        print("FAIL 启动过程中出现致命信号：")
        for name, line in fatal:
            print("     [%s] %s" % (name, line))
        return 1

    print("PASS 真实镜像启动成功：%d 行串口输出，里程碑齐全，无 panic/abort/断言/栈溢出"
          % len(lines))
    for name, _ in MILESTONES:
        print("     ok  %s" % name)
    if "entering download mode" in log:
        print("     注：固件随后进入 BOOT 键下载模式 —— QEMU 的 GPIO0 恒为低所致，")
        print("         是**正确行为**（真机按住 BOOT 就是这样），本工具到不了 normal init。")
    print("     ⚠ 本工具**不**证明 3.0 链路可连（QEMU 无 WiFi 模拟）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
