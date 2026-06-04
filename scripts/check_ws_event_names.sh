#!/usr/bin/env bash
# 验证前后端 WS 事件名一致 (union match)
set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

BE_FILE="$ROOT_DIR/backend/internal/events/events.go"
FE_FILE="$ROOT_DIR/frontend-shared/src/events/events.ts"

# 已知仅前端使用、后端尚未定义的事件名 (用空格分隔)
KNOWN_FE_ONLY="channel_write_error"

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
  MAGIC_STRINGS=$(grep -rohP "subscribe\('\K[a-z_]+'" "$ROOT_DIR/frontend-shared/src/" 2>/dev/null | sed "s/'$//" | sort -u || true)
  UNEXPECTED=""
  for s in $MAGIC_STRINGS; do
    if ! echo "$KNOWN_FE_ONLY" | grep -qw "$s"; then
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
FE_SUBSCRIBE=$(grep -rohP "subscribe\('\K[a-z_]+'" "$ROOT_DIR/frontend-shared/src/" 2>/dev/null | sed "s/'$//" | sort -u || true)
UNEXPECTED=""
for s in $FE_SUBSCRIBE; do
  if ! echo "$KNOWN_FE_ONLY" | grep -qw "$s"; then
    UNEXPECTED="$UNEXPECTED $s"
  fi
done

if [ -n "$UNEXPECTED" ]; then
  echo "❌ Found unexpected magic string subscribe() calls (should use WS_EVENT.XXX):$UNEXPECTED"
  exit 1
fi

# 5. 比较常量定义是否一致
MISSING_IN_BE=$(comm -23 <(echo "$FE_NAMES") <(echo "$BE_NAMES"))
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
