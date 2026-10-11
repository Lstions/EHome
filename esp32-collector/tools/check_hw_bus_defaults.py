#!/usr/bin/env python3
"""门禁：hw_tables 的**总线默认引脚**必须（a）不落在保留脚上、（b）彼此不重号。

## 为什么需要它（2026-10-08/09 两次实测发现）

### 缺陷一（§197，真机复现）：默认引脚落在保留脚上
    hw_tables.c:62   "S3: ... GPIO48=RGB LED"     ← 同一个文件自己写的
    hw_tables.c:91   I2C1.default_scl = 48         ← 却配在它上面
    main.c:45        BOARD_LED_GPIO 48             ← 固件真在驱动这个脚
⇒ 用户按默认值建 I2C1 ⇒ 设备 preinstall rejected: ESP_ERR_INVALID_ARG
  ⇒ **整份 manifest 被拒**（连其它通道一起不装），而界面显示「创建成功」。

### 缺陷二（§198，从设备上报的 capabilities 里发现）：两条总线抢同一个引脚
    C6: UART1.default_rx_pin = 21
    C6: I2C0.default_sda     = 21     ← 同一个脚
⇒ 用户同时启用 UART1 与 I2C0 ⇒ 固件 claim_resource_pin 冲突 ⇒ 同样整份被拒。

### 为什么必须做成门禁
两个清单（保留脚 / 总线默认值）**在同一文件里**，两次仍然写错；
缺陷二是"两条总线各自看都对、放一起才冲突"，人眼更难发现。
而这条断言 20 行就能写。

退出码：0 = 无冲突（已登记项除外）；1 = 发现新冲突；2 = 无法解析。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TABLES = os.path.join(ROOT, "components", "hw_profile", "hw_tables.c")
HEADER = os.path.join(ROOT, "components", "hw_profile", "include", "hw_tables.h")

PIN_FIELDS = ("default_tx_pin", "default_rx_pin", "default_sda", "default_scl",
              "default_mosi", "default_miso", "default_sclk", "default_cs")

# ⚠ 必须把 ifdef **排在** if 前面：正则交替最左优先，写成 (?:if|elif) 时
#   #ifdef 里的 "if" 会先匹配、接着 \s+ 撞到 "def" 而失败 ⇒ #ifdef 永远匹配不到。
#   第一版就栽在这里：只认出 #elif 那一支，整段 S3 被跳过 ⇒ 漏报真实冲突（假绿）。
TARGET_RE = re.compile(
    r"#\s*(?:ifdef|if|elif)\s+(?:defined\s*\(\s*)?CONFIG_IDF_TARGET_(\w+)")
ID_RE = re.compile(r"\.id\s*=\s*[^\w]*(\w+)")

# 已登记的总线默认引脚冲突（每条必须写明理由与复核期限）。
# ⚠ 登记**不是**"允许"：门禁会把它打印成 NOTE，且一旦消失（修好了）会提示删条目。
KNOWN_BUS_PIN_COLLISIONS = [
    {
        "target": "ESP32C6",
        "pin": 21,
        "uses": ["UART1.default_rx_pin", "I2C0.default_sda"],
        "why": ("C6 的 UART1(RX=21) 与 I2C0(SDA=21) 抢同一个脚。 "
                "⚠ **待硬件确认正解**：若板上 I2C0 实际就接在 21，则它与 UART1 是物理冲突，"
                "正解是**不把其中一条总线报进资源表**；若只是取值时没交叉核对，"
                "则把 I2C0 的 SDA 改到一个空闲脚（0-30 内未被占用的）。"
                "两者都需要硬件信息 ⇒ 本轮只登记，不猜。"),
        "review_by": "2026-11-15",
    },
]


def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return f.read()
    except OSError as e:
        print("无法读取 %s: %s" % (path, e))
        sys.exit(2)


def reserved_pins(hdr):
    out, target = {}, None
    for line in hdr.splitlines():
        m = TARGET_RE.search(line)
        if m:
            target = m.group(1)
            out.setdefault(target, {})
            continue
        if re.match(r"\s*#\s*(else|endif)", line):
            target = None
            continue
        m = re.search(r"#\s*define\s+HW_RESERVED_(\w+)\s+(\d+)", line)
        if m and target:
            out[target][m.group(1)] = int(m.group(2))
    bad = sorted(t for t, d in out.items() if not d)
    if not out or bad:
        print("FAIL 无法从 %s 解析出保留脚（targets=%r 空的=%r）" % (HEADER, sorted(out), bad))
        sys.exit(2)
    return out


def bus_defaults(tab):
    out, target, cur_id = [], None, None
    for i, line in enumerate(tab.splitlines(), 1):
        m = TARGET_RE.search(line)
        if m:
            target, cur_id = m.group(1), None
            continue
        if re.match(r"\s*#\s*(else|endif)", line):
            target, cur_id = None, None
            continue
        if target is None:
            continue
        mid = ID_RE.search(line)
        if mid:
            cur_id = mid.group(1)
        if cur_id is None:
            continue
        for field in PIN_FIELDS:
            fm = re.search(r"\.%s\s*=\s*(\d+)" % field, line)
            if fm:
                out.append((target, cur_id, field, int(fm.group(1)), i))
    return out


def main():
    hdr, tab = read(HEADER), read(TABLES)
    reserved, defaults = reserved_pins(hdr), bus_defaults(tab)

    # ⚠ 解析器的**自检断言**：这三条不是形式，而是第一版真出过假绿：
    #   只解析出 1 个 target、10 条引脚，却静默 PASS。
    if len(reserved) < 2:
        print("FAIL 应至少解析出 S3 与 C6 两个 target，实际只有 %r ⇒ 解析器多半又写错了"
              % sorted(reserved))
        return 2
    if len(defaults) < 15:
        print("FAIL 总线默认引脚只解析出 %d 条（应 >= 15）⇒ 解析器多半又写错了" % len(defaults))
        return 2

    print("核对 target=%s，保留脚 %d 个，默认引脚 %d 条"
          % ("/".join(sorted(reserved)), sum(len(d) for d in reserved.values()), len(defaults)))

    new_conflicts, known_hits = [], set()

    # ① 保留脚
    for target, bid, field, pin, lineno in defaults:
        for name, rpin in reserved.get(target, {}).items():
            if pin == rpin:
                new_conflicts.append("%s: %s 的 %s = GPIO%d，而 GPIO%d 是保留脚（HW_RESERVED_%s）"
                                     "  [hw_tables.c:%d]"
                                     % (target, bid, field, pin, pin, name, lineno))

    # ② 总线之间抢同一个引脚
    per_pin = {}
    for target, bid, field, pin, lineno in defaults:
        per_pin.setdefault((target, pin), []).append(("%s.%s" % (bid, field), lineno))
    for (target, pin), uses in sorted(per_pin.items()):
        if len(uses) < 2:
            continue
        names = sorted(u for u, _ in uses)
        lines = ", ".join(": %d" % ln for _, ln in sorted(uses))
        reg = next((k for k in KNOWN_BUS_PIN_COLLISIONS
                    if k["target"] == target and k["pin"] == pin
                    and sorted(k["uses"]) == names), None)
        if reg:
            known_hits.add((target, pin))
            print("NOTE %s GPIO%d 被 %d 处共用（**已登记，待硬件确认**，复核 %s）：%s"
                  % (target, pin, len(uses), reg.get("review_by", "?"), ", ".join(names)))
            for l in reg["why"].split(",", 0)[0].splitlines():
                print("     %s" % l.strip())
        else:
            new_conflicts.append("%s: GPIO%d 被 %d 条总线同时当作默认引脚：%s  [hw_tables.c%s]"
                                 % (target, pin, len(uses), ", ".join(names), lines))

    # ③ 登记项若已不复存在，提示删条目（否则登记表会腐烂）
    for k in KNOWN_BUS_PIN_COLLISIONS:
        if (k["target"], k["pin"]) not in known_hits:
            print("NOTE 登记表里的 %s GPIO%d 冲突**已不存在** ⇒ 请删掉该条目"
                  % (k["target"], k["pin"]))

    for c in new_conflicts:
        print("FAIL %s" % c)
    if new_conflicts:
        print("")
        print("含义：设备按默认值启用这些总线时，固件的 resource plan 会返回")
        print("      ESP_ERR_INVALID_ARG ⇒ **整份 manifest 被拒**（连其它通道一起不装），")
        print("      而后端在界面上显示的是「创建成功」。")
        print("处置：改掉其中一个默认引脚，或从资源上报里去掉其中一条总线。")
        return 1

    print("PASS 默认引脚不落在保留脚上，且总线之间不抢引脚（已登记项见上）")
    return 0


def _run():
    """⚠ 把"崩溃"与"发现冲突"分开：未捕获异常在 Python 里也退 rc=1，
    那会让**门禁自己崩了**看起来和**门禁咬到了**一模一样（都非零）。
    ⇒ 异常一律转成 rc=2（"无法判定"），与 rc=1（"确实发现冲突"）区分开。"""
    try:
        return main()
    except SystemExit:
        raise
    except Exception:
        import traceback
        traceback.print_exc()
        print("")
        # ⚠ Python 字面量里不能再嵌同种引号；用单引号包中文引号部分。
        #   （这个坑我在本轮踩了两次，故写在这里。）
        print('FAIL 门禁**自身崩溃**(rc=2) —— 注意这**不是**「发现了冲突」，'
              '而是判据没跑完 ⇒ 本次结论无效，须先修门禁。')
        return 2


if __name__ == "__main__":
    sys.exit(_run())