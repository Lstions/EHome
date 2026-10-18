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
                              "device_op_wiring.c"
                              "device_link_wiring.c"
                              "device_link_handshake.c"
                              # ⚠ session_transport.c **必须**在这里：它的 IDF 胶水段
                              # （session_transport_create）不在宿主测试里编，
                              # 漏掉它 ⇒ 宿主全绿，而 IDF 链接直接
                              # "undefined reference to session_transport_create"。
                              # （2026-10-07 实测踩到：宿主绿、目标红。）
                              #
                              # ⭐ 2026-10-08（§194）：uplink_arbiter.c 与
                              # uplink_mqtt_transport.c **已删除** —— 它们的唯一职责是
                              # "在 MQTT 与 3.0 之间选一条以防双发"，MQTT 移除后无对象。
                              "session_transport.c"
                       INCLUDE_DIRS "."
                       REQUIRES freertos nvs_flash device_op
                       session tls_esp variant
                       # SNTP 接线（task-11）：sntp_mgr 是状态机，lwip 提供
                       # esp_sntp.h，tls_guard 提供「时间可信」的唯一判据。
                       sntp_mgr tls_guard lwip
                       # ⭐ §194：ehome_mqtt 与 transport_sel 已删除（MQTT 彻底移除）
                       frame wifi_mgr transport ehome_tcp msg_handler
                       config_mgr scheduler ota rgb_led factory_reset
                       sync_manager bus_dma hw_profile dma_pool
                       bus_worker bus_manager log_stream gpio_ctrl pwm_ctrl periph_owner
                       netpolicy report_stats
                       LDFRAGMENTS "linker.lf")

# Inject version from top-level CMakeLists.txt PROJECT_VER as a compile definition.
# This is the single source of truth — app_state.c reads EHOME_PROJECT_VER instead
# of hardcoding FIRMWARE_VERSION.
target_compile_definitions(${COMPONENT_LIB} PRIVATE EHOME_PROJECT_VER="${PROJECT_VER}")

# 可选：EHOME_MEM_DIAG=1 打开启动里程碑/逐总线/逐步骤的堆探针（默认关闭，
# 正常启动会多打十几行日志）。
#
# 为什么做成 CMake 选项而不是像以前那样注释里写"需要时自己加一行"：
# main.c / app_callbacks.c / bus_manager.c 的注释都让人手动改 CMakeLists ——
# 那会让**源码树产生未提交的临时改动**，而这些探针恰恰是排障时最需要、
# 也最容易把"临时改动"误提交的一类代码。改成一行 -D 后，源码树永远是交付态。
#
# 用法：cmake -DEHOME_MEM_DIAG=1 ...（或 idf.py -DEHOME_MEM_DIAG=1 build）
# 开关本身定义在顶层 CMakeLists.txt（project() 之前），因为 components/bus_manager
# 的 [busheap] 探针也看这个宏 —— 只在这里定义会让 -DEHOME_MEM_DIAG=1 静默地
# 只打开 main 组件的一半探针。

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
# ── 可选：把设备证书编译进 app 分区（EHOME_EMBED_DEVICE_CERTS）────────────
#
# 用途：给"还跑着 MQTT 固件、NVS 里没有服务端当前 PKI"的现场设备做
# 一次性 OTA 迁移。OTA 只写 app 分区、碰不到 NVS，所以证书必须随固件走。
# 详见 device_link_wiring.c 里 devlink_load_embedded_certs 的长注释。
#
# 为什么用 target_add_binary_data 而不是 C 数组字面量：
#   证书是 PEM 文本，手工转义成 C 字符串既易错又不可读；这个 CMake 函数
#   把文件直接放进 .rodata，符号名 = _binary_<文件名去点>_start/_end。
#
# 用法（只对需要的那台设备构建一次，**不要**提交生成物）：
#   cmake -DEHOME_EMBED_CERT_DIR=/path/to/pki/DD84 ... build
#   目录里需有 ca.pem / cert.pem / key.pem 三个文件。
#
# 缺省（未指定 EHOME_EMBED_CERT_DIR）= 不内嵌，行为与既有构建逐字节一致。
if(DEFINED EHOME_EMBED_CERT_DIR)
    if(NOT EXISTS "${EHOME_EMBED_CERT_DIR}/ca.pem" OR
       NOT EXISTS "${EHOME_EMBED_CERT_DIR}/cert.pem" OR
       NOT EXISTS "${EHOME_EMBED_CERT_DIR}/key.pem")
        message(FATAL_ERROR
            "EHOME_EMBED_CERT_DIR=${EHOME_EMBED_CERT_DIR} 缺少 ca.pem/cert.pem/key.pem")
    endif()
    # ⚠ 必须把 .S 放进组件的 SRCS，**不能** 用 target_add_binary_data
    # 指向 COMPONENT_LIB，也不能建 OBJECT 库。
    #
    # 实测踩到两个坑（都试过）：
    #   ① target_add_binary_data(COMPONENT_LIB ...) —— .S 编出的目标文件
    #      只是静态库 main.a 的一个成员，而静态库成员只有在被显式引用时才
    #      被链接器拉入 ⇒ nm 能看到符号，最终链接却 undefined reference。
    #   ② add_library(... OBJECT) + target_link_libraries —— IDF 的 ldgen
    #      会对每个 target 求 $<TARGET_FILE:...>，而 OBJECT 库没有该属性
    #      ⇒ "Target is not an executable or library" 配置期失败。
    #
    # 唯一可靠路径：把生成好的 .S 作为普通源文件交给 idf_component_register。
    # 所以这里要求调用方预先生成三个 .S（见 tools/gen_embed_certs.sh），
    # 本文件只负责把它们编进组件。缺文件时**明确失败**而不是静默跳过 ——
    # 静默跳过会让"固件里没有证书"在设备失联时才被发现。
    set(_embed_asm "")
    foreach(_pair "ca_pem" "cert_pem" "key_pem")
        set(_f "${EHOME_EMBED_CERT_DIR}/gen/${_pair}.S")
        if(NOT EXISTS "${_f}")
            message(FATAL_ERROR
                "缺少内嵌证书汇编文件：${_f}\n"
                "先运行：tools/gen_embed_certs.sh ${EHOME_EMBED_CERT_DIR}")
        endif()
        list(APPEND _embed_asm "${_f}")
    endforeach()
    target_sources(${COMPONENT_LIB} PRIVATE ${_embed_asm})
    target_compile_definitions(${COMPONENT_LIB} PRIVATE EHOME_EMBED_DEVICE_CERTS=1)
    message(STATUS "EHOME: 内嵌设备证书 <- ${EHOME_EMBED_CERT_DIR}")
endif()