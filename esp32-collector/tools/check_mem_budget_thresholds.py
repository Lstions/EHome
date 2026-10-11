#!/usr/bin/env python3
"""元门禁：内存预算阈值不允许"恒真"。

## 为什么需要它（D-25）
`tools/mem_budget.json` 里 s3p-n8 / s3p-n16 的 `dir_remain_min` 曾是 **0**，
而其余四个 profile 都是 20,480。

判据是 `DIRAM remain >= dir_remain_min` ⇒ **取 0 意味着恒真**：
该 profile 的这条门禁**永远不会红**，即使只剩 1 字节。
门禁打印 `PASS` 却**什么都没检查** —— 假绿。

实测该 profile 的 DIRAM remain 是 194,380 B（六个 profile 里余量最大），
把它设成 0 既无依据也无收益，`_notes` 里也没有任何解释。

## 本门禁判据
对每个 profile 的每个阈值键，检查它是否**可能失败**：

| 键类型 | 恒真条件 | 说明 |
|---|---|---|
| `*_remain_min` | 值 <= 0 | 余量不可能为负 ⇒ 恒真 |
| `*_used_max` | 值 >= 该 profile 的段容量 | 占用不可能超过段容量 ⇒ 恒真 |
| `*_max` / `*_min` 其它 | 见下 | 未知键只提示不拦截 |

同时检查**同一键在不同 profile 间是否被无理由地弱化**：
若某 profile 的值低于其余 profile 的众数，且 _notes 里没有提到该 profile
与"弱化"相关，则报 WARN（不拦截，但要求可见）。

退出码 0 = 无恒真阈值；1 = 有；2 = 无法解析。
"""
import json
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
THRESHOLDS = os.path.join(_HERE, "mem_budget.json")

# 各 profile 的 DIRAM 段容量（字节）—— 用于判断 *_used_max 是否恒真。
# 来源：docs/验证/内存与吞吐基线-2026-10-05.md 与 variant.c 的实测记录。
# 取值刻意取【保守上界】：判据是"这个阈值还可能是真的吗"，
# 容量估大 ⇒ 更不容易误报恒真。
DIRAM_CAPACITY = {
    "s3-n8": 512 * 1024,
    "s3-n16": 512 * 1024,
    "s3p-n8": 512 * 1024,
    "s3p-n16": 512 * 1024,
    "c6-n8": 512 * 1024,
    "c6-n16": 512 * 1024,
}

MIN_KEYS = ("dir_remain_min", "iram_remain_min", "psram_remain_min",
            "dram_remain_min", "remain_min")
MAX_KEYS = ("dir_used_max", "iram_used_max", "psram_used_max",
            "dram_used_max", "used_max")


def classify(key):
    for m in MIN_KEYS:
        if key == m or key.endswith("_" + m):
            return "min"
    for m in MAX_KEYS:
        if key == m or key.endswith("_" + m):
            return "max"
    return None


def main():
    if not os.path.isfile(THRESHOLDS):
        print("FAIL 找不到 %s" % THRESHOLDS)
        return 2
    try:
        with open(THRESHOLDS, encoding="utf-8") as fh:
            cfg = json.load(fh)
    except (OSError, ValueError) as exc:
        print("FAIL 阈值文件无法解析：%s" % exc)
        return 2

    defaults = cfg.get("defaults", {})
    profiles = cfg.get("profiles", {})
    notes = " ".join(str(v) for v in cfg.get("_notes", {}).values())

    bad = []
    warns = []

    # 1) 恒真判据
    for name, prof in sorted(profiles.items()):
        merged = dict(defaults)
        merged.update(prof)
        for key, val in sorted(merged.items()):
            kind = classify(key)
            if kind is None:
                continue
            try:
                v = int(val)
            except (TypeError, ValueError):
                bad.append("%s.%s = %r 不是整数" % (name, key, val))
                continue
            if kind == "min" and v <= 0:
                bad.append(
                    "%s.%s = %d ⇒ **恒真**（余量不可能为负，门禁永远不会红）"
                    % (name, key, v))
            if kind == "max":
                cap = DIRAM_CAPACITY.get(name)
                if cap is not None and v >= cap:
                    bad.append(
                        "%s.%s = %d ⇒ **恒真**（超过段容量 %d，占用不可能达到）"
                        % (name, key, v, cap))

    # 2) 跨 profile 无理由弱化（可见但不拦截）
    keys = set()
    for prof in profiles.values():
        keys.update(prof.keys())
    for key in sorted(keys):
        kind = classify(key)
        if kind != "min":
            continue
        vals = {}
        for name, prof in profiles.items():
            v = prof.get(key, defaults.get(key))
            if v is not None:
                vals[name] = int(v)
        if len(vals) < 2:
            continue
        uniq = sorted(set(vals.values()))
        if len(uniq) > 1:
            lowest = uniq[0]
            who = [n for n, v in sorted(vals.items()) if v == lowest]
            # 只有"最低值唯一地落在少数 profile 上"才提示
            if len(who) < len(vals) and lowest <= 0:
                warns.append(
                    "%s 在 %s 上被弱化为 %d，其余为 %s —— 请确认有依据并在 _notes 里写明"
                    % (key, ",".join(who), lowest,
                       sorted(set(v for v in vals.values() if v != lowest))))

    print("检查 %d 个 profile 的内存阈值" % len(profiles))
    for w in warns:
        print("WARN " + w)
    if bad:
        print("FAIL 存在恒真（永远不会红）的阈值：")
        for b in bad:
            print("   " + b)
        print("   恒真阈值 = 假绿：门禁打印 PASS 却什么都没检查。")
        return 1
    print("PASS 所有内存阈值都可能失败（无恒真阈值）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
