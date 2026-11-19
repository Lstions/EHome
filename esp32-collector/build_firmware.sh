#!/usr/bin/env bash
set -euo pipefail

# MSys/Git-Bash spells paths as /e/WorkSpace/... but CMake, Ninja and the
# toolchain are native Windows programs, so a path that is not converted is
# simply "does not exist" to them -- which is how a Git-Bash build died with:
#   SDKCONFIG_DEFAULTS '/e/WorkSpace/.../sdkconfig.defaults' does not exist.
# cygpath -m gives the mixed form (E:/WorkSpace/...) that both MSys and native
# tools understand.  On Linux cygpath is absent and this is a no-op.
to_native_path() {
    if command -v cygpath >/dev/null 2>&1; then
        cygpath -m "$1"
    else
        printf '%s\n' "$1"
    fi
}

# ESP-IDF version of the checkout IDF_PATH points at, without starting python.
#
# Two sources, because a checkout may carry either:
#   * version.txt      ("v6.1.0" in a release checkout)
#   * git describe     ("v6.1" in a tag checkout -- the file can be absent)
# Empty output means "unknown", which callers treat as "do not judge".
idf_version_string() {
    local _vf="${IDF_PATH:-}/version.txt" _v=""
    if [[ -f "$_vf" ]]; then
        _v="$(head -n1 "$_vf" 2>/dev/null | tr -d '[:space:]')"
        _v="${_v#v}"
    fi
    if [[ -z "$_v" && -n "${IDF_PATH:-}" ]] && command -v git >/dev/null 2>&1; then
        _v="$(git -C "$IDF_PATH" describe --tags --always 2>/dev/null || true)"
        _v="${_v#v}"
    fi
    printf '%s\n' "$_v"
}

# Major.minor only ("6.1.0" -> "6.1"), so a patch bump does not look like a
# different toolchain.
idf_version_short() {
    printf '%s\n' "${1:-}" | grep -oE '^[0-9]+\.[0-9]+' || true
}

# A build directory is bound to the toolchain that configured it: CMake records
# the configuring interpreter (CMakeCache.txt) and the IDF version (config.env).
# Reusing it after an IDF switch makes idf.py abort part-way through the build:
#   "…idf6.1_py3.14_env/bin/python is currently active in the environment while
#    the project was configured with …idf6.0_py3.14_env/bin/python. Run idf.py
#    fullclean"
# That message arrives only after the build has started, so check up front and
# say what to do.  A checkout replaced in place (same IDF_PATH, new version) is
# caught by the recorded IDF_VERSION, which the python-env check would miss.
check_build_dir_compat() {
    local build_dir="$1" cur_py="$2"
    local env_file="$build_dir/config.env" cache="$build_dir/CMakeCache.txt"

    if [[ -f "$env_file" ]]; then
        local have_idf cur_idf
        have_idf="$(grep -oE '"IDF_VERSION"[[:space:]]*:[[:space:]]*"[^"]*"' "$env_file" 2>/dev/null \
            | head -1 | sed -E 's/.*"([^"]*)"$/\1/' || true)"
        cur_idf="$(idf_version_string)"
        if [[ -n "$have_idf" && -n "$cur_idf" ]]; then
            local hs cs
            hs="$(idf_version_short "$have_idf")"
            cs="$(idf_version_short "$cur_idf")"
            if [[ -n "$hs" && -n "$cs" && "$hs" != "$cs" ]]; then
                echo "ERROR: $build_dir was configured with ESP-IDF $have_idf," >&2
                echo "       but IDF_PATH now points at $cur_idf." >&2
                echo "       A build directory cannot be reused across IDF versions." >&2
                echo "       Fix: rm -rf $build_dir   (or build into a fresh one:" >&2
                echo "            BUILD_ROOT=$PWD/build-$cs ./build_firmware.sh ...)" >&2
                return 1
            fi
        fi
    fi

    if [[ -f "$cache" ]]; then
        local have_py
        have_py="$(grep -E '^PYTHON:UNINITIALIZED=' "$cache" 2>/dev/null | head -1 | cut -d= -f2- || true)"
        if [[ -n "$have_py" && -n "$cur_py" && "$have_py" != "$cur_py" ]]; then
            echo "ERROR: $build_dir was configured with a different python:" >&2
            echo "         configured: $have_py" >&2
            echo "         active now: $cur_py" >&2
            echo "       idf.py would abort mid-build.  Fix: rm -rf $build_dir" >&2
            return 1
        fi
    fi
    return 0
}

PROJECT_DIR="$(to_native_path "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)")"

BUILD_ROOT="${BUILD_ROOT:-$PROJECT_DIR/build}"
BUILD_ROOT="$(to_native_path "$BUILD_ROOT")"

# 仓库根：VERSION 文件与 git tag 都在这里找。
REPO_ROOT="$(to_native_path "$(cd "$PROJECT_DIR/.." && pwd)")"

# 发布产物根（方案 §7.2）：dist/<version>/<profile>/，dist/ 已 gitignore。
DIST_ROOT="${DIST_ROOT:-$PROJECT_DIR/dist}"
DIST_ROOT="$(to_native_path "$DIST_ROOT")"

# ---------------------------------------------------------------------------
# 版本：单一真源（方案 §7.3）
#
#   1. FW_VERSION 环境变量                                <- CI / 一次性发版
#   2. git describe --tags --exact-match --match 'fw-v*'  <- 打 tag 发布
#   3. VERSION 文件（仓库根）                              <- 日常
#   4. 都没有 -> 构建失败（不猜，也不退回 CMakeLists 里的常量）
#
# 版本串必须严格 X.Y.Z：IDF 会把 PROJECT_VER 写进镜像，而
# firmwareid.CompareVersion 只比前 3 段数字 —— "2.9.0-dirty.abc" 与 "2.9.0"
# 会被判**相等**，OTA 闸门于是当成「不是升级」。commit/dirty 只进包内清单。
# ---------------------------------------------------------------------------
# 环境变量先存下来再清空 FW_VERSION：下面解析出的才叫「已解析的版本」，
# 否则 FW_VERSION="" 会把调用方传进来的值直接抹掉（静默退回 VERSION 文件）。
FW_VERSION_ENV="${FW_VERSION:-}"
FW_VERSION=""
FW_VERSION_SOURCE=""

repo_dirty() {
    if ! git -C "$REPO_ROOT" rev-parse --git-dir >/dev/null 2>&1; then
        printf 'unknown'
        return 0
    fi
    # --untracked-files=no：未跟踪文件（dist/、构建日志）不算源码被改。
    if [[ -n "$(git -C "$REPO_ROOT" status --porcelain --untracked-files=no 2>/dev/null)" ]]; then
        printf 'dirty'
    else
        printf 'clean'
    fi
}

resolve_fw_version() {
    local value="" source="" tag=""
    if [[ -n "$FW_VERSION_ENV" ]]; then
        value="$FW_VERSION_ENV"
        source="env FW_VERSION"
    elif tag="$(git -C "$REPO_ROOT" describe --tags --exact-match --match 'fw-v*' 2>/dev/null)" \
            && [[ -n "$tag" ]]; then
        value="${tag#fw-v}"
        source="git $tag"
    elif [[ -f "$REPO_ROOT/VERSION" ]]; then
        value="$(head -n1 "$REPO_ROOT/VERSION" | tr -d '[:space:]')"
        source="VERSION file"
    else
        echo "ERROR: cannot determine the firmware version. Give one of:" >&2
        echo "         1) FW_VERSION=x.y.z $0 <profile>" >&2
        echo "         2) git tag fw-v<x.y.z> on this commit" >&2
        echo "         3) $REPO_ROOT/VERSION containing x.y.z" >&2
        return 1
    fi
    if ! printf '%s' "$value" | grep -qE "^[0-9]+[.][0-9]+[.][0-9]+$"; then
        echo "ERROR: firmware version '$value' (from $source) is not X.Y.Z." >&2
        echo "       A suffix such as -dirty or +<sha> would make CompareVersion" >&2
        echo "       treat two different builds as the same version (方案 §7.3)." >&2
        return 1
    fi
    FW_VERSION="$value"
    FW_VERSION_SOURCE="$source"
}

# 打印解析结果。$1=1 时带构建日志前缀，否则裸版本（供 `make fw-version`）。
print_fw_version() {
    if [[ "${1:-}" == "1" ]]; then
        printf '==> version %s (source: %s, %s)\n' "$FW_VERSION" "$FW_VERSION_SOURCE" "$(repo_dirty)"
    else
        printf '%s (source: %s, %s)\n' "$FW_VERSION" "$FW_VERSION_SOURCE" "$(repo_dirty)"
    fi
}

# 复制一个发布产物到 dist/<version>/<profile>/；缺文件直接失败
# （一个「看起来完整」的 dist 目录比报错更危险）。
copy_dist_file() {
    local src="$1" dst="$2"
    if [[ ! -f "$src" ]]; then
        echo "ERROR: missing build artifact: $src" >&2
        return 1
    fi
    cp -f "$src" "$dst"
}

# SHA256SUMS：必须 sort（AC-P8），但排的是**路径列**（等价 `sort -k2`），不是整行。
# 按整行排会以 sha256 开头排 —— 那么「任何一个文件变了」都会重排整个文件，
# 两次构建的 diff 就失去意义；按路径排则一处变更只动一行，且仍与构建顺序无关。
# 路径相对 dist/<version>/，因此 `cd dist/<version> && sha256sum -c SHA256SUMS` 直接可用。
write_sha256sums() {
    local version="$1" dir sums tmp file
    dir="$DIST_ROOT/$version"
    sums="$dir/SHA256SUMS"
    tmp="$sums.tmp"
    : > "$tmp"
    while IFS= read -r file; do
        printf '%s  %s\n' "$(sha256sum "$file" | cut -d' ' -f1)" "${file#"$dir"/}" >> "$tmp"
    done < <(find "$dir" -type f ! -name 'SHA256SUMS*' | LC_ALL=C sort)
    mv -f "$tmp" "$sums"
    echo "==> SHA256SUMS: $sums ($(wc -l < "$sums") lines)"
}

# dist/<version>/ 完整性门禁（方案 §7.6）：6 个档位缺一却当成全量发布。
# 判据是「每个档位的 5 个文件都在 SUM 里 + sha256sum -c 通过 + SUM 已排序」。
check_dist_complete() {
    local version="$1" dir sums profile line missing=""
    dir="$DIST_ROOT/$version"
    sums="$dir/SHA256SUMS"
    if [[ ! -f "$sums" ]]; then
        echo "ERROR: $sums is missing" >&2
        return 1
    fi
    # 排序判据是**路径列**（awk 取 $2 后再 sort -c）：见 write_sha256sums 的说明。
    if ! awk '{print $2}' "$sums" | LC_ALL=C sort -c 2>/dev/null; then
        echo "ERROR: $sums is not sorted by path (column 2)" >&2
        return 1
    fi
    for profile in c6-n8 c6-n16 s3-n8 s3-n16 s3p-n8 s3p-n16; do
        for line in ehome_collector.bin bootloader.bin partition-table.bin flash_args \
                    "ehome_collector-$version-$profile.ehfw"; do
            if ! grep -qE "  ${profile}/${line}$" "$sums"; then
                missing="$missing $profile/$line"
            fi
        done
    done
    if [[ -n "$missing" ]]; then
        echo "ERROR: dist/$version is incomplete; missing:$missing" >&2
        echo "       (a full release needs 6 profiles x 5 files)" >&2
        return 1
    fi
    if ! ( cd "$dir" && sha256sum -c SHA256SUMS >/dev/null ); then
        echo "ERROR: sha256sum -c failed in $dir" >&2
        return 1
    fi
    echo "==> dist/$version complete: 6/6 profiles, $(wc -l < "$sums") files verified"
}

usage() {
    cat <<'EOF'
Usage: ./build_firmware.sh <profile|all>
       ./build_firmware.sh --version        print the resolved version and exit

Version (single source of truth, 方案 §7.3):
  FW_VERSION > exact git tag "fw-v*" > VERSION file (repo root) > fail.
  Must be plain X.Y.Z: a suffix like -dirty would make CompareVersion treat
  two different builds as the same version.  commit/dirty go into the
  package manifest only, never into the version string.

Output:
  build/<profile>/                        intermediate build tree
  dist/<version>/<profile>/*.ehfw         the package to upload (one file)
  dist/<version>/<profile>/ehome_collector.bin, bootloader.bin,
      partition-table.bin, flash_args    for factory flashing (not packed)
  dist/<version>/SHA256SUMS               sorted; `sha256sum -c` verifies

Profiles:
  c6-n8    ESP32-C6 with 8MB flash
  c6-n16   ESP32-C6 with 16MB flash
  s3-n8    ESP32-S3 with 8MB flash
  s3-n16   ESP32-S3 with 16MB flash
  s3p-n8   ESP32-S3 with PSRAM + 8MB flash
  s3p-n16  ESP32-S3 with PSRAM + 16MB flash
  all      Build all profiles

Backend device link (required):
  ⭐ 2026-10-08（§194）：**MQTT 已彻底移除** —— 3.0 TCP+TLS 是**唯一**上行通道。
  ⇒ 构建前必须给 3.0 的 host/端口；地址编译进固件、无运行期覆盖。

    1. EXTRA_SDKCONFIG_DEFAULTS=<file>    （本地联调/一次性覆盖，推荐）
       CONFIG_EHOME_DEVICE_LINK_HOST="<后端 IP 或域名>"
       CONFIG_EHOME_DEVICE_LINK_PORT=8443

    2. 直接改 main/Kconfig.projbuild 的默认值（仅当你确定要改出厂默认）

  ⚠ 占位地址（TEST-NET-1 等不可路由地址）构建出的固件**永远连不上**，
    build_firmware.sh 会**拒绝**这种构建。

Other environment:
  BUILD_ROOT   place build directories elsewhere.  A build directory
               cannot be reused across ESP-IDF versions: this script
               refuses rather than letting idf.py abort mid-build.
EOF
}

# ⭐ 2026-10-08（§194）：PLACEHOLDER_BROKER_PREFIXES 与 BROKER_DEFAULTS_FILE
#   已删除（MQTT broker 不复存在）。占位地址的拒绝逻辑现在只针对 3.0 的 host，
#   见下方 EHOME_DEVICE_LINK_HOST 的处理。

# ---------------------------------------------------------------------------
# How idf.py gets invoked.
#
# `command -v idf.py` is NOT a sufficient check on Windows: the ESP-IDF
# installer puts C:\Espressif\tools\idf-exe\<ver>\idf.py.exe on PATH, and under
# MSys/Git-Bash that launcher exits 0 while printing nothing at all.  A build
# driven from bash therefore "succeeds" without ever producing an ELF -- which
# is exactly what happened on 2026-10-02: profiles reported success, then the
# IRAM gate failed with "no such ELF: .../ehome_collector.elf".
#
# Always drive idf.py through the IDF python environment, and refuse to start
# when that cannot be resolved.
# ---------------------------------------------------------------------------
IDF_PY_CMD=()

resolve_idf_python() {
    local cand=""
    if [[ -n "${IDF_PYTHON_ENV_PATH:-}" ]]; then
        for cand in \
            "$IDF_PYTHON_ENV_PATH/Scripts/python.exe" \
            "$IDF_PYTHON_ENV_PATH/bin/python" \
            "$IDF_PYTHON_ENV_PATH/bin/python3"; do
            if [[ -x "$cand" ]]; then
                printf '%s\n' "$cand"
                return 0
            fi
        done
    fi
    for cand in python3 python; do
        if command -v "$cand" >/dev/null 2>&1; then
            command -v "$cand"
            return 0
        fi
    done
    return 1
}

init_idf_py() {
    local py=""
    if [[ -z "${IDF_PATH:-}" || ! -f "$IDF_PATH/tools/idf.py" ]]; then
        echo "ERROR: IDF_PATH is not set to a usable ESP-IDF checkout (got '${IDF_PATH:-<unset>}')." >&2
        echo "       Source the export script first:  . \$IDF_PATH/export.sh" >&2
        return 127
    fi
    # cmake is needed by IDF's CMake build.  Check it here rather than letting
    # idf.py discover it: idf.py prints "ESP-IDF v6.1.0" BEFORE it reaches the
    # build, so a missing cmake otherwise looks like a working environment and
    # the failure lands mid-build with exit code 2.
    if ! command -v cmake >/dev/null 2>&1; then
        echo "ERROR: cmake is not on PATH; ESP-IDF's build cannot start." >&2
        echo "       This checkout ships one at: $PROJECT_DIR/../.tools/cmake-*/bin" >&2
        echo "       Add it to PATH (or source the ESP-IDF export script) and retry." >&2
        return 127
    fi
    py="$(resolve_idf_python)" || {
        echo "ERROR: no python interpreter found to run \$IDF_PATH/tools/idf.py." >&2
        echo "       Set IDF_PYTHON_ENV_PATH (source the ESP-IDF export script) and retry." >&2
        return 127
    }
    # Run through tools/idf_shim.py, never idf.py directly: under MSys/Git-Bash
    # idf.py prints an "MSys/Mingw is no longer supported" warning and exits 0
    # WITHOUT building anything (see that file's docstring for the exact
    # mechanism, and why `env -u MSYSTEM` cannot fix it).
    if [[ -f "$PROJECT_DIR/tools/idf_shim.py" ]]; then
        IDF_PY_CMD=("$py" "$PROJECT_DIR/tools/idf_shim.py")
    else
        IDF_PY_CMD=("$py" "$IDF_PATH/tools/idf.py")
    fi
}

idf_py() {
    if [[ ${#IDF_PY_CMD[@]} -eq 0 ]]; then
        echo "ERROR: idf.py has not been resolved; call init_idf_py first." >&2
        return 127
    fi
    # IDF_PY_CMD already points at tools/idf_shim.py, which strips MSYSTEM
    # inside the interpreter (env -u cannot: MSys re-injects it into children).
    "${IDF_PY_CMD[@]}" "$@"
}


# Each profile is echoed as four fields:
#   <target> <flash> <model-defaults-suffix> <inject-psram-switch>
# The third/fourth fields are empty for the existing c6/s3 profiles, which
# keeps their SDKCONFIG_DEFAULTS chain and behaviour unchanged.  "s3p" is the
# same SoC as "s3" plus the model dimension: it chains
# sdkconfig.defaults.esp32s3psram (content owned by WS-B/task-2; this script
# only references it) and forces CONFIG_COLLECTOR_PSRAM=y for model naming.
profile_settings() {
    case "$1" in
        c6-n8)   printf '%s %s %s %s\n' esp32c6 n8  ''           '' ;;
        c6-n16)  printf '%s %s %s %s\n' esp32c6 n16 ''           '' ;;
        s3-n8)   printf '%s %s %s %s\n' esp32s3 n8  ''           '' ;;
        s3-n16)  printf '%s %s %s %s\n' esp32s3 n16 ''           '' ;;
        s3p-n8)  printf '%s %s %s %s\n' esp32s3 n8  esp32s3psram y  ;;
        s3p-n16) printf '%s %s %s %s\n' esp32s3 n16 esp32s3psram y  ;;
        *) return 1 ;;
    esac
}

build_profile() {
    local profile="$1"
    local settings target flash_profile model_defaults model_psram build_dir sdkconfig defaults lock_file
    local _model_defaults _model_switch_defaults

    settings="$(profile_settings "$profile")" || {
        echo "Unknown firmware profile: $profile" >&2
        usage >&2
        return 2
    }
    read -r target flash_profile model_defaults model_psram <<<"$settings"

    build_dir="$BUILD_ROOT/$profile"
    sdkconfig="$build_dir/sdkconfig"
    lock_file="$build_dir/dependencies.lock"
    defaults="$PROJECT_DIR/sdkconfig.defaults;$PROJECT_DIR/config/flash/$flash_profile.defaults"

    # Refuse up front when this directory was configured by another IDF/python;
    # idf.py would otherwise abort only after the build has started.
    check_build_dir_compat "$build_dir" "${IDF_PY_CMD[0]:-}" || return 1

    # ⭐ 2026-10-08（§194）：**MQTT broker 解析块已整体删除**（MQTT 已彻底移除）。
    #   ⚠ 删掉的不只是"解析地址"，还有它承载的**构建期保护**：
    #     原先若未配置 broker（或仍是 TEST-NET 占位地址）就**拒绝构建**，
    #     因为地址是编译进去的、没有运行期覆盖，配错=设备永远连不上。
    #   ⇒ 现在对应位置的保护是 EHOME_DEVICE_LINK_HOST 的占位拒绝
    #     （见下方 link host 的处理），语义相同但目标换成了 3.0 的 host。

    # 目标专属 defaults 必须显式加入链（脚本设置了 SDKCONFIG_DEFAULTS，这会**取代**
    # IDF 默认的"sdkconfig.defaults + 自动 sdkconfig.defaults.<target>"行为）。
    #
    # 2026-10-04 实错：sdkconfig.defaults.esp32s3 里的 CONFIG_SPIRAM=y 一直不生效，
    # 因为本脚本的链里从来没有它；而直接跑 `idf.py build` 时 IDF 会自动带上，
    # 于是"同一条命令编出的固件不同"——S3 一直以 16KB 内部堆运行，
    # 导致 OTA 起不来（ota_task 需 8KB）与配置同步失败（manifest 事务需约 16KB）。
    _target_defaults="$PROJECT_DIR/sdkconfig.defaults.$target"
    defaults="$PROJECT_DIR/sdkconfig.defaults"
    if [[ -f "$_target_defaults" ]]; then
        defaults="$defaults;$_target_defaults"
    fi

    # 第三级（型号 defaults）：同一 SoC 的 PSRAM / 非 PSRAM 型号差异。
    # 该文件由 WS-B(task-2) 维护，本脚本只负责在存在时加入链 —— 尚未就绪时
    # 明确警告并跳过，而不是假装它存在。
    if [[ -n "$model_defaults" ]]; then
        _model_defaults="$PROJECT_DIR/sdkconfig.defaults.$model_defaults"
        if [[ -f "$_model_defaults" ]]; then
            defaults="$defaults;$_model_defaults"
        else
            echo "WARNING: $profile: $_model_defaults not found; building without model defaults" >&2
        fi
    fi

    # ⚠ 2026-10-08（§194）：这里补回 mkdir -p "$build_dir"。
    #   原先它写在已删除的 MQTT broker 块里（那句"Apply the resolved broker last"），
    #   MQTT 一删就**连带丢了**，而下面 .model.defaults 的写入依赖该目录存在
    #   ⇒ 构建直接失败："No such file or directory: .../.model.defaults"。
    #   ⚠ 教训：删一个块时，要检查块里是否夹带了**与本块主题无关**的副作用语句。
    mkdir -p "$build_dir"

    # 型号开关是 profile 的显式语义（s3p = 带 PSRAM 的型号）注入链尾，
    # 防止"profile 叫 s3p、Kconfig 却报非 PSRAM 型号"的静默漂移。
    # 板上是否真有 PSRAM 仍由 app_state.c 运行时 heap_caps 探测决定，二者缺一不可。
    if [[ "$model_psram" == "y" ]]; then
        _model_switch_defaults="$build_dir/.model.defaults"
        printf 'CONFIG_COLLECTOR_PSRAM=y\n' > "$_model_switch_defaults"
        defaults="$defaults;$_model_switch_defaults"
    fi

    # ⭐ 2026-10-08（§194）：原先这里链尾还拼了 $_broker_defaults（MQTT broker）。
    #   MQTT 已移除 ⇒ 该变量不存在，拼接也必须删（否则 set -u 下直接爆 unbound）。
    defaults="$defaults;$PROJECT_DIR/config/flash/$flash_profile.defaults"
    if [[ -n "${EXTRA_SDKCONFIG_DEFAULTS:-}" ]]; then
        defaults="$defaults;$EXTRA_SDKCONFIG_DEFAULTS"
        # sdkconfig takes precedence over sdkconfig.defaults.  An explicit
        # override (for example an isolated development backend host) must
        # therefore regenerate this profile's derived sdkconfig instead of
        # silently retaining a previous value.
        rm -f "$sdkconfig"
    fi

    if [[ ! -f "$lock_file" && -f "$PROJECT_DIR/dependencies.lock" ]]; then
        cp "$PROJECT_DIR/dependencies.lock" "$lock_file"
    fi

    # Guard against a stale derived sdkconfig silently pinning values that the
    # defaults files are supposed to own.
    #
    # kconfgen only lets a defaults file override a value that sdkconfig still
    # records as its default.  Once a symbol has been written as *user-set*
    # (menuconfig, or a one-off EXTRA_SDKCONFIG_DEFAULTS run), a later plain
    # build keeps that old value and ignores sdkconfig.defaults -- silently.
    #
    # We therefore refuse to build when a defaults-owned symbol in the existing
    # sdkconfig disagrees with what the defaults files now say.  Rebuilding is
    # always available via a clean profile directory.
    #
    # ⭐ 2026-10-08（§194）：CONFIG_COLLECTOR_MQTT_BROKER_URL 与
    #   CONFIG_MQTT_TASK_STACK_SIZE 已从本清单移除 —— MQTT 已彻底移除，
    #   这两个符号不再存在（留在清单里会让"漂移检测"去比对一个不存在的键）。
    _guard_symbols=(
        CONFIG_ESP_WIFI_IRAM_OPT
        CONFIG_ESP_WIFI_RX_IRAM_OPT
        CONFIG_ESP_WIFI_EXTRA_IRAM_OPT
        # PSRAM 决定可用堆总量。被陈旧的派生 sdkconfig 静默钉成 n 时，
        # 设备会带着约 16KB 内部堆上线，OTA 与配置同步都会失败，
        # 而构建日志一切正常 —— 这正是 2026-10-04 的故障形态。
        # WS-A: s3p profile 的型号开关。陈旧派生 sdkconfig 若把它钉在 n，
        # 设备会以非 PSRAM 型号上线（后端按型号下 manifest），构建日志却正常，
        # 与下面 CONFIG_SPIRAM 属于同一种"静默钉旧值"形态。
        CONFIG_COLLECTOR_PSRAM
        CONFIG_SPIRAM
        CONFIG_SPIRAM_MODE_OCT
        # 自动回滚开关（M3a/T3a.1，2026-11-18）。它编进 **bootloader.bin**，
        # 而 OTA 只写应用分区 ⇒ 派生 sdkconfig 一旦把它钉在 n，这份固件烧下去
        # **根本没有回滚能力**：坏镜像启动即崩也不会退回旧分区，而构建日志、
        # 门禁、内存数字全都正常 —— "以为有回滚，其实没有"。
        # 实测（2026-11-18）：把 sdkconfig.defaults 改成 y 之后，既有的
        # build61/c6-n16/sdkconfig 仍然是 n，**连 `idf.py reconfigure` 都翻不
        # 过来**（kconfgen/kconfiglib 把"与已加载 defaults 不同的现值"当作
        # user value，sdkconfig 胜出）；而 tools/check_sdkconfig_symbols.py
        # 只验"符号在 Kconfig 树里存在"、不验取值，所以那次构建它照样打 PASS。
        # ⇒ 只能靠这条守卫把"静默漂移"变成构建失败（与上面 PSRAM 那组同一个
        #   道理：都属于"看起来配了、其实没生效"）。
        CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE
    )
    if [[ -f "$sdkconfig" ]]; then
        local _drift=0 _sym _want _have _want_all=""
        # Concatenate the defaults files in order; last assignment wins.
        while IFS= read -r _f; do
            [[ -f "$_f" ]] && _want_all+="$(cat "$_f")"$'\n'
        done < <(printf '%s\n' "$defaults" | tr ';' '\n')
        for _sym in "${_guard_symbols[@]}"; do
            # grep 在这里**合法地可能无匹配**（该符号在 defaults 与 sdkconfig 两边都不出现），
            # 而本脚本开头是 `set -euo pipefail`：管道里 grep 返回 1 会让整个函数
            # 静默中止，构建在没有任何错误信息的情况下退出 1。
            # 2026-10-04 实测：加入 CONFIG_SPIRAM 到守卫名单后，sdkconfig.defaults.esp32s3
            # 通篇没有以 CONFIG_SPIRAM= 开头的行（只有注释里提到它），于是 _want 为空、
            # grep 退出 1，脚本在 "==> Broker:" 之后直接消失 —— 很难查。
            # 因此显式吞掉退出码：无匹配就是"两边都没有该符号"，属正常情况。
            _want="$(printf '%s\n' "$_want_all" | grep -E "^${_sym}=|^# ${_sym} is not set" | tail -1 || true)"
            _have="$(grep -E "^${_sym}=|^# ${_sym} is not set" "$sdkconfig" | tail -1 || true)"
            # Normalise `CONFIG_X=n` and `# CONFIG_X is not set`: kconfgen writes
            # the latter for a disabled bool, so comparing them literally would
            # report drift where there is none.
            [[ "$_want" == "# ${_sym} is not set" ]] && _want="${_sym}=n"
            [[ "$_have" == "# ${_sym} is not set" ]] && _have="${_sym}=n"
            if [[ -n "$_want" && -n "$_have" && "$_want" != "$_have" ]]; then
                echo "ERROR: $profile: $build_dir/sdkconfig is stale for $_sym" >&2
                echo "         defaults say: $_want" >&2
                echo "         sdkconfig has: $_have" >&2
                echo "       A stale user-set value silently overrides sdkconfig.defaults." >&2
                echo "       Fix: rm -rf $build_dir   (or pass EXTRA_SDKCONFIG_DEFAULTS to regenerate)" >&2
                _drift=1
            fi
        done
        if [[ "$_drift" -ne 0 ]]; then
            return 1
        fi
    fi

    echo "==> Building $profile (target=$target, flash=$flash_profile)"
    # -D FW_VERSION 是版本**唯一**的入口（方案 §7.3）：CMakeLists 只在 project()
    # 之前读它，写晚了镜像里仍是旧版本且无声。缺省 0.0.0-dev 只保证
    # `idf.py build` 能直接编，pack_firmware.py 会拒绝这种产物。
    idf_py \
        --project-dir "$PROJECT_DIR" \
        -B "$build_dir" \
        -D "IDF_TARGET=$target" \
        -D "SDKCONFIG=$sdkconfig" \
        -D "SDKCONFIG_DEFAULTS=$defaults" \
        -D "FW_VERSION=$FW_VERSION" \
        build
    echo "==> Firmware: $build_dir/ehome_collector.bin"

    # ---- defaults "落地核查"（2026-10-07 新增）----
    #
    # ⚠⚠ 位置很重要：必须**在 idf.py build 之后**。
    #
    # 我第一版把它放在 build **之前**（本文件的旧位置），而它要读的
    # `$sdkconfig` 正是 build 这一步才生成的 ⇒ **全新 BUILD_ROOT 永远失败、
    # 且重跑不自愈**。实测症状：
    #     cannot verify: 读不到生成配置 /tmp/.../s3-n16/sdkconfig
    #     ERROR: s3-n16: some sdkconfig.defaults lines are silently inert
    # 它把"首次构建的正常状态"误判成"写了但没生效"—— 属于**误报**，
    # 不是漏报。教训：加门禁除了问"该红时会不会红"（变异自证），
    # 还必须问"**不该红时会不会红**"（误报测试）—— 我当时只做了前者。
    #
    # 放在 build 之后还有个好处：那时 sdkconfig **一定**已生成，
    # 于是"首次构建"与"增量构建"两条路都能真的核查，**没有静默放行的残留**。
    #
    # ⚠ 必须把**本 profile 真正生效的链**（$defaults）传进去：门禁默认会并集
    #   全部 sdkconfig.defaults*，其中 esp32s3psram 只对 s3p 生效，
    #   并进来会让 s3/c6 报假问题（实测过）。链只有这里知道 ⇒ 由这里传（P4）。
    local _syms="$PROJECT_DIR/tools/check_sdkconfig_symbols.py"
    if [[ -f "$_syms" ]]; then
        echo "==> Checking that every non-n sdkconfig.defaults line actually took effect ($profile)"
        if ! "${IDF_PY_CMD[0]}" "$_syms" \
                --generated "$sdkconfig" \
                --profile "$profile" \
                --defaults "$defaults"; then
            echo "ERROR: $profile: some sdkconfig.defaults lines are silently inert (see above)" >&2
            echo "       A line that looks like it configures something but does not is worse" >&2
            echo "       than no line: the next reader believes the value is in effect." >&2
            return 1
        fi
    fi

    # A build that reports success must have produced an image.  Without this,
    # a no-op idf.py (see the launcher note above) reaches the IRAM gate and is
    # only caught there, with an error that points at the gate instead of the
    # build.
    local _elf="$build_dir/ehome_collector.elf"
    if [[ ! -f "$build_dir/ehome_collector.bin" || ! -f "$_elf" ]]; then
        echo "ERROR: $profile: build reported success but produced no image." >&2
        echo "       Missing: $build_dir/ehome_collector.bin or $_elf" >&2
        echo "       Check that idf.py actually ran (see the IDF launcher note in this script)." >&2
        return 1
    fi

    # Post-link gate: no flash-resident code may be reachable from the Wi-Fi ISR.
    #
    # The Wi-Fi ISR is registered raw (xt_set_interrupt_handler), so
    # esp_intr_noniram_disable() never masks it and it DOES run while the flash
    # cache is disabled during OTA writes.  Any flash-resident function in its
    # call graph is therefore an IllegalInstruction crash during OTA -- exactly
    # when a field device is being repaired.  See the script's header for the
    # full mechanism.  Exit 2 means "could not verify", which must not pass.
    local _chk="$PROJECT_DIR/tools/check_iram_isr_safety.py"
    if [[ -f "$_chk" ]]; then
        echo "==> Checking Wi-Fi ISR IRAM safety"
        if ! "${IDF_PY_CMD[0]}" "$_chk" "$build_dir/ehome_collector.elf"; then
            echo "ERROR: $profile: Wi-Fi ISR IRAM safety check failed (see above)" >&2
            echo "       Refusing to leave a firmware that can crash during OTA." >&2
            return 1
        fi
    fi

    # ---- 静态内存预算门禁（WS-G）----
    # 2026-10-06（D-27）接入。**此前该门禁存在但从未被任何流程调用** ——
    # 只写在 README 的"建议插入点"里，于是它谁也没保护：
    # 一个不运行的检查与没有检查等价，而且更糟 —— 它让文档看起来覆盖了这件事。
    # 现在与 ISR 检查同一处、同一形态：超预算让**构建失败**，
    # 而不是把超预算固件推给设备。
    local _budget="$PROJECT_DIR/tools/mem_budget_check.py"
    if [[ -f "$_budget" ]]; then
        echo "==> Checking static memory budget ($profile)"
        if ! "${IDF_PY_CMD[0]}" "$_budget" \
                --profile "$profile" \
                --map "$build_dir/ehome_collector.map"; then
            echo "ERROR: $profile: static memory budget check failed (see above)" >&2
            echo "       Refusing to leave a firmware that exceeds the memory budget." >&2
            return 1
        fi
    fi

    # ---- 打包 + 落 dist（方案 §7.4 / §7.5 / §7.2）----
    #
    # 为什么放在**所有门禁之后**：被 IRAM/内存/落地核查拒掉的产物不该变成
    # 一个「看起来可上传」的包。与旧 sidecar 同一理由（方案 §7.4 末段）。
    #
    # 顺序：打包 → 立即 --verify（与服务端同规则的自检）→ 把包与裸产物一起
    # 复制到 dist/<version>/<profile>/ → 重算 SHA256SUMS（必须 sort，AC-P8）。
    # 自检失败要**删包**：半个/坏包比没有更糟（上传时才 400，且更难查）。
    local _pack="$PROJECT_DIR/tools/pack_firmware.py"
    local _dist_dir="$DIST_ROOT/$FW_VERSION/$profile"
    local _bin="$build_dir/ehome_collector.bin"
    local _pkg="$_dist_dir/ehome_collector-$FW_VERSION-$profile.ehfw"
    if [[ ! -f "$_pack" ]]; then
        echo "ERROR: $profile: packer not found: $_pack" >&2
        return 1
    fi
    mkdir -p "$_dist_dir"
    echo "==> Packing $(basename "$_pkg")"
    if ! "${IDF_PY_CMD[0]}" "$_pack" \
            --bin "$_bin" \
            --sdkconfig "$sdkconfig" \
            --profile "$profile" \
            --version "$FW_VERSION" \
            --out "$_pkg"; then
        echo "ERROR: $profile: packing failed (see above)" >&2
        return 1
    fi
    echo "==> Verifying package (same rules as server)"
    if ! "${IDF_PY_CMD[0]}" "$_pack" --verify "$_pkg"; then
        rm -f "$_pkg"
        echo "ERROR: $profile: package self-check failed; the package was removed" >&2
        return 1
    fi

    # 产线烧录用裸产物：不进包（方案 §3.7 / §7.2）。缺一个就失败 ——
    # 少一个文件的 dist 目录会被当成「可以发版」。
    copy_dist_file "$_bin" "$_dist_dir/ehome_collector.bin" || return 1
    copy_dist_file "$build_dir/bootloader/bootloader.bin" \
        "$_dist_dir/bootloader.bin" || return 1
    copy_dist_file "$build_dir/partition_table/partition-table.bin" \
        "$_dist_dir/partition-table.bin" || return 1
    copy_dist_file "$build_dir/flash_args" "$_dist_dir/flash_args" || return 1
    write_sha256sums "$FW_VERSION"
}

main() {
    local profile="${1:-}"

    # --version：只解析版本、不碰 IDF（`make fw-version` 与 CI 要能单独问）。
    if [[ "$profile" == "--version" || "$profile" == "-V" ]]; then
        if [[ $# -gt 1 ]]; then
            echo "ERROR: --version takes no further arguments" >&2
            return 2
        fi
        resolve_fw_version || return $?
        print_fw_version
        return 0
    fi

    # 拒绝多余参数（2026-10-06，D-26）。
    # 原先 main 只读 $1 并**静默忽略**其余参数 —— 于是
    #   ./build_firmware.sh s3p-n16 /tmp/xxx
    # 里的 /tmp/xxx 被丢掉，构建落在默认 build/ 而不是调用方以为的目录。
    # 这种"看起来指定了、其实没生效"正是本项目反复出现的静默失效形态
    # （对照 D-03 死代码、L-05 假绿阈值）：**不报错 = 以为成功**。
    # 构建目录要用 BUILD_ROOT 环境变量指定（见 usage）。
    if [[ $# -gt 1 ]]; then
        echo "ERROR: 多余参数：'$2'（本脚本只接受一个 profile 参数）" >&2
        echo "       要改构建目录请用环境变量：BUILD_ROOT=<dir> $0 <profile>" >&2
        return 2
    fi

    if [[ "$profile" != "-h" && "$profile" != "--help" && -n "$profile" ]]; then
        init_idf_py || return $?
        local _idf_ver
        _idf_ver="$(idf_version_string)"
        echo "==> ESP-IDF: ${_idf_ver:-unknown}  (IDF_PATH=$IDF_PATH, build root=$BUILD_ROOT)"
        # 版本是构建的**输入**：解析失败就别开始编（30 分钟后才发现不值得）。
        resolve_fw_version || return $?
        print_fw_version 1
    fi

    case "$profile" in
        all)
            local item
            for item in c6-n8 c6-n16 s3-n8 s3-n16 s3p-n8 s3p-n16; do
                build_profile "$item" || return $?
            done
            # 6 档位缺一却当成全量发布 = 少发一个型号（方案 §7.6）。
            check_dist_complete "$FW_VERSION" || return $?
            ;;
        c6-n8|c6-n16|s3-n8|s3-n16|s3p-n8|s3p-n16)
            build_profile "$profile"
            ;;
        -h|--help|'')
            usage
            ;;
        *)
            echo "Unknown firmware profile: $profile" >&2
            usage >&2
            return 2
            ;;
    esac
}

main "$@"
