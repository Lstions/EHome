#!/bin/bash
set -e

echo "Checking WS event name consistency..."

BACKEND_DIR="$(cd "$(dirname "$0")/.." && pwd)"

# Extract Go WS event names (BroadcastEvent calls and event constants)
GO_EVENTS=$(cd "$BACKEND_DIR" && grep -rn 'BroadcastEvent\|wsHub\.Broadcast' \
  --include='*.go' | grep -oP 'events\.\w+' | sort -u)

# Extract TS event names (subscribe/onMessage patterns)
# Look for frontend directory relative to backend
FRONTEND_DIR="$(cd "$BACKEND_DIR/../frontend" 2>/dev/null && pwd || true)"

if [ -n "$FRONTEND_DIR" ] && [ -d "$FRONTEND_DIR" ]; then
  TS_EVENTS=$(cd "$FRONTEND_DIR" && grep -rn 'subscribe\|onMessage\|\.on(' \
    --include='*.ts' --include='*.tsx' --include='*.vue' | \
    grep -oP '"\w+"' | tr -d '"' | sort -u)
else
  echo "  No frontend directory found, skipping TS check"
  TS_EVENTS=""
fi

# Extract Go event constant definitions (format: Name = "string_value")
GO_CONSTS=$(cd "$BACKEND_DIR" && grep -rP '^\s*\w+\s*=\s*"' \
  internal/events/ --include='*.go' -h | sed 's/^\s*//' | cut -d= -f1 | tr -d ' \t' | sort -u)

echo "  Go event constants: $(echo "$GO_CONSTS" | grep -c '^' || true)"
echo "  Go BroadcastEvent calls: $(echo "$GO_EVENTS" | grep -c '^' || true)"
if [ -n "$TS_EVENTS" ]; then
  echo "  TS subscribe events: $(echo "$TS_EVENTS" | wc -l)"
fi

# Verify that every BroadcastEvent call references a defined constant
MISSING=0
for ev in $GO_EVENTS; do
  CONST_NAME=$(echo "$ev" | sed 's/events\.//')
  if ! echo "$GO_CONSTS" | grep -q "^${CONST_NAME}$"; then
    echo "  WARN: BroadcastEvent uses '$ev' but no matching constant in events package"
    MISSING=$((MISSING + 1))
  fi
done

if [ "$MISSING" -gt 0 ]; then
  echo "FAIL: $MISSING event name(s) inconsistent"
  exit 1
fi

echo "PASS: WS event names consistent"
