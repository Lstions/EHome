#!/usr/bin/env python3
"""门禁：任务栈必须容得下"已知最大栈帧 + 调用链"，不能只按**均值场景**的实测峰值定。

## 为什么需要它（2026-10-09，§201 压测实测）
`REPORT_TASK_STACK` 曾按"实测峰值 3464、余量 2680"从 6144 收到 **4096**（留 ~630 B），
当时结论是安全的 —— 但那是在**单通道、有应答**的条件下测的。

三路 UART **同时**满载压测时：
```
  ***ERROR*** A stack overflow in task report_tx has been detected.
  Backtrace: 0x40381fd5 ... |<-CORRUPTED
  rst:0xc (RTC_SW_CPU_RST)        => 150 秒内重启 12 次
```
触发条件是"三路同时等响应 ⇒ RX_TASK 同时报多个超时 ⇒ report_tx 一轮处理多个报告
⇒ 2416 B 栈帧叠加"。

## 判据
对每个登记的任务，断言：`STACK_SIZE >= LARGEST_FRAME * MIN_MULTIPLIER`。
  · LARGEST_FRAME = 该任务调用链上最大的单函数栈帧（用 -Wframe-larger-than 门禁盯住）
  · MIN_MULTIPLIER = 2（**不是** 1.18）

## ⚠ 为什么倍数是 2 而不是"实测余量刚好够"
实测峰值是**一个场景**的样本。栈溢出是**不可恢复**的（设备重启），
而多花 2 KB 内部 RAM 的代价远低于"现场随机重启"。
⇒ 宁可留 2 倍余量，也不要为了省 2 KB 把设备押在"峰值不会变"上。

退出码：0 = 全部满足；1 = 有任务余量不足；2 = 无法判定（含解析失败/门禁自身崩溃）。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# 倍率：见文件头"为什么是 2 而不是实测余量"。
MIN_MULTIPLIER = 2.0

# 已知的最大栈帧（字节）。来源：组件级 -Wframe-larger-than 门禁盯着的那几处，
# 以及 handler_data.c 的 buf[1400]（注释写明"不能缩小"）。
# ⚠ 新增"大栈帧"必须加进来，否则本门禁会低估需求。
KNOWN_LARGEST_FRAMES = [
    ("msg_handler_send_data_report / 批量编码", 2416,
     "handler_data.c 的 uint8_t buf[1400]（至少 1024B 数据块 + 报头开销）"),
]

# 受管任务：任务名 -> (栈宏名, 源文件, 该任务调用链上的最大帧索引)
MANAGED_TASKS = [
    ("report_tx", "REPORT_TASK_STACK", "components/bus_worker/bus_worker.c", 0),
]


def read(path):
    try:
        with open(path, encoding="utf-8") as f:
            return f.read()
    except OSError as e:
        print("FAIL 读不到 %s: %s" % (path, e))
        sys.exit(2)


def main():
    bad = []
    checked = 0
    for task, macro, rel, frame_idx in MANAGED_TASKS:
        src = read(os.path.join(ROOT, rel))
        m = re.search(r"#define\s+%s\s+(\d+)" % re.escape(macro), src)
        if not m:
            print("FAIL 在 %s 里找不到 #define %s" % (rel, macro))
            bad.append(task)
            continue
        stack = int(m.group(1))
        frame_desc, frame, why = KNOWN_LARGEST_FRAMES[frame_idx]
        need = frame * MIN_MULTIPLIER
        checked += 1
        status = "OK " if stack >= need else "FAIL"
        print("%s %-12s 栈=%5d B  最大帧=%5d B (%s)  要求>=%.0f B  余量=%+d B"
              % (status, task, stack, frame, frame_desc, need, stack - need))
        if stack < need:
            bad.append(task)
            print("     帧来自：%s" % why)

    # ⚠ 自检：受管任务必须真的被检查到，否则"改了个名字"会让门禁静默失效。
    if checked == 0:
        print("FAIL 一个受管任务都没检查到 => 门禁自己写错了（假绿）")
        return 2

    if bad:
        print("")
        print('含义：这些任务的栈容不下「最大栈帧 x %.1f」。' % MIN_MULTIPLIER)
        print("      栈溢出**不可恢复**（设备 rst:0xc 重启），而多留几 KB 内部 RAM 代价很小。")
        print("处置：提高该任务的栈宏；若内部 RAM 真的不够，应把大栈帧**移出栈**")
        print("      （改静态/堆分配），而不是压低栈。")
        return 1

    print("PASS %d 个受管任务的栈余量均 >= 最大帧 x %.1f" % (checked, MIN_MULTIPLIER))
    return 0


def _run():
    try:
        return main()
    except SystemExit:
        raise
    except Exception:
        import traceback
        traceback.print_exc()
        print("")
        print("FAIL 门禁自身崩溃(rc=2) —— 这不是发现了违例，本次结论无效。")
        return 2


if __name__ == "__main__":
    sys.exit(_run())