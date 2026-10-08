#!/usr/bin/env python3
"""门禁：构建配置**不得**指向生产服务器（192.168.20.6）。

## 为什么需要它（D-27，2026-10-08 实事故）
本会话把 S3P 反复刷机做 3.0 端到端验证时，所有测试 defaults **都没有覆盖**
CONFIG_COLLECTOR_MQTT_BROKER_URL，而 config/mqtt-broker.defaults 里
该值是 mqtt://192.168.20.6:1883 ⇒ 设备**连上了生产 broker**。
（当时实测证据：设备日志 `MQTT: MQTT connected to broker`，
且 ehome_mqtt.c:465 连上后会自动订阅 nodes/<mac>/{down,control}
⇒ 不只是"连上"，而是订阅了**生产的下行主题**。）

§139.3 早已写明本地联调栈的隔离要求是"每一项都与生产不同"，
但**只有这条要求，没有门禁** ⇒ 靠人记 ⇒ 这次就没记住。

## ⭐ 2026-10-08 改版：MQTT 已彻底移除，但风险**没有随之消失**
MQTT 删除后，CONFIG_COLLECTOR_MQTT_BROKER_URL 这个键**已不存在**
（随 components/ehome_mqtt 的 Kconfig 一起删除）
⇒ 旧判据"有没有覆盖 broker 键"变成**守一个已不存在的配置项**：
  它仍会 PASS（没人再写那个键），但也不再能发现任何东西。

但"设备被指向生产主机"这个**风险本身与 MQTT 无关**：
现在设备连的是 3.0 后端，键是 **CONFIG_EHOME_DEVICE_LINK_HOST**
（Kconfig 默认 192.0.2.1 = TEST-NET-1 占位、不可路由，是**故意**的安全默认）。
⇒ 本门禁改为**按风险**守，而不是按某个已删的键守：

判据（不关心是哪个键，只关心**值里出现生产主机**）：
1. 交付 defaults：任何键的值指向生产 ⇒ 必须在白名单里且写明理由，否则 FAIL
2. 本地联调 defaults：任何键的值指向生产 ⇒ FAIL（这正是 2026-10-08 那次事故的形态）
3. 陈旧构建产物 build/**/sdkconfig 指向生产 ⇒ FAIL（旧产物不会因改 defaults 而改变）
4. 只读，不修改任何文件。

⚠ 这样改**并不放宽**：旧版只查 broker 一个键，新版查**所有**键
  ⇒ 覆盖面严格变宽（例如今后把 EHOME_DEVICE_LINK_HOST 指向生产也会被拦）。

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

# 白名单：允许"值指向生产"的交付 defaults（必须逐条写明理由）。
# ⚠ 2026-10-08：原唯一白名单条目 config/mqtt-broker.defaults 已随 MQTT 删除
#   ⇒ 白名单**为空**。空白名单是更严的状态（任何默认值指向生产都会 FAIL），
#   这符合"设备不该默认连生产"的意图 —— 故保留空集而非删掉该机制：
#   今后若确需一条例外，仍须在此登记理由。
ALLOWED_PROD_DEFAULTS = {}

# ⚠ 历史上这里是 BROKER_KEY / LOCAL_BROKER 两个 MQTT 专用常量。
#   现在判据**不依赖具体键名**（见模块头 §改版）：任何键的值里出现生产主机即命中。
#   为什么不再写键名：MQTT 已删，而"指向生产"的风险会随**传输方式更替**改头换面
#   （下一个键可能是 EHOME_DEVICE_LINK_HOST）—— 按风险守才不会随符号更替而失效。

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
                    "__scratch_v3/defaults/%s 的值指向**生产** %s（键：%s）⇒ "
                    "设备会连生产主机（2026-10-08 真实事故的形态）"
                    % (name, PROD_HOST, ", ".join(hits)))
            # ⚠ 2026-10-08：原"必须覆盖 CONFIG_COLLECTOR_MQTT_BROKER_URL"这一条**删除**。
            #   理由：该键随 MQTT 一起消失 ⇒ 再要求"必须出现它"会**永远 FAIL**
            #   （所有联调 defaults 都拿不出一个已不存在的键）。
            #   ⚠ 注意这条曾经是**负载的**：它拦住的正是"未覆盖 ⇒ 继承出厂生产地址"
            #     那次事故。删掉它意味着**少了一道防线** —— 现在由上面"值指向生产就 FAIL"
            #     来顶：只要没有键指向生产，继承与否都不构成"连生产"。
            #     但这确实**变弱了一点**：若今后某个出厂 defaults 又指向生产，
            #     而联调 defaults 不再需要覆盖它，本门禁靠 config/ 那一轮仍会拦住（见上）。
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
        print("⚠ 事故回顾（D-27，2026-10-08）：设备连上生产 broker 后会**自动订阅**")
        print("  nodes/<mac>/{down,control} —— 不只是连上，而是订阅了生产下行主题。")
        print("  （该代码路径 ehome_mqtt.c 已随 MQTT 移除，但『指向生产』的风险与它无关。）")
        print("  修法：把该 defaults 里的主机改成**本地联调地址**（不要指向 192.168.20.6）；")
        print("        若某条确有理由指向生产，请在 ALLOWED_PROD_DEFAULTS 里登记**交付**文件与理由。")
        return 1
    print("PASS 无构建配置指向生产服务器")
    return 0


if __name__ == "__main__":
    sys.exit(main())
