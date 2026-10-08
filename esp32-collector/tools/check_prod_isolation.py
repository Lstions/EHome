#!/usr/bin/env python3
"""门禁：构建配置**不得**指向生产服务器（192.168.20.6）。

## 为什么需要它（D-27，2026-10-08 实事故）
本会话把 S3P 反复刷机做 3.0 端到端验证时，所有测试 defaults **都没有覆盖**
CONFIG_COLLECTOR_MQTT_BROKER_URL，而 config/mqtt-broker.defaults 里
该值是 mqtt://192.168.20.6:1883 ⇒ 设备**连上了生产 broker**。

实测证据（设备侧日志）：
    I (2251) MQTT: MQTT connected to broker
且 ehome_mqtt.c:465 在连接成功后会**自动订阅** nodes/<mac>/{down,control}
⇒ 不只是"连上"，而是**订阅了生产的下行主题**。

§139.3 早已写明本地联调栈的隔离要求是"库/端口/broker **每一项**都与生产不同"，
但**只有这条要求，没有门禁** ⇒ 靠人记 ⇒ 这次就没记住。

## 本门禁判据
1. 交付 defaults（config/*.defaults）里出现生产地址 ⇒ 必须在白名单里且写明理由，否则 FAIL
2. 本地联调 defaults（__scratch_*/defaults/*.defaults）若**未覆盖** broker、
   或覆盖成了生产地址 ⇒ FAIL
3. 只读，不修改任何文件。

## 用法
  python3 tools/check_prod_isolation.py
退出码 0 = 隔离完好；1 = 发现指向生产；2 = 无法解析。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
SCRATCH_DIR = os.path.join(ROOT, "..", "__scratch_v3", "defaults")

PROD_HOST = "192.168.20.6"
BROKER_KEY = "CONFIG_COLLECTOR_MQTT_BROKER_URL"
LOCAL_BROKER = "mqtt://192.168.20.3:1884"

ALLOWED_PROD_DEFAULTS = {
    os.path.join("config", "mqtt-broker.defaults"):
        "出厂默认 broker（现场地址）；本地联调**必须覆盖**它",
}

RE_LINE = re.compile(r"^\s*(CONFIG_[A-Z0-9_]+)\s*=\s*(.+?)\s*$")


def read(path):
    try:
        return open(path, encoding="utf-8", errors="replace").read()
    except OSError:
        return None


def scan_prod_keys(path):
    src = read(path)
    if src is None:
        return None
    hits = []
    for line in src.splitlines():
        m = RE_LINE.match(line)
        if m and PROD_HOST in m.group(2):
            hits.append(m.group(1))
    return hits


def main():
    problems = []
    checked = 0

    cfg_dir = os.path.join(ROOT, "config")
    if os.path.isdir(cfg_dir):
        for name in sorted(os.listdir(cfg_dir)):
            if not name.endswith(".defaults"):
                continue
            rel = os.path.join("config", name)
            hits = scan_prod_keys(os.path.join(cfg_dir, name))
            if hits is None:
                continue
            checked += 1
            if hits and rel not in ALLOWED_PROD_DEFAULTS:
                problems.append(
                    "%s 指向生产 %s（键：%s）且不在白名单 —— "
                    "交付默认值不该指向生产，除非显式登记理由" % (rel, PROD_HOST, ", ".join(hits)))
            elif hits:
                print("  OK  %s（白名单）：%s" % (rel, ", ".join(hits)))

    if os.path.isdir(SCRATCH_DIR):
        for name in sorted(os.listdir(SCRATCH_DIR)):
            if not name.endswith(".defaults"):
                continue
            p = os.path.join(SCRATCH_DIR, name)
            src = read(p)
            if src is None:
                continue
            checked += 1
            hits = scan_prod_keys(p)
            if hits:
                problems.append(
                    "__scratch_v3/defaults/%s 把 broker 指向**生产** %s ⇒ "
                    "设备会连生产 broker（本会话真实事故）" % (name, PROD_HOST))
            elif BROKER_KEY not in src:
                problems.append(
                    "__scratch_v3/defaults/%s **未覆盖** %s ⇒ 继承 config/mqtt-broker.defaults "
                    "里的生产地址 ⇒ 设备会连生产 broker（§139.3 的隔离要求）"
                    % (name, BROKER_KEY))
    else:
        print("  SKIP 未找到本地联调 defaults 目录（%s）" % SCRATCH_DIR)

    # ── 3) ⚠ 陈旧构建产物：build/ 下的 sdkconfig 若指向生产 ⇒ FAIL ──
    # 为什么必须查它（2026-10-08 本轮发现）：
    #   esp32-collector/build/s3p-n16/ 是 **2026-10-06** 的旧产物，里面 9 个文件
    #   （sdkconfig / .broker.defaults / ehome_collector.bin / .elf / .obj / .a ...）
    #   都嵌着**生产地址**。任何 flash 脚本若误用它刷机，设备就会连生产 broker。
    #   ⚠ 光修 defaults 文件**不够** —— 旧产物不会因此改变。
    #   build/ 是 gitignore 的，但它**确实存在于工作树里**且可被刷机脚本引用。
    # ⚠ 两个位置都要扫：交付构建目录 build/，以及本地联调构建目录 __scratch_v3/build/。
    #   2026-10-08 实测：两处共 **189 个文件**（9 + 180）嵌着生产地址 —— 全是修复前构建的。
    for build_dir in (os.path.join(ROOT, "build"),
                      os.path.join(ROOT, "..", "__scratch_v3", "build")):
        if not os.path.isdir(build_dir):
            continue
        for dirpath, _dirnames, filenames in os.walk(build_dir):
            for fn in filenames:
                if fn != "sdkconfig":
                    continue
                p = os.path.join(dirpath, fn)
                src = read(p)
                if src is None or PROD_HOST not in src:
                    continue
                checked += 1
                problems.append(
                    "%s（**陈旧构建产物**）指向生产 %s ⇒ 误用它刷机就会连生产 broker；"
                    "修法：删掉该 build 目录重新构建（build/ 是 gitignore 的，可安全重建）"
                    % (os.path.relpath(p, ROOT), PROD_HOST))

    print("")
    print("生产隔离核对（生产主机 %s）：检查 %d 个 defaults/sdkconfig 文件" % (PROD_HOST, checked))
    if problems:
        print("FAIL: 发现指向生产的构建配置：")
        for p in problems:
            print("  - " + p)
        print("")
        print("⚠ 设备连上生产 broker 后会**自动订阅** nodes/<mac>/{down,control}")
        print("  （ehome_mqtt.c:465）—— 不只是连上，而是订阅了生产下行主题。")
        print("  修法：在测试 defaults 里加 " + BROKER_KEY + "=" + LOCAL_BROKER)
        return 1
    print("PASS 无构建配置指向生产服务器")
    return 0


if __name__ == "__main__":
    sys.exit(main())
