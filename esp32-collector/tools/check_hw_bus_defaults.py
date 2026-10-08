#!/usr/bin/env python3
"""门禁：hw_tables 里的**总线默认引脚**不得落在保留脚上。

## 为什么需要它（2026-10-08 实测发现，§197）

hw_tables.c 自己在这一行就写明：
    S3: GPIO19=USB_D-, GPIO20=USB_D+, **GPIO48=RGB LED**   (hw_tables.c:62)
而**同一个文件**的 I2C1 默认值却是：
    { .id = "I2C1", .port = 1, .default_sda = 47, .default_scl = **48** }  (hw_tables.c:91)

⇒ 后果（真机复现）：用户按默认值建一条 I2C1 通道 ⇒ 后端下发 manifest ⇒ 设备
    BUS_MGR: preinstall rejected by resource plan: ESP_ERR_INVALID_ARG
    ⇒ **整份 manifest 被拒**（连同 3 条 UART 一起不装）⇒ ConfigResult success=0。
    操作员在界面上看到的是「通道创建成功」。
  ⇒ 这正是 hw_tables.c:104-107 记过的那族缺陷（GPIO0 是 BOOT 脚，
     PWM 配到 GPIO0 导致设备每 8.8 秒恢复出厂一次）——**同一个坑，换个引脚又来一次**。

## 判据
对每个 target（S3/C6），读 HW_RESERVED_* 与所有总线表的默认引脚，
断言：**任何默认引脚都不等于任何保留脚**。

## 解析器为什么这样写（第一版写错，记下来）
  ① 必须同时认 #ifdef X 与 #if defined(X) / #elif defined(X)；
  ② .id 与引脚字段**可能跨行**（SPI 的 default_sclk/default_cs 就在下一行）
     ⇒ 用「**最近见过的 .id**」配对，而不是「必须同一行」；
  ③ 必须把 HW_RESERVED_BOOT 也算进保留脚（第一版漏了它）。
  ⚠ 第一版三条都错 ⇒ 只解析出 1 个 target、10 条引脚，并**漏报了真实冲突**（假绿）。
    门禁的解析器写错比没有门禁更危险：它还会让人以为查过了。

退出码：0 = 无冲突；1 = 发现冲突；2 = 无法解析。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
TABLES = os.path.join(ROOT, "components", "hw_profile", "hw_tables.c")
HEADER = os.path.join(ROOT, "components", "hw_profile", "include", "hw_tables.h")

PIN_FIELDS = ("default_tx_pin", "default_rx_pin", "default_sda", "default_scl",
              "default_mosi", "default_miso", "default_sclk", "default_cs")

# ⚠ 必须把 ifdef **排在** if 前面：正则交替是"最左优先"，
#   写成 (?:if|elif) 时，#ifdef 里的 "if" 会先匹配上、接着 \s+ 撞到 "def" 而失败，
#   ⇒ **#ifdef 永远匹配不到**。第一版就栽在这里：只认出了 #elif 那一支（C6），
#   漏掉整个 S3 段 ⇒ 漏报真实冲突（假绿）。
TARGET_RE = re.compile(
    r"#\s*(?:ifdef|if|elif)\s+(?:defined\s*\(\s*)?CONFIG_IDF_TARGET_(\w+)")


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
        print("FAIL 无法从 %s 解析出保留脚（targets=%r 空的=%r）"
              % (HEADER, sorted(out), bad))
        sys.exit(2)
    return out


def bus_defaults(tab):
    # .id 的引号用 [^\w]* 吃掉 —— 这样正则里**不需要出现引号**，
    # 免得在多层嵌套引号里写错（第一版就是这么写挂的）。
    id_re = re.compile(r"\.id\s*=\s*[^\w]*(\w+)")
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
        mid = id_re.search(line)
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
    if not defaults:
        print("FAIL 无法从 %s 解析出任何总线默认引脚" % TABLES)
        return 2

    n_res = sum(len(d) for d in reserved.values())
    print("核对 target=%s，保留脚 %d 个，总线默认引脚 %d 条"
          % ("/".join(sorted(reserved)), n_res, len(defaults)))
    if len(reserved) < 2:
        print("FAIL 应至少解析出 S3 与 C6 两个 target，实际只有 %r" % sorted(reserved))
        return 2
    if len(defaults) < 15:
        print("FAIL 总线默认引脚只解析出 %d 条（应 >= 15）⇒ 解析器多半又写错了"
              % len(defaults))
        return 2

    hits = []
    for target, bid, field, pin, lineno in defaults:
        for name, rpin in reserved.get(target, {}).items():
            if pin == rpin:
                hits.append((target, bid, field, pin, name, lineno))

    for target, bid, field, pin, name, lineno in hits:
        print("FAIL %s: %s 的 %s = GPIO%d，而 GPIO%d 是保留脚（HW_RESERVED_%s）"
              "  [hw_tables.c:%d]" % (target, bid, field, pin, pin, name, lineno))
    if hits:
        print("")
        print("含义：设备按默认值建这条总线时，固件的 resource plan 会返回")
        print("      ESP_ERR_INVALID_ARG ⇒ **整份 manifest 被拒**（连其它通道一起不装），")
        print("      而后端在界面上显示的是「创建成功」。")
        print("处置：把该默认引脚改到一个**非保留**脚，或从资源上报里去掉这条总线。")
        return 1

    print("PASS 所有总线默认引脚都不落在保留脚上")
    return 0


if __name__ == "__main__":
    sys.exit(main())