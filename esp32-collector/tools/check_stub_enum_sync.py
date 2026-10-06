#!/usr/bin/env python3
"""门禁：宿主测试里【手抄】的枚举必须与生产头文件逐值一致。

## 为什么需要它
有些宿主测试用 `#define EHOME_MQTT_H` 挡掉真实头文件（因为 esp-mqtt 是 IDF
managed component，不能在宿主编译），然后手写一整套桩 —— 其中包括
`mqtt_publish_result_t` 的**副本**。

副本会漂移。2026-10-06 就漂移了：给生产枚举加了 MQTT_PUBLISH_BACKPRESSURE 之后，
两个测试文件立刻编译失败。那次**是编译器抓到的**（因为恰好有 switch 用到新值）——
但编译器只在"新值被用到"时才说话；如果新值只被赋值、从不被判断，
漂移会**静默**存活，测试继续全绿而语义已经分叉。

本门禁把这种漂移变成确定的红。

## 口径
对被守护的枚举，逐个 (名字, 值) 与生产头文件比对：
  - 生产有、桩没有  -> FAIL（桩落后）
  - 桩有、生产没有  -> FAIL（桩凭空多出）
  - 值不同          -> FAIL

## 扩展方式
在 ENUM_TARGETS 里加一条即可。
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)

# 被守护的枚举：(枚举类型名, 生产头文件, [手抄该枚举的测试文件...])
ENUM_TARGETS = [
    (
        "mqtt_publish_result_t",
        "components/ehome_mqtt/ehome_mqtt.h",
        [
            "host_tests/mqtt_event_tests.c",
            "host_tests/mqtt_recovery_tests.c",
        ],
    ),
]


def parse_enum(text, type_name):
    """抓 typedef enum { ... } <type_name>;  返回 {名字: 值}。

    只处理显式赋值的整型枚举（本仓的用法），不猜隐式自增 ——
    猜错会让门禁给出假的"一致"。遇到没写 = 的成员直接报错，
    逼作者写清楚，而不是让门禁去推断。
    """
    # 用 [^}]* 而不是 .*?：枚举体里没有花括号，而 .*? 会从【更早的】
    # typedef enum { 一路跨过来，把别的枚举的成员也算进来
    #（第一次就踩到了：报出 MQTT_CLIENT_DISCONNECTED 没有显式赋值）。
    m = re.search(
        r"typedef\s+enum\s*\{([^}]*)\}\s*" + re.escape(type_name) + r"\s*;",
        text,
        re.S,
    )
    if not m:
        return None
    body = re.sub(r"//[^\n]*", " ", m.group(1))
    body = re.sub(r"/\*.*?\*/", " ", body, flags=re.S)
    out = {}
    for item in body.split(","):
        item = item.strip()
        if not item:
            continue
        if "=" not in item:
            raise ValueError(
                "枚举 %s 的成员 %r 没有显式赋值；门禁拒绝猜测（请补 = 值）"
                % (type_name, item)
            )
        name, _, val = item.partition("=")
        name = name.strip()
        val = val.strip()
        if not name:
            continue
        try:
            out[name] = int(val, 0)
        except ValueError:
            raise ValueError("枚举 %s 的成员 %s 的值 %r 不是整数字面量"
                             % (type_name, name, val))
    return out


def main():
    failures = []
    checked = 0
    for type_name, header_rel, stub_rels in ENUM_TARGETS:
        header_path = os.path.join(ROOT, header_rel)
        with open(header_path, encoding="utf-8", errors="replace") as fh:
            prod = parse_enum(fh.read(), type_name)
        if prod is None:
            failures.append("生产头文件 %s 里找不到枚举 %s" % (header_rel, type_name))
            continue

        for stub_rel in stub_rels:
            stub_path = os.path.join(ROOT, stub_rel)
            if not os.path.isfile(stub_path):
                failures.append("手抄文件不存在：%s" % stub_rel)
                continue
            with open(stub_path, encoding="utf-8", errors="replace") as fh:
                stub = parse_enum(fh.read(), type_name)
            if stub is None:
                # 该文件可能改成 include 真实头了 —— 那是好事，跳过。
                continue
            checked += 1

            for k in sorted(set(prod) - set(stub)):
                failures.append(
                    "%s 缺少 %s.%s = %d（生产有、桩没有 ⇒ 桩落后）"
                    % (stub_rel, type_name, k, prod[k]))
            for k in sorted(set(stub) - set(prod)):
                failures.append(
                    "%s 多出 %s.%s = %d（桩有、生产没有 ⇒ 凭空多出）"
                    % (stub_rel, type_name, k, stub[k]))
            for k in sorted(set(prod) & set(stub)):
                if prod[k] != stub[k]:
                    failures.append(
                        "%s 的 %s.%s = %d，生产是 %d（值不同）"
                        % (stub_rel, type_name, k, stub[k], prod[k]))

    print("检查了 %d 处枚举副本，守护 %d 个枚举类型"
          % (checked, len(ENUM_TARGETS)))
    if failures:
        print("FAIL 手抄枚举与生产不一致：")
        for f in failures:
            print("   " + f)
        return 1
    print("PASS 手抄枚举与生产头文件一致")
    return 0


if __name__ == "__main__":
    try:
        sys.exit(main())
    except ValueError as exc:
        print("FAIL 解析失败：%s" % exc)
        sys.exit(2)
