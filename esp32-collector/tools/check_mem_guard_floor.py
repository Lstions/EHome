#!/usr/bin/env python3
"""门禁：mem_guard 的**型号 floor 值**必须与设计文档声明一致（防静默改动）。

## 为什么需要它（D-26）
2026-10-08 的型号分叉审计（§186）发现一个**测试盲区**：

  host_tests/mem_guard_tests.c:103 用**自适应宏** `#define FLOOR mem_guard_floor_bytes()`，
  所有断言都相对 FLOOR 写（"恰好等于 floor"、"floor-1"、"floor+4096"）。

⇒ 后果：把 s3p 的 floor 从 16 KiB 改成 8 KiB，**宿主测试仍然全绿**（109/109）。
   即：**这个门禁无法发现"型号 floor 被改动"**。
   而 floor 是**行为阈值**（决定配置事务/OTA 是否放行），
   也是**协议字段**（MemReport field5 上报给后端）——
   静默改它会同时改变设备行为与后端看到的数据，却没有门禁拦住。

## 本门禁判据
从权威源（设计文档）读出三个型号的声明值，与 mem_guard.c 的实际值逐项比对。
任一不符 => FAIL。

## 权威源的选择（为什么用文档而不是另写一份常量表）
设计文档是**决策记录**，floor 的值就是那里的决策产物；
在代码里再抄一份常量表会制造**第二事实源**（P4 违规）。
本门禁只做"文档声明值 == 代码实际值"的一致性核对，不新增事实源。

## 用法
  python3 tools/check_mem_guard_floor.py
退出码 0 = 一致；1 = 不一致；2 = 无法解析（视为失败，不静默通过）。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
MEM_GUARD = os.path.join(ROOT, "main", "mem_guard.c")
# 权威源：floor 的**唯一**书面声明处（逐字格式：s3p ≥16 KiB / s3 ≥8 KiB / c6 ≥12 KiB）。
# ⚠ 决策文档（决策-3.0-s3p-内部RAM）只**引用**该值，不重复声明 ⇒ 不用它做源，
#   否则会制造第二事实源（P4）。
DECISION_DOC = os.path.join(ROOT, "..", "docs", "设计",
                            "配置事务确定性设计-2026-10-05.md")

# 文档里的声明格式：s3p ≥16 KiB / s3 ≥8 KiB / c6 ≥12 KiB
RE_DOC = re.compile(
    r"s3p\s*[≥>=]+\s*(\d+)\s*Ki?B.*?"
    r"s3\s*[≥>=]+\s*(\d+)\s*Ki?B.*?"
    r"c6\s*[≥>=]+\s*(\d+)\s*Ki?B",
    re.S)

# 代码里的分支：每个 #define 前面的条件决定它属于哪个型号
RE_DEFINE = re.compile(r"#define\s+MEM_GUARD_FLOOR_BYTES\s+\((\d+)u?\s*\*\s*(\d+)u?\)")
RE_COND = re.compile(r"#\s*(if|elif)\s+(.+)")


def read(path):
    try:
        return open(path, encoding="utf-8", errors="replace").read()
    except OSError:
        return None


def parse_code(src):
    """返回 {'psram': 字节, 's3': 字节, 'c6': 字节}（缺失的键不出现）。"""
    out = {}
    cond = None
    for line in src.splitlines():
        m = RE_COND.match(line.strip())
        if m:
            cond = m.group(2)
            continue
        m = RE_DEFINE.search(line)
        if m and cond is not None:
            val = int(m.group(1)) * int(m.group(2))
            c = cond
            if "COLLECTOR_PSRAM" in c:
                out["psram"] = val
            elif "ESP32S3" in c:
                out["s3"] = val
            elif "ESP32C6" in c:
                out["c6"] = val
            cond = None
    return out


def main():
    code_src = read(MEM_GUARD)
    if code_src is None:
        print("FAIL: 读不到 %s" % MEM_GUARD)
        return 2
    doc_src = read(DECISION_DOC)
    if doc_src is None:
        print("FAIL: 读不到决策文档 %s（floor 的权威声明处）" % DECISION_DOC)
        return 2

    m = RE_DOC.search(doc_src)
    if not m:
        print("FAIL: 决策文档里找不到 floor 声明（格式：s3p >=16 KiB / s3 >=8 KiB / c6 >=12 KiB）")
        return 2
    doc = {"psram": int(m.group(1)) * 1024,
           "s3": int(m.group(2)) * 1024,
           "c6": int(m.group(3)) * 1024}

    code = parse_code(code_src)
    if set(code) != set(doc):
        print("FAIL: 代码里解析到的型号集合 %s 与文档 %s 不一致" % (sorted(code), sorted(doc)))
        return 2

    bad = []
    for k in ("psram", "s3", "c6"):
        if code[k] != doc[k]:
            bad.append("%s: 代码=%d 文档=%d" % (k, code[k], doc[k]))

    label = {"psram": "s3p(PSRAM)", "s3": "s3", "c6": "c6"}
    print("型号 floor 核对（代码 vs 决策文档）：")
    for k in ("psram", "s3", "c6"):
        mark = "OK " if code[k] == doc[k] else "BAD"
        print("  %s %-11s 代码=%6d  文档=%6d" % (mark, label[k], code[k], doc[k]))

    if bad:
        print("")
        print("FAIL: 型号 floor 与设计文档声明不一致：")
        for b in bad:
            print("  - " + b)
        print("")
        print("⚠ floor 是**行为阈值**（决定配置事务/OTA 是否放行）且**作为 MemReport field5 上报**。")
        print("  改它必须同时改决策文档并走评审 —— 不能静默改。")
        print("  （host_tests 用自适应 FLOOR 宏，改 floor 后测试仍全绿，故需要本门禁。）")
        return 1
    print("PASS 三型号 floor 与决策文档一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
