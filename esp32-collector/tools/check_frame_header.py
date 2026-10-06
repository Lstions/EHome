#!/usr/bin/env python3
"""独立复核 12 B 定界头向量（Python 侧，不读 C 代码）。

为什么需要：golden vector 若只由被它约束的那个实现来核对，
就是"自己证明自己"。本脚本用**独立的结构体定义**重新生成每条 wire 值
并与向量文件比对 —— 两端任一漂移都会红。

语法：case / 字段行 / payload / wire / expect_* / end
"""
import struct
import sys
import os

HDR = struct.Struct(">HBBHIH")   # 大端：magic ver type flags seq payload_len
assert HDR.size == 12, HDR.size
MAGIC, VER = 0x4548, 0x30
PAYLOAD_MAX = 16368              # 16384 - 12 - 4（恰好一个 TLS 记录）


def crc32c(data):
    crc = 0xFFFFFFFF
    for byte in data:
        crc ^= byte
        for _ in range(8):
            crc = (crc >> 1) ^ (0x82F63B78 & (-(crc & 1)))
    return crc ^ 0xFFFFFFFF


def main():
    path = sys.argv[1] if len(sys.argv) > 1 else os.path.join(
        os.path.dirname(os.path.abspath(__file__)),
        "..", "..", "protocol", "vectors", "frame_header.txt")
    cur, cases, bad = {}, [], 0
    for lineno, raw in enumerate(open(path, encoding="utf-8"), 1):
        line = raw.strip()
        if not line or line.startswith("#"):
            continue
        kw, _, arg = line.partition(" ")
        if kw == "case":
            cur = {"name": arg, "line": lineno}
        elif kw == "end":
            cases.append(cur)
        elif kw == "expect_magic":
            cur["expect"] = "MAGIC"
        elif kw == "expect_version":
            cur["expect"] = "VERSION"
        elif kw == "expect_range":
            cur["expect"] = "RANGE"
        elif kw == "expect_structure":
            cur["expect"] = "STRUCTURE"
        else:
            cur[kw] = arg

    for c in cases:
        wire = bytes.fromhex(c["wire"])
        payload = b"" if c.get("payload", "-") == "-" else bytes.fromhex(c["payload"])
        flags = int(c["flags"], 16)
        crc_on = bool(flags & 0x0008)

        # 负例：只核对"独立实现对坏输入会拒绝"
        if "expect" in c:
            if c["expect"] == "MAGIC":
                if wire[:2] == struct.pack(">H", MAGIC): bad += 1; print("FAIL", c["name"], "magic 应为坏值")
            elif c["expect"] == "VERSION":
                if wire[2] == VER: bad += 1; print("FAIL", c["name"], "ver 应为坏值")
            elif c["expect"] == "RANGE":
                plen = struct.unpack(">H", wire[10:12])[0]
                if plen <= PAYLOAD_MAX: bad += 1; print("FAIL", c["name"], "payload_len 应超上界")
            continue

        plen = int(c["payload_len"], 16)
        built = HDR.pack(MAGIC, int(c["ver"], 16), int(c["type"], 16), flags,
                         int(c["seq"], 16), plen) + payload
        if crc_on:
            built += struct.pack(">I", crc32c(payload))
        if built != wire:
            bad += 1
            print("FAIL %s\n  文件: %s\n  独立: %s" % (c["name"], wire.hex(), built.hex()))
            continue
        if len(payload) != plen:
            bad += 1; print("FAIL", c["name"], "载荷长度与头内 payload_len 不符")
        if crc_on and len(wire) != 12 + plen + 4:
            bad += 1; print("FAIL", c["name"], "带 CRC 时总长不对")

    # 上界自洽：头 + 上界 + CRC 必须恰好 == 一个 TLS 记录
    worst = 12 + PAYLOAD_MAX + 4
    if worst != 16384:
        bad += 1; print("FAIL 上界不自洽：%d != 16384" % worst)

    print("独立复核 %d 条向量，%d 条不一致（上界自洽性已校验）" % (len(cases), bad))
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
