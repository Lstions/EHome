#!/usr/bin/env python3
"""门禁：证书的 NVS 契约（命名空间 + 三个键名）两端必须一致。

## 为什么需要（2026-10-07）

证书链路有**两个定义处**，分属两种语言，而中间**没有任何东西把它们钉在一起**：

    firmware  main/device_link_wiring.c:349   nvs_open(CONFIG_EHOME_DEVICE_LINK_NVS_NS)
                                             nvs_read_blob_alloc(h, "ca"  / "cert" / "key")
    tool      tools/nvs_certs_gen.py:55       DEFAULT_NS = "eh_tls"
                                             items = [("ca",..), ("cert",..), ("key",..)]

这正是 P4（一处语义一处定义）要防的形态。而**失效后果是静默的**：
两端名字若漂移，工具会照样生成镜像、**读回校验也照样全绿** ——
因为它的 verify() 用的是**它自己的** ns/键名，比的是"镜像与它自己一致"，
**从没比过"镜像与固件读的名字一致"**。

⇒ 真机上表现为 nvs_open 返回 ESP_ERR_NVS_NOT_FOUND 或某个 nvs_read_blob
返回缺失，链路停在 FATAL；而"我刚生成的镜像校验全过"会把人引向错误方向。

## 判据（读源码，不读编译产物）
1. 固件的 ns：CONFIG_EHOME_DEVICE_LINK_NVS_NS 的字面值（Kconfig 可能覆盖它，
   所以**同时**检查 Kconfig 里是否给了同一个值）；
2. 固件读的键名：nvs_read_blob_alloc(h, "...") 的实参列表；
3. 工具的 ns 与键名：DEFAULT_NS 与 items 里的键。
三者必须**逐字相等**。

## 用法
    check_cert_nvs_contract.py
退出码 0 = 一致，1 = 不一致，2 = 无法判定（"无法判定"不得静默通过）。
"""

from __future__ import annotations

import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)          # esp32-collector
WT = os.path.dirname(ROOT)             # worktree root

WIRING_C = os.path.join(ROOT, "main", "device_link_wiring.c")
KCONFIG = os.path.join(ROOT, "main", "Kconfig.projbuild")
GEN_PY = os.path.join(ROOT, "tools", "nvs_certs_gen.py")

# 固件键名的数量与顺序是契约的一部分：少一个 = 该材料永远读不到。
EXPECTED_KEYS = ["ca", "cert", "key"]


def fail(msg):
    print("FAIL %s" % msg)
    return 1


def cannot(msg):
    print("cannot verify: %s" % msg)
    return 2


def parse_firmware():
    """从固件源码取出 (ns, [keys...])。"""
    if not os.path.exists(WIRING_C):
        return None, None, "找不到 %s" % WIRING_C
    src = open(WIRING_C, encoding="utf-8").read()

    m = re.search(r'#define\s+CONFIG_EHOME_DEVICE_LINK_NVS_NS\s+"([^"]+)"', src)
    if not m:
        # 该宏可能只在 Kconfig 里定义（源码里没有 #define 兜底）——
        # 这时 ns 由 Kconfig 决定，下面的 kconfig_ns 会拿到。
        ns = None
    else:
        ns = m.group(1)

    # 键名：只认 nvs_read_blob_alloc(h, "X", ...) 这种形态。
    keys = re.findall(r'nvs_read_blob_alloc\(\s*h\s*,\s*"([^"]+)"', src)
    if not keys:
        return ns, None, "在 %s 里找不到任何 nvs_read_blob_alloc(h, \"<key>\") 调用" % WIRING_C
    return ns, keys, None


def parse_kconfig():
    """Kconfig 里 CONFIG_EHOME_DEVICE_LINK_NVS_NS 的默认值（可能覆盖源码兜底）。"""
    if not os.path.exists(KCONFIG):
        return None, "找不到 %s" % KCONFIG
    txt = open(KCONFIG, encoding="utf-8").read()
    m = re.search(r'config\s+EHOME_DEVICE_LINK_NVS_NS(.*?)(?=\nconfig\s|\Z)', txt, re.S)
    if not m:
        return None, None       # 该 config 不存在也是合法情况
    body = m.group(1)
    d = re.search(r'default\s+"([^"]+)"', body)
    return (d.group(1) if d else None), None


def parse_tool():
    """从工具源码取出 (ns, [keys...])。"""
    if not os.path.exists(GEN_PY):
        return None, None, "找不到 %s" % GEN_PY
    src = open(GEN_PY, encoding="utf-8").read()
    m = re.search(r'DEFAULT_NS\s*=\s*"([^"]+)"', src)
    if not m:
        return None, None, "在 %s 里找不到 DEFAULT_NS" % GEN_PY
    ns = m.group(1)
    # ⚠ 工具里有**三处** items 列表（生成 / 示例 / selftest），
    # 我第一版只解析第一处 ⇒ 另外两处漂移时门禁**静默放过**。
    # 实测发现的：把锚点限定到第一处才会红，删第二处根本不红（假绿）。
    # ⇒ 必须**每一处都查**：任何一处与固件读的键名不一致都要红。
    # ⚠ 正则必须用 \b 前缀：否则 "sitems = [...]" 也会被当成 "items = [...]" 命中，
    # 而它内部有 paths[0] —— 括号内的 "]" 会让 \[(.*?)\] 提前收尾，
    # 于是解析出的键名变成 ['ca']（截断），门禁报了一条**不存在**的问题（假红）。
    # 我用第 3 处列表"应该怎样"的猜测是错的；真因是解析器的边界没画对。
    lists = re.findall(r'(?<![A-Za-z0-9_])items\s*=\s*\[([^\]]*)\]', src)
    if not lists:
        return ns, None, "在 %s 里找不到 items = [...] 列表" % GEN_PY
    all_lists = []
    for i, body in enumerate(lists):
        ks = re.findall(r'\("([^"]+)",', body)
        if not ks:
            return ns, None, "第 %d 处 items 列表里解析不出键名（body=%r）" % (i + 1, body[:60])
        all_lists.append(ks)
    return ns, all_lists, None


def main():
    fw_ns, fw_keys, err = parse_firmware()
    if err:
        return cannot(err)
    kc_ns, err2 = parse_kconfig()
    if err2:
        return cannot(err2)
    tool_ns, tool_lists, err3 = parse_tool()
    if err3:
        return cannot(err3)
    # 后端只关心"生成用的那一处"，但其余几处一旦漂移，人读代码时会得到错的印象，
    # 而 selftest 那一处更会拿"错的键名"当正确期望 ⇒ 必须全部一致。
    tool_keys = tool_lists[0]
    tool_keys_extra_bad = [ks for ks in tool_lists[1:] if ks != EXPECTED_KEYS]

    problems = []

    # ── 判据 1：命名空间 ──────────────────────────────────────────────
    # 固件的实际 ns = Kconfig 的值（若给了）否则源码兜底值。
    eff_ns = kc_ns if kc_ns else fw_ns
    if eff_ns is None:
        problems.append("固件侧取不到命名空间：源码里没有 #define，Kconfig 里也没有 default")
    if fw_ns is not None and kc_ns is not None and fw_ns != kc_ns:
        problems.append(
            "固件侧**自身**两处不一致：源码兜底 #define = %r，Kconfig default = %r "
            "（生效的是 Kconfig）" % (fw_ns, kc_ns))
    if eff_ns is not None and eff_ns != tool_ns:
        problems.append(
            "命名空间不一致：固件读 %r，而工具写 %r —— "
            "工具生成的镜像固件**读不到**（真机上表现为 NVS_NOT_FOUND / 证书缺失，"
            "而工具的读回校验仍会全绿，因为它比的是镜像与它自己）"
            % (eff_ns, tool_ns))

    # ── 判据 2：键名（含数量与顺序）──────────────────────────────────
    if fw_keys != EXPECTED_KEYS:
        problems.append("固件读的键名不是预期的 %r，而是 %r（少一个 = 该材料永远读不到）"
                        % (EXPECTED_KEYS, fw_keys))
    if tool_keys != EXPECTED_KEYS:
        problems.append("工具写的键名不是预期的 %r，而是 %r" % (EXPECTED_KEYS, tool_keys))
    if fw_keys is not None and tool_keys is not None and fw_keys != tool_keys:
        problems.append("键名不一致：固件读 %r，工具写 %r" % (fw_keys, tool_keys))
    for i, ks in enumerate(tool_lists[1:], start=2):
        if ks != EXPECTED_KEYS:
            problems.append(
                "工具里第 %d 处 items 列表的键名是 %r，应为 %r —— "
                "我只在运行时用第一处，但**人读代码时会得到错的印象**，"
                "而 selftest 那处会拿错的键名当正确期望 ⇒ 必须同样一致"
                % (i, ks, EXPECTED_KEYS))

    if problems:
        print("FAIL 证书 NVS 契约两端不一致（P4：一处语义一处定义）：")
        for p in problems:
            print("   - %s" % p)
        print()
        print("   固件: %s" % WIRING_C)
        print("   工具: %s" % GEN_PY)
        return 1

    print("OK 证书 NVS 契约两端一致：ns=%r keys=%r" % (eff_ns, fw_keys))
    if fw_ns is not None and kc_ns is not None:
        print("   （固件侧源码兜底与 Kconfig 也一致）")
    return 0


if __name__ == "__main__":
    sys.exit(main())
