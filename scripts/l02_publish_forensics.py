#!/usr/bin/env python3
"""L-02 上行发布失败取证：把「每帧 2 次 Publish failed」这一现象变成可复跑的数字。

背景（缺陷文档 §2.2）：
  100 Hz 台架日志里 Publish failed(3,873) 多于成功(3,227)，且每次广播失败
  (No transport connected) 伴随**约 2 次** Publish failed。既有调用链
  （msg_handler_publish_checked -> transport_broadcast 失败 -> mqtt publish）
  每一步最多产生 1 次，**解释不了这个 2**。

本脚本回答三个问题（全部只用日志，不需要设备）：
  Q1 计数是否自洽（PF / NT / BF / FS）
  Q2 PF 是否**按帧成对**出现（同一帧 2 次），还是均匀独立地出现
  Q3 是否存在"整体停顿"（>1s 无任何日志）—— 用于判别 outbox 饱和 vs TCP 写阻塞

用法：
    python3 scripts/l02_publish_forensics.py \
        docs/验证/证据/2026-10-05-实机压测/step2-ch8-115200-100hz-console.log
"""
from __future__ import annotations

import collections
import re
import sys

EVENT_RE = re.compile(r"^([IWEDV]) \((\d+)\) ([A-Za-z_0-9]+): (.*)$")

# 同一帧的两次 publish 在日志里相隔很近（同一次 report_tx 调用内），
# 因此用时间窗把 PF 归并成"帧"。窗口取 5 ms：远大于单次调用开销，
# 又远小于 25 ms 的帧间隔，不会把相邻帧并到一起。
FRAME_WINDOW_MS = 5
# 判"整体停顿"的阈值：正常日志密度 >> 1 行/秒，超过这个值空闲即为异常停顿。
STALL_MS = 1000


def parse(path: str):
    events = []
    with open(path, encoding="utf-8", errors="replace") as fh:
        for lineno, raw in enumerate(fh, 1):
            m = EVENT_RE.match(raw.rstrip("\n"))
            if not m:
                continue
            events.append((int(m.group(2)), m.group(3), m.group(4), lineno))
    return events


def classify(msg: str) -> str | None:
    if "Publish failed" in msg:
        return "PF"
    if "No transport connected for broadcast" in msg:
        return "NT"
    if "Broadcast failed, falling back to MQTT" in msg:
        return "BF"
    if "Failed to send via current transport" in msg:
        return "FS"
    return None


def main() -> int:
    if len(sys.argv) != 2:
        print(__doc__)
        return 2
    path = sys.argv[1]
    events = parse(path)

    counts = collections.Counter()
    for _ts, _tag, msg, _ln in events:
        k = classify(msg)
        if k:
            counts[k] += 1

    print(f"log: {path}")
    print(f"parsed events: {len(events)}")
    print("\n--- Q1 计数 ---")
    for k in ("PF", "NT", "BF", "FS"):
        print(f"  {k:3s} {counts[k]:6d}")
    if counts["NT"]:
        print(f"  PF/NT = {counts['PF'] / counts['NT']:.3f}  (调用链预期 <= 1.0)")

    # ---- Q2: PF 按帧成对？ ----
    pfs = [ts for ts, _t, msg, _l in events if "Publish failed" in msg]
    framemap: dict[int, int] = {}
    for ts in pfs:
        framemap[ts // FRAME_WINDOW_MS] = framemap.get(ts // FRAME_WINDOW_MS, 0) + 1
    hist = collections.Counter(framemap.values())
    print(f"\n--- Q2 每个 {FRAME_WINDOW_MS} ms 窗口内的 PF 次数 ---")
    total_windows = sum(hist.values())
    for n in sorted(hist):
        share = 100.0 * hist[n] / total_windows if total_windows else 0.0
        print(f"  {n} 次: {hist[n]:6d} 窗口 ({share:5.1f}%)")
    print(f"  窗口总数 {total_windows}；PF 总数 {len(pfs)}")
    if total_windows:
        print(f"  平均每窗口 {len(pfs) / total_windows:.3f} 次")
    if n_pair := hist.get(2, 0):
        print(f"\n  结论：{n_pair} 个窗口恰有 2 次 -> 与「同一帧发布两次」一致；")
        print(f"        2 次窗口占 {100.0 * n_pair / total_windows:.1f}% 的窗口。")

    # ---- Q3: 整体停顿 ----
    stamps = sorted(ts for ts, _t, _m, _l in events)
    stalls = []
    for a, b in zip(stamps, stamps[1:]):
        if b - a >= STALL_MS:
            stalls.append((b - a, a, b))
    stalls.sort(reverse=True)
    print(f"\n--- Q3 停顿（相邻日志间隔 >= {STALL_MS} ms）---")
    print(f"  停顿次数 {len(stalls)}；最长 {stalls[0][0] if stalls else 0} ms")
    for dur, a, b in stalls[:10]:
        print(f"  gap={dur:6d} ms  {a} -> {b}")
    if not stalls:
        print("  无停顿：日志时间线连续，未出现 >1s 的空窗。")

    return 0


if __name__ == "__main__":
    sys.exit(main())
