#!/usr/bin/env python3
"""门禁：内存基线文档的阈值表必须与 mem_budget.json 一致。

## 为什么需要它（D-28）
`tools/README-mem-budget.md` 写着一条规则：

> 阈值改动必须同步 `docs/验证/内存与吞吐基线-2026-10-05.md` 的对照表；
> 否则下一次回归没有可解释的基准。

**这条规则当时没有任何强制**。结果 L-05 改了 JSON 阈值（168,000→172,000、
185,000→190,000）却漏改文档表 —— 文档仍在说 168,000/185,000。
"可解释的基准"正是以规则**预告**的方式失效了。

同一族问题在本项目反复出现（D-25 假绿阈值、D-26 被吞掉的参数、D-27 从未运行的门禁）：
**规则/检查只要不被执行，就等于不存在。**

本门禁把那条规则变成可执行的：解析基线文档里的阈值表，
逐 profile 与 `mem_budget.json` 实际生效值比对，不一致即 FAIL。

## 口径
- 只比对表里出现的 profile 行；
- 文档允许多列（实测值、余量列），它们不参与比对；
- 文档缺少某 profile 行不报错（不强制列全），但**值不一致必报错**。

退出码 0 = 一致；1 = 不一致；2 = 无法解析。
"""
import json
import os
import re
import sys

BQ = chr(96)

_HERE = os.path.dirname(os.path.abspath(__file__))
REPO = os.path.dirname(_HERE)
THRESHOLDS = os.path.join(_HERE, "mem_budget.json")
BASELINE_DOC = os.path.join(REPO, "..", "docs", "验证",
                            "内存与吞吐基线-2026-10-05.md")

PROFILES = ("s3-n8", "s3-n16", "s3p-n8", "s3p-n16", "c6-n8", "c6-n16")


def effective(profile, cfg):
    """合并 defaults 后的生效阈值。"""
    merged = dict(cfg.get("defaults", {}))
    merged.update(cfg.get("profiles", {}).get(profile, {}))
    return merged


# 只检查【标题里点了名】的章节：含"阈值"或"门禁"的 ## 小节。
# 为什么必须限定范围：同一份文档里还有多张带 profile 名的表 ——
#   §1.1 / §1.2 是【实测】表（首列是 Flash Code 等，不是阈值）
#   §7       是【当日复验快照】（标题写明"快照"，属历史记录，不该被改写）
# 若不限定，门禁会把实测表误判成"阈值不一致"（第一次正是这样误报的）。
# 判据写在文档标题里（含"阈值/门禁"），不是硬编码行号。
SECTION_MARKERS = ("阈值", "门禁")


def parse_doc_table(text):
    """只解析"阈值/门禁"章节里的 profile 行：{profile: [int, ...]}。"""
    found = {}
    in_scope = False
    for line in text.splitlines():
        stripped = line.strip()
        if stripped.startswith("## "):
            in_scope = any(m in stripped for m in SECTION_MARKERS)
            continue
        if not in_scope:
            continue
        line = stripped
        if not line.startswith("|"):
            continue
        cells = [c.strip() for c in line.strip("|").split("|")]
        if not cells:
            continue
        name = cells[0].replace(BQ, "").replace("*", "").strip()
        if name not in PROFILES:
            continue
        vals = []
        for c in cells[1:]:
            for m in re.finditer(r"[0-9][0-9,]*", c):
                vals.append(int(m.group(0).replace(",", "")))
        found[name] = vals
    return found


def main():
    if not os.path.isfile(THRESHOLDS):
        print("FAIL 找不到 %s" % THRESHOLDS)
        return 2
    if not os.path.isfile(BASELINE_DOC):
        print("FAIL 找不到基线文档 %s" % os.path.normpath(BASELINE_DOC))
        return 2

    with open(THRESHOLDS, encoding="utf-8") as fh:
        cfg = json.load(fh)
    with open(BASELINE_DOC, encoding="utf-8") as fh:
        text = fh.read()
    table = parse_doc_table(text)

    if not table:
        print("FAIL 基线文档里没解析到任何 profile 行（表格格式变了？）")
        return 2

    bad = []
    checked = 0
    for prof, vals in sorted(table.items()):
        eff = effective(prof, cfg)
        want_used = eff.get("dir_used_max")
        want_remain = eff.get("dir_remain_min")
        if want_used is None or not vals:
            continue
        checked += 1
        # 约定：该行第一个数字 = dir_used_max
        if vals[0] != want_used:
            bad.append("%s: 文档 dir_used_max=%d，JSON 生效值=%d"
                       % (prof, vals[0], want_used))
        # dir_remain_min 只要求"该行出现过" —— 避免因列顺序调整而误报
        if want_remain is not None and want_remain not in vals:
            bad.append("%s: 文档里找不到 dir_remain_min=%d（该行数字：%r）"
                       % (prof, want_remain, vals))

    print("比对了 %d 个 profile 的阈值表" % checked)
    if bad:
        print("FAIL 基线文档与 mem_budget.json 不一致（README 的同步规则被违反）：")
        for b in bad:
            print("   " + b)
        print("   规则原文：阈值改动必须同步 docs/验证/内存与吞吐基线-2026-10-05.md")
        print("             的对照表；否则下一次回归没有可解释的基准。")
        return 1
    print("PASS 基线文档阈值表与 mem_budget.json 一致")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except (OSError, ValueError) as exc:
        print("FAIL 解析失败：%s" % exc)
        sys.exit(2)
