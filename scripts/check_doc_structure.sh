#!/usr/bin/env bash
# 反模式文档（docs/验证/测试判据反模式-*.md）的结构自检。
#
# 适用范围：**只**适用于那份文档。它按「反模式 N」章节与「N 个变体」表格做的
# 专项核对，换一份文档（比如演练清单）会正常地报「找不到章节」—— 那是误用，
# 不是文档有问题。给其他文档做结构自检时，只复用它的**思路**（CHECKS+FAILS
# 双计数 + 逐条打印实际值），不要直接套这个脚本。
#
# 这个脚本本身就是「反模式 8」的落地：
#   只数「不合规项」为 0 是不够的（零违规 和 零检查 同形），
#   所以每个判据都先断言「检查了多少条」，再断言「其中多少条违规」。
#
# 用法：bash check_doc_structure.sh <markdown 文件>
set -uo pipefail
DOC="${1:?用法: bash check_doc_structure.sh <markdown 文件>}"
[ -f "$DOC" ] || { echo "文件不存在: $DOC"; exit 2; }

FAILS=0
CHECKS=0
fail() { FAILS=$((FAILS+1)); echo "  [X] $*"; }
ok()   { echo "  [OK] $*"; }

echo "=== 自检 1：章节号严格递增且无跳号 ==="
CHECKS=$((CHECKS+1))
nums="$(grep -oE '^## [0-9]+\.' "$DOC" | grep -oE '[0-9]+' | tr "\n" " ")"
echo "  实际章节号: $nums"
expected=0
section_fail=0
while read -r n; do
  [ -z "$n" ] && continue
  if [ "$n" -ne "$expected" ]; then
    fail "章节号断裂：期望 $expected，实际 $n"
    section_fail=1
    break
  fi
  expected=$((expected+1))
done <<< "$(echo $nums | tr " " "\n")"
[ "$section_fail" -eq 0 ] && ok "0-$((expected-1)) 连续无跳号"

echo
echo "=== 自检 2：反模式章节数 == 变体表行数 == 正文声明的 N ==="
CHECKS=$((CHECKS+1))
AP_COUNT="$(grep -cE '^## [0-9]+\. 反模式' "$DOC")"
TABLE_ROWS="$(sed -n '/^## [0-9]*\. 本轮/,/^---$/p' "$DOC" | grep -cE '^\| [0-9]+ \|')"
CLAIM="$(grep -oE '本轮 [0-9]+ 个变体' "$DOC" | grep -oE '[0-9]+' | head -1)"
echo "  反模式章节数: $AP_COUNT"
echo "  变体表行数  : $TABLE_ROWS"
echo "  正文声明 N  : ${CLAIM:-<未找到>}"
if [ "$AP_COUNT" -eq 0 ]; then
  fail "一个反模式章节都没数到 —— 检查是不是标题格式变了（这是「零检查」陷阱）"
elif [ "$AP_COUNT" -ne "$TABLE_ROWS" ]; then
  fail "反模式数($AP_COUNT) != 变体表行数($TABLE_ROWS) —— 汇总数字与实际条目脱钩"
elif [ -n "${CLAIM:-}" ] && [ "$AP_COUNT" -ne "$CLAIM" ]; then
  fail "正文声明 $CLAIM 个，实际 $AP_COUNT 个"
else
  ok "$AP_COUNT 个反模式 / $TABLE_ROWS 行表格 / 声明 $CLAIM —— 三者一致"
fi

echo
echo "=== 自检 3：分隔线格式（无孤立单破折号、无重复 ---、--- 后有空行）==="
CHECKS=$((CHECKS+1))
n_stray="$(grep -c '^-$' "$DOC" || true)"
n_dup="$(awk 'prev=="---" && $0=="---"{c++} {prev=$0} END{print c+0}' "$DOC")"
n_noblank="$(awk 'prev=="---" && $0!="" && $0!="---"{c++} {prev=$0} END{print c+0}' "$DOC")"
echo "  孤立单破折号: $n_stray / 重复---: $n_dup / ---后缺空行: $n_noblank"
[ "$n_stray" -eq 0 ] && [ "$n_dup" -eq 0 ] && [ "$n_noblank" -eq 0 ] \
  && ok "分隔线格式正确" \
  || fail "分隔线格式有问题（见上）"

echo
echo "=== 自检 4：自查问题条数 == 标题声明的中文数字 ==="
CHECKS=$((CHECKS+1))
CLAIM_CN="$(grep -oE '^## [0-9]+\. [一二三四五六七八九十]+条自查问题' "$DOC" | grep -oE '[一二三四五六七八九十]+' | head -1)"
N_ITEMS="$(sed -n '/^## [0-9]*\..*条自查问题/,/^### /p' "$DOC" | grep -cE '^[0-9]+\. ')"
echo "  标题声明: ${CLAIM_CN:-<未找到>} / 实际条数: $N_ITEMS"
case "${CLAIM_CN:-}" in
  一) cn=1;; 二) cn=2;; 三) cn=3;; 四) cn=4;; 五) cn=5;;
  六) cn=6;; 七) cn=7;; 八) cn=8;; 九) cn=9;; 十) cn=10;; *) cn=-1;;
esac
if [ "$cn" -lt 0 ]; then
  fail "标题里没找到中文数字条数（格式变了？）"
elif [ "$cn" -ne "$N_ITEMS" ]; then
  fail "标题写「$CLAIM_CN 条」实际 $N_ITEMS 条"
else
  ok "标题「$CLAIM_CN 条」= 实际 $N_ITEMS 条"
fi

echo
echo "=== 汇总 ==="
echo "  共执行 $CHECKS 项检查，$FAILS 项失败"
if [ "$FAILS" -gt 0 ]; then
  echo "  [X] 文档结构自检不通过"
  exit 1
fi
echo "  [OK] 全部通过"
exit 0