#!/usr/bin/env bash
# check_doc_commands.sh — 文档里的可执行命令不得引用 .tmp-* 路径。
#
# ============ 规则 ============
#
# .tmp-*/ 已被 .gitignore 排除（.tmp-telemetry / .tmp-verify / .tmp-frontend / ...）。
# 任何**指向它的命令**在别人 clone 后必然失效 —— 而文档不会自己喊。
#
# 所以：docs/**/*.md 的代码块内、以及 scripts/* 的实现行里，
#       出现 .tmp- 即判违规（exit 1）。
#
# ============ 规则：可执行命令里的路径必须「clone 后存在」 ============
#
#   ✓ scripts/gow.sh            （已提交）
#   ✗ .tmp-telemetry/gow.sh     （被 .gitignore 排除，clone 后不存在）
#
# ============ 数据夹具是另一回事（不是「例外」，是第二类路径）============
#
#   .tmp-telemetry/realdata/ 是**仓外产物** —— 它不该进仓（114 MB 生产 CSV），
#   所以命令指向它是**正确**的，需要读者自行准备。
#   判定：路径含 .tmp-telemetry/realdata/ => 属于这一类。
#
# 两者不是「规则 + 例外」，是**两类路径，各有各的要求**。
# 用「例外」措辞的害处：例外 = 「原则上不该出现，这次放行」=> 暗示有豁免机制
# => 坏的引用也能塞进来。用「两类」措辞：坏的引用没有归属。
#
# ============ 三类机器可判定的放行 ============
#
# ② **代码块开头的 requires-local-artifacts 标记**：
#    ```bash requires-local-artifacts
#    表示「这段命令需要仓外产物，读者需自行准备」。
#    用于「记录当时怎么跑的」这类历史记录 —— 改路径就是篡改历史。
#
# ③ **行尾 doc-check: allow**：单行放行，理由须写在同一行的注释里。
#
# ④ **`.tmp-*` 通配符提及**：没指向任何具体文件，不可能失效
#    （如 "已排除 .tmp-* / .analysis"）。
#
# ============ 扫描范围（范围本身就排除了两类假阳性）============
#
# 扫：docs/**/*.md + **/README.md 的**代码块**
# 不扫：scripts/*.sh —— 它是「实现」不是「文档」，自己就是可执行的，
#       没有「照抄失败」问题；而它 echo 出 `.tmp-*` 是在**定义**排除规则（自指）。
# 只查代码块不查行内代码 —— 行内代码是「提及」，不是「请照抄执行」。
#
# ============ 四个实现细节（都会导致漏报或误报）============
#
# 坑 1：语言标记不统一（bash / sh / shell / console / 无标记）=> **无标记也查**
# 坑 2：注释行 —— 行首 # 才排除（行内注释会误报，可接受）
# 坑 3：行末续行符 \  —— 续行合并成一行再判断（否则 `CMD=\` + `  .tmp-x/y z` 漏报）
# 坑 4：**引用块（>）内的代码块不豁免** —— 读者可能照抄，
#       而「被引用的坏命令」更应该被抓
#
# ============ 用法 ============
#
#   scripts/check_doc_commands.sh          # 检查，违规则 exit 1
#   scripts/check_doc_commands.sh -v       # 同时列出豁免项（便于审计豁免是否滥用）
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"
VERBOSE=${1:-}

python3 - "$VERBOSE" <<'PY'
import re, subprocess, sys

verbose = sys.argv[1] == "-v"

FIXTURE_OK = ".tmp-telemetry/realdata/"   # 第二类路径（仓外产物，指向它是对的）
FENCE_MARK = "requires-local-artifacts"   # 放行②：代码块级
LINE_MARK  = "doc-check: allow"           # 放行③：单行级

def is_comment(line):
    """行首注释才排除（行内注释会误报，可接受）。
    覆盖 # （sh/py/yaml）与 // （js/ts/go）—— 只认行首，避免把 URL 里的 // 当注释。"""
    t = line.strip()
    return not t or t.startswith("#") or t.startswith("//")

def is_glob_mention(line):
    """`.tmp-*` 是通配符提及，不是路径引用（没指向具体文件，不可能失效）。"""
    return re.search(r'\.tmp-\*', line) is not None and re.search(r'\.tmp-[A-Za-z0-9_]+/', line) is None

docs_files = subprocess.run(["git","ls-files","docs"], capture_output=True, text=True).stdout.split()
readmes = subprocess.run(["git","ls-files","*README.md","README.md","*/README.md"],
                         capture_output=True, text=True).stdout.split()
md = sorted({f for f in docs_files + readmes if f.endswith(".md")})

violations, exemptions = [], []

for f in md:
    try:
        lines = open(f, encoding="utf-8", errors="replace").read().split("\n")
    except OSError:
        continue

    infence, fence_exempt = False, False
    i = 0
    while i < len(lines):
        raw = lines[i]
        # 坑4：先剥掉 Markdown 引用块前缀（> / >> / 带缩进），再判断 fence 与内容。
        # 不剥的话 "> ```bash" 不以 ``` 开头 => 整个引用块内的代码块**根本不会被识别**，
        # 于是「被引用的坏命令」反而永远抓不到 —— 而读者可能照抄。
        logical = re.sub(r'^(\s*>\s?)+', '', raw)
        t = logical.strip()

        if t.startswith("```"):
            if not infence:
                infence = True
                fence_exempt = FENCE_MARK in t   # 坑1：无标记也查，只有显式 marker 才放行
            else:
                infence, fence_exempt = False, False
            i += 1
            continue

        if not infence:
            i += 1
            continue

        # 坑3：合并行末续行符 \（把 `CMD=\` + 下一行 拼成一条完整命令再判断）
        start_ln = i + 1
        merged = logical
        j = i
        while merged.rstrip().endswith("\\") and j + 1 < len(lines):
            merged = merged.rstrip()[:-1] + " " + lines[j + 1].strip()
            j += 1
        i = j + 1

        if is_comment(merged) or ".tmp-" not in merged:
            continue
        if FIXTURE_OK in merged:
            if verbose: exemptions.append((f, start_ln, "第二类路径(夹具)", merged.strip()[:80]))
            continue
        if LINE_MARK in merged:
            if verbose: exemptions.append((f, start_ln, "行放行", merged.strip()[:80]))
            continue
        if fence_exempt:
            if verbose: exemptions.append((f, start_ln, "块放行", merged.strip()[:80]))
            continue
        if is_glob_mention(merged):
            if verbose: exemptions.append((f, start_ln, "通配符提及", merged.strip()[:80]))
            continue
        violations.append((f, start_ln, merged.strip()))

if verbose and exemptions:
    print("=== 放行项（应逐条确认理由成立）===")
    for f, ln, why, txt in exemptions:
        print(f"  [{why}] {f}:{ln}  {txt}")
    print()

if violations:
    print("=== 违规：可执行命令引用了 clone 后不存在的 .tmp-* 路径 ===")
    for f, ln, txt in violations:
        print(f"  {f}:{ln}  {txt}")
    print()
    print(f"共 {len(violations)} 处。处置：")
    print("  · 路径已提交   => 改成已提交的路径（如 scripts/gow.sh）")
    print("  · 仓外数据夹具 => 用 " + FIXTURE_OK + " 前缀（第二类路径，天然放行）")
    print("  · 历史记录     => 代码块开头写 ```bash " + FENCE_MARK + "，或行尾 " + LINE_MARK)
    sys.exit(1)

print(f"OK：{len(md)} 份文档（docs/ + README），代码块内无违规的可执行命令引用 .tmp-* 路径")
PY
