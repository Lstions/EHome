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

usage() {
    cat <<'EOF'
Usage: ./build_firmware.sh <profile|all>

Profiles:
  c6-n8    ESP32-C6 with 8MB flash
  c6-n16   ESP32-C6 with 16MB flash
  s3-n8    ESP32-S3 with 8MB flash
  s3-n16   ESP32-S3 with 16MB flash
  s3p-n8   ESP32-S3 with PSRAM + 8MB flash
  s3p-n16  ESP32-S3 with PSRAM + 16MB flash
  all      Build all profiles

MQTT broker (required):
  The broker URL is compiled into the firmware and cannot be changed at
  runtime, so it is never committed.  Provide it one of these ways:

    1. config/mqtt-broker.defaults        (recommended, gitignored)
       cp config/mqtt-broker.defaults.example config/mqtt-broker.defaults
       then edit the address.

    2. EXTRA_SDKCONFIG_DEFAULTS=<file>    (one-off override)

  A build without a real broker address fails on purpose: the built-in
  default is an unroutable placeholder (TEST-NET-1), and shipping a firmware
  that cannot reach its broker is worse than refusing to build.

Other environment:
  BUILD_ROOT   place build directories elsewhere.  A build directory
               cannot be reused across ESP-IDF versions: this script
               refuses rather than letting idf.py abort mid-build.
EOF
}

# Placeholder brokers that must never reach a flashable image.
#
# Matched as a prefix so the whole reserved block is caught, not just one host:
# 192.0.2.0/24 is TEST-NET-1 (RFC 5737) and 198.51.100.0/24 is TEST-NET-2; both
# are documentation-only and guaranteed unroutable. 10.42.0.1 is the historical
# Kconfig default.
PLACEHOLDER_BROKER_PREFIXES=(
    "mqtt://192.0.2."
    "mqtts://192.0.2."
    "mqtt://198.51.100."
    "mqtts://198.51.100."
    "mqtt://10.42.0.1:"
)

# Where a per-deployment broker may be configured (first match wins).
BROKER_DEFAULTS_FILE="$PROJECT_DIR/config/mqtt-broker.defaults"

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

broker_from_file() {
    # Echo the CONFIG_COLLECTOR_MQTT_BROKER_URL value from the given file, if any.
    [[ -f "$1" ]] || return 1
    grep -E '^CONFIG_COLLECTOR_MQTT_BROKER_URL=' "$1" | tail -1 | sed -E 's/^[^=]+="?([^"]*)"?$/\1/'
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

    # ---- MQTT broker resolution -------------------------------------------
    # The broker is compiled in, so an unset or placeholder value produces a
    # device that cannot connect. Resolve it explicitly and refuse to build
    # until a real address is supplied.
    local broker="" broker_src=""
    if [[ -n "${EXTRA_SDKCONFIG_DEFAULTS:-}" ]]; then
        broker="$(broker_from_file "${EXTRA_SDKCONFIG_DEFAULTS%%;*}" || true)"
        [[ -n "$broker" ]] && broker_src="EXTRA_SDKCONFIG_DEFAULTS"
    fi
    if [[ -z "$broker" ]]; then
        broker="$(broker_from_file "$BROKER_DEFAULTS_FILE" || true)"
        [[ -n "$broker" ]] && broker_src="$BROKER_DEFAULTS_FILE"
    fi

    if [[ -z "$broker" ]]; then
        cat >&2 <<EOF
ERROR: $profile: no MQTT broker configured.

The broker URL is compiled into the firmware and has no runtime override, so
it is not committed. Set it for this deployment:

    cp config/mqtt-broker.defaults.example config/mqtt-broker.defaults
    \$EDITOR config/mqtt-broker.defaults        # set mqtt://<host>:<port>

config/mqtt-broker.defaults is gitignored, so the address stays out of git.
Alternatively pass EXTRA_SDKCONFIG_DEFAULTS=<file> for a one-off build.
EOF
        return 1
    fi

    for _ph in "${PLACEHOLDER_BROKER_PREFIXES[@]}"; do
        if [[ "$broker" == "$_ph"* ]]; then
            cat >&2 <<EOF
ERROR: $profile: MQTT broker is still the placeholder ($broker).

That address is TEST-NET-1 / a documentation default and is not routable, so
the resulting device could never reach a broker. Configure the real broker in
config/mqtt-broker.defaults (gitignored) or via EXTRA_SDKCONFIG_DEFAULTS.

Source of this value: $broker_src
EOF
            return 1
        fi
    done

    case "$broker" in
        mqtt://*|mqtts://*) ;;
        *)
            echo "ERROR: $profile: broker must start with mqtt:// or mqtts:// (got '$broker')" >&2
            return 1
            ;;
    esac

    echo "==> Broker: $broker  (from $broker_src)"

    # Apply the resolved broker last so it wins over the committed placeholder.
    local _broker_defaults="$build_dir/.broker.defaults"
    mkdir -p "$build_dir"
    printf 'CONFIG_COLLECTOR_MQTT_BROKER_URL="%s"\n' "$broker" > "$_broker_defaults"

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

    # 型号开关是 profile 的显式语义（s3p = 带 PSRAM 的型号），与 broker 同理注入
    # 链尾，防止"profile 叫 s3p、Kconfig 却报非 PSRAM 型号"的静默漂移。
    # 板上是否真有 PSRAM 仍由 app_state.c 运行时 heap_caps 探测决定，二者缺一不可。
    if [[ "$model_psram" == "y" ]]; then
        _model_switch_defaults="$build_dir/.model.defaults"
        printf 'CONFIG_COLLECTOR_PSRAM=y\n' > "$_model_switch_defaults"
        defaults="$defaults;$_model_switch_defaults"
    fi

    defaults="$defaults;$PROJECT_DIR/config/flash/$flash_profile.defaults;$_broker_defaults"
    if [[ -n "${EXTRA_SDKCONFIG_DEFAULTS:-}" ]]; then
        defaults="$defaults;$EXTRA_SDKCONFIG_DEFAULTS"
        # sdkconfig takes precedence over sdkconfig.defaults.  An explicit
        # override (for example an isolated development broker) must therefore
        # regenerate this profile's derived sdkconfig instead of silently
        # retaining a previous value.
        rm -f "$sdkconfig"
    fi

    if [[ ! -f "$lock_file" && -f "$PROJECT_DIR/dependencies.lock" ]]; then
        cp "$PROJECT_DIR/dependencies.lock" "$lock_file"
    fi

    # The broker is an explicit per-build input, so a derived sdkconfig that
    # still holds a different broker is simply out of date: regenerate it
    # rather than making the user clean the profile by hand.  This is what makes
    # a broker change actually take effect (kconfgen would otherwise keep the
    # previously written user-set value).
    if [[ -f "$sdkconfig" ]]; then
        local _have_broker
        _have_broker="$(broker_from_file "$sdkconfig" || true)"
        if [[ -n "$_have_broker" && "$_have_broker" != "$broker" ]]; then
            echo "==> Broker changed ($_have_broker -> $broker); regenerating $sdkconfig"
            rm -f "$sdkconfig"
        fi
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
    # CONFIG_COLLECTOR_MQTT_BROKER_URL is deliberately absent: it is handled
    # above, because an explicit per-build input should take effect rather than
    # be reported as drift.
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
        # MQTT 客户端任务栈：sdkconfig.defaults 给 8192，而陈旧的派生
        # sdkconfig 会把它钉在旧的 6144 上。构建日志无任何提示，
        # 设备在 MQTT 收包路径上栈溢出 —— 同属"静默钉住旧值"形态。
        CONFIG_MQTT_TASK_STACK_SIZE
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

    # ---- defaults "落地核查"（2026-10-07 新增）----
    #
    # 上面的 _drift 只查一个**手写名单**里那几个符号有没有被陈旧 sdkconfig 钉住。
    # 它查不到另一类更安静的问题：**defaults 里写了、但在这个 profile 的依赖集下
    # Kconfig 把它丢掉**（符号存在，所以 check_sdkconfig_symbols 的旧判据也绿）。
    # 实测两例：
    #   · CONFIG_ESP_WIFI_DYNAMIC_TX_BUFFER_NUM=16 —— 仅 s3p 两个 profile 上失效
    #     （SPIRAM_TRY_ALLOCATE_WIFI_LWIP=y 使 IDF 的该选择分支不可达）；
    #   · CONFIG_DEBUG_TCP_PORT=8088 —— depends on DEBUG_TCP_ENABLED，而后者=n。
    #
    # ⚠ 必须把**本 profile 真正生效的链**（$defaults）传进去：
    #   门禁默认会并集全部 sdkconfig.defaults*，其中 esp32s3psram 只对 s3p 生效，
    #   并进来会让 s3/c6 报 6 条假问题（实测过）。链只有这里知道，所以由这里传（P4）。
    #
    # 为什么挂在**构建期**而不是只放 ctest：ctest 拿不到"这次构建实际用了哪些
    # defaults、生成了什么 sdkconfig"这两个事实，只有构建时才知道。
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

    echo "==> Building $profile (target=$target, flash=$flash_profile)"
    idf_py \
        --project-dir "$PROJECT_DIR" \
        -B "$build_dir" \
        -D "IDF_TARGET=$target" \
        -D "SDKCONFIG=$sdkconfig" \
        -D "SDKCONFIG_DEFAULTS=$defaults" \
        build
    echo "==> Firmware: $build_dir/ehome_collector.bin"

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
}

main() {
    local profile="${1:-}"

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
    fi

    case "$profile" in
        all)
            local item
            for item in c6-n8 c6-n16 s3-n8 s3-n16 s3p-n8 s3p-n16; do
                build_profile "$item"
            done
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
