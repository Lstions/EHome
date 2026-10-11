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

SYM_RE_VAL = re.compile(r"(CONFIG_[A-Za-z0-9_]+)=(.+)$")
SYM_UNSET_RE = re.compile(r"# (CONFIG_[A-Za-z0-9_]+) is not set$")
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


# ── 落地核查：defaults 里"写了但没生效"的行（2026-10-07 新增）─────────────
#
# ## 为什么需要（这是本门禁**结构上抓不到**的一类）
# 上面的判据只回答"这个符号在 Kconfig 树里存在吗"，因此对下面这种情形**必然绿**：
#
#   · 符号确实存在（所以本门禁不报）；
#   · 但它的 depends 在当前 profile 的依赖集下**不满足**，于是 Kconfig 把它**丢掉**；
#   · ⇒ 生成配置里根本没有这一行 ⇒ **你写的值一个字节都没生效，且没有任何提示**。
#
# 2026-10-07 实测到的两例（都不是假想）：
#   1. CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER_NUM=16
#      IDF esp_wifi/Kconfig:81 该选择分支带
#      `depends on !(SPIRAM_TRY_ALLOCATE_WIFI_LWIP && !SPIRAM_IGNORE_NOTFOUND)`；
#      s3p 两个 profile 同时开了 SPIRAM 与 TRY_ALLOCATE ⇒ 不满足
#      ⇒ **仅 s3p 上这行静默失效**（由 /tmp/b82-* 六份生成配置实测确认）。
#   2. CONFIG_DEBUG_TCP_PORT=8088
#      它 depends on CONFIG_DEBUG_TCP_ENABLED，而 defaults 里后者=n
#      ⇒ 端口那行永远不生效（值恰好等于 Kconfig default，所以无害）。
#
# ## 判据（刻意只取"值非 n"的行，避免噪音）
# 只看 defaults 里**值不是 n** 的符号 —— 那些确实在"要一个具体值"。
# `=n` 的行刻意跳过：bool 的 n 在许多 Kconfig 里本来就不会出现在生成配置里，
# 把它们算进来会产生十几条噪音（本门禁最早那版就是因此被否掉的）。
# 实测该判据在六 profile 上只命中 2 个**互不相同**的符号 ⇒ 信号清晰、无噪音。
#
# ## 已确认良性的条目（allowlist，每条必须带理由）
# 加进这里等于**明确写下"我知道它不生效，且这是可接受的"**，
# 而不是让下一个人再查一遍。
DEPENDS_UNREACHABLE_OK = {
    # (符号, profile 前缀) -> 理由。前缀 "s3p" 匹配 s3p-n8 / s3p-n16。
    ("CONFIG_DEBUG_TCP_PORT", "*"):
        "它 depends on CONFIG_DEBUG_TCP_ENABLED，而 defaults 里后者=n（有意关闭），"
        "所以端口行永远不生效；值 8088 恰好等于 Kconfig default ⇒ 无行为差异。"
        "保留该行是为了将来打开 ENABLED 时端口就绪。",
    ("CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER_NUM", "s3p"):
        "s3p 上 SPIRAM_TRY_ALLOCATE_WIFI_LWIP=y 使 IDF 的该选择分支不可达 ⇒ "
        "TX 走 **STATIC** 路径（见 sdkconfig.defaults.esp32s3psram 的注释），"
        "本符号按设计不存在。这不是漏配，而是 s3p 的既定放置策略。",
}


def _parse_config(path):
    """解析一份 sdkconfig：=value 与 "# X is not set"（记作 n）都算已落地。"""
    out = {}
    if not os.path.exists(path):
        return out
    for line in open(path, encoding="utf-8", errors="replace"):
        t = line.strip()
        m = SYM_RE_VAL.match(t)
        if m:
            out[m.group(1)] = m.group(2)
            continue
        m = SYM_UNSET_RE.match(t)
        if m:
            out[m.group(1)] = "n"
    return out


def check_landed(defaults_paths, generated_path, profile):
    """返回 (problems, allowed)。

    problems = "写了但没生效、且未登记为良性" 的符号列表；
    allowed  = 命中 allowlist 的（带理由）。
    problems 为 None 表示"读不到生成配置，无法判定"。
    """
    defs = {}
    for f in defaults_paths:
        defs.update(_parse_config(f))
    gen = _parse_config(generated_path)
    if not gen:
        return None, []
    wanted = {k: v for k, v in defs.items() if v != "n"}
    problems, allowed = [], []
    prefix = profile.split("-")[0]
    for sym, val in sorted(wanted.items()):
        if sym in gen:
            continue
        reason = (DEPENDS_UNREACHABLE_OK.get((sym, prefix))
                  or DEPENDS_UNREACHABLE_OK.get((sym, "*")))
        if reason:
            allowed.append((sym, val, reason))
        else:
            problems.append((sym, val))
    return problems, allowed

def main(argv):
    # 可选的落地核查模式：
    #   --generated <sdkconfig> --profile <p>
    # 给出**本项目刚生成的**配置，则额外检查"defaults 里写了但没生效"的行。
    # 不给这两个参数时行为与以前**完全一致**（向后兼容，ctest 里的用法不变）。
    gen_path, profile, defs_arg = None, None, None
    i = 0
    while i < len(argv):
        if argv[i] == "--generated" and i + 1 < len(argv):
            gen_path = argv[i + 1]
            i += 2
        elif argv[i] == "--profile" and i + 1 < len(argv):
            profile = argv[i + 1]
            i += 2
        elif argv[i] == "--defaults" and i + 1 < len(argv):
            defs_arg = argv[i + 1]
            i += 2
        else:
            i += 1

    if gen_path is not None:
        if defs_arg:
            files0 = [f for f in defs_arg.split(";") if f]
        else:
            files0 = defaults_files()
        if not files0:
            print("FAIL 找不到 sdkconfig.defaults*")
            return 1
        problems, allowed = check_landed(files0, gen_path, profile or "?")
        if problems is None:
            print("cannot verify: 读不到生成配置 %s" % gen_path)
            print("      （无法判定 defaults 是否落地 —— 不静默当作通过）")
            return 2
        for sym, val, reason in allowed:
            print("NOTE %s=%s 在本 profile 下不生效（已登记为良性）" % (sym, val))
            print("     %s" % reason)
        if problems:
            print("FAIL 下列 defaults 行写了但**在本 profile 下没有生效**：")
            for sym, val in problems:
                print("    %s=%s 在生成配置里不存在" % (sym, val))
            print()
            print("     含义：该符号在 Kconfig 里存在，但它的 depends 在本 profile")
            print("           的依赖集下不满足，Kconfig 把它丢掉，你写的值没用上。")
            print("     处置：① 确认有意，登记进 DEPENDS_UNREACHABLE_OK 并写理由；")
            print("           ② 或删掉这行（写在共享 defaults 里会误导读者）；")
            print("           ③ 或移进对应 profile 的 sdkconfig.defaults.<target>。")
            return 1
        print("PASS 落地核查（profile=%s）：defaults 里值非 n 的符号都真的生效了" % (profile or "?"))
        return 0

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
