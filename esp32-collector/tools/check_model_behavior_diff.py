#!/usr/bin/env python3
"""门禁：两型号（s3 / s3p）的 sdkconfig 差异必须是**容量/放置类**。

## 为什么需要它（2026-10-09，§200 实测发现）
把 s3 与 s3p 的 sdkconfig 逐项 diff，发现 **P8 有一条真实违例**：
    CONFIG_INT_WDT_TIMEOUT_MS / CONFIG_ESP_INT_WDT_TIMEOUT_MS   s3=300  s3p=800
    CONFIG_*_WIFI_*_TX_BUFFER_TYPE                              s3=1    s3p=0
=> 同样卡死 500ms：s3 会 panic 重启，s3p 不会。**可观察行为随型号改变**。
⚠ 而这些值**都不是我们设的** —— 是 IDF 通过 depends on SPIRAM 推导的：
    esp_system/Kconfig:284-285  default 300 if !SPIRAM / default 800 if SPIRAM
    esp_wifi/Kconfig:81         Dynamic choice depends on
                                !(SPIRAM_TRY_ALLOCATE_WIFI_LWIP && !SPIRAM_IGNORE_NOTFOUND)

## 为什么必须做成门禁
① P8（型号差异只影响放置/容量，绝不影响可观察行为）是本次重构的核心原则，
   而它**在真实构建产物上不成立**。只审自己的源码会得出 P8 成立的**假结论**。
② 这类差异会**随 IDF 版本静默变化**，而没人会去看两份 sdkconfig 的 diff。
③ 变成门禁 = 强制每个行为差异都被**显式登记**（像 §198 的保留脚表那样）。

## ⚠ 三条设计决策（都是踩过之后才定下的）
1. **sdkconfig 里有大量废弃别名**：IDF 用 "# default:" 前缀标记旧名
   （如 CONFIG_INT_WDT_TIMEOUT_MS 是 CONFIG_ESP_INT_WDT_TIMEOUT_MS 的旧名，
     见 esp_system/sdkconfig.rename:22）。⇒ **同一根因会以两个键名各出现一次**。
   若逐个键名登记，就会"修了一个另一个还在红" ⇒ 必须有**族匹配**。
2. **键名会随 IDF 版本变**（实测：WIFI_RMT_* 与不带前缀的 ESP_WIFI_* 同时存在）。
   ⇒ 族匹配用**子串**、不写全名，否则 IDF 升级后门禁**假红**。
3. **崩溃必须与"发现违例"区分**：Python 未捕获异常也退非零，
   会让**门禁自己崩了**看起来和**门禁咬到了**一模一样 ⇒ 异常一律转 rc=2。

## 用法
    python3 tools/check_model_behavior_diff.py [<s3-sdkconfig> <s3p-sdkconfig>]
未给参数时自动在 __scratch_v3/build 下找。

退出码：0 = 差异全在白名单/已登记；1 = 有未登记的行为差异；2 = 无法判定（含自身崩溃）。
"""
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))

# ⚠ 白名单语义：这些前缀下的键**只影响"有多少/放在哪"**，不影响"怎么做"。
CAPACITY_PREFIXES = [
    "CONFIG_SPIRAM",
    "CONFIG_ESP32S3_SPIRAM",
    "CONFIG_DEFAULT_PSRAM_",
    "CONFIG_ESP_SLEEP_PSRAM_",
    "CONFIG_PM_SLP_SPIRAM_",
    "CONFIG_STDATOMIC_S32C1I_SPIRAM_WORKAROUND",
    "CONFIG_FATFS_ALLOC_PREFER_EXTRAM",
    "CONFIG_COLLECTOR_PSRAM",
]

# ⚠ 容量类用**子串**匹配（不是全名）：键名会随 IDF 版本变，全名会让门禁假红。
#   每一项都是"数目/窗口大小"，不含"用哪种策略"。
CAPACITY_SUBSTRINGS = [
    # ⚠ 用**去掉前缀**的子串：IDF 同一语义有三套键名（ESP32_/ESP_/WIFI_RMT_），
    #   只写其中一套会让另外两套漏进 FAIL（本轮实测漏了 3 个 CACHE/RX_BA/RX_NUM）。
    "STATIC_RX_BUFFER_NUM",
    "DYNAMIC_RX_BUFFER_NUM",
    "STATIC_TX_BUFFER_NUM",
    "DYNAMIC_TX_BUFFER_NUM",
    "CACHE_TX_BUFFER_NUM",
    "RX_BA_WIN",
]

# ⚠ "选哪种策略"的 choice 成员 ⇒ 属行为，交给登记表（不能进容量白名单）。
STRATEGY_CHOICE_KEYS = (
    "CONFIG_ESP32_WIFI_STATIC_TX_BUFFER", "CONFIG_ESP_WIFI_STATIC_TX_BUFFER",
    "CONFIG_WIFI_RMT_STATIC_TX_BUFFER", "CONFIG_ESP32_WIFI_DYNAMIC_TX_BUFFER",
    "CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER", "CONFIG_WIFI_RMT_DYNAMIC_TX_BUFFER",
)

# ⚠ 已登记的**行为类**差异（不是"允许"，而是"已知且待决策"）。
#   每条写明 why / impact / unresolved / review_by。
KNOWN_BEHAVIOR_DIFFS = [
    {
        "family": "TX_BUFFER_TYPE",
        "also": ["_STATIC_TX_BUFFER", "_DYNAMIC_TX_BUFFER"],
        "why": [
            "IDF：esp_wifi/Kconfig:81 的 Dynamic choice 带 depends on",
            "!(SPIRAM_TRY_ALLOCATE_WIFI_LWIP && !SPIRAM_IGNORE_NOTFOUND)；我们开了",
            "SPIRAM_TRY_ALLOCATE_WIFI_LWIP，于是 dynamic 分支被**排除**，整组键翻转。",
        ],
        "impact": "TX 缓冲分配策略不同（static vs dynamic）⇒ 高负载下丢包/延迟表现可能不同。",
        "unresolved": [
            "在当前配置下**无法消除**：要么放弃 SPIRAM_TRY_ALLOCATE_WIFI_LWIP",
            "（Wi-Fi 缓冲回到内部 RAM —— 那正是 s3p 开 PSRAM 要解决的问题），",
            "要么接受差异。属**设计取舍**，需产品决策。",
        ],
        "review_by": "2026-11-15",
    },
    {
        "family": "INT_WDT_TIMEOUT_MS",
        "why": [
            "IDF 自己按 SPIRAM 分档：esp_system/Kconfig:284-285",
            "default 300 if !SPIRAM / default 800 if SPIRAM。",
        ],
        "impact": [
            "同样卡死 500ms：s3 会 panic 重启，s3p 不会。",
            "现场按 300ms 的心智模型解释 s3p 会得出错误结论。",
        ],
        "unresolved": [
            "可显式设 300，但 IDF 用 800 有理由（PSRAM 访问比内部 RAM 慢，",
            "中断处理耗时更长）⇒ 盲目统一可能引入**新的**看门狗误触发。",
            "需真机做卡死注入验证，而 S3P 硬件当前不可达。",
        ],
        "review_by": "2026-11-15",
    },
    {
        "family": "TCP_OOSEQ_MAX_PBUFS",
        "why": "IDF 按可用内存推导 out-of-order 队列深度；s3p=0 表示关闭该队列。",
        "impact": "乱序 TCP 段不再缓存 ⇒ 丢包时重传而非等待补洞，行为可观察。",
        "unresolved": "⚠ **未查清为何 s3p 推导成 0**（s3p 内存更多，按理应 >= s3）。",
        "review_by": "2026-11-15",
    },
]


def parse(path):
    """读 sdkconfig -> {KEY: value}。value 为去引号后的字符串。"""
    out = {}
    with open(path, encoding="utf-8", errors="replace") as f:
        for line in f:
            m = re.match(r"^(CONFIG_[A-Za-z0-9_]+)=(.*)$", line.strip())
            if m:
                out[m.group(1)] = m.group(2).strip().strip(chr(34))
    return out


def is_capacity(key):
    if key in STRATEGY_CHOICE_KEYS:
        return False
    if any(key.startswith(p) for p in CAPACITY_PREFIXES):
        return True
    return any(sub in key for sub in CAPACITY_SUBSTRINGS)


def find_sdkconfigs():
    build = os.path.join(os.path.dirname(ROOT), "__scratch_v3", "build")
    if not os.path.isdir(build):
        return None, None
    s3 = s3p = None
    for d in sorted(os.listdir(build)):
        if s3 is None:
            p = os.path.join(build, d, "s3-n16", "sdkconfig")
            if os.path.isfile(p):
                s3 = p
        if s3p is None:
            p = os.path.join(build, d, "s3p-n16", "sdkconfig")
            if os.path.isfile(p):
                s3p = p
    return s3, s3p


def build_known():
    known = []
    for e in KNOWN_BEHAVIOR_DIFFS:
        fams = list(e.get("also", []))
        if "family" in e:
            fams.append(e["family"])
        if not fams:
            print("FAIL 登记表条目既无 family 也无 also ⇒ 门禁自己写错了")
            return None
        known.append(dict(e, families=tuple(fams)))
    return known


def lookup(key, known):
    for e in known:
        for sub in e["families"]:
            if sub in key:
                return e
    return None


def show(entry, indent="     "):
    for field in ("why", "impact", "unresolved"):
        v = entry.get(field)
        if v is None:
            continue
        for part in ([v] if isinstance(v, str) else v):
            print("%s%s" % (indent, part.strip()))


def main():
    if len(sys.argv) >= 3:
        s3_path, s3p_path = sys.argv[1], sys.argv[2]
    else:
        s3_path, s3p_path = find_sdkconfigs()
    if not s3_path or not s3p_path:
        print("FAIL 找不到 s3 / s3p 的 sdkconfig（rc=2，无法判定）")
        return 2
    for p in (s3_path, s3p_path):
        if not os.path.isfile(p) or os.path.getsize(p) == 0:
            print("FAIL sdkconfig 缺失或为空: %s（门禁会因空文件而假绿）" % p)
            return 2

    a, b = parse(s3_path), parse(s3p_path)
    # ⚠ 解析器自检：否则"一个键都没解析到"会被当成"两型号完全一致"（假绿）。
    if len(a) < 500 or len(b) < 500:
        print("FAIL 解析出的键太少（s3=%d s3p=%d，应 >= 500）⇒ 解析器多半写错了"
              % (len(a), len(b)))
        return 2

    known = build_known()
    if known is None:
        return 2

    print("核对 s3 =%s" % os.path.relpath(s3_path, ROOT))
    print("     s3p=%s" % os.path.relpath(s3p_path, ROOT))
    print("     s3 %d 键，s3p %d 键" % (len(a), len(b)))

    registered, unknown, seen = [], [], set()
    for key in sorted(set(a) | set(b)):
        va, vb = a.get(key), b.get(key)
        if va == vb or is_capacity(key):
            continue
        entry = lookup(key, known)
        if entry is not None:
            registered.append((key, va, vb, entry))
            seen.add(id(entry))
        else:
            unknown.append((key, va, vb))

    print("")
    if registered:
        # ⚠ **按登记条目分组**打印，而不是按键打印：
        #   一个根因会牵动一整组键（本轮实测：TX 缓冲那一族 6 个键同一个根因），
        #   逐键打印会把同一段说明重复 6 遍 ⇒ 输出长度爆炸、真正的新信息被淹没。
        #   （这正是"日志越长越没人看"的成因，门禁自己也会犯。）
        print("--- 已登记的行为差异（NOTE，待决策）---")
        by_entry = {}
        for key, va, vb, e in registered:
            by_entry.setdefault(id(e), (e, []))[1].append((key, va, vb))
        families = [e for e in by_entry.values()]
        families.sort(key=lambda pair: pair[1][0][0])
        for e, hits in families:
            print(" NOTE 族【%s】（复核 %s）—— 命中 %d 个键："
                  % (e["families"][0], e.get("review_by", "?"), len(hits)))
            for key, va, vb in hits:
                print("        %-44s s3=%-8s s3p=%s" % (key, va or "(无)", vb or "(无)"))
            show(e)
            print("")
        print("")
    else:
        print("--- 无已登记行为差异 ---")
        print("")

    for e in known:
        if id(e) not in seen:
            print("NOTE 登记表里的族 %s **已不再命中任何差异** ⇒ 请删掉该条目"
                  % e["families"][0])

    # ⚠⚠ 这里曾经把 return 1 写在 for 循环体**内部**（多缩进 4 格），
    #   于是整个块落在 for 里：循环跑完最后一轮**不会**再执行 return，
    #   函数继续走到 return 0 ⇒ **打了 FAIL 却退 0**（门禁形同虚设）。
    #   实测发现（输出里有 FAIL 而 rc=0）。
    #   ⇒ 教训：**门禁必须自检自己的退出码** —— 只看输出有 FAIL 不够。
    #     这与「日志说失败了但进程退 0」是同一族静默失效。
    if unknown:
        for key, va, vb in unknown:
            print('FAIL %s: s3=%s  s3p=%s' % (key, va or '(无)', vb or '(无)'))
        print('')
        print('含义：这是**行为类**差异（不只是容量/放置）⇒ 违反 P8：')
        print('      「型号差异只影响放置/大小，绝不影响可观察行为」。')
        print('处置（二选一，都要写清理由）：')
        print('  ① 消除：在 sdkconfig.defaults.esp32s3psram 里显式设成与 s3 相同；')
        print('  ② 接受：加进 KNOWN_BEHAVIOR_DIFFS，写明 why/impact/unresolved/review_by。')
        return 1

    print('PASS 两型号差异全部是容量/放置类，或已显式登记（见上 NOTE）')
    return 0


def _run():
    """⚠ 把"崩溃"与"发现违例"分开（Python 未捕获异常也退非零）。"""
    try:
        return main()
    except SystemExit:
        raise
    except Exception:
        import traceback
        traceback.print_exc()
        print("")
        print("FAIL 门禁自身崩溃(rc=2) —— 这**不是**发现了违例，而是判据没跑完，本次结论无效。")
        return 2


if __name__ == "__main__":
    sys.exit(_run())