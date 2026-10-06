#!/usr/bin/env python3
"""D-24 门禁：度量"从未被宿主测试编译"的生产代码，并阻止它变多。

为什么需要它：本仓约 29% 的生产代码（18 文件 / 6.5k 行）从未被宿主编译器
看过一眼 —— 其中包括 3.0 最关键的三个模块（msg_handler.c / ehome_tcp.c /
mqtt_transport_adapter.c）。这些文件里的缺陷**结构上无法被测试发现**
（D-03 的不可达代码、D-09 的契约缺失都是这么潜伏的）。

本门禁给出**可跟踪的数字**，并提供 --max-uncovered 阈值：
只允许它变小，不允许变大 —— 新增未覆盖文件会让门禁变红。

口径（说清楚，否则数字没意义）：
  一个生产 .c 被视为"被宿主测试编译"当且仅当满足其一：
    (a) 它出现在 host_tests/CMakeLists.txt 里（作为某 target 的源文件）；
    (b) 某个 host_tests/*.c 用 #include "....c" 把它摊进 TU。
  两者都意味着"这段代码真的过了宿主编译器"。

用法：
  python3 tools/check_host_coverage.py                    # 只报告
  python3 tools/check_host_coverage.py --max-uncovered 18 # 阈值门禁
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)
HT = os.path.join(ROOT, "host_tests")

SRC_RE = re.compile(r"\.\./((?:components|main)/[A-Za-z0-9_/]+\.c)")
INC_RE = re.compile(r'#include\s+"\.\./((?:components|main)/[A-Za-z0-9_/]+\.c)"')


def collect():
    prod = []
    for base in ("components", "main"):
        for dirpath, _dirnames, filenames in os.walk(os.path.join(ROOT, base)):
            for fn in filenames:
                if fn.endswith(".c"):
                    prod.append(os.path.relpath(os.path.join(dirpath, fn), ROOT))

    cmake = open(os.path.join(HT, "CMakeLists.txt"), encoding="utf-8").read()
    refs = set(SRC_RE.findall(cmake))

    for fn in os.listdir(HT):
        if fn.endswith(".c"):
            txt = open(os.path.join(HT, fn), encoding="utf-8").read()
            refs.update(INC_RE.findall(txt))

    covered = set(prod) & refs
    uncovered = sorted(set(prod) - refs)
    return prod, covered, uncovered


def loc(path):
    try:
        with open(os.path.join(ROOT, path), encoding="utf-8", errors="replace") as fh:
            return sum(1 for _ in fh)
    except OSError:
        return 0


def main(argv):
    prod, covered, uncovered = collect()
    total_loc = sum(loc(p) for p in prod)
    unc_loc = sum(loc(p) for p in uncovered)

    print("生产 .c 文件: %d 个, %d 行" % (len(prod), total_loc))
    print("被宿主测试编译: %d 个" % len(covered))
    print("从未被编译: %d 个, %d 行 (%.1f%%)"
          % (len(uncovered), unc_loc, 100.0 * unc_loc / max(total_loc, 1)))
    print()
    print("按行数排序（top 20）:")
    for p in sorted(uncovered, key=loc, reverse=True)[:20]:
        print("  %6d  %s" % (loc(p), p))

    if "--max-uncovered" in argv:
        limit = int(argv[argv.index("--max-uncovered") + 1])
        print()
        if len(uncovered) > limit:
            print("FAIL 未覆盖文件数 %d > 阈值 %d —— 新增的生产代码必须进宿主测试。"
                  % (len(uncovered), limit))
            return 1
        print("PASS 未覆盖文件数 %d <= 阈值 %d" % (len(uncovered), limit))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
