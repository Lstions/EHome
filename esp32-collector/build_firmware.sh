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

Set BUILD_ROOT to place build directories elsewhere.
Set EXTRA_SDKCONFIG_DEFAULTS to append a semicolon-separated sdkconfig defaults file
(for example, an isolated development MQTT broker override). Supplying it regenerates
the selected profile's derived sdkconfig so the override takes effect.
EOF
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
    if [[ -n "${EXTRA_SDKCONFIG_DEFAULTS:-}" ]]; then
        defaults="$defaults;$EXTRA_SDKCONFIG_DEFAULTS"
        # sdkconfig takes precedence over sdkconfig.defaults.  An explicit
        # override (for example the isolated development MQTT broker) must
        # therefore regenerate this profile's derived sdkconfig instead of
        # silently retaining a previous production value.
        rm -f "$sdkconfig"
    fi

    mkdir -p "$build_dir"
    if [[ ! -f "$lock_file" && -f "$PROJECT_DIR/dependencies.lock" ]]; then
        cp "$PROJECT_DIR/dependencies.lock" "$lock_file"
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
