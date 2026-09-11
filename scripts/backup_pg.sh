#!/usr/bin/env bash
# ============================================================
# EHomeSystem PostgreSQL 备份脚本
# 用法: bash scripts/backup_pg.sh   （或 make backup）
# 环境变量:
#   POSTGRES_USER / POSTGRES_DB / COMPOSE_FILE / BACKUP_DIR / BACKUP_KEEP
# 流程: pg_dump(custom) -> TOC 自校验 -> sha256 -> meta -> 保留策略清理
# 任一步失败立即非零退出（set -euo pipefail）。
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

POSTGRES_USER="${POSTGRES_USER:-ehome}"
POSTGRES_DB="${POSTGRES_DB:-ehome}"
BACKUP_DIR="${BACKUP_DIR:-./backups}"
BACKUP_KEEP="${BACKUP_KEEP:-7}"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.yml}"
COMPOSE="docker compose -f $COMPOSE_FILE"

log() { echo "[备份] $*"; }
die() { echo "[备份][错误] $*" >&2; exit 1; }

[[ "$BACKUP_KEEP" =~ ^[0-9]+$ ]] || die "BACKUP_KEEP 必须是非负整数（当前: $BACKUP_KEEP）"

mkdir -p "$BACKUP_DIR"
STAMP="$(date +%Y%m%d_%H%M%S)"
DUMP="$BACKUP_DIR/ehome_${STAMP}.dump"
META="$DUMP.meta"
SHA="$DUMP.sha256"

# ---- 1. 容器内 pg 版本（写入 meta，并顺带探活）----
log "获取容器内 pg_dump 版本..."
PGVER="$($COMPOSE exec -T postgres pg_dump --version | head -n1)" || die "无法连接 postgres 容器（docker compose exec 失败）"
log "版本: $PGVER"

# ---- 2. pg_dump（custom 格式 + gzip 压缩 level 6，生产库只读，不写任何数据）----
log "开始备份数据库 '$POSTGRES_DB' -> $DUMP"
START_TS=$(date +%s)
$COMPOSE exec -T postgres pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" \
    --format=custom --compress=6 > "$DUMP" || { rm -f "$DUMP"; die "pg_dump 失败（可能磁盘不足或连接中断），半成品已删除"; }
ELAPSED=$(( $(date +%s) - START_TS ))
SIZE_BYTES=$(stat -c %s "$DUMP")
log "dump 完成，耗时 ${ELAPSED}s，大小 ${SIZE_BYTES} 字节"

# ---- 3. 自校验：pg_restore --list 能列出 TOC（防截断文件被当成好备份）----
log "自校验 TOC 可读性（pg_restore --list）..."
TOC_COUNT="$($COMPOSE exec -T postgres pg_restore --list < "$DUMP" | grep -cE '^[0-9]+;' || true)" \
    || die "TOC 校验命令失败"
[[ "$TOC_COUNT" =~ ^[0-9]+$ && "$TOC_COUNT" -gt 0 ]] \
    || { rm -f "$DUMP"; die "TOC 无法读取或条目数为 0，dump 可能损坏，已删除 $DUMP"; }
log "TOC 自校验通过：${TOC_COUNT} 个条目"

# ---- 4. sha256 校验文件 ----
( cd "$BACKUP_DIR" && sha256sum "$(basename "$DUMP")" > "$(basename "$SHA")" )
SHA_VALUE="$(awk '{print $1}' "$SHA")"
log "sha256 已写入 $SHA"

# ---- 5. meta 文件 ----
cat > "$META" <<EOF
created_at: $(date '+%Y-%m-%d %H:%M:%S %z')
source_database: ${POSTGRES_DB}
user: ${POSTGRES_USER}
file: $(basename "$DUMP")
size_bytes: ${SIZE_BYTES}
duration_seconds: ${ELAPSED}
pg_dump_version: ${PGVER}
toc_entries: ${TOC_COUNT}
sha256: ${SHA_VALUE}
EOF
log "meta 已写入 $META"

# ---- 6. 保留策略：只保留最近 BACKUP_KEEP 个（按 mtime），删前打印 ----
mapfile -t OLD < <(ls -1t "$BACKUP_DIR"/ehome_*.dump 2>/dev/null | tail -n +$((BACKUP_KEEP + 1)) || true)
if ((${#OLD[@]} > 0)); then
    log "保留策略 BACKUP_KEEP=${BACKUP_KEEP}，以下旧备份将被删除："
    for f in "${OLD[@]}"; do
        echo "  - $f"
        rm -f "$f" "$f.meta" "$f.sha256"
    done
else
    log "保留策略：当前 $(ls -1 "$BACKUP_DIR"/ehome_*.dump 2>/dev/null | wc -l) 个备份，未超过 ${BACKUP_KEEP} 个，无需清理"
fi

log "✅ 备份成功: $DUMP（${ELAPSED}s, ${SIZE_BYTES} 字节, TOC ${TOC_COUNT} 条）"
