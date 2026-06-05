#!/bin/bash
set -e

echo "Checking protocol field definitions..."

BACKEND_DIR="$(cd "$(dirname "$0")/.." && pwd)"
FRAME_FILE="$BACKEND_DIR/pkg/frame/frame.go"

if [ ! -f "$FRAME_FILE" ]; then
  echo "FAIL: frame.go not found at $FRAME_FILE"
  exit 1
fi

# Expected message types per design doc §3.3
EXPECTED_TYPES=(
  "0x01:MsgHello"
  "0x02:MsgStatusRpt"
  "0x03:MsgDataRpt"
  "0x04:MsgConfigMfst"
  "0x05:MsgConfigRslt"
  "0x06:MsgWriteCmd"
  "0x07:MsgWriteRsp"
  "0x08:MsgPing"
  "0x09:MsgPong"
  "0x0A:MsgOtaCmd"
  "0x0B:MsgOtaProg"
  "0x0C:MsgScanRpt"
  "0x0D:MsgScanReq"
  "0x0E:MsgQueryReq"
  "0x0F:MsgQueryRsp"
  "0x10:MsgConfigQuery"
  "0x11:MsgConfigReport"
  "0x12:MsgHelloAck"
  "0x13:MsgConfigSyncReq"
  "0x14:MsgConfigSyncRsp"
  "0x18:MsgPongAck"
)

MISSING=0
for entry in "${EXPECTED_TYPES[@]}"; do
  HEX_VAL="${entry%%:*}"
  CONST_NAME="${entry##*:}"
  if ! grep -qP "^\s*${CONST_NAME}\s*=\s*${HEX_VAL}" "$FRAME_FILE"; then
    echo "  MISSING: $CONST_NAME = $HEX_VAL"
    MISSING=$((MISSING + 1))
  fi
done

# Check MsgTypeName covers all defined constants
DEFINED_CONSTS=$(grep -oP 'Msg\w+\s*=\s*0x[0-9A-Fa-f]+' "$FRAME_FILE" | wc -l)
NAMED_TYPES=$(grep -A100 'MsgTypeName' "$FRAME_FILE" | grep -oP '0x[0-9A-Fa-f]+.*:"' | wc -l)

echo "  Defined constants: $DEFINED_CONSTS"
echo "  Named in MsgTypeName: $NAMED_TYPES"

if [ "$MISSING" -gt 0 ]; then
  echo "FAIL: $MISSING protocol field(s) missing from frame.go"
  exit 1
fi

echo "PASS: Protocol fields verified"
