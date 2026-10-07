#!/usr/bin/env python3
"""比较两个 sdkconfig 的**有效差异** —— 用来在"跨构建比数字"之前做前置检查。

## 为什么需要它（真实事故，两次同族）
- §140：我拿 2.x 固件的 largest=8257536 与 3.0 的 15872 比，
  两者**打印口径**不同（旧版打 MALLOC_CAP_8BIT 含 PSRAM，新版打 INTERNAL|8BIT）。
- §146.3：我拿两张**量栈镜像**的 largest 比（8704/9216 vs 15360），
  它们 **CONFIG_COLLECTOR_PSRAM 不同**（一张未设、一张 =y）⇒ 是两个型号配置，
  其中一张连链路都拒绝启动。
⇒ 同一个病：**先比数字，后看配置**。

## 用法
    python3 tools/sdkconfig_diff.py <a/sdkconfig> <b/sdkconfig> [--only-relevant]

退出码：0 = 无显著差异；1 = 有差异（打印差异项）。
--only-relevant 只看会**改变行为**的开关（链路/PSRAM/型号/日志/内存相关），
用于"我这两个构建能不能拿来对比"这个具体问题。
"""
import re
import sys


def parse(path):
    """把 sdkconfig 解析成 {key: value}；未设置的项记为 "<unset>"。

    sdkconfig 里未设置的开关写作 `# CONFIG_FOO is not set`，
    而"根本不涉及本 target 的项"是**整行缺失** —— 两者必须在语义上区分：
    前者是"显式关闭"，后者是"与本 target 无关"。本工具把前者记 <unset>，
    后者不出现（对比时若一边缺、一边有，会被报出来）。"""
    out = {}
    try:
        lines = open(path, encoding="utf-8", errors="replace").read().splitlines()
    except OSError as e:
        sys.stderr.write("cannot read %s: %s\n" % (path, e))
        sys.exit(2)
    for ln in lines:
        ln = ln.strip()
        m = re.match(r"^#\s*(CONFIG_\w+) is not set$", ln)
        if m:
            out[m.group(1)] = "<unset>"
            continue
        m = re.match(r"^(CONFIG_\w+)=(.*)$", ln)
        if m:
            out[m.group(1)] = m.group(2)
    return out


# 会**改变运行期行为/内存布局**的开关：跨构建比数字前必须一致
# ⚠ 2026-10-07 补：原正则**漏了 STACK / TASK** —— 我改 `CONFIG_ESP_MAIN_TASK_STACK_SIZE`
#   做因果实验时，本工具报"差异=0"，而该值从 16384 变到 8192 确实改变行为
#   （largest 15360 → 6656）。**工具漏报正是它最该防的那类失败**：
#   它会让"先 diff 配置再比数字"这条纪律**看起来做了、实际没做**。
#   ⇒ 凡是会改变**内存布局/任务栈**的开关都必须进这张表。
RELEVANT = re.compile(
    r"COLLECTOR_PSRAM|DEVICE_LINK|MBEDTLS|SPIRAM|PSRAM|LOG_|FREERTOS|"
    r"IDF_TARGET|ESP32S3|ESP32C6|HEAP|MALLOC|WIFI_|OPTIMIZATION|"
    r"COMPILER_|ASSERT|DEBUG|STACK|TASK|BUFFER|IRAM|CACHE|CONSOLE|UART"
)


def main(argv):
    args = [a for a in argv[1:] if not a.startswith("--")]
    only = "--only-relevant" in argv
    if len(args) != 2:
        sys.stderr.write(__doc__)
        return 2
    a, b = parse(args[0]), parse(args[1])
    keys = sorted(set(a) | set(b))
    diffs = []
    for k in keys:
        if only and not RELEVANT.search(k):
            continue
        va, vb = a.get(k, "<absent>"), b.get(k, "<absent>")
        if va != vb:
            diffs.append((k, va, vb))

    print("A = %s" % args[0])
    print("B = %s" % args[1])
    print("A 项数=%d  B 项数=%d  差异=%d%s"
          % (len(a), len(b), len(diffs), "（仅相关开关）" if only else ""))
    if not diffs:
        print("\n✅ 无差异 ⇒ 这两个构建的配置一致，可以比数字。")
        return 0
    print("\n❌ 存在差异 ⇒ **先解释每一处差异**，再决定能不能比数字：")
    for k, va, vb in diffs[:80]:
        print("   %-56s A=%-22s B=%s" % (k, va, vb))
    if len(diffs) > 80:
        print("   … 另有 %d 处" % (len(diffs) - 80))
    print("\n⚠ 若差异落在 %s" % "（相关开关）" if only else "行为相关项",
          "上，则这两个构建**不是同一个被测对象**，其数字不可直接比较。")
    return 1


if __name__ == "__main__":
    sys.exit(main(sys.argv))