#!/usr/bin/env bash
# -----------------------------------------------------------------------------
# tools/docs/claims-audit.sh ——「声称-实现」对账脚本（计划 §3 P1-A）
#
# 【它做什么】
#   把仓库里"可证伪的绝对化声称"扫成一份 **人工复核清单**：
#     - docs/设计/*.md                     全文
#     - backend/**/*.go                     注释行
#     - frontend-shared/src/**/*.{ts,vue}   注释行
#
# 【纪律：它不做什么】
#   脚本 **只负责找出候选，不负责裁决真假**。
#   它 **不会** 打印"通过 / 无问题 / 全绿"这类结论 —— "扫不到"不等于"没问题"。
#
# 【退出码语义】
#   默认（扫描模式）：**总是退出 0**，只产出清单，不作门禁判据。
#     选择理由：清单每一条都要人来判真假，命中数多 ≠ 仓库坏了。若改成"有候选就非 0"，
#     CI 会因为一条中性的"已实现"字样变红 ⇒ 团队的第一反应是收窄规则到扫不到，
#     于是又制造一次"机器假绿" —— 那正是 P1-A 要防的东西。
#   --selftest：**分类器自校准**（唯一会因"检出问题"而非 0 的路径）。
#     断言 ①固定语料里的已知漂移全部被报出 ②正常文本不被误报 ③临时注入的漂移被报出。
#     全通过 ⇒ 0；任一失败 ⇒ 1。它守护的是"**工具没坏**"，不是"仓库没问题"，
#     这是本脚本可以进 CI 的形式。
#   --fail-on-candidates：显式 opt-in 的硬门禁（默认关闭、未接线）。
#     仅在分母收敛后由主控决定是否启用。
# -----------------------------------------------------------------------------
set -uo pipefail
shopt -s nullglob

SELF="${BASH_SOURCE[0]}"
REPO_ROOT="${REPO_ROOT:-$(cd "$(dirname "$SELF")/../.." && pwd)}"
SELF_REL="tools/docs/claims-audit.sh"
CORPUS_DIR="$REPO_ROOT/tools/docs/testdata"

# ── 规则表（显式集中于此，不散落）──────────────────────────────────────────────
# 每行：规则名 <TAB> ERE <TAB> 说明
rules_emit() {
  cat <<'RULES'
R1-absence-scope	(全仓|全项目|全站|全库|整个仓库|整个项目|整个代码库|仓库内|库内)[^。；]{0,6}(没有|均无|皆无|不存在|为零|皆为零|无)	带范围限定的"某能力不存在"（"全仓没有 X"）——最危险的一类，范围限定让读者以为已核实
R2-absence-any	(没有任何|无任何|不存在任何|尚无|没有一处|无一(处|个|例外)|找不到任何|并无|一个也没有)	无范围的绝对缺失断言（"没有任何 X"）
R3-count-arabic	(仅|只|共|唯一|全部)[^。；]{0,4}[0-9]+[[:space:]]*[处个张条次份行项]	阿拉伯数字计数断言（"仅 N 处"）
R4-count-cjk	(仅|只|共)[^。；]{0,4}[二三四五六七八九十两][[:space:]]*[处个张条次份行项]	中文数字计数断言（"只有两处"）；"一个"排除在外——它是口语量词，噪音压倒收益
R5-count-singular	(仅此一处|唯一一处|仅一处|只有一处|唯一的一处)	"只有一处"型唯一性断言
R6-status-impl	(已实现|未实现|已修复|未修复|已支持|不支持|已上线|未上线|已删除|已移除|已退役|已覆盖|未覆盖)	实现/修复状态断言
R7-status-pending	(待实现|待更新|待补齐|待接入|待完成|待回写|待收敛|未完成|未闭环|未接入|尚未)	"待办/未闭环"状态断言
R8-unverifiable	(当前不可验证|不可验证|无法验证|无法实测|未经实测|无法复现|不可复现|无法证明|不能验证)	"无法验证/不可复现"断言
R9-capability-absent	无[^，。；、！？：a-zA-Z0-9]{0,6}(能力|通道|实现|支持|覆盖|测试|接口|端点|消费者|调用点|清理器|页面|字段|机制|入口|开关|路由|迁移)	"无 X（能力名词）"型缺失断言
R10-zero-x	零(消费者|消费|调用|引用|命中|覆盖|使用|实现|写入|测试|配置|日志)	"零消费/零调用"型缺失断言（本仓惯用说法）
R11-absence-enum	(没有|无)[[:space:]]*(webhook|机器人|邮件|短信|推送|外发通道|通知渠道|清理器)	枚举式"没有 X/Y/Z"能力缺失断言（G-1 原文的第二行即此形）
RULES
}

usage() {
  cat <<'USAGE'
用法：
  tools/docs/claims-audit.sh [选项]

选项：
  --selftest              运行分类器自校准（唯一会非 0 退出的路径）
  --rules                 只打印规则表
  --paths <文件>...       只扫指定文件（显式路径 = 全文扫描，不做注释过滤）
  --include-docs-analysis 追加扫描 docs/分析/*.md（默认不扫，见 README 的收敛建议）
  --source-scope <mode>   comments（默认，只扫源码注释行）| all（扫全部行，噪音大）
  --max-lines <N>         清单最多打印 N 条（0 = 全部，默认 0）
  --fail-on-candidates    检出候选时退出 2（默认关闭；未接线，见文件头）
  -h, --help              本帮助

退出码：
  0  扫描模式（默认，总是 0）／--selftest 全部自校准断言通过
  1  --selftest 有断言失败（分类器坏了）
  2  --fail-on-candidates 且检出候选（opt-in）
  3  用法错误

本脚本查不到什么（同样重要）：
  * 查不到**语义真假** —— 它只能把"长得像可证伪声称"的句子捞出来，对错要人去核。
  * 查不到**没写出来的声称** —— 沉默的错误（该写而没写）它一无所知。
  * 查不到**改写过的说法** —— 换个措辞（如"目前尚未提供外发能力"）就会漏；规则表是
    黑名单，不是语义理解。
  * 查不到**计数是否算对** —— "仅 4 处"里的 4 对不对，得人去重跑 grep。
  * 默认**不扫源码的非注释行**（字符串字面量、测试期望值）与 docs/设计/ 之外的文档；
    用 --source-scope all / --include-docs-analysis 可显式扩大，但噪音随之上升。
  * 查不到**运行时行为** —— 它是纯文本扫描，不执行任何代码。
USAGE
}

# ── 文件收集 ──────────────────────────────────────────────────────────────────
collect_docs() { ( cd "$REPO_ROOT" && ls docs/设计/*.md 2>/dev/null ); }
collect_docs_analysis() { ( cd "$REPO_ROOT" && ls docs/分析/*.md 2>/dev/null ); }
collect_src() {
  ( cd "$REPO_ROOT" && find backend -name '*.go' -not -path '*/vendor/*' 2>/dev/null
    cd "$REPO_ROOT" && find frontend-shared/src -type f \( -name '*.ts' -o -name '*.vue' \) 2>/dev/null ) | sort
}

# ── 扫描：把命中写成 path<TAB>line<TAB>rule<TAB>text ───────────────────────────
SCAN_SRC_SCOPE="comments"
scan_files() {
  local rule rpat rdesc
  local tmp; tmp="$(mktemp)" || return 1
  : > "$tmp"
  while IFS=$'\t' read -r rule rpat rdesc; do
    [ -z "${rule:-}" ] && continue
    grep -nHE -- "$rpat" "$@" 2>/dev/null | awk -v rule="$rule" -v scope="$SCAN_SRC_SCOPE" '
      {
        i = index($0, ":"); if (i == 0) next
        rest = substr($0, i+1)
        j = index(rest, ":"); if (j == 0) next
        path = substr($0, 1, i-1)
        ln   = substr(rest, 1, j-1)
        txt  = substr(rest, j+1)
        if (ln !~ /^[0-9]+$/) next
        if (scope == "comments" && path ~ /\.(go|ts|vue)$/ && txt !~ /^[ \t]*(\/\/|\/\*|\*|<!--)/) next
        print path "\t" ln "\t" rule "\t" txt
      }' >> "$tmp" || true
  done < <(rules_emit)
  cat "$tmp"
  rm -f "$tmp"
}

# ── 渲染：把扫描结果变成人工复核清单 ──────────────────────────────────────────
render_report() {
  local scanned="$1" docn="$2" srcn="$3" docsan=""
  local tmp; tmp="$(mktemp)"
  awk -F'\t' '{ if ($4 != "") print }' "$scanned" | sort -t$'\t' -k1,1 -k2,2n -u > "$tmp"
  local total_rules total_lines
  total_rules=$(wc -l < "$tmp" | tr -d ' ')
  total_lines=$(cut -f1,2 "$tmp" | sort -u | wc -l | tr -d ' ')

  printf '%s\n' "================================================================================"
  printf '%s\n' "「声称-实现」对账清单 —— 候选，待人工复核（P1-A）"
  printf '%s\n' "================================================================================"
  printf '%s\n' ""
  printf '%s\n' "> 这是 **人工复核清单，不是结论**。"
  printf '%s\n' "> 脚本只负责找出候选，不负责裁决真假；没有命中 **不等于** 没有问题。"
  printf '%s\n' "> 请逐条人工判定：① 声称是否仍然成立 ② 若不成立，改文档还是改代码 ③ 补上可复跑的核验命令。"
  printf '%s\n' ""
  printf '采集时刻 : %s (本地 %s)\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$(date '+%Y-%m-%d %H:%M:%S %z')"
  printf '工作树   : %s\n' "$( cd "$REPO_ROOT" && git rev-parse --short HEAD 2>/dev/null || echo '(非 git 工作树)' )"
  printf '复跑命令 : bash %s\n' "$SELF_REL"
  printf '扫描文件 : docs/设计/*.md = %s ; 源码 = %s (注释行过滤=%s) ; 显式路径附加 = %s\n' \
    "$docn" "$srcn" "$SCAN_SRC_SCOPE" "$docsan"
  printf '规则条数 : %s\n' "$(rules_emit | wc -l | tr -d ' ')"
  printf '总命中   : **%s 行**（行 = 一行文本命中 ≥1 条规则）／ %s 条（行 × 规则，一行可命中多条）\n' "$total_lines" "$total_rules"

  printf '%s\n' ""
  printf '%s\n' "── 按规则分布 ─────────────────────────────────────────────────────────────────"
  printf '%-22s %6s\n' "规则" "命中"
  awk -F'\t' '{c[$3]++} END{for (k in c) printf "%s\t%d\n", k, c[k]}' "$tmp" | sort | while IFS=$'\t' read -r k v; do printf '%-22s %6s\n' "$k" "$v"; done

  printf '%s\n' ""
  printf '%s\n' "── 按文件分布（Top 15）───────────────────────────────────────────────────────"
  awk -F'\t' '{c[$1]++} END{for (k in c) printf "%d\t%s\n", c[k], k}' "$tmp" | sort -rn | head -15 | while IFS=$'\t' read -r v k; do printf '%6s  %s\n' "$v" "$k"; done

  printf '%s\n' ""
  printf '%s\n' "── 清单（文件:行 + 原文 + 命中规则）───────────────────────────────────────────"
  printf '%s\n' "# 格式: 路径:行号: [规则名] 原文(截断显示, 完整原文见该行)"
  local limit="${MAX_LINES:-0}"
  local shown=0
  while IFS=$'\t' read -r path ln rule txt; do
    shown=$((shown+1))
    if [ "$limit" -gt 0 ] && [ "$shown" -gt "$limit" ]; then
      printf '... 其余 %s 条被 --max-lines %s 截断（完整清单请去掉该选项重跑）\n' "$((total_lines-shown+1))" "$limit"
      break
    fi
    printf '%s:%s: [%s] %s\n' "$path" "$ln" "$rule" "$(printf '%s' "$txt" | cut -c1-160)"
  done < "$tmp"

  printf '%s\n' ""
  printf '%s\n' "── 脚本查不到什么（务必连这段一起读）────────────────────────────────────────"
  printf '%s\n' "1. 查不到语义真假：它只捞出"长得像可证伪声称"的句子，真假要人去核。"
  printf '%s\n' "2. 查不到没写出来的声称：该写而没写的错误（沉默的错），它一无所知。"
  printf '%s\n' "3. 查不到改写过的说法：换个措辞（如"目前尚未提供外发能力"）就会漏——规则表是黑名单，不是语义理解。"
  printf '%s\n' "4. 查不到计数是否算对："仅 4 处"里的 4 对不对，要人重跑 grep。"
  printf '%s\n' "5. 默认不扫源码非注释行、不扫 docs/设计/ 之外的文档（可用 --source-scope all / --include-docs-analysis 扩大）。"
  printf '%s\n' "6. 查不到运行时行为：纯文本扫描，不执行任何代码。"
  printf '%s\n' ""
  printf '%s\n' "⇒ 清单为空 **不代表** 无漂移；只代表"当前规则表在当前扫描范围内没有捞到候选"。"
  printf '%s\n' "================================================================================"
  rm -f "$tmp"
}

# ── 自校准 ────────────────────────────────────────────────────────────────────
selftest() {
  local fail=0
  printf '%s\n' "== claims-audit 自校准（断言的是**分类器**，不是仓库）=="

  local must="$CORPUS_DIR/must-detect.txt" mustnot="$CORPUS_DIR/must-not-detect.txt"
  [ -f "$must" ]    || { echo "FAIL 语料缺失: $must"; return 1; }
  [ -f "$mustnot" ] || { echo "FAIL 语料缺失: $mustnot"; return 1; }

  # S1: 固定语料（G-1..G-4 的**原文**）必须全部被报出
  local got; got="$(scan_files "$must")"
  local texts; texts="$(printf '%s\n' "$got" | awk -F'\t' '{print $4}')"
  local n=0 missed=0 skipped_ctx=0 cur_is_ctx=0
  while IFS= read -r want; do
    case "$want" in
      '') continue;;
      '@@'*)
        # 语料头部注释可以声明**后续行只是上下文**（不是独立可证伪声称）。
        # 语料里确实有这种行（G-4 的"这是一个**产品缺口**"只是判断，不是断言），
        # 若不认这个标记，自校准会要求一条本就不该被检出的行 ⇒ 永远假红。
        # 判据用显式标记，而不是"猜哪一行像上下文" —— 后者会让语料悄悄失去约束力。
        case "$want" in
          *非独立声称*|*上下文行*) cur_is_ctx=1;;
          *) cur_is_ctx=0;;
        esac
        continue;;
    esac
    if [ "$cur_is_ctx" = 1 ]; then
      skipped_ctx=$((skipped_ctx+1)); continue
    fi
    n=$((n+1))
    if ! printf '%s\n' "$texts" | grep -qxF -- "$want"; then
      printf 'FAIL [S1 must-detect] 已知漂移未被报出: %s\n' "$want"
      missed=$((missed+1)); fail=1
    fi
  done < "$must"
  printf '  S1 固定语料 must-detect : 应报出 %s 行，漏报 %s 行（另有 %s 行已标记为纯上下文，不参与断言）\n' \
    "$n" "$missed" "$skipped_ctx"

  # S2: 正常文本不得被报出（防误报爆炸）
  local got2; got2="$(scan_files "$mustnot")"
  local texts2; texts2="$(printf '%s\n' "$got2" | awk -F'\t' '{print $4}')"
  local m=0 bad=0
  while IFS= read -r line; do
    case "$line" in '@@'*|'') continue;; esac
    m=$((m+1))
    if printf '%s\n' "$texts2" | grep -qxF -- "$line"; then
      printf 'FAIL [S2 must-NOT-detect] 正常文本被误报: %s\n' "$line"
      bad=$((bad+1)); fail=1
    fi
  done < "$mustnot"
  printf '  S2 固定语料 must-NOT-detect : %s 行，误报 %s 行\n' "$m" "$bad"

  # S3: 临时注入一条已知漂移 ⇒ 必须被报出（不依赖仓库当前内容）
  local tmpd; tmpd="$(mktemp -d)"
  local inj="$tmpd/injected-drift.md"
  {
    echo "# 注入用例（临时目录，跑完即删）"
    echo "本文件由 --selftest 动态生成，用于证明脚本对**新出现**的漂移同样敏感。"
    echo "经全仓核实，当前不存在任何 notification_channels 表。"
    echo "该能力仅 1 处调用点，尚未实现。"
  } > "$inj"
  local got3; got3="$(scan_files "$inj")"
  local ok=1
  for want in "经全仓核实，当前不存在任何 notification_channels 表。" "该能力仅 1 处调用点，尚未实现。"; do
    if ! printf '%s\n' "$got3" | awk -F'\t' '{print $4}' | grep -qxF -- "$want"; then
      printf 'FAIL [S3 动态注入] 未被报出: %s\n' "$want"
      ok=0; fail=1
    fi
  done
  printf '  S3 动态注入到临时目录      : %s 行，全部被报出=%s\n' "2" "$ok"
  rm -rf "$tmpd"

  # S4: 规则表里**每一条规则**都必须至少被固定语料触发一次。
  #     没有这条，往规则表里加一条永远匹配不到/被改坏的正则时，自校准会静默通过。
  local hit_rules uncovered=0 nrules=0
  hit_rules="$(printf '%s\n' "$got" | awk -F'\t' '{print $3}' | sort -u)"
  while IFS=$'\t' read -r rule _rest; do
    [ -z "${rule:-}" ] && continue
    nrules=$((nrules+1))
    if ! printf '%s\n' "$hit_rules" | grep -qxF -- "$rule"; then
      printf 'FAIL [S4 规则覆盖] 规则未被任何固定语料触发（规则已坏或语料缺例）: %s\n' "$rule"
      uncovered=$((uncovered+1)); fail=1
    fi
  done < <(rules_emit)
  printf '  S4 规则覆盖度              : 规则表 %s 条，未被语料覆盖 %s 条\n' "$nrules" "$uncovered"

  if [ "$fail" -eq 0 ]; then
    printf '%s\n' "== 自校准结果: 分类器全部断言通过（注意：这**只**说明工具没坏，不说明仓库没问题）=="
    return 0
  fi
  printf '%s\n' "== 自校准结果: 有断言失败 —— 分类器已坏，清单不可信 =="
  return 1
}

# ── 主流程 ────────────────────────────────────────────────────────────────────
MODE="scan"; MAX_LINES=0; FAIL_ON_CANDIDATES=0; INCLUDE_ANALYSIS=0; EXPLICIT_PATHS=()
while [ $# -gt 0 ]; do
  case "$1" in
    --selftest) MODE="selftest"; shift;;
    --rules) MODE="rules"; shift;;
    --include-docs-analysis) INCLUDE_ANALYSIS=1; shift;;
    --source-scope) SCAN_SRC_SCOPE="${2:-comments}"; shift 2;;
    --max-lines) MAX_LINES="${2:-0}"; shift 2;;
    --fail-on-candidates) FAIL_ON_CANDIDATES=1; shift;;
    --paths) shift; while [ $# -gt 0 ] && [ "${1#--}" = "$1" ]; do EXPLICIT_PATHS+=("$1"); shift; done;;
    -h|--help) usage; exit 0;;
    *) echo "未知选项: $1" >&2; usage >&2; exit 3;;
  esac
done
export MAX_LINES

case "$MODE" in
  rules) rules_emit | awk -F'\t' '{printf "%-22s %s\n    正则: %s\n", $1, $3, $2}'; exit 0;;
  selftest) selftest; exit $?;;
esac

if [ "${#EXPLICIT_PATHS[@]}" -gt 0 ]; then
  out="$(scan_files "${EXPLICIT_PATHS[@]}")"
  printf '%s\n' "$out" | render_report /dev/stdin "0" "0" "${#EXPLICIT_PATHS[@]}"
  exit 0
fi

mapfile -t DOC_FILES < <(collect_docs)
DOC_ANALYSIS_FILES=()
[ "$INCLUDE_ANALYSIS" -eq 1 ] && mapfile -t DOC_ANALYSIS_FILES < <(collect_docs_analysis)
mapfile -t SRC_FILES < <(collect_src)
ALL=("${DOC_FILES[@]}" "${DOC_ANALYSIS_FILES[@]}" "${SRC_FILES[@]}")

if [ "${#ALL[@]}" -eq 0 ]; then
  echo "未收集到任何待扫文件（REPO_ROOT=$REPO_ROOT）—— 这**不是**'无漂移'，而是扫描器没跑起来" >&2
  exit 0
fi

scan_files "${ALL[@]}" | render_report /dev/stdin "${#DOC_FILES[@]}" "${#SRC_FILES[@]}" "0"

if [ "$FAIL_ON_CANDIDATES" -eq 1 ]; then
  n="$(scan_files "${ALL[@]}" | wc -l | tr -d ' ')"
  [ "$n" -gt 0 ] && exit 2
fi
exit 0
