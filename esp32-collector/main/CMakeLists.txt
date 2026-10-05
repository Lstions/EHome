# main/linker.lf keeps the Wi-Fi blob's logger in IRAM so the raw-registered
# Wi-Fi ISR stays flash-cache-safe during OTA writes (see the file's header).
idf_component_register(SRCS "main.c"
                              "mem_guard.c"
                              "app_state.c"
                              "app_callbacks.c"
                              "config_apply_transaction.c"
                              "crash_diag.c"
                              "boot_guard.c"
                              "panic_capture.c"
                              "hello_handshake.c"
                              "hello_handshake_runtime.c"
                              "hello_handshake_state.c"
                       INCLUDE_DIRS "."
                       REQUIRES freertos nvs_flash
                       frame wifi_mgr transport ehome_mqtt ehome_tcp msg_handler
                       config_mgr scheduler ota rgb_led factory_reset
                       sync_manager bus_dma hw_profile dma_pool
                       bus_worker bus_manager log_stream gpio_ctrl pwm_ctrl periph_owner
                       LDFRAGMENTS "linker.lf")

# Inject version from top-level CMakeLists.txt PROJECT_VER as a compile definition.
# This is the single source of truth — app_state.c reads EHOME_PROJECT_VER instead
# of hardcoding FIRMWARE_VERSION.
target_compile_definitions(${COMPONENT_LIB} PRIVATE EHOME_PROJECT_VER="${PROJECT_VER}")

# ConfigManifest rollback snapshots/workspaces must stay off callback task stacks.
set_source_files_properties(app_callbacks.c PROPERTIES
    COMPILE_OPTIONS "-Wframe-larger-than=1024")

# The Hello runtime keeps genuinely lock-free 32-bit atomics even when IDF
# enables its global S32C1I/PSRAM workaround (CONFIG_SPIRAM on ESP32/S3):
# esp_libc/project_include.cmake adds -mdisable-hardware-atomics to every TU,
# which drops ATOMIC_INT_LOCK_FREE from 2 to 1.  This is the same escape hatch
# IDF uses for src/port/xtensa/stdatomic_s32c1i.c
# (esp_libc/CMakeLists.txt:129-131): re-enable hardware atomics for the one
# file that must stay lock-free.  Source-level COMPILE_OPTIONS come after the
# global toolchain flags, so -mno-disable-hardware-atomics wins.
#
# Safety: s_runtime is pinned to internal RAM with DRAM_ATTR
# (hello_handshake.c), and hello_runtime_init_with_seed() asserts at runtime
# that it was never placed in the PSRAM address window.  Xtensa S32C1I is valid
# on internal RAM only; IDF's esp_stdatomic.h:55 uses the same range test to
# pick its critical-section fallback for external addresses.
if(CONFIG_STDATOMIC_S32C1I_SPIRAM_WORKAROUND)
    set_source_files_properties(hello_handshake_runtime.c PROPERTIES
        COMPILE_OPTIONS "-mno-disable-hardware-atomics")
endif()

# Crash capture (v2.6): intercept esp_panic_handler to snapshot the exception
# frame into RTC_NOINIT before IDF's own panic path runs.
#
# Why --wrap: ESP-IDF 6.0 exposes no panic-hook registration API. The call site
# (components/esp_system/port/panic_handler.c:258) is a cross-TU call, so the
# linker wrapper reliably intercepts it. __wrap_* captures and then forwards to
# __real_*, so panic printing and reset behaviour are unchanged.
#
# Why not espcoredump: it needs a new "coredump" partition, and a partition-table
# change cannot be delivered over OTA — it needs a physical reflash, which is not
# possible for a remotely deployed node.
idf_build_set_property(LINK_OPTIONS "-Wl,--wrap=esp_panic_handler" APPEND)
