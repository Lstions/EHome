#!/usr/bin/env python3
"""D-03 回归门禁：TCP 启动必须【在 case 的 break 之前】。

为什么需要它：D-03 是"不可达代码"缺陷 —— 启动块写在 break 之后，
编译器不报错、sdkconfig 开关也是 y，但 TCP 传输从不启动。
这类缺陷【静态可查、运行期难查】，所以用一条门禁把它钉住。

判定方式（结构化，不做正则全文匹配）：
  1. 定位 "case WIFI_MGR_CONNECTED:"
  2. 找该 case 体内【第一条 8 空格缩进的 break;】
  3. 断言 TCP 的 start 调用出现在它【之前】

退出码：0 = 通过；1 = 违规；2 = 找不到目标结构（结构变了，门禁需更新）。
"""
import os
import re
import sys

# 相对【脚本自身】定位，这样从任何 cwd 调用都成立（ctest 的 cwd 不一定是仓库根）
_HERE = os.path.dirname(os.path.abspath(__file__))
TARGET = os.path.join(os.path.dirname(_HERE), "main", "app_callbacks.c")
CASE_RE = re.compile(r"^(\s*)case\s+WIFI_MGR_CONNECTED\s*:")
BREAK_RE = re.compile(r"^(\s{8})break\s*;")
TCP_START_RE = re.compile(r"tcp_transport->ops->start\s*\(")


def main(path: str = TARGET) -> int:
    try:
        lines = open(path, encoding="utf-8").read().split("\n")
    except OSError as e:
        print(f"FAIL cannot read {path}: {e}")
        return 2

    case_idx = None
    case_indent = ""
    for i, ln in enumerate(lines):
        m = CASE_RE.match(ln)
        if m:
            case_idx = i
            case_indent = m.group(1)
            break
    if case_idx is None:
        print(f"FAIL {path}: 找不到 case WIFI_MGR_CONNECTED（结构已变，请更新本门禁）")
        return 2

    # 该 case 体内第一条 break;（缩进比 case 深一级）
    body_break_idx = None
    for j in range(case_idx + 1, len(lines)):
        if BREAK_RE.match(lines[j]):
            body_break_idx = j
            break
    if body_break_idx is None:
        print(f"FAIL {path}: case WIFI_MGR_CONNECTED 体内找不到 break;（结构已变）")
        return 2

    tcp_idx = None
    for k in range(case_idx + 1, len(lines)):
        if TCP_START_RE.search(lines[k]):
            tcp_idx = k
            break

    if tcp_idx is None:
        print(f"FAIL {path}: case 内找不到 TCP start 调用 —— "
              f"TCP 永远不会启动（这正是 D-03）")
        return 1

    if tcp_idx > body_break_idx:
        print(f"FAIL {path}: TCP start 在第 {tcp_idx + 1} 行，"
              f"而 case 的 break 在第 {body_break_idx + 1} 行 —— "
              f"启动块是【不可达代码】（D-03 复现）")
        return 1

    print(f"PASS TCP start (line {tcp_idx + 1}) 在 case break "
          f"(line {body_break_idx + 1}) 之前，可达")
    return 0


if __name__ == "__main__":
    sys.exit(main(*sys.argv[1:]))
