#!/usr/bin/env python3
"""门禁：**关键任务**必须静态分配（不得依赖堆碎片）。

## 为什么需要它（D-28，2026-10-08 发现）
2026-10-01 现场事故（记录在 ota.c 与项目记忆）：OTA 起不来，真因是
xTaskCreate() 要一整块**连续**堆内存（栈+TCB），而设备只有十几 KB 堆、碎片常态化。

当时的修法是把三条**关键路径**的任务改成静态分配（栈与 TCB 进 .bss，与堆解耦）：
  - ota_task   —— OTA 是设备远程不可达时**唯一的救命通道**，不能看运气
  - log_tx     —— 诊断通道，同样不该由碎片决定成败
  - scheduler  —— 采集主路径

⚠ 但 2026-10-08 核实发现：**这条不变量没有任何测试/门禁保护**。
   有人把 ota.c 的 xTaskCreateStatic 改回 xTaskCreate，全套测试**仍然全绿**
   （log_stream 有一个宿主测试断言了 Static，但 OTA 与 scheduler 没有）。
   ⇒ 一旦改回去，就会**重现** 2026-10-01 那个现场事故，而 CI 不会拦。

## 本门禁判据
对每个关键任务，在其源码文件里断言：
  ① 存在 xTaskCreateStatic 调用；
  ② 不存在 xTaskCreate / xTaskCreatePinnedToCore 调用
     （② 是关键：只查①会漏掉"新旧两处都在、而旧写法仍在用"的退步。）

## 为什么用静态源码检查而不是宿主测试
这三个任务分属 OTA / log_stream / scheduler 三个组件，写宿主测试要造三套桩；
而它们的不变量是"用了哪个创建函数"——这是**源码层面**的事实，
静态检查直接、无桩、无假绿风险。宿主测试仍有价值（log_stream 已有一个），两者互补。

## 用法
  python3 tools/check_critical_tasks_static.py
退出码 0 = 全部静态；1 = 有退步；2 = 无法解析（视为失败）。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# (人读名, 源码相对路径, 说明)
TASKS = [
    ("ota_task", os.path.join("components", "ota", "ota.c"),
     "OTA 是远程救命通道，栈与 TCB 必须进 .bss 与堆解耦（2026-10-01 现场事故）"),
    ("log_tx", os.path.join("components", "log_stream", "log_stream.c"),
     "诊断通道，成败不该由堆碎片决定"),
    ("scheduler", os.path.join("components", "scheduler", "scheduler.c"),
     "采集主路径"),
]

# ⚠ 静态创建有**两个**变体，都要认（2026-10-08 我第一版漏了后者，
#   于是把 scheduler.c 误判成"没有静态创建"——假阳性来自我的正则，不是代码）：
#     xTaskCreateStatic()                  —— 不绑核
#     xTaskCreateStaticPinnedToCore()      —— 绑核（scheduler 用的就是这个）
RE_STATIC = re.compile(r"xTaskCreateStatic(?:PinnedToCore)?\s*\(")
# 动态创建的两种写法。负向断言 (?!Static) 用于排除静态变体。
# ⚠ xTaskCreatePinnedToCore( 不会被 RE_DYNAMIC 命中（其后是 "Pinned" 而非 "("），
#   故必须单独列一条。
RE_DYNAMIC = re.compile(r"xTaskCreate(?!Static)\s*\(")
RE_PINNED = re.compile(r"xTaskCreatePinnedToCore\s*\(")


def strip_comments(src):
    """去掉 C 注释 —— 否则注释里提到的函数名会造成误判。"""
    src = re.sub(r"/\*.*?\*/", "", src, flags=re.S)
    src = re.sub(r"//[^\n]*", "", src)
    return src


def main():
    problems = []
    ok = 0
    for name, rel, why in TASKS:
        p = os.path.join(ROOT, rel)
        try:
            raw = open(p, encoding="utf-8", errors="replace").read()
        except OSError:
            problems.append("%s: 读不到 %s" % (name, rel))
            continue
        src = strip_comments(raw)

        n_static = len(RE_STATIC.findall(src))
        n_dyn = len(RE_DYNAMIC.findall(src))
        n_pinned = len(RE_PINNED.findall(src))

        if n_static == 0:
            problems.append(
                "%s（%s）：没有 xTaskCreateStatic —— 该任务必须静态分配，否则重新依赖堆碎片。"
                "理由：%s" % (name, rel, why))
            continue

        if n_dyn > 0 or n_pinned > 0:
            problems.append(
                "%s（%s）：同时存在 xTaskCreate=%d 处、xTaskCreatePinnedToCore=%d 处。"
                "若同一关键任务仍有动态创建，就会重现堆碎片依赖（2026-10-01 事故）。"
                "请人工确认；确认后更新本门禁的例外清单。" % (name, rel, n_dyn, n_pinned))
            continue

        print("  OK  %-10s %s（xTaskCreateStatic x%d）" % (name, rel, n_static))
        ok += 1

    print("")
    print("关键任务静态分配核对：%d/%d 通过" % (ok, len(TASKS)))
    if problems:
        print("FAIL:")
        for x in problems:
            print("  - " + x)
        return 1
    print("PASS 所有关键任务均为静态分配")
    return 0


if __name__ == "__main__":
    sys.exit(main())
