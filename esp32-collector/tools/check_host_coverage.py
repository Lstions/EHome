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

## 豁免（EXEMPT）—— 窄口子，且**自我举证**
有些文件**结构上无法**进宿主测试：它们直接 #include IDF 头（esp_tls / freertos…），
宿主机上没有这些头。典型是 tls_esp.c（真 esp_tls 调用）。

处理方式不是"把阈值调大"—— 那会让**所有**文件的约束一起变松。
而是列一份**显式、带理由、且可自动校验**的豁免表：
  1. 只认**精确路径**，不用通配；
  2. 每个条目必须带**理由字符串**（逼作者写清楚为什么）；
  3. 门禁会**读该文件**，确认它真的 #include 了 IDF 头 ——
     否则拒绝豁免（防止把本可宿主测试的文件偷偷塞进来）；
  4. 每次运行都把豁免项打印出来（不可见的口子会越开越大）。

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

# 无法进宿主测试的生产文件（精确路径 -> 理由）。加条目请连同理由一起加。
EXEMPT = {
    "components/tls_esp/tls_esp.c":
        "直接 #include esp_tls.h / esp_tls_errors.h / esp_log.h，宿主机无这些头；"
        "本文件刻意做薄：所有判定已下沉到可测层（tls_io / tls_link_adapt / tls_guard）",
}

# 豁免必须满足的举证条件：文件里出现下列任一 IDF 头 include。
IDF_INCLUDE_RE = re.compile(r'#include\s+[<"](esp_[a-z0-9_]+\.h|freertos/[a-z_]+\.h|mbedtls/[a-z_]+\.h)[>"]')


def is_justified_exempt(relpath):
    """豁免必须真的依赖 IDF —— 否则不接受（防止把可测文件塞进豁免表）。"""
    try:
        with open(os.path.join(ROOT, relpath), encoding="utf-8", errors="replace") as fh:
            txt = fh.read()
    except OSError:
        return False, "文件不存在"
    if not IDF_INCLUDE_RE.search(txt):
        return False, "未发现 IDF 头 include —— 本可进宿主测试，不接受豁免"
    return True, ""


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

    # 豁免校验：不满足举证条件的条目直接判失败（不静默忽略）
    exempt_bad = []
    for p in sorted(EXEMPT):
        if p in covered:
            exempt_bad.append("%s 已进宿主测试 —— 请从豁免表移除（口子要收）" % p)
            continue
        ok, why = is_justified_exempt(p)
        if not ok:
            exempt_bad.append("%s: %s" % (p, why))

    uncovered = [p for p in uncovered if p not in EXEMPT]

    total_loc = sum(loc(p) for p in prod)
    unc_loc = sum(loc(p) for p in uncovered)

    print("生产 .c 文件: %d 个, %d 行" % (len(prod), total_loc))
    print("被宿主测试编译: %d 个" % len(covered))
    print("从未被编译: %d 个, %d 行 (%.1f%%)"
          % (len(uncovered), unc_loc, 100.0 * unc_loc / max(total_loc, 1)))
    if EXEMPT:
        print("豁免（结构上无法进宿主测试，逐条已举证）: %d 个" % len(EXEMPT))
        for p in sorted(EXEMPT):
            print("    %s  <- %s" % (p, EXEMPT[p]))
    print()
    print("按行数排序（top 20）:")
    for p in sorted(uncovered, key=loc, reverse=True)[:20]:
        print("  %6d  %s" % (loc(p), p))

    if exempt_bad:
        print()
        for b in exempt_bad:
            print("FAIL 豁免表问题: " + b)
        return 1

    if "--max-uncovered" in argv:
        limit = int(argv[argv.index("--max-uncovered") + 1])
        print()
        if len(uncovered) > limit:
            print("FAIL 未覆盖文件数 %d > 阈值 %d —— 新增的生产代码必须进宿主测试。"
                  % (len(uncovered), limit))
            print("     （若确实无法测试，请在 EXEMPT 里加**精确路径 + 理由**，")
            print("       而不是调大阈值 —— 那会让所有文件一起变松。）")
            return 1
        print("PASS 未覆盖文件数 %d <= 阈值 %d" % (len(uncovered), limit))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
