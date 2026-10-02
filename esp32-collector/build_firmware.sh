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
  BUILD_ROOT   place build directories elsewhere.
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

profile_settings() {
    case "$1" in
        c6-n8)  printf '%s %s\n' esp32c6 n8 ;;
        c6-n16) printf '%s %s\n' esp32c6 n16 ;;
        s3-n8)  printf '%s %s\n' esp32s3 n8 ;;
        s3-n16) printf '%s %s\n' esp32s3 n16 ;;
        *) return 1 ;;
    esac
}

build_profile() {
    local profile="$1"
    local settings target flash_profile build_dir sdkconfig defaults lock_file

    settings="$(profile_settings "$profile")" || {
        echo "Unknown firmware profile: $profile" >&2
        usage >&2
        return 2
    }
    read -r target flash_profile <<<"$settings"

    build_dir="$BUILD_ROOT/$profile"
    sdkconfig="$build_dir/sdkconfig"
    lock_file="$build_dir/dependencies.lock"
    defaults="$PROJECT_DIR/sdkconfig.defaults;$PROJECT_DIR/config/flash/$flash_profile.defaults"

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

    defaults="$PROJECT_DIR/sdkconfig.defaults;$PROJECT_DIR/config/flash/$flash_profile.defaults;$_broker_defaults"
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
    )
    if [[ -f "$sdkconfig" ]]; then
        local _drift=0 _sym _want _have _want_all=""
        # Concatenate the defaults files in order; last assignment wins.
        while IFS= read -r _f; do
            [[ -f "$_f" ]] && _want_all+="$(cat "$_f")"$'\n'
        done < <(printf '%s\n' "$defaults" | tr ';' '\n')
        for _sym in "${_guard_symbols[@]}"; do
            _want="$(printf '%s\n' "$_want_all" | grep -E "^${_sym}=|^# ${_sym} is not set" | tail -1)"
            _have="$(grep -E "^${_sym}=|^# ${_sym} is not set" "$sdkconfig" | tail -1)"
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
}

main() {
    local profile="${1:-}"

    if [[ "$profile" != "-h" && "$profile" != "--help" && -n "$profile" ]]; then
        init_idf_py || return $?
        echo "==> ESP-IDF: $(idf_py --version 2>/dev/null || echo unknown)  (IDF_PATH=$IDF_PATH)"
    fi

    case "$profile" in
        all)
            local item
            for item in c6-n8 c6-n16 s3-n8 s3-n16; do
                build_profile "$item"
            done
            ;;
        c6-n8|c6-n16|s3-n8|s3-n16)
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
