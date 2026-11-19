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

# 已登记：组件名 -> dict(what/why/next/test/review_by)
#
# 登记在这里是让"建好了但没插电"这件事**可见**，不是把它藏起来。
#
# ⚠ 2026-10-07 更正：原注释写"**骨架 + 宿主测试已完成**"—— 这句**是错的**。
# 实测：nvs_helper **一个测试文件都没有**（host_tests/ 里没有 nvs_*，
# CMakeLists 只挂了它的 include 路径）。也就是说这句话把一个**未经验证的**
# 说法写成了事实，而恰好它是假的 —— 与"门禁打印 PASS 却什么都没检查"同族。
# ⇒ 修法：把"有没有测试"从**散文**变成**可核查字段**（test），并加 review_by。
#   门禁会真的去查 test 指向的文件是否存在、是否真的引用了该组件。
#
# ── 2026-11-19：三条登记的 review_by（2026-11-15）到期，逐条做了决策 ──────
# 本清单因此清空。决策 + 证据（同日落地）：
#   · dispatch  → **接线**。msg_handler.c 的 switch 已换成 s_msg_routes 数据表，
#                 旧 switch 删除（只接线不删 switch 会变成两处定义）；
#                 msg_handler 的 REQUIRES 补 dispatch。宿主 dispatch_tests 不变。
#   · msgcodec  → **降为测试用件**。它唯一的用途是与 frame_codec 逐字节对拍；
#                 接进生产会让"对拍"退化成自己比自己，判据价值归零。
#                 源码移到 host_tests/oracle/msgcodec/（理由见该目录 README）。
#   · nvs_helper → **删除**。169 行 header-only、零测试、零调用者，且不含固件
#                 唯一需要的 blob 读（device_link_wiring.c 一直自己手写）。
#                 留着只会让"已经有一套 NVS 封装"的错觉继续存在。
# ⚠ 写给下一位往这里加条目的人：登记不是免费的 —— review_by 一到，你必须
#   接或删，**不能再续期**；续期等于把清单变成没人看的公告板。
PENDING_WIRING = {}

# ⚠ 「可达」的确切含义（2026-10-07，我在这里写错过一次，已按实测更正）
#
# 本门禁的"可达"是**源码层**判据：main 声明了依赖（REQUIRES）或 #include 了它。
# 它**不保证**组件在运行期真的被**启用**：
#   - 由 Kconfig 开关控制的调用点（如 device_link_wiring.c 里的 3.0 链路）在开关为 n 时
#     会走**运行期早退**（`if (!devlink_wanted()) return;`），不建任务、不分配、不发包。
#   ⇒ "可达"= "源码里有真实调用点（读得通、编译过）"，**不等于**"运行期会启用"。
#   判断"是否真的会跑"要看 Kconfig 与运行时日志，不能只看本门禁。
#
# ❌ 一条我写错后删掉的旧说法（保留以免后人再写一遍）：
#   "开关 n 时整个分支会被编译器消除、再被链接器 --gc-sections 丢掉，ELF 里符号数为 0。"
#   **这是错的。** 实测（s3-n16，ENABLED=n）：session_create / tls_esp_io / rx_pump_create /
#   wire_delim_create / sntp_mgr_create … **11/11 符号都在**，
#   device_link_wiring_init = T size 0x29f，反汇编里真的调 device_link_check_placement。
#   原因：`devlink_wanted()` 是**运行期函数**，不是编译期常量 ⇒ 分支不被消除。
#   我当时测出"0"是因为跑 nm 时**没有 source export.sh**（命令不存在），
#   又用 2>/dev/null 吞掉了 "command not found"，于是 grep -c 打印的 0 被我读成了"符号不存在"。
#   **"命令跑通"≠"测到了东西"** —— 负面断言必须先证搜索面成立。
# 这条限制写在这里而不是留给下一个人去猜。

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


# ── 下界断言（防"扫描器坏了 ⇒ 永远绿"）──
# 门禁本身也会坏：路径错、正则失效、写法变了……
# 一个坏掉的扫描器会**永远报 PASS**（vacuous pass）。
#
# ⚠ 实测过的边界（写下来是因为我第一次**说错了**）：
#   只把 REQ_RE 改成永不匹配时，门禁**仍然是对的** ——
#   main 的直接依赖从 26 条降到 24 条，可达数仍是 24，
#   因为 **#include 那条边**把图撑住了。
#   必须**两处解析同时失效**（REQ_RE + INC_RE）才会出现
#   "可达 0 个，而它们全在待接线清单里 ⇒ PASS" 的假绿。
#   ⇒ 单点失效有冗余兜底；这里的下界是为了拦住**整体**失效。
#
# 给出**下界**：扫描面与可达数低到不可能的程度 ⇒ 直接失败，
# 并提示"先怀疑扫描器，不要怀疑代码库"。
MIN_COMPONENTS = 30     # 当前 37（2026-11-19：删 nvs_helper、msgcodec 转测试判据后）
MIN_REACHABLE = 20      # 当前 37（全部可达；清单为空）


def self_check(comps, reachable, main_deps):
    """返回错误列表；空列表表示扫描器看起来是活的。"""
    errs = []
    if len(comps) < MIN_COMPONENTS:
        errs.append("只扫到 %d 个组件（下界 %d）—— 路径错或目录结构变了"
                    % (len(comps), MIN_COMPONENTS))
    if len(main_deps) == 0:
        errs.append("main 的依赖解析为 0 条 —— REQUIRES 正则失效或写法变了。"
                    "此时所有组件都会'不可达'，而它们都在待接线清单里 ⇒ 门禁假绿")
    if len(reachable) < MIN_REACHABLE:
        errs.append("从 main 只可达 %d 个组件（下界 %d）—— 依赖图解析很可能坏了"
                    % (len(reachable), MIN_REACHABLE))
    return errs


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
    main_deps = edges(MAIN, "main")
    stack = list(main_deps)
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

    # ⚠ 先自检扫描器，再看代码库。顺序很重要：
    # 扫描器坏了会让"代码库有问题"和"扫描器没看见"看起来一样。
    errs = self_check(comps, seen, main_deps)
    if errs:
        print()
        print("FAIL **门禁自身失效**（先怀疑扫描器，不要怀疑代码库）：")
        for e in errs:
            print("    %s" % e)
        print()
        print("  一个坏掉的扫描器会**永远报 PASS** —— 那比不装门禁更危险，")
        print("  因为它给出的是'已经守住了'的错觉。")
        return 2

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
            e = PENDING_WIRING[c]
            print("  - %-16s %s" % (c, e["what"]))
            print("      未接原因: %s" % e["why"])
            print("      下一步  : %s" % e["next"])
            if e.get("test"):
                print("      宿主测试: %s" % e["test"])
            else:
                print("      宿主测试: **无** —— %s" % e.get("test_note", "（未说明）"))
            print("      决策截止: %s" % e["review_by"])

    # ── 待接线清单自身的可核查判据（2026-10-07 补）───────────────────
    #
    # 为什么：原注释把"骨架 + 宿主测试已完成"写成了事实，而**它是假的**
    # （nvs_helper 一个测试都没有）。这正是本仓反复出现的形态 ——
    # 一句未经核查的散文声称"都做过了"，而没有任何东西会去核对它。
    #
    # ⇒ 把"有没有测试"变成**去文件系统里查**的判据：
    #     · 声明了测试文件 ⇒ 该文件必须存在、且真的引用这个组件；
    #     · 声明为 None   ⇒ 必须写明理由（test_note），否则 FAIL。
    #   顺带把 review_by 也查掉：过期即 FAIL —— 免得待接线清单
    #   变成"放进去就再也没人看"的地方。
    import datetime as _dt
    today = _dt.date.today()
    reg_problems = []
    for c in unreachable:
        e = PENDING_WIRING[c]
        tf = e.get("test")
        if tf:
            fp = os.path.join(ROOT, "host_tests", tf)
            if not os.path.exists(fp):
                reg_problems.append("%s: 声明了宿主测试 %s，但该文件不存在" % (c, tf))
            else:
                body = open(fp, encoding="utf-8", errors="replace").read()
                if c not in body:
                    reg_problems.append("%s: %s 里根本没提到 %s（引用是假的）" % (c, tf, c))
        else:
            if not e.get("test_note"):
                reg_problems.append("%s: 既没有测试文件、也没写 test_note 说明原因" % c)
        rb = e.get("review_by")
        try:
            d = _dt.date.fromisoformat(rb) if rb else None
        except ValueError:
            reg_problems.append("%s: review_by 不是合法日期: %r" % (c, rb))
            d = None
        if d is None:
            reg_problems.append("%s: 缺少 review_by（待接线清单不能无限期挂着）" % c)
        elif d < today:
            reg_problems.append(
                "%s: 决策已逾期（review_by=%s，今天 %s）—— 接还是删，请现在就定"
                % (c, rb, today.isoformat()))

    if reg_problems:
        print()
        print("FAIL 待接线清单里有经不起核查的条目：")
        for x in reg_problems:
            print("    %s" % x)
        return 1

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
