#!/usr/bin/env python3
"""防回退门禁：诊断打印的"内存口径"必须与门禁（mem_guard）一致。

为什么需要这个文件
==================
2026-10-05 的实机缺陷是"口径选错"：`heap_caps_get_*(MALLOC_CAP_8BIT)` 在开了
`CONFIG_SPIRAM_USE_MALLOC` 的 s3p 上跨"内部 RAM + PSRAM"取合计/最大值，于是
门禁看着 PSRAM 的 8.25 MB 恒放行，而真正会耗尽的内部 RAM 只剩 23 KiB。

门禁（`main/mem_guard.c`）已被 task-4 修好，并被 `host_tests/mem_guard_tests.c`
的 caps 断言锁住。**但读数没被锁住**：同一类裸 `MALLOC_CAP_8BIT` 还散落在 9 个
文件的诊断打印里（task-7 修掉）。宿主测试抓不到它们 —— 它们只打印，不参与判决，
任何 ctest 都不会因为一行日志的 caps 用错而变红。这个文件就是补上那个洞。

判据（为什么可行）
==================
1. `MALLOC_CAP_8BIT` 在**没有 PSRAM 的构建里**与内部口径等价（单堆），所以它本身
   不是错误；错误只发生在"有 PSRAM 的构建"导致它悄悄变成合计口径。
2. 本仓只有 `sdkconfig.defaults.esp32s3psram` 打开 `CONFIG_SPIRAM_USE_MALLOC=y`。
   `MALLOC_CAP_SPIRAM` 在**任何** profile 下都只指向外部 PSRAM，因此
   "8BIT 必须与 INTERNAL 或 SPIRAM 同行出现"是一条**在所有 profile 下都成立**的
   静态规则 —— 它不需要知道当前编的是哪个型号。这正是它能用 grep 式静态检查
   取代"真机跑一遍"的原因。
3. 反过来说：只要一处写成裸 `MALLOC_CAP_8BIT`，它在 s3p 上就必然是被污染的口径。
   规则与缺陷是**一一对应**的，没有误报空间（`MALLOC_CAP_8BIT` 只有"可按字节
   寻址"一个语义，任何度量查询都必须同时限定堆域）。

覆盖边界（诚实登记）
====================
- `esp_get_free_heap_size()` / `esp_get_minimum_free_heap_size()` 是
  `heap_caps_get_*_size(MALLOC_CAP_DEFAULT)` 的封装，在 PSRAM 构建里同样被污染，
  但机械 grep 无法证明"返回值被当成了度量"。本脚本对它们只做**语法级**检查：
  排除函数定义（宿主测试的桩）与显式豁免文件，其余出现即报错。
  如果某个度量被赋值给变量、过几行再打印，静态检查**看不见** —— 这一族只能靠
  `docs/验证/实机缺陷-PSRAM型号内存门禁恒放行-2026-10-05.md` §7 的人工清单覆盖。
- 本脚本只扫 `esp32-collector/`（固件真源），不扫 `docs/` 与构建目录。

退出码：0=干净，1=发现违例，2=脚本自身异常/根目录缺失。
"""
from __future__ import annotations

import argparse
import os
import re
import sys

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), os.pardir))
SCAN_DIRS = ("main", "components", "host_tests")
SRC_EXT = (".c", ".h", ".cpp", ".hpp", ".cc")
SKIP_DIR_PREFIXES = ("build", "managed_components", ".git")

CAP_8BIT = re.compile(r"\bMALLOC_CAP_8BIT\b")
CAP_INTERNAL = re.compile(r"\bMALLOC_CAP_INTERNAL\b")
CAP_SPIRAM = re.compile(r"\bMALLOC_CAP_SPIRAM\b")
# 定义 "MALLOC_CAP_<x> (1 << n)" 是在声明能力位本身，不是使用它。
CAP_DEFINE = re.compile(r"^\s*#\s*define\s+\w*MALLOC_CAP")
ESP_GET_METRIC = re.compile(r"\besp_get_(?:free|minimum_free)_heap_size\s*\(")
# 宿主测试的桩：size_t esp_get_free_heap_size(void) { return 200000; }
ESP_GET_DEFINITION = re.compile(
    r"\besp_get_(?:free|minimum_free)_heap_size\s*\(\s*void\s*\)\s*\{"
)

# 豁免：文件级、必须带原因。**只给"这一处被静态检查误伤"的情况**，
# 不给"我还没修"的欠债 —— 后者应当让门禁变红，而不是加豁免。
EXEMPT_FILES = {
    # handler_data.c 的 StatusReport heap 字段在 ESP_PLATFORM 分支已改为内部口径；
    # 其 #else 分支是宿主测试路径（esp_get_* 由测试桩提供），
    # 说明见该文件 243-253 行与 host_tests/stubs/esp_heap_caps.h 的注释。
    "components/msg_handler/handler_data.c": "host-test #else branch keeps esp_get_* doubles",
}


class Violation:
    __slots__ = ("path", "line", "rule", "statement", "why")

    def __init__(self, path, line, rule, statement, why):
        self.path, self.line, self.rule = path, line, rule
        self.statement, self.why = statement, why


def iter_sources(root):
    for base in SCAN_DIRS:
        top = os.path.join(root, base)
        if not os.path.isdir(top):
            continue
        for dirpath, dirnames, filenames in os.walk(top):
            dirnames[:] = [d for d in dirnames if not d.startswith(SKIP_DIR_PREFIXES)]
            for name in sorted(filenames):
                if name.endswith(SRC_EXT):
                    yield os.path.join(dirpath, name)


def strip_comments(text):
    """按行剥离注释，字符串字面量原样保留。

    返回 [(lineno, code, raw_line)]。必须做状态机而不是正则：本仓大量诊断打印的
    格式串里带 `*` / `/`，而注释里又大量引用 `MALLOC_CAP_8BIT` 作为文档 ——
    正则会把两者混起来，产生假绿（文档行被当成代码）或假红（格式串被当成注释）。
    """
    out = []
    in_block = False
    for lineno, raw in enumerate(text.splitlines(), 1):
        code = []
        i, n = 0, len(raw)
        while i < n:
            if in_block:
                if raw.startswith("*/", i):
                    in_block = False
                    i += 2
                else:
                    i += 1
            elif raw.startswith("/*", i):
                in_block = True
                i += 2
            elif raw.startswith("//", i):
                break
            elif raw[i] in "\"'":
                quote = raw[i]
                j = i + 1
                while j < n:
                    if raw[j] == "\\":
                        j += 2
                        continue
                    if raw[j] == quote:
                        j += 1
                        break
                    j += 1
                code.append(raw[i:j])
                i = j
            else:
                code.append(raw[i])
                i += 1
        out.append((lineno, "".join(code), raw))
    return out


def statements(lines):
    """把 (lineno, code) 归并成语句：(起始行号, 语句文本)。

    规则 A 要的是"同一个语句"，不是"同一行"：`heap_caps_get_free_size(\n
    MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT)` 跨两行，按行检查会假红。
    """
    buf, start, depth = [], None, 0
    preproc = False
    for lineno, code in lines:
        stripped = code.strip()
        if start is None:
            if not stripped:
                continue
            start = lineno
            preproc = stripped.startswith("#")
        buf.append(code)
        if preproc:
            if not code.rstrip().endswith("\\"):
                yield start, "\n".join(buf)
                buf, start = [], None
            continue
        depth += code.count("(") - code.count(")")
        text = "".join(buf).rstrip()
        if depth <= 0 and text.endswith((";", "{", "}")):
            yield start, "\n".join(buf)
            buf, start, depth = [], None, 0
    if buf:
        yield start, "\n".join(buf)


def scan_file(path, rel):
    with open(path, "r", encoding="utf-8", errors="replace") as fh:
        raw_lines = strip_comments(fh.read())
    code_lines = [(ln, code) for ln, code, _ in raw_lines]
    violations = []
    for stmt_line, stmt in statements(code_lines):
        violations.extend(classify(rel, stmt, stmt_line, code_lines))
    return violations


def classify(rel, stmt, stmt_line, code_lines):
    """判定一条语句是否违例。run() 与 self_test() **共用**这个函数。

    为什么必须共用：自测如果自己重写一遍判据，它验证的就是那段副本而不是
    run() 真正执行的东西 —— 本文件第一版正是如此，于是 "#define 不算使用"
    这条规则的实现顺序有 bug 时自测仍然全绿、真实扫描却误报。
    """
    out = []
    is_define = len(stmt.splitlines()) == 1 and bool(CAP_DEFINE.match(stmt))
    if CAP_8BIT.search(stmt) and not is_define:
        if not (CAP_INTERNAL.search(stmt) or CAP_SPIRAM.search(stmt)):
            occ = next(ln for ln, code in code_lines
                       if ln >= stmt_line and CAP_8BIT.search(code))
            out.append(Violation(
                rel, occ, "bare-8bit",
                " ".join(stmt.split())[:160],
                "MALLOC_CAP_8BIT alone means 'internal + PSRAM' on the s3p build; "
                "pair it with MALLOC_CAP_INTERNAL (or MALLOC_CAP_SPIRAM) in the "
                "same statement",
            ))
    if ESP_GET_METRIC.search(stmt) and not ESP_GET_DEFINITION.search(stmt):
        if rel not in EXEMPT_FILES:
            occ = next(ln for ln, code in code_lines
                       if ln >= stmt_line and ESP_GET_METRIC.search(code))
            out.append(Violation(
                rel, occ, "default-caps-shorthand",
                " ".join(stmt.split())[:160],
                "esp_get_*_heap_size() is heap_caps_get_*_size(MALLOC_CAP_DEFAULT); it "
                "includes PSRAM on the s3p build. Use MALLOC_CAP_INTERNAL explicitly "
                "(or add a file exemption with a reason)",
            ))
    return out


def run(root):
    files = list(iter_sources(root))
    if not files:
        print("diag-metric gate: no sources found under %s" % root, file=sys.stderr)
        return 2, 0, []
    all_v = []
    for path in files:
        rel = os.path.relpath(path, root)
        all_v.extend(scan_file(path, rel))
    all_v.sort(key=lambda v: (v.path, v.line))
    return (1 if all_v else 0), len(files), all_v


def self_test():
    """证明上面两条判据真的会红 —— 用合成输入走**同一套** predication。

    只有 ctest 变红还不够：如果正则本身写错（例如退化成永远匹配不到），
    门禁会以"全绿"的形式失效。这里对每个规则各喂一个必须变红的样本和一个
    必须放行的样本。
    """
    checks = [
        ("bare-8bit must fire", "x = heap_caps_get_free_size(MALLOC_CAP_8BIT);", True),
        ("spiram-is-explicit", "x = heap_caps_malloc(n, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);", False),
        ("internal-pairing-ok", "x = heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);", False),
        ("caps-define-is-not-use", "#define MALLOC_CAP_8BIT      (1 << 2)", False),
        ("bare-8bit-in-comment-ok", "/* docs say MALLOC_CAP_8BIT is wrong here */", False),
        ("default-caps-must-fire", "x = esp_get_free_heap_size();", True),
        ("host-double-definition-ok", "size_t esp_get_free_heap_size(void) { return 200000; }", False),
        ("exempt-file-suppresses", "x = esp_get_free_heap_size();", False,
         "components/msg_handler/handler_data.c"),
    ]
    failed = 0
    for case in checks:
        name, code, want_flag = case[0], case[1], case[2]
        rel = case[3] if len(case) > 3 else "synthetic.c"
        lines = strip_comments(code)
        code_lines = [(ln, c) for ln, c, _ in lines]
        flag = False
        for stmt_line, stmt in statements(code_lines):
            if classify(rel, stmt, stmt_line, code_lines):
                flag = True
        if flag != want_flag:
            print("SELFTEST FAIL %s: expected flagged=%s got %s" % (name, want_flag, flag),
                  file=sys.stderr)
            failed += 1
    if failed:
        return 1
    print("diag-metric gate self-test: %d cases ok" % len(checks))
    return 0


def main():
    ap = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    ap.add_argument("--root", default=ROOT, help="repository root (default: repo of this script)")
    ap.add_argument("--self-test", action="store_true", help="exercise the two rules on synthetic input")
    args = ap.parse_args()
    if args.self_test:
        return self_test()
    status, nfiles, violations = run(os.path.abspath(args.root))
    if status == 2:
        return 2
    if violations:
        print("diag-metric gate FAILED: %d violation(s) in %d files scanned"
              % (len(violations), nfiles))
        for v in violations:
            print("  %s:%d: [%s] %s" % (v.path, v.line, v.rule, v.why))
            print("      %s" % v.statement)
        print("\nWhy this matters: a bare MALLOC_CAP_8BIT metric prints ~8.25 MB on the "
              "s3p build")
        print("while the mem_guard gate decides on ~23 KiB of internal RAM.  See "
              "docs/验证/实机缺陷-PSRAM型号内存门禁恒放行-2026-10-05.md")
        return 1
    print("diag-metric gate: OK (%d files scanned, 0 violations)" % nfiles)
    return 0


if __name__ == "__main__":
    sys.exit(main())
