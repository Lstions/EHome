#!/usr/bin/env python3
"""B2 回归门禁：生产代码里【不得】再有 __attribute__((weak)) 功能钩子。

为什么需要它：这一个反模式在本仓出现过 8 次（见 msg_handler_hooks.h 的表），
共同形态是"弱默认实现 + 什么都不做/看起来成功"：
  ehome_mem_can_start -> true（内存门禁静默放行）
  on_write_cmd_received -> 空（写命令静默忽略）
  ...
生效与否取决于【链接顺序】，而宿主测试走另一套符号表
⇒ 弱定义一旦生效，功能静默消失且没有测试会红（与 D-06 完全同型）。

现在钩子统一在 msg_handler_hooks.h 声明为普通 extern，
**漏实现 = 链接错误**（构建期、响亮）。这条门禁防止弱符号被重新引入。

扫描范围：components/ 与 main/ 下的 .c/.h。
注释中被剥离后再判断 —— 说明性提到弱符号是允许的（甚至是必要的）。

退出码：0 = 通过；1 = 发现弱符号；2 = 无法读取。
"""
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
_ROOT = os.path.dirname(_HERE)
SCAN_DIRS = [os.path.join(_ROOT, "components"), os.path.join(_ROOT, "main")]
MARKER = "__attribute__((weak))"


def strip_comments(text):
    out = []
    i = 0
    n = len(text)
    while i < n:
        if text.startswith("/*", i):
            j = text.find("*/", i + 2)
            i = n if j < 0 else j + 2
        elif text.startswith("//", i):
            j = text.find(chr(10), i)
            i = n if j < 0 else j
        else:
            out.append(text[i])
            i += 1
    return "".join(out)


# 下界（防"扫描器坏了 ⇒ 永远绿"）。
# ⚠ 实测过：把 SCAN_DIRS 指到不存在的路径 ⇒ 原实现**一个文件都没扫**，
#   却打印 "PASS 生产代码无弱符号功能钩子" 并返回 0。
#   一个坏掉的扫描器会永远绿，那比不装门禁更危险 ——
#   它给出的是"已经守住了"的错觉。
# 实测值（2026-10-06）：components/ + main/ 共 145 个 .c/.h。
# 下界取 100（留一半余量，避免正常增删触发误报），
# 低于它说明路径错或后缀判断坏了 —— 那是"什么都没扫"，不是"代码库干净"。
MIN_FILES_SCANNED = 100


def main():
    hits = []
    scanned = 0
    for base in SCAN_DIRS:
        for dirpath, _dirnames, filenames in os.walk(base):
            for fn in filenames:
                if not fn.endswith((".c", ".h")):
                    continue
                scanned += 1
                path = os.path.join(dirpath, fn)
                try:
                    raw = open(path, encoding="utf-8").read()
                except OSError as e:
                    print("FAIL cannot read %s: %s" % (path, e))
                    return 2
                code = strip_comments(raw)
                if MARKER in code:
                    for lineno, line in enumerate(code.split(chr(10)), 1):
                        if MARKER in line:
                            hits.append((os.path.relpath(path, _ROOT), lineno))
    # ⚠ 先自检扫描面，再看结论。顺序很重要：
    # "代码库干净"与"扫描器没看见"在输出上是一样的。
    if scanned < MIN_FILES_SCANNED:
        print("FAIL **门禁自身失效**：只扫到 %d 个 .c/.h 文件（下界 %d）"
              % (scanned, MIN_FILES_SCANNED))
        print("    路径错或目录结构变了。此时'没有弱符号'不代表代码库干净，")
        print("    只代表**什么都没扫**。请先修 SCAN_DIRS。")
        return 2

    if hits:
        print("FAIL 生产代码里仍有 %d 处 __attribute__((weak)) 功能钩子：" % len(hits))
        for path, lineno in hits[:20]:
            print("   %s:%d" % (path, lineno))
        print("   钩子请统一声明在 components/msg_handler/msg_handler_hooks.h（普通 extern），")
        print("   由 main/ 提供强实现 —— 漏实现应当是【链接错误】，而不是静默无操作。")
        return 1
    print("PASS 生产代码无弱符号功能钩子（钩子统一在 msg_handler_hooks.h 声明）"
          "（已扫 %d 个文件）" % scanned)
    return 0


if __name__ == "__main__":
    sys.exit(main())
