#!/usr/bin/env python3
"""S0 契约门禁：用【第三份独立实现】校验 golden vector 自洽。

## 为什么需要"第三份实现"
protocol/vectors/wire_primitives.txt 是 ESP32(C) 与后端(Go) 共用的唯一向量来源。
但若只用其中一端的实现去"验"它，就只是让那一端给自己打分 ——
两端**同时自洽却互相不通**正是 S0 要拦的事。

本脚本用 Python **重新实现**一遍线路格式（varint / tag / 长度定界），
逐条校验向量文件里的 wire 字节。三份实现（C/Go/Python）对上，
"文件写错了"与"某一端漂移了"才可能被区分开。

## 校验内容（每条用例）
1. 按用例列出的字段重新编码 == wire        （编码一致）
2. wire 解回来 == 用例列出的字段            （解码一致）
3. decode(encode(x)) == x                   （往返）
4. wire 必须能【恰好】解析完，无尾随字节    （长度自洽）

退出码 0 = 全部一致；1 = 有不一致；2 = 文件格式错误。
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)
VECTORS = os.path.join(ROOT, "..", "protocol", "vectors", "wire_primitives.txt")

WIRE_VARINT = 0
WIRE_LEN = 2


def encode_varint(v):
    if v < 0:
        raise ValueError("varint 不接受负值")
    out = bytearray()
    while True:
        b = v & 0x7F
        v >>= 7
        if v:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def decode_varint(buf, pos):
    shift = 0
    val = 0
    start = pos
    while True:
        if pos >= len(buf):
            raise ValueError("varint 截断（读到 buffer 末尾）")
        b = buf[pos]
        pos += 1
        val |= (b & 0x7F) << shift
        if not (b & 0x80):
            break
        shift += 7
        if shift > 63:
            raise ValueError("varint 超过 64 位")
    return val, pos


def encode_tag(field, wire):
    return encode_varint((field << 3) | wire)


def encode_case(case):
    out = bytearray()
    if case["type"] is not None:
        out.append(case["type"])
    for kind, field, value in case["fields"]:
        if kind == "u64":
            out += encode_tag(field, WIRE_VARINT)
            out += encode_varint(value)
        elif kind == "bytes":
            out += encode_tag(field, WIRE_LEN)
            out += encode_varint(len(value))
            out += value
        else:
            raise ValueError("未知字段类型 %s" % kind)
    return bytes(out)


def decode_case(case, buf):
    """按用例的描述解回字段，顺带检查"恰好解析完"。"""
    pos = 0
    if case["type"] is not None:
        if not buf:
            raise ValueError("空帧")
        got_type, pos = buf[0], 1
        if got_type != case["type"]:
            raise ValueError("类型字节 %d != %d" % (got_type, case["type"]))
    got = []
    for kind, field, _value in case["fields"]:
        if pos >= len(buf):
            raise ValueError("字段 %d 缺失（已到末尾）" % field)
        tag, pos = decode_varint(buf, pos)
        f, w = tag >> 3, tag & 0x7
        if f != field:
            raise ValueError("字段号 %d != 期望 %d" % (f, field))
        if kind == "u64":
            if w != WIRE_VARINT:
                raise ValueError("字段 %d 的 wire type %d != varint" % (field, w))
            v, pos = decode_varint(buf, pos)
            got.append(("u64", field, v))
        elif kind == "bytes":
            if w != WIRE_LEN:
                raise ValueError("字段 %d 的 wire type %d != length-delimited" % (field, w))
            ln, pos = decode_varint(buf, pos)
            if pos + ln > len(buf):
                raise ValueError("字段 %d 声明长度 %d 超出剩余 %d" % (field, ln, len(buf) - pos))
            got.append(("bytes", field, bytes(buf[pos:pos + ln])))
            pos += ln
    if pos != len(buf):
        raise ValueError("解析未恰好结束：剩余 %d 字节（尾随数据）" % (len(buf) - pos))
    return got


def parse(path):
    cases = []
    cur = None
    with open(path, encoding="utf-8") as fh:
        for lineno, raw in enumerate(fh, 1):
            line = raw.strip()
            if not line or line.startswith("#"):
                continue
            parts = line.split()
            kw = parts[0]
            if kw == "case":
                if cur is not None:
                    raise ValueError("第 %d 行：上一个 case 未用 end 收尾" % lineno)
                cur = {"name": parts[1], "type": None, "fields": [], "wire": None, "line": lineno}
            elif kw == "sub":
                cur["type"] = None
            elif kw == "type":
                cur["type"] = int(parts[1])
            elif kw == "u64":
                cur["fields"].append(("u64", int(parts[1]), int(parts[2])))
            elif kw == "bytes":
                hexpart = parts[2]
                data = b"" if hexpart == "-" else bytes.fromhex(hexpart)
                cur["fields"].append(("bytes", int(parts[1]), data))
            elif kw == "wire":
                cur["wire"] = bytes.fromhex(parts[1])
            elif kw == "end":
                if cur["wire"] is None:
                    raise ValueError("第 %d 行：case %s 没有 wire" % (lineno, cur["name"]))
                cases.append(cur)
                cur = None
            else:
                raise ValueError("第 %d 行：未知关键字 %r" % (lineno, kw))
    if cur is not None:
        raise ValueError("文件末尾的 case %s 未收尾" % cur["name"])
    return cases


def main():
    path = os.path.normpath(VECTORS)
    if not os.path.isfile(path):
        print("FAIL 找不到向量文件 %s" % path)
        return 2
    try:
        cases = parse(path)
    except ValueError as exc:
        print("FAIL 向量文件格式错误：%s" % exc)
        return 2

    bad = 0
    for c in cases:
        problems = []
        try:
            mine = encode_case(c)
            if mine != c["wire"]:
                problems.append("编码不符：python=%s 文件=%s"
                                % (mine.hex(), c["wire"].hex()))
            got = decode_case(c, c["wire"])
            want = [(k, f, v) for (k, f, v) in c["fields"]]
            if got != want:
                problems.append("解码不符：%r != %r" % (got, want))
            if decode_case(c, mine) != want:
                problems.append("往返不符")
        except ValueError as exc:
            problems.append("异常：%s" % exc)
        if problems:
            bad += 1
            print("FAIL %s（第 %d 行）" % (c["name"], c["line"]))
            for p in problems:
                print("     %s" % p)

    print("校验 %d 条向量，%d 条不一致" % (len(cases), bad))
    if bad:
        print("FAIL golden vector 与独立实现不一致 —— 契约文件或某一端已漂移。")
        return 1
    print("PASS golden vector 与第三份独立实现（Python）完全一致")
    return 0


if __name__ == "__main__":
    sys.exit(main())
