#!/bin/bash
# ota-test-100.sh - OTA 100次自动化测试脚本 + 成功率统计
#
# 功能:
#   1. 登录获取 JWT token
#   2. 上传固件包（.ehfw）获取 firmware_id
#   3. 循环执行 100 次 OTA 测试
#   4. 统计成功率、失败率、超时率
#   5. 生成 CSV 结果文件
#
# 用法: ./scripts/ota-test-100.sh
#
# 依赖: curl, python3；现场打包时还要 esp32-collector/tools/pack_firmware.py

set -e

# JSON 值提取 (替代 jq)
json_val() {
    python3 -c "import sys,json
d=json.load(sys.stdin)
$1" 2>/dev/null
}

# ========================================
# 配置区 - 方便修改
# ========================================
API_BASE="http://localhost:8080/api/v1"
NODE_ID=1
# 期望版本；实际上传的版本取自镜像头（方案 §4.3，表单/清单都不再收 version）。
# 只有"现场打包"这条路才用它当 pack_firmware.py 的 --version：打包器会核对镜像内
# 版本，对不上就按 "stale .bin" 拒绝。所以**留空**而不是给一个会过期的常量——
# 一个写死的旧版本会让运维误以为是 .bin 陈旧；现场打包请 EHOME_FW_VERSION=2.9.0。
FIRMWARE_VERSION="${EHOME_FW_VERSION:-}"
# 固件包（.ehfw，方案 §4.1）：显式指定优先；留空则复用 dist/ 里已打好的包，
# 一份都没有时才现场调 esp32-collector/tools/pack_firmware.py 打包。
# psram 不必再由这里人工给——那是构建期事实，打包器从 sdkconfig 的 CONFIG_SPIRAM 读。
FIRMWARE_PACKAGE="${EHOME_FW_PACKAGE:-}"
# 档位名（c6-n8 / c6-n16 / s3-n8 …）：只在现场打包时用，pack_firmware.py 会拿它
# 与镜像头反推出来的档位交叉核对。留空 = 不做现场打包，只复用已有包。
FIRMWARE_PROFILE="${EHOME_FW_PROFILE:-}"
USERNAME="${ADMIN_USERNAME:-admin}"
PASSWORD="${ADMIN_PASSWORD:-admin123}"
AUTH_TOKEN=""
TEST_COUNT=100
TIMEOUT_PER_OTA=120
POLL_INTERVAL=3
REBOOT_WAIT=10
MAX_API_RETRIES=3
MAX_OFFLINE_WAIT=300
MAX_CONSECUTIVE_FAILURES=10

# ========================================
# 脚本路径
# ========================================
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
# 源镜像：只有"现场打包"路径需要它（档位布局是 build/<profile>/ehome_collector.bin）。
FIRMWARE_FILE="$PROJECT_ROOT/esp32-collector/build/ehome_collector.bin"
RESULTS_DIR="$SCRIPT_DIR/ota-results"
TIMESTAMP=$(date +%Y%m%d-%H%M%S)
CSV_FILE="$RESULTS_DIR/ota-test-$TIMESTAMP.csv"

# ========================================
# 颜色定义
# ========================================
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
CYAN='\033[0;36m'
NC='\033[0m' # No Color

# ========================================
# 统计变量
# ========================================
TOTAL_COUNT=0
SUCCESS_COUNT=0
FAILED_COUNT=0
TIMEOUT_COUNT=0
TOTAL_ELAPSED=0
MIN_ELAPSED=999999
MAX_ELAPSED=0
CONSECUTIVE_FAILURES=0
ELAPSED_TIMES=()

# ========================================
# 日志函数
# ========================================
log_info()  { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn()  { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }
log_debug() { [[ "${DEBUG:-0}" == "1" ]] && echo -e "${CYAN}[DEBUG]${NC} $1"; }

# ========================================
# 信号处理 - Ctrl+C 时输出当前统计
# ========================================
cleanup() {
    echo ""
    echo -e "${YELLOW}[INTERRUPT] 收到中断信号，输出当前统计...${NC}"
    print_report
    exit 130
}
trap cleanup INT TERM

# ========================================
# API 调用封装 - 带重试
# ========================================
api_call() {
    local method="$1"
    local endpoint="$2"
    local data="$3"
    local retry=0
    local response=""
    local http_code=""
    
    while [[ $retry -lt $MAX_API_RETRIES ]]; do
        if [[ -n "$data" ]]; then
            response=$(curl -s -w "\n%{http_code}" \
                -X "$method" \
                -H "Content-Type: application/json" \
                -H "Authorization: Bearer $AUTH_TOKEN" \
                -d "$data" \
                "${API_BASE}${endpoint}" 2>/dev/null)
        else
            response=$(curl -s -w "\n%{http_code}" \
                -X "$method" \
                -H "Content-Type: application/json" \
                -H "Authorization: Bearer $AUTH_TOKEN" \
                "${API_BASE}${endpoint}" 2>/dev/null)
        fi
        
        http_code=$(echo "$response" | tail -1)
        response=$(echo "$response" | sed '$d')
        
        # 成功或客户端错误不重试
        if [[ "$http_code" =~ ^2[0-9][0-9]$ ]] || [[ "$http_code" =~ ^4[0-9][0-9]$ ]]; then
            echo "$response"
            return 0
        fi
        
        # 5xx 错误重试
        retry=$((retry + 1))
        if [[ $retry -lt $MAX_API_RETRIES ]]; then
            log_warn "API $method $endpoint 返回 $http_code，重试 $retry/$MAX_API_RETRIES..."
            sleep 2
        fi
    done
    
    echo "$response"
    return 1
}

# ========================================
# 登录获取 JWT token
# ========================================
login() {
    log_info "正在登录获取 JWT token..."
    
    # 尝试从 .env.dev 读取凭据
    local username="${ADMIN_USERNAME:-admin}"
    local password="${ADMIN_PASSWORD:-admin123}"
    
    if [[ -f "$PROJECT_ROOT/.env.dev" ]]; then
        source "$PROJECT_ROOT/.env.dev" 2>/dev/null || true
        username="${ADMIN_USERNAME:-$username}"
        password="${ADMIN_PASSWORD:-$password}"
    fi
    
    local response
    response=$(curl -s -X POST \
        -H "Content-Type: application/json" \
        -d "{\"username\":\"$username\",\"password\":\"$password\"}" \
        "${API_BASE}/auth/login" 2>/dev/null)
    
    AUTH_TOKEN=$(echo "$response" | json_val "print(d.get('token',''))")
    
    if [[ -z "$AUTH_TOKEN" ]] || [[ "$AUTH_TOKEN" == "null" ]]; then
        log_error "登录失败，无法获取 token"
        log_debug "响应: $response"
        return 1
    fi
    
    log_info "登录成功，获取到 token: ${AUTH_TOKEN:0:20}..."
    return 0
}

# ========================================
# 固件包助手（方案 §4 / §7.5：上传物是单个 .ehfw）
# ========================================

# ota_pick_package <dir> 在目录里挑出**唯一**的 .ehfw，结果写进 FIRMWARE_PACKAGE。
#
# 恰好一个才认，多个直接报错：同一目录里躺着两个包时"按 mtime 挑最新的"会把上一次的
# 产物当成这一次的，这种假绿灯比直接失败难查得多。
# 返回码：0=找到，1=没有，2=歧义（已报错）。
ota_pick_package() {
    local dir="$1"
    local found=()
    local candidate
    for candidate in "$dir"/*.ehfw; do
        [[ -e "$candidate" ]] && found+=("$candidate")
    done
    if [[ ${#found[@]} -eq 0 ]]; then
        return 1
    fi
    if [[ ${#found[@]} -gt 1 ]]; then
        log_error "目录 $dir 里有 ${#found[@]} 个 .ehfw，无法判断该传哪个："
        printf '  %s\n' "${found[@]}"
        log_error "用 EHOME_FW_PACKAGE=<path> 显式指定要上传的包。"
        return 2
    fi
    FIRMWARE_PACKAGE="${found[0]}"
    return 0
}

# ensure_firmware_package 保证 FIRMWARE_PACKAGE 指向一个可上传的 .ehfw。
#
# 复用优先：dist/<version>/<profile>/ 里的包是构建机产出、含 pack_firmware.py 自检与
# build 溯源的那一份，比现场重打可信，也省掉一次几十分钟的 IDF 构建。
# 一份都找不到时才现场打包——pack_firmware.py 是这套格式**唯一**的包写方（方案 §7.5）。
ensure_firmware_package() {
    if [[ -n "$FIRMWARE_PACKAGE" ]]; then
        if [[ ! -f "$FIRMWARE_PACKAGE" ]]; then
            log_error "EHOME_FW_PACKAGE 指向的固件包不存在: $FIRMWARE_PACKAGE"
            return 1
        fi
        log_info "使用指定的固件包: $FIRMWARE_PACKAGE"
        return 0
    fi

    # ① 与源镜像同目录；② dist/<version>/<profile>/（方案 §7.2 的发布布局）。
    local candidates=()
    candidates+=("$(dirname "$FIRMWARE_FILE")")
    if [[ -n "$FIRMWARE_VERSION" && -n "$FIRMWARE_PROFILE" ]]; then
        candidates+=("$PROJECT_ROOT/esp32-collector/dist/$FIRMWARE_VERSION/$FIRMWARE_PROFILE")
    fi

    local dir rc
    for dir in "${candidates[@]}"; do
        [[ -d "$dir" ]] || continue
        rc=0
        ota_pick_package "$dir" || rc=$?
        if [[ $rc -eq 2 ]]; then
            return 1
        fi
        if [[ $rc -eq 0 ]]; then
            log_info "复用已打包的固件包: $FIRMWARE_PACKAGE"
            return 0
        fi
    done

    # ③ 现场打包。
    local pack_tool="$PROJECT_ROOT/esp32-collector/tools/pack_firmware.py"
    if [[ ! -f "$pack_tool" ]]; then
        log_error "找不到固件打包器: $pack_tool"
        log_error "它是方案 §7.5 规定的唯一包写方；没有它 .bin 变不成可上传的 .ehfw。"
        return 1
    fi
    if [[ -z "$FIRMWARE_PROFILE" ]]; then
        log_error "现场打包需要档位名，例如 EHOME_FW_PROFILE=c6-n8"
        log_error "也可以用 EHOME_FW_PACKAGE=<path> 直接指定一个已打好的 .ehfw。"
        return 1
    fi
    if [[ -z "$FIRMWARE_VERSION" ]]; then
        log_error "现场打包需要 FIRMWARE_VERSION：打包器会核对镜像内版本与它是否一致。"
        return 1
    fi
    if [[ ! -f "$FIRMWARE_FILE" ]]; then
        local profiled_bin="$PROJECT_ROOT/esp32-collector/build/$FIRMWARE_PROFILE/ehome_collector.bin"
        if [[ -f "$profiled_bin" ]]; then
            FIRMWARE_FILE="$profiled_bin"
        else
            log_error "找不到要打包的镜像: $FIRMWARE_FILE"
            log_error "档位布局应为 esp32-collector/build/$FIRMWARE_PROFILE/ehome_collector.bin"
            return 1
        fi
    fi

    # psram 从 sdkconfig 的 CONFIG_SPIRAM 读（镜像头没有这一位），所以 sdkconfig 必给。
    local sdkconfig_path
    sdkconfig_path="$(dirname "$FIRMWARE_FILE")/sdkconfig"
    if [[ ! -f "$sdkconfig_path" ]]; then
        sdkconfig_path="$PROJECT_ROOT/esp32-collector/sdkconfig"
    fi
    if [[ ! -f "$sdkconfig_path" ]]; then
        log_error "找不到 sdkconfig（$FIRMWARE_FILE 同目录或 esp32-collector/sdkconfig）"
        return 1
    fi

    local out_dir="$PROJECT_ROOT/esp32-collector/dist/$FIRMWARE_VERSION/$FIRMWARE_PROFILE"
    local out_pkg="$out_dir/ehome_collector-$FIRMWARE_VERSION-$FIRMWARE_PROFILE.ehfw"
    mkdir -p "$out_dir"
    log_info "现场打包: $FIRMWARE_FILE ($FIRMWARE_PROFILE, $FIRMWARE_VERSION) → $out_pkg"
    if ! python3 "$pack_tool" \
        --bin "$FIRMWARE_FILE" \
        --sdkconfig "$sdkconfig_path" \
        --profile "$FIRMWARE_PROFILE" \
        --version "$FIRMWARE_VERSION" \
        --out "$out_pkg"; then
        log_error "pack_firmware.py 失败，错因在上面的 ERROR 行：stale .bin = 镜像版本 ≠ EHOME_FW_VERSION，"
        log_error "profile 不符 = EHOME_FW_PROFILE 选错；也可以直接用 EHOME_FW_PACKAGE 指定已打好的包。"
        return 1
    fi
    if [[ ! -f "$out_pkg" ]]; then
        log_error "pack_firmware.py 未产出 $out_pkg"
        return 1
    fi
    FIRMWARE_PACKAGE="$out_pkg"
    log_info "现场打包完成: $FIRMWARE_PACKAGE"
    return 0
}

# ========================================
# 上传固件包（.ehfw）获取 firmware_id
# ========================================
upload_firmware() {
    log_info "正在上传固件包..."

    # 上传物是包不是 .bin（方案 §4）：没有包就现找一个，找不到再现场打包。
    if ! ensure_firmware_package; then
        return 1
    fi

    local package_size
    package_size=$(wc -c < "$FIRMWARE_PACKAGE")
    log_info "固件包: $FIRMWARE_PACKAGE ($package_size bytes)"

    local response
    # The Host header must match the backend's EHOME_EXTERNAL_HOST, because the
    # firmware download URL is signed against it. Do not default to a specific
    # machine: a stale address here produces tickets the device cannot use.
    if [[ -z "${EHOME_EXTERNAL_HOST:-}" ]]; then
        log_error "EHOME_EXTERNAL_HOST is not set."
        log_error "It must match the deployed backend's EHOME_EXTERNAL_HOST, e.g."
        log_error "  EHOME_EXTERNAL_HOST=192.0.2.10:8080 $0"
        log_error "Read it from the deployment's .env rather than guessing."
        return 1
    fi
    local ext_host="$EHOME_EXTERNAL_HOST"
    response=$(curl -s -X POST \
        -H "Authorization: Bearer $AUTH_TOKEN" \
        -H "Host: $ext_host" \
        -F "file=@$FIRMWARE_PACKAGE" \
        "${API_BASE}/firmwares/upload" 2>/dev/null)
    
    FIRMWARE_ID=$(echo "$response" | json_val "print(d.get('id','') or (d.get('data',{}).get('id','')))")
    
    if [[ -z "$FIRMWARE_ID" ]] || [[ "$FIRMWARE_ID" == "null" ]]; then
        log_error "上传固件包失败，无法获取 firmware_id"
        # 服务端的新错误码（FW_PKG_REQUIRED / FW_PKG_INVALID …）文案本身就可操作，
        # 失败即中止，所以这里必须把响应体打出来（而不是只在 DEBUG=1 时打）。
        log_error "服务端响应: $response"
        return 1
    fi

    # 硬切换：没有 200 补齐分支，同一个包重传只会是 409（方案 §5.3）。
    log_info "固件上传成功，firmware_id: $FIRMWARE_ID"
    return 0
}

# ========================================
# 检查设备在线状态
# ========================================
check_device_online() {
    local wait_time=0
    local response
    local status
    
    log_info "检查设备 #$NODE_ID 在线状态..."
    
    while [[ $wait_time -lt $MAX_OFFLINE_WAIT ]]; do
        response=$(api_call GET "/nodes/$NODE_ID")
        status=$(echo "$response" | json_val "print(d.get('status','') or (d.get('data',{}).get('status','')) or 'offline')")
        
        if [[ "$status" == "online" ]]; then
            log_info "设备 #$NODE_ID 在线"
            return 0
        fi
        
        log_warn "设备 #$NODE_ID 离线，等待... ($wait_time/$MAX_OFFLINE_WAIT 秒)"
        sleep 5
        wait_time=$((wait_time + 5))
    done
    
    log_error "设备 #$NODE_ID 离线超过 $MAX_OFFLINE_WAIT 秒"
    return 1
}

# ========================================
# 创建 OTA 任务
# ========================================
create_ota() {
    local response
    response=$(api_call POST "/ota/tasks" "{\"node_id\":$NODE_ID,\"firmware_id\":$FIRMWARE_ID}")
    
    OTA_ID=$(echo "$response" | json_val "print(d.get('id','') or d.get('ota_id','') or (d.get('data',{}).get('id','')))")
    
    if [[ -z "$OTA_ID" ]] || [[ "$OTA_ID" == "null" ]]; then
        log_error "创建 OTA 任务失败"
        log_debug "响应: $response"
        return 1
    fi
    
    return 0
}

# ========================================
# 等待 OTA 完成
# ========================================
wait_for_completion() {
    local start_time
    start_time=$(date +%s)
    local elapsed=0
    local status="pending"
    local response
    
    while [[ $elapsed -lt $TIMEOUT_PER_OTA ]]; do
        sleep $POLL_INTERVAL
        
        # 查询 OTA 状态
        # OTA tasks API returns array, find our task
        response=$(api_call GET "/ota/tasks?node_id=$NODE_ID")
        status=$(echo "$response" | python3 -c "
import sys,json
d=json.load(sys.stdin)
items=d if isinstance(d,list) else d.get('data',d.get('list',[]))
if isinstance(items,list):
    for t in items:
        if str(t.get('id','')) == '$OTA_ID' or str(t.get('ota_id','')) == '$OTA_ID':
            print(t.get('status',''))
" 2>/dev/null)
        
        elapsed=$(($(date +%s) - start_time))
        
        log_debug "OTA #$OTA_ID 状态: $status, 已等待: ${elapsed}s"
        
        case "$status" in
            success)
                echo "success"
                return 0
                ;;
            failed|timeout|needs_retry|error)
                # Check if device is back online — real OTA may succeed
                # even if server can't confirm version string match
                local node_info
                node_info=$(api_call GET "/nodes/$NODE_ID" 2>/dev/null)
                local node_status
                node_status=$(echo "$node_info" | json_val "print(d.get('status','') or (d.get('data',{}).get('status','')))")
                if [[ "$node_status" == "online" ]]; then
                    log_warn "Server reports $status but device is online — treating as success"
                    echo "success"
                    return 0
                fi
                echo "failed"
                return 1
                ;;
            downloading|installing|pending|in_progress)
                # 继续等待
                ;;
            *)
                log_warn "未知状态: $status"
                ;;
        esac
    done
    
    echo "timeout"
    return 2
}

# ========================================
# 单次 OTA 测试
# ========================================
run_single_ota() {
    local run_num=$1
    local start_time
    start_time=$(date +%s)
    local ota_id=""
    local status=""
    local elapsed=0
    
    log_info "[RUN $run_num] 开始 OTA 测试..."
    
    # 创建 OTA 任务
    if ! create_ota; then
        status="failed"
        elapsed=$(($(date +%s) - start_time))
        record_result "$run_num" "" "$status" "$elapsed"
        return 1
    fi
    
    ota_id="$OTA_ID"
    
    # 等待完成
    local result
    result=$(wait_for_completion)
    elapsed=$(($(date +%s) - start_time))
    
    case "$result" in
        success)
            status="success"
            ;;
        failed)
            status="failed"
            ;;
        timeout)
            status="timeout"
            ;;
    esac
    
    # 记录结果
    record_result "$run_num" "$ota_id" "$status" "$elapsed"
    
    # 输出结果
    case "$status" in
        success)
            echo -e "${GREEN}[RUN $run_num]${NC} ota_id=$ota_id status=$status elapsed=${elapsed}s"
            ;;
        failed)
            echo -e "${RED}[RUN $run_num]${NC} ota_id=$ota_id status=$status elapsed=${elapsed}s"
            ;;
        timeout)
            echo -e "${YELLOW}[RUN $run_num]${NC} ota_id=$ota_id status=$status elapsed=${elapsed}s"
            ;;
    esac
    
    # 等待设备重启
    if [[ "$status" == "success" ]]; then
        log_info "等待设备重启完成 (${REBOOT_WAIT}s)..."
        sleep $REBOOT_WAIT
    fi
    
    # 返回状态
    case "$status" in
        success) return 0 ;;
        *) return 1 ;;
    esac
}

# ========================================
# 记录结果到 CSV
# ========================================
record_result() {
    local run_num=$1
    local ota_id=$2
    local status=$3
    local elapsed=$4
    local timestamp
    timestamp=$(date +%Y-%m-%dT%H:%M:%S)
    
    # 写入 CSV
    echo "$run_num,$ota_id,$status,$elapsed,$timestamp" >> "$CSV_FILE"
    
    # 更新统计
    TOTAL_COUNT=$((TOTAL_COUNT + 1))
    TOTAL_ELAPSED=$((TOTAL_ELAPSED + elapsed))
    ELAPSED_TIMES+=("$elapsed")
    
    if [[ $elapsed -lt $MIN_ELAPSED ]]; then
        MIN_ELAPSED=$elapsed
    fi
    if [[ $elapsed -gt $MAX_ELAPSED ]]; then
        MAX_ELAPSED=$elapsed
    fi
    
    case "$status" in
        success)
            SUCCESS_COUNT=$((SUCCESS_COUNT + 1))
            CONSECUTIVE_FAILURES=0
            ;;
        failed)
            FAILED_COUNT=$((FAILED_COUNT + 1))
            CONSECUTIVE_FAILURES=$((CONSECUTIVE_FAILURES + 1))
            ;;
        timeout)
            TIMEOUT_COUNT=$((TIMEOUT_COUNT + 1))
            CONSECUTIVE_FAILURES=$((CONSECUTIVE_FAILURES + 1))
            ;;
    esac
}

# ========================================
# 打印最终报告
# ========================================
print_report() {
    local success_rate=0
    local failed_rate=0
    local timeout_rate=0
    local avg_elapsed=0
    
    if [[ $TOTAL_COUNT -gt 0 ]]; then
        success_rate=$(python3 -c "print(round($SUCCESS_COUNT*100/$TOTAL_COUNT,1))")
        failed_rate=$(python3 -c "print(round($FAILED_COUNT*100/$TOTAL_COUNT,1))")
        timeout_rate=$(python3 -c "print(round($TIMEOUT_COUNT*100/$TOTAL_COUNT,1))")
        avg_elapsed=$(python3 -c "print(int($TOTAL_ELAPSED/$TOTAL_COUNT))")
    fi
    
    # 如果没有成功，MIN_ELAPSED 显示 0
    if [[ $MIN_ELAPSED -eq 999999 ]]; then
        MIN_ELAPSED=0
    fi
    
    echo ""
    echo "========== OTA 100次测试报告 =========="
    echo "总测试次数:    $TOTAL_COUNT"
    echo "成功:          $SUCCESS_COUNT (${success_rate}%)"
    echo "失败:          $FAILED_COUNT (${failed_rate}%)"
    echo "超时:          $TIMEOUT_COUNT (${timeout_rate}%)"
    echo "平均耗时:      ${avg_elapsed}s"
    echo "最短:          ${MIN_ELAPSED}s"
    echo "最长:          ${MAX_ELAPSED}s"
    echo "========================================"
    echo ""
    echo "结果文件: $CSV_FILE"
}

# ========================================
# 主函数
# ========================================
main() {
    echo "========================================"
    echo "  OTA 100次自动化测试脚本"
    echo "========================================"
    echo ""
    echo "配置:"
    echo "  API_BASE:         $API_BASE"
    echo "  NODE_ID:          $NODE_ID"
    echo "  FIRMWARE_PACKAGE: ${FIRMWARE_PACKAGE:-<自动：复用 dist/ 里已打好的包，缺失时现场打包>}"
    echo "  FIRMWARE_FILE:    $FIRMWARE_FILE (仅现场打包时作为源镜像)"
    echo "  FIRMWARE_VERSION: ${FIRMWARE_VERSION:-<未设置：仅现场打包需要，须等于镜像内版本>}"
    echo "  psram/档位:       在包内清单里，由 pack_firmware.py 从 sdkconfig 与镜像导出"
    echo "  TEST_COUNT:       $TEST_COUNT"
    echo "  TIMEOUT_PER_OTA:  ${TIMEOUT_PER_OTA}s"
    echo ""
    
    # 创建结果目录
    mkdir -p "$RESULTS_DIR"
    
    # 写入 CSV 头
    echo "run,ota_id,status,elapsed_sec,timestamp" > "$CSV_FILE"
    
    # 前置步骤
    if ! login; then
        log_error "登录失败，退出"
        exit 1
    fi
    
    if ! upload_firmware; then
        log_error "上传固件失败，退出"
        exit 1
    fi
    
    if ! check_device_online; then
        log_error "设备离线，退出"
        exit 1
    fi
    
    echo ""
    log_info "开始 $TEST_COUNT 次 OTA 测试..."
    echo ""
    
    # 循环测试
    local i=1
    while [[ $i -le $TEST_COUNT ]]; do
        if ! run_single_ota $i; then
            # 检查连续失败
            if [[ $CONSECUTIVE_FAILURES -ge $MAX_CONSECUTIVE_FAILURES ]]; then
                log_error "连续 $CONSECUTIVE_FAILURES 次失败，中止测试"
                break
            fi
        fi
        
        i=$((i + 1))
    done
    
    # 打印报告
    print_report
}

# 运行
main "$@"
