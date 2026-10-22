#!/usr/bin/env bash
# 校验 EHome 镜像是否由**当前主分支代码**构建（防止用旧代码树构建后部署）。
#
# 背景（2026-10-21 事故）：某次部署的镜像 ehome-new:ui-fix13 由一份旧代码树构建
# （不含已推送到 main 的解析器修正）。部署后生产温度语义回退：8 字段温度帧又被写成
# pv2_temp / dc_rectifier_temp（风扇转速被当温度）。6 分钟后发现并回滚。
# 根因不是代码，而是"部署了不是从 main 构建的产物"。
#
# 用法：
#   tools/deploy/verify_image.sh ehome-new:inverter-fix2           # 本机镜像
#   tools/deploy/verify_image.sh ehome-new:xxx --remote 192.168.20.6
#   tools/deploy/verify_image.sh ehome-new:xxx --require another-marker
#
# 退出码：0 = 全部标记存在；1 = 缺标记（不要部署）；2 = 用法/环境错误。

set -uo pipefail

IMAGE="${1:-}"
[ -z "$IMAGE" ] && { echo "用法: $0 <image> [--remote <host>] [--require <marker>]" >&2; exit 2; }
shift

REMOTE=""
EXTRA=()
while [ $# -gt 0 ]; do
    case "$1" in
        --remote)  REMOTE="${2:-}"; shift 2 ;;
        --require) EXTRA+=("${2:-}"); shift 2 ;;
        *) echo "未知参数: $1" >&2; exit 2 ;;
    esac
done

# 每条标记对应一个**已合并到 main 的关键修复**。新增关键修复时在此追加，
# "旧树构建"的镜像就会在部署前被拦下，而不是上线后靠数据发现。
MARKERS=(
    "techfineCommandByWire"   # 解析器命令注册表（单一真相源 + fail-closed）
    "or exactly 10 ("         # HTEMP 协议 V0.5 §5.8 的 10 字段形状
    "non-binary char"         # HBMS1 状态串必须是 0/1
    "bms_temp_high"           # HBMS1 c7 = BMS 温度过高
    "leadingFixedDigits"      # HTEMP 里 KKK 与 O 粘连的定宽解析
    # ── 2026-10-22 来源组(source-group) 批次 ────────────────────────────
    # 注意：标记必须是**会进二进制的字符串字面量**（值或日志文本），不能是标识符 —
    # Go 编译后常量名/函数名通常被消除（实测 SourceGroupKindPrimaryBackup=0 命中），
    # 用标识符做标记会让门禁对"任何镜像"都报缺失（假红）。
    "cooldown_suppressed"           # R10 冷却抑制也发 ActiveChange{Next:0}（架构终审 B-1）
    "no_active_after_cooldown"      # 组失去权威的降级健康事件
    "source_frame_discarded"        # 丢帧观测（指标 + 健康事件）
    "no_available_member"           # 复合组缺员 unbound 的原因码
    "primary_backup"                # 来源组主备 kind 的字面量（4 表 + 状态机）
)
for m in "${EXTRA[@]:-}"; do
    [ -n "$m" ] && MARKERS+=("$m")
done

# 在镜像内跑：strings 找标记，缺的打印 MISSING:<marker>。
# 把标记作为 "$@" 传进去（sh -c SCRIPT sh ARG1 ARG2 ...），避免三层引号嵌套。
PROBE='for s in "$@"; do c=$(strings /app/ehome-server | grep -cF "$s"); [ "$c" -gt 0 ] || echo "MISSING:$s"; done'

if [ -n "$REMOTE" ]; then
    QMARKERS=""
    for m in "${MARKERS[@]}"; do QMARKERS="$QMARKERS $(printf '%q' "$m")"; done
    OUT=$(ssh "$REMOTE" "docker run --rm --entrypoint sh $IMAGE -c $(printf '%q' "$PROBE") sh$QMARKERS" 2>&1)
else
    OUT=$(docker run --rm --entrypoint sh "$IMAGE" -c "$PROBE" sh "${MARKERS[@]}" 2>&1)
fi

if echo "$OUT" | grep -q 'MISSING:'; then
    echo "镜像 $IMAGE 不是当前主分支构建 —— 缺少以下代码标记：" >&2
    echo "$OUT" | grep '^MISSING:' | sed 's/^MISSING:/  - /' >&2
    echo "" >&2
    echo "不要部署该镜像。请从 main 的干净树重建：" >&2
    echo "  git archive <sha> | tar -x -C .deploy-src" >&2
    echo "  docker build -t ehome-new:<tag> .deploy-src" >&2
    exit 1
fi

echo "镜像 $IMAGE 通过校验（${#MARKERS[@]} 个代码标记全部存在）"
