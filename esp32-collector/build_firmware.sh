#!/usr/bin/env bash
set -euo pipefail

PROJECT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BUILD_ROOT="${BUILD_ROOT:-$PROJECT_DIR/build}"

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
    idf.py \
        --project-dir "$PROJECT_DIR" \
        -B "$build_dir" \
        -D "IDF_TARGET=$target" \
        -D "SDKCONFIG=$sdkconfig" \
        -D "SDKCONFIG_DEFAULTS=$defaults" \
        build
    echo "==> Firmware: $build_dir/ehome_collector.bin"

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
        if ! python3 "$_chk" "$build_dir/ehome_collector.elf"; then
            echo "ERROR: $profile: Wi-Fi ISR IRAM safety check failed (see above)" >&2
            echo "       Refusing to leave a firmware that can crash during OTA." >&2
            return 1
        fi
    fi
}

main() {
    local profile="${1:-}"

    if [[ "$profile" != "-h" && "$profile" != "--help" && -n "$profile" ]] && ! command -v idf.py >/dev/null 2>&1; then
        echo "idf.py was not found. Source the ESP-IDF export script before building:" >&2
        echo "  . \$IDF_PATH/export.sh" >&2
        return 127
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
