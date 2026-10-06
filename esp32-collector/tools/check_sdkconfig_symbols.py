#!/usr/bin/env python3
"""门禁：sdkconfig.defaults 里的每个 CONFIG_ 符号都必须是**真实存在的 Kconfig 符号**。

## 为什么需要
Kconfig 对 sdkconfig.defaults 里**不存在的符号**是**静默忽略**的：
不报错、不警告、也不出现在生成的 sdkconfig 里。
于是"我加了个开关"可能什么也没做，而人看着配置文件以为生效了。

## 真实案例（2026-10-06，本仓，我自己犯的）
我为 SNTP 写了 `CONFIG_LWIP_SNTP=y`。实测发现 IDF v6.1 里 SNTP 是
**无条件 menu**（components/lwip/Kconfig:1153 `menu "SNTP"`），
根本没有 menuconfig 开关 ⇒ 该行被静默丢弃。
而缺 SNTP 正是会把设备变成 K11（mTLS 全站失联）的那件事，
**一个看起来生效、实际无效的开关**在这种地方尤其危险。

## ⚠ 判据必须是"Kconfig 树里有这个符号"，**不是**"生成配置里有这一行"
我第一版按后者写，结果报了 **16 个假警报**：那些符号（如
`CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER_NUM`）在 Kconfig 里**真实存在**，
只是在本 profile 下**条件不满足**因而不出现在生成配置里。
⇒ 假警报会把门禁本身变成噪音，进而被人忽略 —— 那比没有门禁更糟。
⇒ 正确判据：符号必须在某处 Kconfig（IDF 或本仓）里有 `config`/`menuconfig` 声明。

找不到 IDF_PATH 时 SKIP（不阻塞无 IDF 的环境），但会明确打印 SKIP。
"""
import os
import re
import sys

_HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(_HERE)

SYM_RE = re.compile(r"^\s*(?:#\s*)?(CONFIG_[A-Z0-9_]+)(?:=| is not set)")
DECL_RE = re.compile(r"^\s*(?:menuconfig|config)\s+([A-Z0-9_]+)\s*$", re.M)

# 由**构建系统注入**、不由任何 Kconfig 的 config 声明的符号。
# 为什么不放宽判据：白名单要求写清理由，而"放宽判据"会让门禁失去鉴别力。
BUILD_INJECTED = {
    # IDF 按目标芯片注入（sdkconfig.defaults.<target> 里会用），
    # 生成配置里确实有 CONFIG_IDF_TARGET=...，但它不在任何 Kconfig 的 config 里。
    "IDF_TARGET",
}


def defaults_files():
    return [os.path.join(ROOT, fn) for fn in sorted(os.listdir(ROOT))
            if fn.startswith("sdkconfig.defaults")]


def find_idf():
    p = os.environ.get("IDF_PATH")
    if p and os.path.isdir(p):
        return p
    for cand in ("/home/sun/env/esp-idf", os.path.expanduser("~/esp/esp-idf")):
        if os.path.isdir(cand):
            return cand
    return None


def renamed_symbols(idf):
    """收集 sdkconfig.rename 里的**旧名**。

    为什么单列一类：IDF 会把旧名自动改名成新名（tools/cmake/kconfig.cmake
    读取各组件的 sdkconfig.rename）。所以旧名**仍然生效**，只是：
      - 它不在任何 Kconfig 的 config 里（会被"找不到符号"误报）；
      - 风险是 IDF 将来可能移除映射，那行就会**变成静默无效**。
    ⇒ 本门禁把旧名**接受但单独提示**，让"用了旧名"这件事可见，而不是报成错误。
    """
    out = set()
    if not idf:
        return out
    for dirpath, dirnames, filenames in os.walk(os.path.join(idf, "components")):
        for fn in filenames:
            if fn == "sdkconfig.rename" or fn.startswith("sdkconfig.rename."):
                try:
                    txt = open(os.path.join(dirpath, fn), encoding="utf-8",
                               errors="replace").read()
                except OSError:
                    continue
                for line in txt.splitlines():
                    line = line.strip()
                    if not line or line.startswith("#"):
                        continue
                    parts = line.split()
                    if parts and parts[0].startswith("CONFIG_"):
                        out.add(parts[0][len("CONFIG_"):])
    return out


def declared_symbols(idf):
    """扫 Kconfig 树，收集所有已声明的符号名（不带 CONFIG_ 前缀）。"""
    syms = set()
    roots = [ROOT]
    if idf:
        roots.append(os.path.join(idf, "components"))
    for root in roots:
        for dirpath, dirnames, filenames in os.walk(root):
            # ⚠ 必须**包含** managed_components：本仓的 CONFIG_MQTT_* 就来自
            #   managed_components/espressif__mqtt（第一版把它排除了 ⇒ 假警报）。
            #   只跳过构建产物目录。
            dirnames[:] = [d for d in dirnames if d not in ("build", "test_apps")]
            for fn in filenames:
                if fn == "Kconfig" or fn.startswith("Kconfig."):
                    try:
                        txt = open(os.path.join(dirpath, fn), encoding="utf-8",
                                   errors="replace").read()
                    except OSError:
                        continue
                    syms.update(DECL_RE.findall(txt))
    return syms


def main(argv):
    files = defaults_files()
    if not files:
        print("FAIL 找不到 sdkconfig.defaults*")
        return 1

    declared = []
    for f in files:
        for i, line in enumerate(open(f, encoding="utf-8", errors="replace"), 1):
            m = SYM_RE.match(line)
            if m:
                declared.append((m.group(1), os.path.basename(f), i))

    idf = find_idf()
    if idf is None:
        print("SKIP check_sdkconfig_symbols: 找不到 IDF_PATH"
              "（无 IDF 环境不阻塞）；本次声明 %d 个符号" % len(declared))
        return 0

    known = declared_symbols(idf)
    renamed = renamed_symbols(idf)
    missing = []
    for sym, f, n in declared:
        short = sym[len("CONFIG_"):] if sym.startswith("CONFIG_") else sym
        if short not in known and short not in BUILD_INJECTED and short not in renamed:
            missing.append((sym, f, n))

    print("核对 %d 个声明符号（对照 Kconfig 树，已收录 %d 个符号）"
          % (len(declared), len(known)))
    if missing:
        print()
        for sym, f, n in missing:
            print("FAIL %s:%d  %s 在 Kconfig 树里找不到 ⇒ **该行被静默忽略**"
                  % (f, n, sym))
        print()
        print("    这通常意味着符号名写错，或它根本不是 Kconfig 符号。")
        print("    被静默忽略的开关**看起来是生效的** —— 别信配置，去 Kconfig 里查。")
        return 1
    used_renamed = [(sym, f, n) for (sym, f, n) in declared
                    if sym[len("CONFIG_"):] in renamed
                    and sym[len("CONFIG_"):] not in known]
    if used_renamed:
        print()
        for sym, f, n in used_renamed:
            print("NOTE %s:%d  %s 是**已被 IDF 改名的旧符号**；"
                  % (f, n, sym))
            print("     改名表(sdkconfig.rename)仍会把它映射到新名 ⇒ 目前**生效**。")
            print("     风险：若 IDF 移除该映射，这行会**静默无效**。建议改用新名。")
    print("PASS check_sdkconfig_symbols: 所有声明符号都在 Kconfig 树中真实存在"
          "（或为已改名旧符号，见上）")
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
