#!/usr/bin/env bash
# ============================================================
# EHomeSystem PostgreSQL 恢复脚本
# 用法: bash scripts/restore_pg.sh <dump_file> <target_db> [--drop] [--force]
#   --drop   允许重建（先删后建）已存在的目标库
#   --force  仅用于目标库为生产库（POSTGRES_DB，默认 ehome）时的显式确认
# 环境变量: POSTGRES_USER / POSTGRES_DB / COMPOSE_FILE
# 安全护栏: 目标为生产库 ehome 且未传 --force 时直接拒绝，恢复演练请使用临时库名。
# ============================================================
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
cd "$ROOT_DIR"

POSTGRES_USER="${POSTGRES_USER:-ehome}"
POSTGRES_DB="${POSTGRES_DB:-ehome}"
COMPOSE_FILE="${COMPOSE_FILE:-$ROOT_DIR/docker-compose.yml}"
COMPOSE="docker compose -f $COMPOSE_FILE"

log() { echo "[恢复] $*"; }
die() { echo "[恢复][错误] $*" >&2; exit 1; }

DUMP="" TARGET_DB="" DROP=0 FORCE=0
while (($#)); do
    case "$1" in
        --drop)  DROP=1 ;;
        --force) FORCE=1 ;;
        -*)      die "未知选项: $1（支持 --drop / --force）" ;;
        *)
            [[ -z "$DUMP" ]] && DUMP="$1" \
                || { [[ -z "$TARGET_DB" ]] && TARGET_DB="$1" || die "多余的参数: $1"; }
            ;;
    esac
    shift
done

if [[ -z "$DUMP" || -z "$TARGET_DB" ]]; then
    cat >&2 <<EOF
[恢复][错误] 用法: bash scripts/restore_pg.sh <dump_file> <target_db> [--drop] [--force]
  恢复演练请指定临时库名（如 ehome_restore_drill），不要指向生产库。
EOF
    exit 1
fi
[[ -f "$DUMP" ]] || die "找不到 dump 文件: $DUMP"
[[ "$TARGET_DB" =~ ^[A-Za-z_][A-Za-z0-9_]*$ ]] || die "目标库名不合法: $TARGET_DB"

# ---- 安全护栏：生产库目标必须 --force ----
if [[ "$TARGET_DB" == "$POSTGRES_DB" ]]; then
    if [[ "$FORCE" != "1" ]]; then
        die "目标库 '$TARGET_DB' 是生产库！恢复将覆盖生产数据。确认无误请加 --force；日常演练请使用临时库（如 ehome_restore_drill）。"
    fi
    echo "[恢复][警告] ⚠️⚠️ 你已指定 --force，即将覆盖生产库 '$TARGET_DB' ⚠️⚠️" >&2
else
    log "目标库为 '$TARGET_DB'（非生产库），本次为恢复演练，生产库未受影响。"
fi

# ---- 校验 dump 文件本身可读 ----
TOC_COUNT="$($COMPOSE exec -T postgres pg_restore --list < "$DUMP" | grep -cE '^[0-9]+;' || true)"
[[ "$TOC_COUNT" =~ ^[0-9]+$ && "$TOC_COUNT" -gt 0 ]] || die "dump 文件 TOC 无法读取，拒绝恢复: $DUMP"
log "dump 可读，TOC 条目数: $TOC_COUNT"

EXISTS="$($COMPOSE exec -T postgres psql -U "$POSTGRES_USER" -d postgres -tAc \
    "SELECT 1 FROM pg_database WHERE datname = '$TARGET_DB'" || true)"

if [[ "$EXISTS" == "1" ]]; then
    if [[ "$DROP" == "1" ]]; then
        [[ "$TARGET_DB" != "$POSTGRES_DB" || "$FORCE" == "1" ]] || die "拒绝 --drop 生产库"
        log "目标库已存在且指定 --drop，正在删除重建 $TARGET_DB ..."
        $COMPOSE exec -T postgres psql -U "$POSTGRES_USER" -d postgres -c \
            "DROP DATABASE IF EXISTS \"$TARGET_DB\" WITH (FORCE);" || die "DROP DATABASE 失败"
        $COMPOSE exec -T postgres createdb -U "$POSTGRES_USER" "$TARGET_DB" || die "CREATE DATABASE 失败"
    elif [[ "$TARGET_DB" == "$POSTGRES_DB" ]]; then
        log "恢复进已存在的生产库（--force 已确认），pg_restore 将在现有对象上执行，如遇对象冲突请改用 --drop。"
    else
        die "目标库 '$TARGET_DB' 已存在。如需重建请加 --drop（仅对非生产库安全）或换一个临时库名。"
    fi
else
    log "目标库不存在，创建 $TARGET_DB ..."
    $COMPOSE exec -T postgres createdb -U "$POSTGRES_USER" "$TARGET_DB" || die "CREATE DATABASE 失败"
fi

# ---- 恢复 ----
log "开始 pg_restore -> $TARGET_DB（--no-owner --no-privileges）..."
set +e
$COMPOSE exec -T postgres pg_restore -U "$POSTGRES_USER" -d "$TARGET_DB" \
    --no-owner --no-privileges < "$DUMP"
RESTORE_RC=$?
set -e
if [[ $RESTORE_RC -ne 0 ]]; then
    log "⚠️ pg_restore 退出码 $RESTORE_RC（通常是个别对象已存在的告警），请核对下方统计。"
else
    log "pg_restore 退出码 0"
fi

# ---- 恢复后核对统计（与生产基准对照：46 表 / 30 分区片 / 188 索引）----
COUNTS="$($COMPOSE exec -T postgres psql -U "$POSTGRES_USER" -d "$TARGET_DB" -tA -F'|' <<'SQL'
SELECT
  (SELECT count(*) FROM pg_tables WHERE schemaname='public'),
  (SELECT count(*) FROM pg_inherits i
      JOIN pg_class c ON c.oid = i.inhrelid
      JOIN pg_namespace n ON n.oid = c.relnamespace
      WHERE n.nspname = 'public'),
  (SELECT count(*) FROM pg_indexes WHERE schemaname='public');
SQL
)" || die "恢复后统计查询失败（目标库可能不完整）"
TABLES="${COUNTS%%|*}"; REST="${COUNTS#*|}"; PARTS="${REST%%|*}"; IDXS="${REST#*|}"

echo ""
log "恢复后统计（public schema）："
log "  表数量   : $TABLES（生产基准对照: 46）"
log "  分区片数 : $PARTS（生产基准对照: 30）"
log "  索引数   : $IDXS（生产基准对照: 188）"
if [[ "$TARGET_DB" != "$POSTGRES_DB" ]]; then
    log "✅ 恢复演练完成，生产库 '$POSTGRES_DB' 全程未受影响。"
    log "   演练结束后可清理临时库: docker exec ehome-postgres psql -U $POSTGRES_USER -d postgres -c \"DROP DATABASE $TARGET_DB;\""
fi
