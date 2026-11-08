#!/usr/bin/env bash
# gow.sh — 在 golang:1.27.3 容器里跑 go 命令（本机没有 go）。
# 用法：GOW_WORKDIR=backend scripts/gow.sh test ./internal/api/
# 会转发所有 EHOME_* 环境变量进容器。
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WD="${GOW_WORKDIR:-backend}"
ARGS=()
if [ $# -eq 0 ]; then
  echo "usage: GOW_WORKDIR=backend $0 <go-args...>" >&2
  exit 2
fi
ENVS=()
for v in $(env | grep -E '^EHOME_' | cut -d= -f1); do
  ENVS+=("-e" "$v=${!v}")
done
if [ ${#ENVS[@]} -gt 0 ]; then
  echo "[gow] 转发 ${#ENVS[@]} 个 EHOME_* 变量进容器" >&2
fi
exec docker run --rm \
  --network host \
  -v "$ROOT:/ws" -w "/ws/$WD" \
  "${ENVS[@]}" \
  golang:1.27.3 go "$@"
