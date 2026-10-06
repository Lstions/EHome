#!/usr/bin/env python3
"""D-06 回归门禁：DataBatch 编码器必须由 main/ 【显式】注入。

为什么需要它：D-06 的形态是"弱符号替代显式注入"——
  生效与否取决于链接顺序，宿主测试走另一套符号表，
  弱定义一旦生效，聚合静默退化为逐样本 0x03 而【没有测试会红】。
删掉弱符号后，"谁把它接上"就只剩 main.c 一行代码；
这一行如果被删，编译与测试都不会报错（只是聚合不再发生），
所以用一条门禁把它钉住。

判据（两条都要满足）：
  1. bus_worker.c 里【不得】再出现提供 msg_handler_send_data_batch 的弱定义；
  2. main.c 必须调用 bus_worker_set_data_batch_cb(...)。

退出码：0 = 通过；1 = 违规；2 = 找不到目标文件（门禁需更新）。
"""
import os
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
_ROOT = os.path.dirname(_HERE)
BUS_WORKER = os.path.join(_ROOT, "components", "bus_worker", "bus_worker.c")
MAIN_C = os.path.join(_ROOT, "main", "main.c")

WEAK_MARKER = "__attribute__((weak))"
BATCH_SYMBOL = "msg_handler_send_data_batch"
INJECT_CALL = "bus_worker_set_data_batch_cb("


def strip_comments(text):
    """去掉注释 —— 注释里提到旧实现是允许的（甚至是必要的）。"""
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


def find_weak_batch_definition(code):
    """返回弱定义位置；没有则 -1。

    判定：某个 __attribute__((weak)) 之后 400 字符内出现 msg_handler_send_data_batch。
    """
    start = 0
    while True:
        k = code.find(WEAK_MARKER, start)
        if k < 0:
            return -1
        if BATCH_SYMBOL in code[k:k + 400]:
            return k
        start = k + len(WEAK_MARKER)


def main():
    try:
        bw = open(BUS_WORKER, encoding="utf-8").read()
        mn = open(MAIN_C, encoding="utf-8").read()
    except OSError as e:
        print("FAIL cannot read source: %s" % e)
        return 2

    pos = find_weak_batch_definition(strip_comments(bw))
    if pos >= 0:
        print("FAIL %s: 仍存在 %s 的弱定义（偏移 %d）—— D-06 形态回归："
              "生效与否取决于链接顺序，静默降级且无测试会红"
              % (BUS_WORKER, BATCH_SYMBOL, pos))
        return 1

    if INJECT_CALL not in strip_comments(mn):
        print("FAIL %s: 找不到 %s —— DataBatch 编码器没有人接上，"
              "聚合会静默退化为逐样本 0x03" % (MAIN_C, INJECT_CALL))
        return 1

    print("PASS DataBatch 编码器由 main/ 显式注入，且 bus_worker 无弱定义")
    return 0


if __name__ == "__main__":
    sys.exit(main())
