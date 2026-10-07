#!/usr/bin/env python3
"""门禁：注释里"某件事还没做"的声称，必须与代码事实一致（防过期清单）。

## 为什么需要（2026-10-07，round 87）

`EHOME_DEVICE_LINK_ENABLED` 的 Kconfig help 写着 "Two prerequisites are
still outstanding"，第一条是 "SNTP is not landed" —— 而 SNTP **早已落地**
（components/sntp_mgr + device_link_wiring.c 在 tls_esp 之前创建它）。
同样过期的话还出现在 main/device_link_wiring.h 与 components/tls_esp/include/tls_esp.h。

**过期清单比没有清单更糟**：读的人看到 "Two prerequisites" 会以为
  ① 清单是完整的；② 这两条都还没做。
于是既不会去核对第一条（其实已完成），也不会怀疑**漏了什么** ——
而这张清单当时确实漏了最致命的第三条（**上行成帧**，见 §134）：
两条"前置条件"都满足时，链路**仍然握不上手**。

这类"声称"是**可判定的**（不像 TODO 那样主观）：
  - 声称 X 未落地 ⇒ 检查 X 是否存在（文件 / 符号 / 调用点）；
  - 存在 ⇒ FAIL，并指出该处应更新。

## 判据（只查**能自动判定**的几条，不猜语义）
1. 某处声称 "SNTP 尚未落地" ⇒ 若 components/sntp_mgr 存在且
   device_link_wiring.c 调用了 sntp_mgr_create ⇒ FAIL；
2. 某处声称 "上行未成帧 / 没有帧头" ⇒ 若生产代码里存在成帧函数的**调用点** ⇒ FAIL。

## 口径与边界（重要）
- **不**扫描 TODO/FIXME：那是意图不是事实，无法自动判定；
- 允许显式豁免：行尾写 `[[stale-ok: 理由]]` 即跳过（要求写理由，不允许空豁免）；
- 叙述里**同时**说明"已修/已落地"的句子，视为在讲历史 ⇒ 不报错
  （否则修好之后反而无法描述它曾经坏过）；
- 命中为 0 时不得静默通过：必须证明**扫描器没瞎**（见下界断言）。

退出码 0 = 无过期声称；1 = 有过期声称；2 = 无法判定（前置缺失）。
"""

from __future__ import annotations

import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)          # esp32-collector
MAIN = os.path.join(ROOT, "main")
COMP = os.path.join(ROOT, "components")

SCAN_DIRS = [MAIN, COMP]
EXTS = (".c", ".h")
EXEMPT = "[[stale-ok:"

# 声称"SNTP 还没做"
CLAIM_SNTP = (
    "SNTP 尚未落地", "SNTP 未落地", "SNTP is not landed",
    "SNTP 还没有落地",
)

# 声称"上行没成帧"
CLAIM_FRAME = (
    "上行未成帧", "上行不成帧", "上行没有帧头",
    "uplink is unframed", "uplink not framed", "unframed uplink",
)

# 已修/已落地/已被指出过期的叙述（同一行出现即认为是在讲历史，不报错）。
#
# ⚠ "此前/原写/已过期/曾" 这几个也必须算 —— 否则**修好之后就再也无法描述它曾经坏过**：
# 本门禁第一次跑就抓到了我自己刚写的那句
#     "⚠ 这一段此前写的是"…SNTP 尚未落地" —— 写的时候是真的，但过期了"
# 它是**正确叙述**（说明了现状），却被判为过期声称。
# ⇒ 判据是"这句话在说现在还是过去"，不是"这句话里有没有那个短语"。
# 若只按短语判，唯一的通过方式是把历史抹掉 —— 那正是最坏的做法（§133 的教训：
# "把缺陷写进注释"比"把缺陷删掉"更安全）。
DONE_MARK = (
    "已修", "已落地", "已实现", "已修复", "FIXED", "LANDED",
    "already landed", "task-31", "task-32",
    "此前", "原写", "原为", "已过期", "过期了", "曾写", "曾经",
)


def iter_source_files():
    for base in SCAN_DIRS:
        if not os.path.isdir(base):
            continue
        for dp, dn, fn in os.walk(base):
            dn[:] = [d for d in dn if d not in ("build", "managed_components")]
            for f in fn:
                if f.endswith(EXTS):
                    yield os.path.join(dp, f)


def sntp_is_landed():
    comp = os.path.join(COMP, "sntp_mgr")
    if not os.path.isdir(comp):
        return None, "找不到 components/sntp_mgr"
    if not [f for f in os.listdir(comp) if f.endswith(".c")]:
        return None, "components/sntp_mgr 里没有 .c"
    wiring = os.path.join(MAIN, "device_link_wiring.c")
    if not os.path.exists(wiring):
        return None, "找不到 main/device_link_wiring.c"
    body = open(wiring, encoding="utf-8", errors="replace").read()
    if "sntp_mgr_create" not in body:
        return False, "device_link_wiring.c 未调用 sntp_mgr_create"
    return True, "components/sntp_mgr 存在且 device_link_wiring.c 调用了 sntp_mgr_create"


def framing_is_landed():
    """生产代码里出现成帧函数的**调用**（而非定义）⇒ 已修。"""
    targets = [
        (os.path.join(MAIN, "device_link_wiring.c"), "devlink_encode_frame"),
        (os.path.join(MAIN, "session_transport.c"), "stx_send_frame"),
    ]
    hits = []
    for path, name in targets:
        if not os.path.exists(path):
            continue
        body = open(path, encoding="utf-8", errors="replace").read()
        if name not in body:
            continue
        for i, line in enumerate(body.splitlines(), 1):
            if name not in line:
                continue
            # 定义行签名形如 "int devlink_encode_frame(" ⇒ 跳过
            if "int " + name in line or "(*" in line:
                continue
            if "(" in line and "(" + name not in line:
                pass
            hits.append("%s:%d" % (os.path.basename(path), i))
    return (len(hits) > 0), ("成帧调用点: " + ", ".join(hits[:4]) if hits
                            else "未找到成帧函数调用点")


def scan(claims):
    """返回 [(相对路径, 行号, 行内容)] —— 只报**未标注已修**的命中。"""
    out = []
    for p in iter_source_files():
        try:
            lines = open(p, encoding="utf-8", errors="replace").read().splitlines()
        except Exception:
            continue
        for i, raw in enumerate(lines, 1):
            if EXEMPT in raw:
                continue
            s = raw.strip()
            if not (s.startswith("*") or s.startswith("//") or s.startswith("/*")):
                continue
            if any(c in raw for c in claims):
                if any(d in raw for d in DONE_MARK):
                    continue          # 同一行说明了"已修" ⇒ 历史叙述
                out.append((os.path.relpath(p, ROOT), i, s[:110]))
    return out


def main():
    # ── 下界断言：证明扫描器没瞎（否则"0 命中"可能只是路径错了）──
    n_files = sum(1 for _ in iter_source_files())
    if n_files < 20:
        print("FAIL 只扫到 %d 个源文件 —— 扫描路径错了，本门禁形同虚设" % n_files)
        print("     ROOT=%s" % ROOT)
        return 2

    bad = []

    landed, why = sntp_is_landed()
    if landed is None:
        print("FAIL 无法判定 SNTP 状态：%s" % why)
        return 2
    if landed:
        for f, i, s in scan(CLAIM_SNTP):
            bad.append((f, i, s, "SNTP 已落地（%s）" % why))

    framed, why_f = framing_is_landed()
    if framed:
        for f, i, s in scan(CLAIM_FRAME):
            bad.append((f, i, s, "上行成帧已修（%s）" % why_f))

    if bad:
        print("FAIL 以下注释的声称与代码事实**不一致**（过期清单）：")
        for f, i, s, truth in bad:
            print("    %s:%d" % (f, i))
            print("        声称: %s" % s)
            print("        事实: %s" % truth)
            print("        修法: 更新该注释（或行尾加 %s 理由]] 显式豁免）" % EXEMPT)
        print()
        print("    为什么这比『没有清单』更糟：过期清单让人以为它是完整的，")
        print("    于是既不去核对已完成的条目，也不会怀疑漏了什么。")
        return 1

    print("PASS 扫描 %d 个源文件，未发现过期的『未完成』声称" % n_files)
    print("     （SNTP: %s；成帧: %s）" % ("已落地" if landed else "未落地", why_f))
    return 0


if __name__ == "__main__":
    sys.exit(main())
