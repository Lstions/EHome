#!/usr/bin/env python3
"""门禁：组件必须"要么能从 main 到达，要么在待接线清单里逐条登记"。

## 为什么需要
本仓反复出现**同一形状**的问题 —— **建好了但没插电**，而**没有任何一处会报错**：
  - `api.WarmupLatestValues` 自 2026-08-21 引入起**一直没有调用者**；
  - 3.0 设备侧链路组件（session / link_tcp / tls_* / transport_sel / sntp_mgr /
    link_rx_adapt / msgcodec / wire / dispatch / rx_pump / link / variant）
    **各自有宿主测试、全部绿**，但 `main/` 对它们**零引用**。
    ⇒ 固件里**根本没有 3.0 链路**。

**单元测试天然测不到"有没有人用它"**：它测的是"这个组件本身对不对"。
所以需要一条**跨组件**的判据。

## 判据
从 `main` 出发，沿两处声明的依赖做可达性搜索：
  1. `CMakeLists.txt` 的 REQUIRES（IDF 真实的依赖声明）；
  2. `#include` 到其它组件的头文件。
不可达的组件必须出现在 `PENDING_WIRING`（含理由 + 下一步）里；
**不在清单里的不可达组件 ⇒ FAIL**。

## 为什么是"清单"而不是"阈值"
阈值（像 `--max-uncovered 15`）只能表达"别更多了"，**说不出"还差哪些、为什么"**，
而且新出现一个、同时修好一个时数字不变 ⇒ **漏报**。
清单式判据：新出现的不可达组件立刻红；已登记的每一条都带着
"它是什么 / 为什么现在还没接 / 下一步接什么"。
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)
COMPONENTS = os.path.join(ROOT, "components")
MAIN = os.path.join(ROOT, "main")

# 已登记：组件名 -> (它是什么, 为什么现在还没接, 下一步)
#
# 这些都是 3.0 重构"打骨架"阶段的产物：**骨架 + 宿主测试已完成**，
# 接线是后续阶段的事。登记在这里是让这件事**可见**，不是把它藏起来。
PENDING_WIRING = {
    "msgcodec": (
        "消息编解码原语（纯函数）",
        "3.0 帧的编解码要由新的 link/rx_pump 路径使用；该路径尚未接管收发",
        "rx_pump/link 接管收发后，由它们调用",
    ),
    "wire": (
        "3.0 帧定界（12 B 头）",
        "同上：定界属于新接收路径，而该路径尚未接管",
        "与 rx_pump 一起接进接收路径",
    ),
    "dispatch": (
        "数据驱动的消息分发表",
        "目前实际分发仍在 msg_handler.c 的 switch 里；分发表尚未替换它",
        "用 dispatch 表替换 msg_handler 的 switch（P4：一处定义）",
    ),
    "rx_pump": (
        "接收泵：字节流 -> 消息（定界）",
        "接收路径仍由既有 transport/msg_handler 负责",
        "接进接收路径并接上 link_rx_adapt",
    ),
    "link": (
        "上行链路抽象（取代 components/transport）",
        "新旧两套传输要并存到 §7.3 的 P3/P4；此刻生产仍走 transport",
        "§7.3 P2（固件 TCP 优先）时接入",
    ),
    "link_mqtt": (
        "MQTT 的 link 驱动",
        "link 抽象本身尚未接管，故其 MQTT 驱动也还没被用",
        "随 link 一起接入（同一阶段）",
    ),
    "link_tcp": (
        "TCP 链路实现（设备为 client）",
        "3.0 链路尚未接管收发",
        "随 link 接入（§7.3 P2）",
    ),
    "link_rx_adapt": (
        "link_tcp_read -> rx_read_fn_t 的翻译层",
        "上游 link_tcp/rx_pump 尚未接管",
        "随 rx_pump 接入",
    ),
    "session": (
        "会话状态机（DOWN/WAIT_HANDSHAKE/READY/BACKOFF/FATAL）",
        "会话由新链路驱动，而新链路尚未接管",
        "随 link 接入（§7.3 P2）",
    ),
    "transport_sel": (
        "TCP 优先 / MQTT 兜底的选择策略（§7.3 P2）",
        "它要选择的两条 link 都还没接管收发",
        "link 接入后由它决定用哪条",
    ),
    "sntp_mgr": (
        "SNTP 管理器（填 TLS_ACTION_SYNC_TIME_FIRST 的另一半）",
        "TLS 握手路径尚未接管，故时间前置条件尚未接上",
        "TLS 接入时挂到握手前置条件上",
    ),
    "tls_guard": (
        "mTLS 前置条件守卫（时间可信性 + 失败分级）",
        "同上：它守卫的握手尚未接管",
        "随 TLS 接入",
    ),
    "tls_io": (
        "esp_tls 返回值 -> 本仓统一语义（归约层）",
        "它的调用者是 tls_link_adapt，而后者尚未接管",
        "随 TLS 接入",
    ),
    "tls_link_adapt": (
        "tls_io -> link_tcp 的适配",
        "同上",
        "随 TLS 接入",
    ),
    "tls_esp": (
        "esp_tls 真实 I/O 适配",
        "尚未接入（且它直接 include IDF 头，宿主侧豁免，见 check_host_coverage.py）",
        "随 TLS 接入",
    ),
    "variant": (
        "型号能力描述（唯一型号知识来源，P8）",
        "⚠ **三型号差异尚未真正驱动行为**：各 profile 的编译产物不同，"
        "但运行期还没有代码读它来决定行为",
        "让内存/通道数等按 variant 取值（P8：差异只影响资源摆放，不影响可观测行为）",
    ),
    "nvs_helper": (
        "NVS 读写辅助",
        "既有代码直接用 nvs_open/nvs_get_*；helper 尚未被采用",
        "用 helper 替换散落的 nvs 调用（或若确认不需要，删除它）",
    ),
}

REQ_RE = re.compile(r"REQUIRES\s+(.*?)\)", re.S)
INC_RE = re.compile(r'#include\s+"([A-Za-z0-9_/]+\.h)"')


def all_components():
    out = []
    for name in sorted(os.listdir(COMPONENTS)):
        d = os.path.join(COMPONENTS, name)
        if os.path.isdir(d) and os.path.isfile(os.path.join(d, "CMakeLists.txt")):
            out.append(name)
    return out


def headers_of(comp):
    out = set()
    for dirpath, _dirs, files in os.walk(os.path.join(COMPONENTS, comp)):
        for f in files:
            if f.endswith(".h"):
                out.add(f)
    return out


def requires_of(path):
    try:
        txt = open(path, encoding="utf-8", errors="replace").read()
    except OSError:
        return set()
    out = set()
    for m in REQ_RE.finditer(txt):
        body = re.sub(r"#[^\n]*", " ", m.group(1))
        for tok in body.split():
            if re.fullmatch(r"[a-z][a-z0-9_]*", tok):
                out.add(tok)
    return out


def includes_of(comp_dir):
    out = set()
    for dirpath, _dirs, files in os.walk(comp_dir):
        for f in files:
            if not f.endswith((".c", ".h")):
                continue
            try:
                txt = open(os.path.join(dirpath, f), encoding="utf-8", errors="replace").read()
            except OSError:
                continue
            for m in INC_RE.finditer(txt):
                out.add(m.group(1))
    return out


def main():
    comps = all_components()
    header_owner = {}
    for c in comps:
        for h in headers_of(c):
            header_owner.setdefault(h, set()).add(c)

    def edges(comp_dir, comp_name):
        deps = set(requires_of(os.path.join(comp_dir, "CMakeLists.txt")))
        for inc in includes_of(comp_dir):
            base = os.path.basename(inc)
            if "/" in inc:
                head = inc.split("/")[0]
                if head in comps:
                    deps.add(head)
            for owner in header_owner.get(base, ()):
                deps.add(owner)
        deps.discard(comp_name)
        return deps

    graph = {c: edges(os.path.join(COMPONENTS, c), c) for c in comps}

    seen = set()
    stack = list(edges(MAIN, "main"))
    while stack:
        cur = stack.pop()
        if cur in seen or cur not in graph:
            continue
        seen.add(cur)
        stack.extend(graph[cur])

    unreachable = [c for c in comps if c not in seen]
    unregistered = [c for c in unreachable if c not in PENDING_WIRING]

    print("组件总数: %d，从 main 可达: %d，待接线（已登记）: %d"
          % (len(comps), len(seen), len(unreachable) - len(unregistered)))

    if unregistered:
        print()
        print("FAIL 以下组件**从 main 不可达，且不在待接线清单里**：")
        for c in unregistered:
            print("    %s" % c)
        print()
        print("  这是'建好了但没插电'，而且**没有任何一处会报错**：")
        print("  组件自己的单元测试是绿的，而'有没有人用它'单元测试天然测不到。")
        print("  修法二选一：")
        print("    ① 接进 main（并在 main/CMakeLists.txt 加 REQUIRES）；")
        print("    ② 若确实还没到接线阶段，在 PENDING_WIRING 里登记")
        print("       **它是什么 + 为什么现在没接 + 下一步接什么**。")
        return 1

    if unreachable:
        print()
        print("待接线清单（可见，不隐藏）：")
        for c in unreachable:
            what, why, nxt = PENDING_WIRING[c]
            print("  - %-16s %s" % (c, what))
            print("      未接原因: %s" % why)
            print("      下一步  : %s" % nxt)

    # 反向检查：清单里已登记的组件若已经可达，说明登记过期了，应当删掉。
    stale = [c for c in PENDING_WIRING if c not in unreachable]
    if stale:
        print()
        print("FAIL 待接线清单里这些组件**已经可达**了，登记已过期，请删除条目：")
        for c in stale:
            print("    %s" % c)
        return 1

    print()
    print("PASS 所有组件要么从 main 可达，要么已在待接线清单里逐条登记")
    return 0


if __name__ == "__main__":
    sys.exit(main())
