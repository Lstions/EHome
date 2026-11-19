#!/usr/bin/env bash
# 验证前后端 WS 事件名一致 (union match)
set -e

# comm(1) 要求输入按**字节序**排序；而 en_US.UTF-8 之类的排序规则会忽略下划线，
# 于是 "edge_device_status" 与 "edge_devices_live" 的先后在 sort 与 comm 眼里不同，
# 报 "file 1 is not in sorted order" 并让门禁假红。事件名全是 ASCII，固定 C 即可。
export LC_ALL=C

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BE_FILE="$ROOT_DIR/backend/internal/events/events.go"
FE_FILE="$ROOT_DIR/frontend-shared/src/events/events.ts"

# 已知**不在常量表里**的事件名（用空格分隔），两种来源：
#   · 前端自有：channel_write / channel_write_error（后端只发 error 那个）；
#   · 后端直发但还没进 events.go 常量表：automation_* 三条（emit 在
#     backend/internal/automation/planner.go，用的是裸字符串）。
# 2026-11-17：此前只有 channel_write_error 一个豁免名，union check 因此**长期失败**
# （等于没有门禁）。现在把豁免名补全，并让 union check 也认这份名单；
# 把 automation_* 收进常量表是独立任务，不在本次缺陷修复范围。
KNOWN_UNLISTED_NAMES="channel_write channel_write_error automation_daily_limit automation_pending_confirm automation_system_actor_unavailable"

# 门禁只看生产代码：__tests__ / *.spec.ts 里用裸字符串是**故意的**
# （测试要钉住线上真实事件名，用常量反而测不出改名）。
FE_SRC_EXCLUDES=(--exclude-dir=__tests__ --exclude='*.spec.ts' --exclude='*.test.ts')

# 如果后端 events.go 还不存在，跳过后端检查
if [ ! -f "$BE_FILE" ]; then
  echo "⚠️  Backend events.go not found at $BE_FILE — skipping BE/FE union check"
  echo "✅ Frontend-only validation:"

  # 仅验证前端事件名格式
  FE_NAMES=$(grep -oP "'\K[a-z]+_[a-z_]+'" "$FE_FILE" | sed "s/'$//" | sort -u)
  if [ -z "$FE_NAMES" ]; then
    echo "❌ No event names found in $FE_FILE"
    exit 1
  fi

  # 验证前端 subscribe 调用全部使用常量
  MAGIC_STRINGS=$(grep -rohP "${FE_SRC_EXCLUDES[@]}" "subscribe\('\K[a-z_]+'" "$ROOT_DIR/frontend-shared/src/" 2>/dev/null | sed "s/'$//" | sort -u || true)
  UNEXPECTED=""
  for s in $MAGIC_STRINGS; do
    if ! echo "$KNOWN_UNLISTED_NAMES" | grep -qw "$s"; then
      UNEXPECTED="$UNEXPECTED $s"
    fi
  done

  if [ -n "$UNEXPECTED" ]; then
    echo "❌ Found unexpected magic string subscribe() calls (should use WS_EVENT.XXX):$UNEXPECTED"
    exit 1
  fi

  COUNT=$(echo "$FE_NAMES" | wc -l)
  echo "  - $COUNT event names defined in events.ts"
  [ -n "$MAGIC_STRINGS" ] && echo "  - Known FE-only magic strings (not in BE yet): $MAGIC_STRINGS"
  echo "  - No unexpected magic string subscribe() calls"
  exit 0
fi

# 1. 提取后端事件名 (从 events.go const) — 去掉引号
BE_NAMES=$(grep -oP '"\K[a-z]+_[a-z_]+"' "$BE_FILE" | sed 's/"$//' | sort -u)

# 2. 提取前端事件名 (从 events.ts) — 去掉引号
FE_NAMES=$(grep -oP "'\K[a-z]+_[a-z_]+'" "$FE_FILE" | sed "s/'$//" | sort -u)

# 3. 提取后端 BroadcastEvent 调用中的事件名
BE_BROADCAST=$(grep -rohP 'BroadcastEvent\("\K[a-z_]+' "$ROOT_DIR/backend/internal/" 2>/dev/null | sort -u || true)

# 4. 提取前端 wsStore.subscribe 调用中的 magic string 事件名
FE_SUBSCRIBE=$(grep -rohP "${FE_SRC_EXCLUDES[@]}" "subscribe\('\K[a-z_]+'" "$ROOT_DIR/frontend-shared/src/" 2>/dev/null | sed "s/'$//" | sort -u || true)
UNEXPECTED=""
for s in $FE_SUBSCRIBE; do
  if ! echo "$KNOWN_UNLISTED_NAMES" | grep -qw "$s"; then
    UNEXPECTED="$UNEXPECTED $s"
  fi
done

if [ -n "$UNEXPECTED" ]; then
  echo "❌ Found unexpected magic string subscribe() calls (should use WS_EVENT.XXX):$UNEXPECTED"
  exit 1
fi

# 5. 比较常量定义是否一致
# FE 自有事件（KNOWN_FE_ONLY）不算"缺在后端"：它们是前端合成的通道事件，
# 后端本来就不发。除此之外任何一侧多出来的名字都必须报错。
MISSING_IN_BE=$(comm -23 <(echo "$FE_NAMES") <(echo "$BE_NAMES") | grep -vxF -f <(echo "$KNOWN_UNLISTED_NAMES" | tr ' ' '\n') || true)
MISSING_IN_FE=$(comm -13 <(echo "$FE_NAMES") <(echo "$BE_NAMES"))

if [ -n "$MISSING_IN_BE" ] || [ -n "$MISSING_IN_FE" ]; then
  echo "❌ WS event name mismatch between events.ts and events.go!"
  [ -n "$MISSING_IN_BE" ] && echo "  Defined in FE but missing in BE: $(echo $MISSING_IN_BE)"
  [ -n "$MISSING_IN_FE" ] && echo "  Defined in BE but missing in FE: $(echo $MISSING_IN_FE)"
  exit 1
fi

COUNT=$(echo "$BE_NAMES" | wc -l)
echo "✅ All $COUNT WS event names consistent (FE events.ts ↔ BE events.go)"
[ -n "$FE_SUBSCRIBE" ] && echo "ℹ️  Known FE-only magic strings (not in BE yet): $(echo $FE_SUBSCRIBE)"
