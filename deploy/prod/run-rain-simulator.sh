#!/usr/bin/env bash
# CP2102 (/dev/ttyUSB0) <-> ESP32 UART0 台架从站模拟器：嘉佰达 BMS + SN-3001 雨量计。
#
# 为什么需要它：台架上的 ESP32 UART0(GPIO16/17) 经 CP2102 接到本机 USB，
# "从站"由本脚本扮演。**它不跑，ESP32 每次轮询都收不到应答**，DataReport 里
# error_code=1、raw=""，UI 上表现为设备在线但一直没有读数。
#
# 用法（受管后台任务，不要用 nohup）：
#   ./deploy/prod/run-rain-simulator.sh
# 参数可调：
#   RAIN_MM=1.2 BMS_SOC=60 PORT=/dev/ttyUSB0 ./deploy/prod/run-rain-simulator.sh
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PORT="${PORT:-/dev/ttyUSB0}"
BAUD="${BAUD:-9600}"
RAIN_MM="${RAIN_MM:-0.5}"
BMS_SOC="${BMS_SOC:-80}"
DURATION="${DURATION:-0}"   # 0 = 一直运行

if [ ! -e "$PORT" ]; then
  echo "错误: 串口 $PORT 不存在。可用串口:" >&2
  ls /dev/ttyUSB* /dev/ttyACM* 2>/dev/null >&2 || true
  exit 1
fi

if fuser "$PORT" >/dev/null 2>&1; then
  echo "警告: $PORT 已被占用（可能已有模拟器在跑，重复启动会互相抢字节）:" >&2
  fuser -v "$PORT" >&2 || true
  exit 1
fi

echo "==> 台架从站模拟器: port=$PORT baud=$BAUD rain=${RAIN_MM}mm soc=${BMS_SOC}%"
exec python3 "$ROOT/scripts/uart0_bms_rain_simulator.py" \
  --port "$PORT" --baud "$BAUD" --rain-mm "$RAIN_MM" --bms-soc "$BMS_SOC" --duration "$DURATION"
