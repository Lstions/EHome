#!/usr/bin/env python3
"""门禁：DMA 能力表不得声称"多个 UART 可同时持有 UART DMA"—— 仅对 C6 强断言。

## 依据（仓库内权威文档）
`docs/设计/DMA资源管理设计.md` 的 v2.0 原文（见 git fab4e2fc）§1.1 + §1.2 + §1.3：
  | 平台 | DMA 通道数 | UART DMA 约束 |
  | ESP32-C6 | 3 对 (6 独立通道) | UART0/1 通过单一 UHCI 接口共享 GDMA |
  | ESP32-S3 | 5 对 (GDMA CH0-5) | CH0-4 通用 |
  §1.2 引用 ESP32-C6 TRM v1.2 Ch.4 pp.122-123:
    "UART0 与 UART1 共用一个 UHCI 接口 -> UHCI 在 GDMA 外设选择矩阵中只占一个槽位。"
  §1.3 "ESP32-C6: UART0/1 支持 DMA 但共享 UHCI 接口，同时只能 1 个 UART 使用 DMA"
=> C6 的 UART DMA 是单槽，有 TRM 依据 => 强断言。

## 为什么不对 S3 强断言（避免过度断言）
文档明确把 UHCI 单槽约束只归给 C6，S3 写的是 CH0-4 通用。
本门禁早期版本据 SOC_UHCI_SUPPORTED=1 推断"S3 也只能一个 UART 用 DMA"并判 FAIL，
那是过度断言：有 UHCI 外设 != UART 必须经 UHCI；未查 S3 TRM，未做真机并发实验。
=> S3 只登记待查（打印 NOTE，不影响 rc）。

退出码：0 = 合规；1 = C6 声称超量；2 = 无法判定（解析失败/门禁自身崩溃）。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
HW_TABLES = os.path.join(ROOT, "components", "hw_profile", "hw_tables.c")

DMA_BUS_UART = 0x01

# ⚠ 2026-10-09（用户明确指正）：S3 **也是单槽**。
#   用户原话："C6 S3的所有UART同时都只有有一个能用DMA！！！"
#   硬件依据：S3 soc_caps.h 也有 SOC_UHCI_SUPPORTED 1，且 uhci_ll.h:80-84 的
#   uhci_ll_attach_uart_port 写三个 uartN_ce 位时只有一个能为 1（后 attach 的
#   会清掉前一个 ⇒ 前者 DMA 静默失效）。
#   ⇒ S3 从 NOTED 提升为 ENFORCED。
#
# ⚠ 我曾在 §207.3 撤回这条判断（因为 DMA 设计文档 v2.0 §1.1 把 S3 写成
#   "CH0-4 通用"）。用户指正后确认：**文档那一行是错的，我最初的判断（§206.2）
#   才对，撤回是过度自我怀疑。** 这条教训已记入文档 §212。
ENFORCED = {"esp32c6": 1, "esp32s3": 1}
NOTED = {}


def read(path):
    with open(path, encoding="utf-8") as f:
        return f.read()


def parse_tables(src):
    tables = {}
    parts = re.split(r"#\s*(?:if|elif)\s+(?:defined\s*\(\s*)?(CONFIG_IDF_TARGET_\w+)", src)
    for i in range(1, len(parts) - 1, 2):
        target = parts[i].replace("CONFIG_IDF_TARGET_", "").lower()
        body = parts[i + 1]
        m = re.search(r"hw_dmas\s*\[[^\]]*\]\s*=\s*\{(.*?)\n\};", body, re.S)
        if not m:
            continue
        rows = []
        for rm in re.finditer(r"\{[^}]*?\.name\s*=\s*\"([^\"]+)\"[^}]*?\}", m.group(1)):
            cb = re.search(r"\.compatible_bus\s*=\s*(0x[0-9A-Fa-f]+|\d+)", rm.group(0))
            rows.append((rm.group(1), int(cb.group(1), 0) if cb else 0))
        if rows:
            tables.setdefault(target, []).extend(rows)
    return tables


def main():
    tables = parse_tables(read(HW_TABLES))
    if not tables:
        print("FAIL 解析不出任何 hw_dmas 表 => 门禁自己写错了（假绿）")
        return 2
    print("解析到 %d 个型号的能力表：%s" % (len(tables), ", ".join(sorted(tables))))
    bad = 0
    for target in sorted(tables):
        n = sum(1 for _, cb in tables[target] if cb & DMA_BUS_UART)
        if target in ENFORCED:
            limit = ENFORCED[target]
            ok = n <= limit
            print("  %-8s 含 UART 能力的通道 %d 条（强断言上限 %d）%s"
                  % (target, n, limit, "OK" if ok else "FAIL"))
            if not ok:
                bad += 1
        elif target in NOTED:
            print("  %-8s 含 UART 能力的通道 %d 条  NOTE 待查：%s" % (target, n, NOTED[target]))
        else:
            print("  %-8s 含 UART 能力的通道 %d 条  （无判据，跳过）" % (target, n))
    if bad:
        print("")
        print("含义：C6 能力表声称多条通道可同时做 UART DMA，但 TRM 明确 UHCI 只有一个 peri-select 槽。")
        print("     后果：dma_pool 会把 UART0/1 分到不同通道且互不冲突地成功，")
        print("     一旦真去调 uhci_*，uhci_ll_attach_uart_port 会让后 attach 的抢走 UHCI，")
        print("     前一个 UART 的 DMA 静默失效（不报错）。")
        print("     处置：C6 表保持只有 1 条 compatible_bus 含 UART（当前即如此）。")
        return 1
    print("PASS 强断言型号的 UART DMA 能力未超硬件上限")
    return 0


def _run():
    try:
        return main()
    except SystemExit:
        raise
    except Exception:
        import traceback
        traceback.print_exc()
        print("")
        print("FAIL 门禁自身崩溃(rc=2) —— 这不是发现了违例，本次结论无效。")
        return 2


if __name__ == "__main__":
    sys.exit(_run())