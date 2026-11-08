#!/usr/bin/env bash
# check_all.sh — 提交前/交付前的**单一入口**：跑全部已有检查，逐维度报结果。
#
# ============ 设计目标：让已有检查不可能被跳过 ============
#
# 2026-11-08 实测的教训：
#
#   check_doc_structure.sh 早就存在（为反模式 12 写的），它**能抓到**
#   「加了章节没加汇总表」这个错 —— 而它抓到了，只是**没人跑**。
#
#   两个人当天都没跑它，理由不同：
#     · impl-l1：「§16 是 impl-l2 加的，我以为『他加的是文档，风险低』」
#     · impl-l2：「我在 5 分钟前才第一次执行它」
#
#   => **不是「工具不存在」，是「工具存在且能抓到，但没进流程」。**
#
# 所以本脚本**不新增任何检查** —— 它只做三件事：
#   ① 一个入口（不用记有几个脚本）
#   ② 逐维度报结果（不合并成一句「全绿」）
#   ③ 任一维度失败 => exit 非 0（失败必须阻断）
#
# ============ 三条设计约束（Lead 定）============
#
# ① **逐维度报结果，不合并**
#    理由：只报总体的 check_all 会重现它要解决的问题 ——
#    它说「全绿」时读者不知道是三个维度都绿了，还是只跑了一个。
#
# ② **树状态前置**
#    先报 HEAD sha + 工作区改动数。因为「哪个 sha 上全绿」是唯一可复现的依据。
#    有未提交改动时**不阻断**（本地自检是正常的），但必须打印出来 ——
#    否则「在改动过的树上验过」会被误读成「提交的那个 sha 验过」。
#
# ③ **不能假设 go 存在，也不能假设 PG 环境可用**
#    本机没有 go（用 docker）。三种情况都要区分：
#      有 go              => go test ./...
#      无 go 有 docker    => scripts/gow.sh test ./...
#      都没有             => SKIP（exit 2），**不计为失败也不计为通过**
#    最后一条是关键：静默跳过 = 「44 包全绿但 PG 用例全部 Skip」的重现。

# ④ **不能假设 EHOME_TEST_DB 已生效**（2026-11-08 自查发现的自身 bug）======

#    第一版本脚本走 `scripts/gow.sh test ./...`，而 gow.sh 只转发**调用方 shell
#    里已存在的** EHOME_* 变量。若调用方没 export EHOME_TEST_DB=postgres，
#    容器里就没有它 => 所有 requirePostgres(t) 的用例**静默 Skip** =>
#    输出仍然是「44 包 ok / 0 FAIL」=> exit 0。

#    **也就是说：本脚本会重现它自己要防的那个事故（反模式 6）。**

#    修法不是「猜环境」，是**测量**：先跑一条已知必走 PG 的用例，
#    断言它 PASS（而不是 SKIP）。这是正向见证，不是聚合判断。
#      PASS  => PG 通了 => 继续跑全量
#      SKIP  => PG 没生效 => 维度 1 记 SKIP（不是 FAIL：代码没问题，环境缺）
#      其它  => 探针未产出 PASS/SKIP => 输出异常 => 也记 SKIP

#    并且全量那条命令**显式带 env**，不依赖 gow.sh 转发；
#    同时在输出里打印实际连的库，让读者知道它验的是哪个库。
#
# ============ 用法 ============
#
#   scripts/check_all.sh              # 全量（含 go test，慢）
#   scripts/check_all.sh --fast       # 跳过 go test，只跑文档类（秒级）
#   scripts/check_all.sh --help
#
# ============ 退出码是三态，不是二态 ============
#
#   0  全部维度通过
#   1  有维度失败
#   2  **没有失败，但也没验全**（有维度被跳过 / 未执行）
#
# 为什么 2 要单独一个码：exit 0 会被读成「全绿」，
# 而「跳过的维度」正是今天反复防的那个洞（「44 包全绿但 PG 用例全部 Skip」）。
# 二态退出码无法表达「部分验证」，而 CI 会把它当绿。
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

FAST=0
case "${1:-}" in
  --fast) FAST=1 ;;
  --help|-h) sed -n '2,50p' "${BASH_SOURCE[0]}"; exit 0 ;;
  "") ;;
  *) echo "unknown arg: $1（用 --help）" >&2; exit 2 ;;
esac

# ---------- 统计（CHECKS 双计数：跑了几个 / 挂了几个）----------
TOTAL=0
FAILED=0
SKIPPED=0
RESULTS=()

emit() { # emit <status: OK|FAIL|SKIP> <name> <detail>
  local st="$1" name="$2" detail="$3"
  TOTAL=$((TOTAL + 1))
  case "$st" in
    OK)   ;;
    SKIP) SKIPPED=$((SKIPPED + 1)) ;;   # 显式未运行：不是失败，但也绝不是通过
    *)    FAILED=$((FAILED + 1)) ;;
  esac
  RESULTS+=("$(printf '  %-6s %-34s %s' "[$st]" "$name" "$detail")")
  printf '  %-6s %-34s %s\n' "[$st]" "$name" "$detail"
}

line() { printf '%s\n' "================================================================================"; }

# failing_packages <output> — 从 go test 输出里抽「红的包名」（约束④）
#
# 为什么需要：44 包 ok 会让「43 包 ok + 1 包红」在视觉上等同 ——
# 而读者真正要知道的是**哪一个包红了**。
#
# 为什么是 '^FAIL[[:space:]]+ehome' 而不是 '^FAIL'：
#   go test 的总结行 `FAIL` 不带包名（单独一个词），裸 '^FAIL' 会命中它，
#   于是输出里出现一个空包名。加 [[:space:]]+ 就排除了它。
# 为什么不匹配 '--- FAIL: TestXxx'：那些是用例级行，不带包名，
#   它们由各维度自己的详细输出负责（这里只点包，避免刷屏）。
failing_packages() {
  printf '%s\n' "$1" | grep -E '^FAIL[[:space:]]+ehome' | awk '{print $2}' | paste -sd, -
}

# ---------- 前置 A：并发互斥（约束②）----------
#
# 为什么需要：本仓全部 PG 用例共用一个库（EHOME_DB_NAME=ehome_test），而
# testutil 的隔离靠「每个测试进程建自己的 schema + search_path 指过去」。
# 两轮 go test **并发**时，其中一轮的探针/建表可能落在另一轮的窗口里 ——
# 2026-11-08 早上那批 42P07「表已存在」/ 42P16「列在主键里」就是这么来的。
#
# 而它的症状最坏：**不是红，是「红得没道理」** —— 单跑绿、并发红、重跑又绿，
# 于是会被归因成 flaky 而忽略（反模式 9 的形态）。
#
# 为什么用 flock 而不是 `docker ps | grep 'go test'`（l2 提的方案 a）：
#   · a 有窗口：两个进程可以都在「还没进 docker ps」时同时启动，互不可见；
#   · a 的匹配是字符串 grep，换个写法就漏、别的命令含那串就误报；
#   · flock 是原子的，无窗口。
#
# ============ 锁文件放在 .git/ 下（2026-11-08 实测修正）============
#
# 第一版用的是 `${TMPDIR:-/tmp}/ehome_check_all.lock` —— **在沙箱里 100% 失效**。
#
# 实测（两个人独立复现，方法不同）：
#
#   CALL A: echo marker > /tmp/probe.txt      => 写入成功，ls /tmp 可见
#   CALL B: ls /tmp/probe.txt                 => No such file（/tmp 是空的）
#
#   /proc/mounts:
#     tmpfs /tmp tmpfs ro,...
#     tmpfs /tmp tmpfs rw,...,uid=1000,inode64
#
#   => **沙箱每次 bash 调用给一个独立的私有 tmpfs 挂载在 /tmp**。
#      两个独立调用各自在**自己的 /tmp** 里建锁 => 互斥从不发生。
#
#   而它**看起来是有效的**：有实现、有注释、同进程 fork 测过能互斥 ——
#   只是真实使用场景里从不生效。这比「没实现」更危险，因为**你有验证记录**。
#
# 改放 .git/ 下（`git rev-parse --git-common-dir`）的三条理由：
#   · 跨 bash 调用持久（.git 是真实文件系统，不是 per-call tmpfs）—— 实测 DENIED 成立
#   · .git/ 内部文件不被 git 追踪 => 不产生未跟踪文件（实测 git status 干净）
#   · `--git-common-dir` 跨 worktree 统一指向主仓 .git => 同机所有 worktree 共用一个锁
#     （这一点比原来的仓内 .tmp-* 更好：后者每个 worktree 各锁各的）
#
# 用 --path-format=absolute 优先（git 2.31+），失败退回相对路径：
# 相对路径在 cd 之后会解析错，而本脚本在 cd "$ROOT" 之后调用它 ——
# 实测当前 git 版本两者都给出可用结果，但绝对路径不依赖 cwd，更稳。
GD="$(git rev-parse --path-format=absolute --git-common-dir 2>/dev/null || git rev-parse --git-common-dir)"
LOCK="$GD/ehome_check_all.lock"
#
# ============ 本锁的覆盖范围（写清它挡不住什么）============
#
# 本锁覆盖：**另一个 check_all.sh 实例**。
#
# 本锁**不覆盖**：有人直接跑 `scripts/gow.sh test ./...`（或裸 `go test`）——
# 而 2026-11-08 那次真实的并发污染正是后者：两份全量 + 两条定向同时在跑，
# 打出 43 包 ok / 5 FAIL，而单跑是 44 ok / 0 FAIL。
#
# 为什么不去给 gow.sh 也加锁：那会让所有单包测试串行化
# （本仓日常就是「改一个包 + 单跑那个包」），代价远大于收益。
#
# 所以本脚本能承诺的只是「我自己不并发」，**不能承诺「整机没有并发」** ——
# 而这句话必须写在输出里，否则读者会以为它挡住了所有并发。
#
# 为什么整个脚本 exit 2 而不是「跳过维度 1 继续跑 2/3」：
# 并发污染不只影响 go test —— 它让**这次结果整体不可信**。
# 而「SKIP 维度 1 继续」会让输出看起来「部分通过」，实际是「这次不算数」。
# 锁放在最前面（而不是「只在跑 go test 前加锁」）：
#   · 现在 --fast 恰好不跑 go test，所以理论上它可以并发；
#   · 但 --fast 的维度集合**会变**（本脚本就是为「加更多检查」而设计的），
#     一旦它含测试维度，「--fast 免锁」就成了漏洞。
#   · 所以锁与「当前 --fast 跑什么」解耦，代价是 --fast 也可能等几秒。
#
# 判据：**锁的语义不该依赖「当前分支恰好不跑什么」这个易变事实。**
#
# exec 失败要单独报：它和「锁被占用」是两件事，而两者的字符串如果相同，
# 归因就会错（2026-11-08 我本人踩过：`flock -n 9` 在 fd 未打开时报
# "Bad file descriptor"，而输出里的 DENIED 看起来像「锁被占用」——
# 差点当成「锁生效」的证据）。
if ! exec 9>"$LOCK"; then
  line
  echo " 锁文件创建失败：$LOCK"
  line
  echo "  ⚠ 这不是「另一轮在跑」—— 该查目录权限 / 磁盘满 / .git 是否可写。"
  echo "     本次结果不可信（互斥未建立）。"
  line
  exit 2
fi
if ! flock -n 9; then
  line
  echo " 另一轮 check_all / go test 正在跑 —— 本脚本拒绝并发执行"
  line
  echo "  锁文件: $LOCK（被占用）"
  echo
  echo "  为什么拒绝：本仓全部 PG 用例共用 ehome_test 库，并发会让彼此的表"
  echo "  落进对方的 schema 窗口 => 42P07/42P16 => 红得没道理（今天的原事故）。"
  echo "  而那种红会被归因成 flaky 而忽略（反模式 9）。"
  echo
  echo "  确认无人在跑后可删除锁文件重试："
  echo "    rm -f $LOCK"
  echo
  echo "  退出码 2 = 没有失败，但也没验全（本次结果不可信，不是「部分通过」）"
  line
  exit 2
fi

line
echo " check_all.sh — 全部已有检查 / 单一入口"
line

# ---------- 前置 B：树状态（约束⑤）----------
HEAD_SHA="$(git rev-parse --short HEAD 2>/dev/null || echo '<no-git>')"
DIRTY="$(git status --porcelain 2>/dev/null | wc -l | tr -d ' ')"
UNTRACKED_SRC="$(git ls-files --others --exclude-standard -- 'backend/**/*.go' 2>/dev/null | wc -l | tr -d ' ')"

echo " 树状态"
echo "   HEAD            : $HEAD_SHA"
echo "   工作区改动      : $DIRTY 个文件"
echo "   未跟踪源码      : $UNTRACKED_SRC 个"
if [ "$DIRTY" != "0" ] || [ "$UNTRACKED_SRC" != "0" ]; then
  echo "   ⚠ 工作区不干净 —— 下面的结果绑定不到某个 commit sha"
  echo "     （不阻断：本地自检在改动过的树上跑是正常的；但别把它当成「那个 sha 验过」）"
fi
echo

# ---------- PG 测试环境的默认值（可被调用方覆盖）----------
#
# 为什么在这里给默认值而不是要求调用方 export：本仓的 PG 集成测试标准环境是固定的
# （127.0.0.1:5432 / ehome / ehome_test），而「没设 env 就静默 Skip」是今天踩过的坑。
# 给了默认值之后，探针会**测量**它是否真的生效；测量失败才记 SKIP —— 不猜。
export EHOME_TEST_DB="${EHOME_TEST_DB:-postgres}"
export EHOME_DB_HOST="${EHOME_DB_HOST:-127.0.0.1}"
export EHOME_DB_PORT="${EHOME_DB_PORT:-5432}"
export EHOME_DB_USER="${EHOME_DB_USER:-ehome}"
export EHOME_DB_PASSWORD="${EHOME_DB_PASSWORD:-ehome123}"
export EHOME_DB_NAME="${EHOME_DB_NAME:-ehome_test}"

# ---------- 维度 1：go test（约束③④：go/docker 与 PG 环境都要测量）----------
if [ "$FAST" = "1" ]; then
  emit SKIP "go test ./..." "(--fast 显式跳过；未验证 != 通过)"
else
  # ── 先测 PG 环境（约束④）：跑一条已知必走 PG 的用例，断言 PASS 而非 SKIP ──
  #
  # 选 TestAutoMigrate_ReallyRanAndBuiltOtherTables 的理由：
  #   · 它本来就 requirePostgres/OpenTestDB，未生效时必然 SKIP（判据分明）
  #   · 它验的是「AutoMigrate 真的建了 3 张见证表」—— 正好是「环境真通了」的直接证据
  #     （而不是「没报错」，那太弱）
  PG_PROBE='^TestAutoMigrate_ReallyRanAndBuiltOtherTables$'
  PG_DESC="${EHOME_DB_NAME}@${EHOME_DB_HOST}:${EHOME_DB_PORT}"
  PG_OK=0
  PG_REASON=""
  echo "  [1/3] go test ./..."
  echo "     PG 探针: $PG_PROBE"
  echo "     DB     : $PG_DESC（探针实测，不是假设）"
  if command -v go >/dev/null 2>&1; then
    PROBE_OUT="$(go test -run "$PG_PROBE" -v ./internal/database/ 2>&1 || true)"
  elif command -v docker >/dev/null 2>&1; then
    PROBE_OUT="$(scripts/gow.sh test -run "$PG_PROBE" -v ./internal/database/ 2>&1 || true)"
  else
    PROBE_OUT=""
  fi
  if printf '%s' "$PROBE_OUT" | grep -q '^--- PASS'; then
    PG_OK=1
  elif printf '%s' "$PROBE_OUT" | grep -q '^--- SKIP'; then
    PG_REASON="PG 探针 Skip —— EHOME_TEST_DB 未生效，PG 用例会全部静默 Skip，跑了也不代表全验"
  else
    PG_REASON="PG 探针未产出 PASS/SKIP —— 输出异常（可能 go/docker 不可用）"
  fi

  if [ "$PG_OK" != "1" ]; then
    echo "  [1/3] go test ./...  => 跳过（$PG_REASON）"
    emit SKIP "go test ./..." "$PG_REASON"
  elif command -v go >/dev/null 2>&1; then
    OUT="$(go test -count=1 ./... 2>&1)"; RC=$?
    OKN="$(printf '%s' "$OUT" | grep -cE '^ok')"
    FAILN="$(printf '%s' "$OUT" | grep -cE '^(FAIL|--- FAIL)')"
    if [ "$RC" -eq 0 ]; then
      emit OK "go test ./..." "$OKN 包 ok / $FAILN FAIL（DB $PG_DESC）"
    else
      RED="$(failing_packages "$OUT")"
      emit FAIL "go test ./..." "红: ${RED:-<未解析到包名>}（$OKN 包 ok / $FAILN FAIL，exit $RC）"
      printf '%s\n' "$OUT" | grep -E '^--- FAIL' | head -10 | sed 's/^/         /'
    fi
  elif command -v docker >/dev/null 2>&1; then
    OUT="$(scripts/gow.sh test -count=1 ./... 2>&1)"; RC=$?
    OKN="$(printf '%s' "$OUT" | grep -cE '^ok')"
    FAILN="$(printf '%s' "$OUT" | grep -cE '^(FAIL|--- FAIL)')"
    if [ "$RC" -eq 0 ]; then
      emit OK "go test ./... (docker)" "$OKN 包 ok / $FAILN FAIL（DB $PG_DESC）"
    else
      RED="$(failing_packages "$OUT")"
      emit FAIL "go test ./... (docker)" "红: ${RED:-<未解析到包名>}（$OKN 包 ok / $FAILN FAIL，exit $RC）"
      printf '%s\n' "$OUT" | grep -E '^--- FAIL' | head -10 | sed 's/^/         /'
    fi
  else
    # 无 go 也无 docker => **SKIP（exit 2）而不是 FAIL（exit 1）**
    #
    # 为什么不是 FAIL：FAIL 意味着「代码或测试有问题」，而这里代码没问题 ——
    # 缺的是环境（本机装 go，或起 docker）。两者混淆会把「环境缺」写成「代码坏」。
    #
    # 为什么也不能是 OK：它**没验**。而「没验」正是今天反复防的那个洞
    # （「44 包全绿但 PG 用例全部 Skip」）。
    #
    # 所以正确编码是第三态 exit 2：没失败，但也没验全。
    #
    # ⚠ 为什么不能因为「反正是环境问题」就放过它：一个永久红的检查会被
    #   `|| true` 绕过，而绕过之后连「没验」都不知道了。SKIP 的语义是
    #   「这次不算数」，它会持续出现在输出里而不会让人想把它消掉。
    emit SKIP "go test ./..." "无 go 也无 docker —— 环境缺失，本维度未执行"
  fi
fi

# ---------- 维度 2：文档结构自检 ----------
if [ -x scripts/check_doc_structure.sh ] || [ -f scripts/check_doc_structure.sh ]; then
  echo "  [2/3] check_doc_structure.sh"
  OUT="$(bash scripts/check_doc_structure.sh 'docs/验证/测试判据反模式-2026-11-07.md' 2>&1)"; RC=$?
  NFAIL="$(printf '%s' "$OUT" | grep -c '\[X\]')"
  NCHECK="$(printf '%s' "$OUT" | grep -oE '共执行 [0-9]+ 项检查' | grep -oE '[0-9]+' | head -1)"
  NCHECK="${NCHECK:-0}"
  if [ "$RC" -eq 0 ]; then
    emit OK "check_doc_structure.sh" "${NCHECK} 项检查 / 0 失败"
  else
    emit FAIL "check_doc_structure.sh" "${NCHECK} 项检查 / ${NFAIL} 失败"
    printf '%s\n' "$OUT" | grep '\[X\]' | sed 's/^/         /'
  fi
else
  # 为什么这里报 FAIL，而维度 1「无 go 也无 docker」报 SKIP：「缺的东西」性质不同。
  #
  #   · 无 go / 无 docker  => 缺的是**本机环境**，换一台机器就能验 => SKIP
  #   · 脚本不存在          => 缺的是**检查能力本身**，换机器也验不了 => FAIL
  #                          且它通常意味着「有人把脚本删了/改名了」—— 那是真问题
  #
  # 判据：缺失的东西如果是「本机偶然没有」=> SKIP；
  #       如果是「这个仓永久没有」=> FAIL。
  emit FAIL "check_doc_structure.sh" "脚本不存在 —— 检查能力缺失（不是环境缺失）"
fi

# ---------- 维度 3：文档命令自检 ----------
if [ -f scripts/check_doc_commands.sh ]; then
  echo "  [3/3] check_doc_commands.sh"
  OUT="$(bash scripts/check_doc_commands.sh 2>&1)"; RC=$?
  NVIOL="$(printf '%s' "$OUT" | grep -cE '^  [^ ].*:[0-9]+  ')"
  if [ "$RC" -eq 0 ]; then
    NDOC="$(printf '%s' "$OUT" | grep -oE '[0-9]+ 份文档' | grep -oE '[0-9]+' | head -1)"
    emit OK "check_doc_commands.sh" "${NDOC:-?} 份文档 / 0 违规"
  else
    emit FAIL "check_doc_commands.sh" "${NVIOL} 处违规"
    printf '%s\n' "$OUT" | grep -E '^  [^ ].*:[0-9]+  ' | head -10 | sed 's/^/         /'
  fi
else
  # 同维度 2：脚本不存在 = 检查能力缺失 => FAIL（理由见那里）
  emit FAIL "check_doc_commands.sh" "脚本不存在 —— 检查能力缺失（不是环境缺失）"
fi

# ---------- 汇总：逐维度列出来，不只报总体（约束①）----------
echo
line
echo " 结果（逐维度）"
line
for r in "${RESULTS[@]}"; do
  printf '%s\n' "$r"
done
line
if [ "$FAILED" -gt 0 ]; then
  echo " FAIL：$TOTAL 个维度中 $FAILED 个失败、$SKIPPED 个跳过（HEAD $HEAD_SHA，工作区 $DIRTY 改动）"
  line
  exit 1
fi
if [ "$SKIPPED" -gt 0 ]; then
  # 退出码 2 = 「没有失败，但也没验全」。
  # 为什么要单独一个码：exit 0 会被读成「全绿」，而这正是今天反复防的那件事。
  echo " 部分通过：$TOTAL 个维度中 $((TOTAL - SKIPPED)) 个通过、$SKIPPED 个跳过（HEAD $HEAD_SHA，工作区 $DIRTY 改动）"
  echo
  echo " ⚠ 这不是「全绿」—— 有维度没运行。要全绿请不带 --fast 重跑。"
  line
  exit 2
fi
echo " 全部 $TOTAL 个维度通过（HEAD $HEAD_SHA，工作区 $DIRTY 改动）"
line
exit 0
