#!/usr/bin/env bash
# 黑盒验证 · project 级排他锁（复用 backend/simulation/harness/lock.go 的 flock 范式）
#
# ===== 为什么需要它（不是过度设计）=====
# 子代理 B 在交付模式（project 必须是契约值 ehome-bb）**连续 3 次**被并发打断，且指出：
#   「label 精确匹配解决不了这个问题 —— 我用 -p ehome-bb，D 也用 -p ehome-bb，
#    label 完全相同，D 的清理必然命中我的容器。」
# 主控已独立证实该论断（造一个 label=com.docker.compose.project=ehome-bb 的容器，
# 确实被同一 filter 命中）⇒ 根因是**同 project 并发**，不是命名、不是 label。
# ⇒ 单侧改判据无法消除，**只能串行化**。本文件就是那把锁。
#
# ===== 为什么用 flock 而不是 PID 文件 =====
# 与本仓仿真 harness 同理由：持有者**进程崩溃时内核自动释放**，不会留下永久死锁。
# 锁文件内写 PID 与开始时间，便于诊断「谁持有」。
#
# ===== 用法（探针头部 source 本文件）=====
#   source "$(dirname "$0")/_lock.sh"
#   bb_acquire_lock "$PROJECT" || exit 3
#   trap 'bb_release_lock' EXIT
#
# 行为：
#   - 抢到锁 ⇒ 返回 0，并把 fd 存在 BB_LOCK_FD
#   - 超时未抢到 ⇒ 返回 1（**不自旋等待**，避免多个代理互相饿死）
#   - 锁是 per-project 的：bbdevb / bbdevc / ehome-bb 各自独立，故 dev 之间不互相阻塞
set -u

# 锁等待上限（秒）。默认 0 = 不等待，立即失败。
# 为什么默认不等待：多个代理并发时，等待会造成「都卡住」的局面；
# 快速失败 + 明确报 COLLISION 更利于主控调度。
BB_LOCK_WAIT="${BB_LOCK_WAIT:-0}"

bb_lock_path() {
  local project="$1"
  local base="${BB_LOCK_DIR:-$PWD/.logs/blackbox}"
  mkdir -p "$base" 2>/dev/null || true
  printf '%s/%s.lock' "$base" "$project"
}

# bb_acquire_lock <project> : 0=抢到, 1=超时/被占
bb_acquire_lock() {
  local project="${1:?project required}"
  local lockfile; lockfile="$(bb_lock_path "$project")"
  local waited=0

  # ── 锁继承（2026-09-16 修复的实际缺陷）────────────────────────────────
  # 问题：run.sh 先取得锁，再以子进程方式调用各探针；探针里的 bb_acquire_lock
  # 会**再次尝试抢同一把锁** ⇒ 被自己父进程挡住，03 报 exit=3（首次全量演练实测）。
  # 这不是「并发冲突」，而是**同一逻辑调用链内的重复加锁**。
  #
  # 修法：父进程取得锁后导出 BB_LOCK_HELD；子进程见到它且 project 相符即直接复用，
  # 不再重复加锁。这样「跨调用链互斥」保住，而「链内重复加锁」消除。
  #
  # 安全性：BB_LOCK_HELD 只表示「本进程组的祖先已持有该 project 的锁」，
  # 不会让**无关**进程绕过加锁 —— 它们没有这个环境变量。
  if [ "${BB_LOCK_HELD:-}" = "$project" ]; then
    BB_LOCK_FD=""            # 子进程不持有 fd，故释放时也不 unlock（父进程负责）
    BB_LOCK_FILE="$lockfile"
    BB_LOCK_INHERITED=1
    return 0
  fi

  while :; do
    # 以 200 号 fd 持有 flock（-n 非阻塞）
    # 注意：**不能**写成 `exec 200>"$lockfile" 2>/dev/null` —— exec 不带命令时其重定向
    # **永久生效于整个 shell**，会把后续所有 `>&2` 吞掉。
    # 主控实测（2026-09-16）：该写法导致本函数的「锁被占用」诊断完全不输出，
    # 违反「失败要可诊断」。改为用大括号限定重定向作用域。
    if ! { exec 200>"$lockfile"; } 2>/dev/null; then
      echo "bb: 无法打开锁文件 $lockfile" >&2
      return 1
    fi
    if flock -n 200; then
      BB_LOCK_FD=200
      BB_LOCK_FILE="$lockfile"
      BB_LOCK_INHERITED=0
      # 导出给子进程（run.sh → 探针）：子进程据此跳过重复加锁。
      export BB_LOCK_HELD="$project"
      # 写入持有者信息，便于诊断「谁持有」
      printf 'pid=%s\nproject=%s\nstarted=%s\n' "$$" "$project" "$(date -Iseconds)" > "${lockfile}.holder" 2>/dev/null || true
      return 0
    fi
    if [ "$waited" -ge "$BB_LOCK_WAIT" ]; then
      local holder; holder="$(cat "${lockfile}.holder" 2>/dev/null | tr '\n' ' ' || true)"
      echo "bb: project '$project' 已被占用（同 project 并发会导致互相删容器）" >&2
      echo "bb: 持有者: ${holder:-未知}" >&2
      echo "bb: 锁文件: $lockfile" >&2
      return 1
    fi
    sleep 1; waited=$((waited+1))
  done
}

bb_release_lock() {
  if [ -n "${BB_LOCK_FD:-}" ]; then
    flock -u "$BB_LOCK_FD" 2>/dev/null || true
    eval "exec ${BB_LOCK_FD}>&-" 2>/dev/null || true
    BB_LOCK_FD=""
    [ -n "${BB_LOCK_FILE:-}" ] && rm -f "${BB_LOCK_FILE}.holder" 2>/dev/null || true
  fi
}
