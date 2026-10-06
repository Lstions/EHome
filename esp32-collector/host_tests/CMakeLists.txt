cmake_minimum_required(VERSION 3.16)
project(ehome_collector_host_tests C)

enable_testing()
set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

add_executable(protocol_log_tests
    protocol_log_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_report_codec.c
    ../components/log_stream/log_capture.c
    ../components/log_stream/log_stream_codec.c
)
target_include_directories(protocol_log_tests PRIVATE
    ../components/frame
    ../components/msg_handler
    ../components/log_stream
    ../components/log_stream/include
    ../components/scheduler)
target_compile_options(protocol_log_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME protocol_log_tests COMMAND protocol_log_tests)

add_executable(writecmd_decoder_tests
    writecmd_decoder_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/handler_writecmd.c
)
target_include_directories(writecmd_decoder_tests PRIVATE
    stubs
    ../components/frame
    ../components/msg_handler
    ../components/config_mgr
)
target_compile_options(writecmd_decoder_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME writecmd_decoder_tests COMMAND writecmd_decoder_tests)

add_executable(channel_cmd_v2_decoder_tests
    channel_cmd_v2_decoder_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/handler_channel_cmd_v2.c
)
target_include_directories(channel_cmd_v2_decoder_tests PRIVATE
    stubs
    ../components/frame
    ../components/msg_handler
    ../components/config_mgr
)
target_compile_options(channel_cmd_v2_decoder_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME channel_cmd_v2_decoder_tests COMMAND channel_cmd_v2_decoder_tests)

add_executable(channel_cmd_v2_slot_tests
    channel_cmd_v2_slot_tests.c
    channel_cmd_v2_slot_app_stubs.c
    ../components/frame/frame_codec.c
)
target_include_directories(channel_cmd_v2_slot_tests PRIVATE
    stubs
    ../components/frame
    ../components/msg_handler
    ../components/config_mgr
)
target_compile_options(channel_cmd_v2_slot_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME channel_cmd_v2_slot_tests COMMAND channel_cmd_v2_slot_tests)

add_executable(legacy_write_guard_tests legacy_write_guard_tests.c)
target_include_directories(legacy_write_guard_tests PRIVATE
    ../components/bus_manager/include
)
target_compile_options(legacy_write_guard_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME legacy_write_guard_tests COMMAND legacy_write_guard_tests)

add_executable(bus_queue_policy_tests bus_queue_policy_tests.c)
target_include_directories(bus_queue_policy_tests PRIVATE
    ../components/bus_worker/include)
target_compile_options(bus_queue_policy_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME bus_queue_policy_tests COMMAND bus_queue_policy_tests)

add_executable(bus_rx_boundary_tests bus_rx_boundary_tests.c)
target_include_directories(bus_rx_boundary_tests PRIVATE
    ../components/bus_worker/include)
target_compile_options(bus_rx_boundary_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME bus_rx_boundary_tests COMMAND bus_rx_boundary_tests)

add_executable(bus_lease_policy_tests bus_lease_policy_tests.c)
target_include_directories(bus_lease_policy_tests PRIVATE
    ../components/bus_manager/include)
target_compile_options(bus_lease_policy_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME bus_lease_policy_tests COMMAND bus_lease_policy_tests)
add_executable(hw_profile_resource_report_tests
    hw_profile_resource_report_tests.c
    ../components/frame/frame_codec.c
    ../components/hw_profile/hw_profile.c
    ../components/hw_profile/hw_tables.c
)
target_include_directories(hw_profile_resource_report_tests PRIVATE
    stubs
    ../components/frame
    ../components/hw_profile/include
    ../components/dma_pool/include
    ../components/config_mgr
)
target_compile_definitions(hw_profile_resource_report_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
)
target_compile_options(hw_profile_resource_report_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME hw_profile_resource_report_tests COMMAND hw_profile_resource_report_tests)

# 保留引脚契约：S3 / C6 各编一份（见文件头注释）。
#
# 为什么必须两份：此处此前所有 host test 都只编 C6，而 2026-10-04 的现场
# 事故发生在 S3 的 GPIO0 上 —— C6 上 GPIO0 是普通引脚，所以那份 C6 用例
# 永远绿，S3 的缺陷在 CI 里连编译都没编译过。两个 target 用不同机制挡住
# BOOT 引脚（S3 靠 reserved 标记、C6 靠不在表里），必须两种都验。
foreach(_ehome_pin_target ESP32S3 ESP32C6)
    string(TOLOWER "${_ehome_pin_target}" _ehome_pin_suffix)
    set(_ehome_pin_exe "reserved_pin_contract_${_ehome_pin_suffix}_tests")
    add_executable(${_ehome_pin_exe}
        reserved_pin_contract_tests.c
        ../components/frame/frame_codec.c
        ../components/hw_profile/hw_profile.c
        ../components/hw_profile/hw_tables.c
    )
    target_include_directories(${_ehome_pin_exe} PRIVATE
        stubs
        ../components/frame
        ../components/hw_profile/include
        ../components/dma_pool/include
        ../components/config_mgr
    )
    target_compile_definitions(${_ehome_pin_exe} PRIVATE
        CONFIG_IDF_TARGET_${_ehome_pin_target}=1
    )
    target_compile_options(${_ehome_pin_exe} PRIVATE -Wall -Wextra -Werror)
    add_test(NAME ${_ehome_pin_exe} COMMAND ${_ehome_pin_exe})
endforeach()

add_executable(peripheral_identity_tests
    peripheral_identity_tests.c
    ../components/pwm_ctrl/pwm_ctrl.c
    ../components/gpio_ctrl/gpio_ctrl.c
    ../components/periph_owner/periph_owner.c
    ../components/hw_profile/hw_tables.c
)
target_include_directories(peripheral_identity_tests PRIVATE
    stubs
    ../components/pwm_ctrl/include
    ../components/gpio_ctrl/include
    ../components/periph_owner/include
    ../components/config_mgr
    ../components/hw_profile/include
    ../components/dma_pool/include
)
target_compile_definitions(peripheral_identity_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
)
target_compile_options(peripheral_identity_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME peripheral_identity_tests COMMAND peripheral_identity_tests)

add_executable(config_manifest_pwm_tests
    config_manifest_pwm_tests.c
    ../components/config_mgr/config_mgr.c
    ../components/frame/frame_codec.c
)
target_include_directories(config_manifest_pwm_tests PRIVATE
    stubs
    ../components/config_mgr
    ../components/frame
    ../components/dma_pool/include
)
target_compile_definitions(config_manifest_pwm_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
)
target_compile_options(config_manifest_pwm_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function)
add_test(NAME config_manifest_pwm_tests COMMAND config_manifest_pwm_tests)

add_executable(dma_pool_contract_tests
    dma_pool_contract_tests.c
    ../components/dma_pool/dma_pool.c
    ../components/frame/frame_codec.c
)
target_include_directories(dma_pool_contract_tests PRIVATE
    stubs
    ../components/dma_pool/include
    ../components/frame
)
target_compile_options(dma_pool_contract_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME dma_pool_contract_tests COMMAND dma_pool_contract_tests)

# ---- P0-P5 multi-bus tests ----

add_executable(bus_manager_manifest_tests bus_manager_manifest_tests.c
    stubs/heap_stub_impl.c
    ../components/hw_profile/hw_profile.c
    ../components/hw_profile/hw_tables.c
    ../components/frame/frame_codec.c
)
target_include_directories(bus_manager_manifest_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_manager/include
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/scheduler
)
target_compile_definitions(bus_manager_manifest_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_manager_manifest_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function)
add_test(NAME bus_manager_manifest_tests COMMAND bus_manager_manifest_tests)

add_executable(bus_dma_lease_tests bus_dma_lease_tests.c
    ../components/hw_profile/hw_profile.c
    ../components/hw_profile/hw_tables.c
    ../components/frame/frame_codec.c
    stubs/heap_stub_impl.c
)
target_include_directories(bus_dma_lease_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_manager/include
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/scheduler
)
target_compile_definitions(bus_dma_lease_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_dma_lease_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function)
add_test(NAME bus_dma_lease_tests COMMAND bus_dma_lease_tests)

add_executable(bus_dma_tests bus_dma_tests.c
    ../components/hw_profile/hw_tables.c
    stubs/heap_stub_impl.c
)
target_include_directories(bus_dma_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_dma/include
    ../components/dma_pool/include
    ../components/hw_profile/include
)
target_compile_definitions(bus_dma_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
    CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG=1
)
target_compile_options(bus_dma_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_dma_tests COMMAND bus_dma_tests)

add_executable(scheduler_route_tests scheduler_route_tests.c
    ../components/scheduler/scheduler_health.c
    ../components/hw_profile/hw_tables.c
    ../components/frame/frame_codec.c
    stubs/heap_stub_impl.c
)
target_include_directories(scheduler_route_tests PRIVATE
    stubs
    stubs/rom
    ../components/scheduler
    ../components/bus_dma/include
    ../components/bus_worker/include
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
)
target_compile_definitions(scheduler_route_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(scheduler_route_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME scheduler_route_tests COMMAND scheduler_route_tests)

add_executable(bus_worker_report_tests bus_worker_report_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_batch_codec.c
    stubs/hello_caps_stub.c
)
target_include_directories(bus_worker_report_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/bus_manager/include
    ../components/scheduler
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/msg_handler
)
target_compile_definitions(bus_worker_report_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_worker_report_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_report_tests COMMAND bus_worker_report_tests)

add_executable(bus_worker_rx_tests bus_worker_rx_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_batch_codec.c
    stubs/hello_caps_stub.c
)
target_include_directories(bus_worker_rx_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/bus_manager/include
    ../components/scheduler
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/msg_handler
)
target_compile_definitions(bus_worker_rx_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_worker_rx_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_rx_tests COMMAND bus_worker_rx_tests)

add_executable(bus_worker_batch_tests bus_worker_batch_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_batch_codec.c
    stubs/hello_caps_stub.c
)
target_include_directories(bus_worker_batch_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/bus_manager/include
    ../components/scheduler
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/msg_handler
)
target_compile_definitions(bus_worker_batch_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1 EHOME_TEST_CLOCK_STEP=1)
target_compile_options(bus_worker_batch_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_batch_tests COMMAND bus_worker_batch_tests)

add_executable(perf_metrics_encode_tests perf_metrics_encode_tests.c
    ../components/frame/frame_codec.c
)
target_include_directories(perf_metrics_encode_tests PRIVATE
    stubs
    ../components/frame
)
target_compile_options(perf_metrics_encode_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function)
add_test(NAME perf_metrics_encode_tests COMMAND perf_metrics_encode_tests)

add_executable(handler_data_tests handler_data_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_report_codec.c
    ../components/msg_handler/data_batch_codec.c
)
target_include_directories(handler_data_tests PRIVATE
    stubs
    stubs/rom
    ../components/frame
    ../components/msg_handler
    ../components/config_mgr
    ../components/sync_manager
    ../components/scheduler
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/ota
    ../components/wifi_mgr
)
target_compile_definitions(handler_data_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(handler_data_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME handler_data_tests COMMAND handler_data_tests)

# Full-stack regression for defect 2 (2026-09-30): an RX timeout must reach
# the EdgeDeviceHealth sub-frame the server receives.  This target compiles the
# REAL bus_worker.c (rx_task timeout path), the REAL scheduler.c (owns the
# reported counter) and the REAL handler_data.c (encodes the wire frame)
# together.  bus_worker_rx_tests.c only proves a notify hook is called; this
# proves the encoded StatusReport actually changes.
add_executable(rx_health_e2e_tests rx_health_e2e_tests.c
    ../components/scheduler/scheduler_health.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_report_codec.c
    ../components/msg_handler/data_batch_codec.c
    stubs/heap_stub_impl.c
    stubs/hello_caps_stub.c
)
target_include_directories(rx_health_e2e_tests PRIVATE
    stubs
    stubs/rom
    ../components/frame
    ../components/msg_handler
    ../components/config_mgr
    ../components/sync_manager
    ../components/scheduler
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/ota
    ../components/wifi_mgr
)
target_compile_definitions(rx_health_e2e_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
    # 本 target 同时编入 bus_worker.c 与真正的 handler_data.c，因此必须关掉
    # bus_worker.c 里的弱默认编码器定义，否则重定义。
    EHOME_HOST_TEST_REAL_DATA_BATCH_ENCODER=1)
target_compile_options(rx_health_e2e_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME rx_health_e2e_tests COMMAND rx_health_e2e_tests)

add_executable(app_state_tests app_state_tests.c
    ../components/hw_profile/hw_tables.c
    ../components/dma_pool/dma_pool.c
    ../components/frame/frame_codec.c
)
target_include_directories(app_state_tests PRIVATE
    stubs
    stubs/rom
    ../main
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/bus_manager/include
    ../components/scheduler
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/transport/include
)
target_compile_definitions(app_state_tests PRIVATE
    CONFIG_IDF_TARGET_ESP32C6=1
    EHOME_PROJECT_VER="test"
    CONFIG_IDF_TARGET="esp32c6"
)
target_compile_options(app_state_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME app_state_tests COMMAND app_state_tests)

add_executable(periph_config_apply_tests
    periph_config_apply_tests.c
    ../components/msg_handler/periph_config_apply.c
    ../components/periph_owner/periph_owner.c
)
target_include_directories(periph_config_apply_tests PRIVATE
    stubs
    ../components/msg_handler
    ../components/config_mgr
    ../components/gpio_ctrl/include
    ../components/pwm_ctrl/include
    ../components/periph_owner/include
)
target_compile_options(periph_config_apply_tests PRIVATE -Wall -Wextra -Werror)
target_link_options(periph_config_apply_tests PRIVATE
    -Wl,--wrap=calloc
    -Wl,--wrap=free
)
set_source_files_properties(
    ../components/msg_handler/periph_config_apply.c
    PROPERTIES COMPILE_OPTIONS "-Wframe-larger-than=256"
)
add_test(NAME periph_config_apply_tests COMMAND periph_config_apply_tests)

# WS-E: config_apply_transaction.c now includes the transaction arena impl and
# consults mem_guard_can_start() before each step, so both TUs must be linked.
add_executable(config_apply_transaction_tests
    config_apply_transaction_tests.c
    stubs/heap_stub_impl.c
    ../main/config_apply_transaction.c
    ../main/mem_guard.c
    ../components/frame/frame_codec.c
)
target_include_directories(config_apply_transaction_tests PRIVATE
    stubs
    ../main
    ../components/config_mgr
    ../components/frame
)
target_compile_options(config_apply_transaction_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME config_apply_transaction_tests COMMAND config_apply_transaction_tests)

add_executable(periph_transaction_gate_tests
    periph_transaction_gate_tests.c
    stubs/heap_stub_impl.c
    ../main/config_apply_transaction.c
    ../main/mem_guard.c
    ../components/frame/frame_codec.c
    ../components/periph_owner/periph_owner.c
)
target_include_directories(periph_transaction_gate_tests PRIVATE
    stubs
    ../main
    ../components/config_mgr
    ../components/frame
    ../components/periph_owner/include
)
target_compile_definitions(periph_transaction_gate_tests PRIVATE _POSIX_C_SOURCE=200809L)
target_compile_options(periph_transaction_gate_tests PRIVATE -Wall -Wextra -Werror)
target_link_libraries(periph_transaction_gate_tests PRIVATE pthread)
add_test(NAME periph_transaction_gate_tests COMMAND periph_transaction_gate_tests)

add_executable(log_capture_esp_tests
    log_capture_esp_tests.c
    ../components/log_stream/log_capture.c
    ../components/log_stream/log_capture_esp.c
)
target_include_directories(log_capture_esp_tests PRIVATE
    wrapper_stubs
    ../components/log_stream/include
)
target_compile_definitions(log_capture_esp_tests PRIVATE LOG_CAPTURE_ESP_HOST_TEST=1)
target_compile_options(log_capture_esp_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME log_capture_esp_tests COMMAND log_capture_esp_tests)

add_executable(log_capture_concurrency_tests
    log_capture_concurrency_tests.c
    ../components/log_stream/log_capture.c
    ../components/log_stream/log_capture_esp.c
)
target_include_directories(log_capture_concurrency_tests PRIVATE
    wrapper_stubs
    ../components/log_stream/include
)
target_compile_definitions(log_capture_concurrency_tests PRIVATE
    _POSIX_C_SOURCE=200809L
    LOG_CAPTURE_ESP_HOST_TEST=1
)
target_compile_options(log_capture_concurrency_tests PRIVATE -Wall -Wextra -Werror)
target_link_libraries(log_capture_concurrency_tests PRIVATE pthread)
add_test(NAME log_capture_concurrency_tests COMMAND log_capture_concurrency_tests)

add_executable(log_stream_lifecycle_tests
    log_stream_lifecycle_tests.c
    ../components/log_stream/log_capture.c
    ../components/log_stream/log_stream.c
)
target_include_directories(log_stream_lifecycle_tests PRIVATE
    lifecycle_stubs
    ../components/log_stream/include
)
target_compile_definitions(log_stream_lifecycle_tests PRIVATE _POSIX_C_SOURCE=200809L)
target_compile_options(log_stream_lifecycle_tests PRIVATE -Wall -Wextra -Werror)
target_link_options(log_stream_lifecycle_tests PRIVATE -Wl,--wrap=log_capture_set_level)
target_link_libraries(log_stream_lifecycle_tests PRIVATE pthread)
add_test(NAME log_stream_lifecycle_tests COMMAND log_stream_lifecycle_tests)

add_executable(mqtt_event_tests mqtt_event_tests.c
    stubs/heap_stub_impl.c
)
target_include_directories(mqtt_event_tests PRIVATE stubs)
target_compile_options(mqtt_event_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-parameter)
add_test(NAME mqtt_event_tests COMMAND mqtt_event_tests)

add_executable(mqtt_recovery_tests mqtt_recovery_tests.c
    stubs/heap_stub_impl.c
)
target_include_directories(mqtt_recovery_tests PRIVATE stubs ../main)
target_compile_options(mqtt_recovery_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-parameter)
add_test(NAME mqtt_recovery_tests COMMAND mqtt_recovery_tests)

add_executable(hello_handshake_tests
    hello_handshake_tests.c
    ../main/hello_handshake.c
    ../main/hello_handshake_runtime.c
    ../main/hello_handshake_state.c
    ../components/msg_handler/handler_hello.c
    ../components/frame/frame_codec.c
)
target_include_directories(hello_handshake_tests PRIVATE
    hello_stubs
    ../main
    ../components/msg_handler
    ../components/frame
)
target_compile_definitions(hello_handshake_tests PRIVATE HELLO_HANDSHAKE_HOST_TEST=1)
target_compile_options(hello_handshake_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME hello_handshake_tests COMMAND hello_handshake_tests)

add_executable(frame_codec_boundary_tests
    frame_codec_boundary_tests.c
    ../components/frame/frame_codec.c
)
target_include_directories(frame_codec_boundary_tests PRIVATE
    ../components/frame
)
target_compile_options(frame_codec_boundary_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME frame_codec_boundary_tests COMMAND frame_codec_boundary_tests)

add_executable(wifi_mgr_rssi_tests wifi_mgr_rssi_tests.c)
target_include_directories(wifi_mgr_rssi_tests PRIVATE
    wifi_stubs
    stubs
    ../components/wifi_mgr
)
target_compile_definitions(wifi_mgr_rssi_tests PRIVATE
    CONFIG_COLLECTOR_WIFI_SSID=""
    CONFIG_COLLECTOR_WIFI_PASSWORD=""
)
target_compile_options(wifi_mgr_rssi_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-parameter)
add_test(NAME wifi_mgr_rssi_tests COMMAND wifi_mgr_rssi_tests)

# 崩溃诊断上报/确认的线协议契约（0x1E / 0x1F）。
# 这条链路在正常运行时永不执行，只有真崩溃时才走 —— 正因如此它最容易腐烂，
# 必须由测试守住字段号与 accepted 语义。
add_executable(crash_diag_codec_tests
    crash_diag_codec_tests.c
    ../components/frame/frame_codec.c
)
target_include_directories(crash_diag_codec_tests PRIVATE
    ../components/frame
)
target_compile_options(crash_diag_codec_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME crash_diag_codec_tests COMMAND crash_diag_codec_tests)

# 运行期内存水位门禁（WS-G）。判错的代价：内存充足时拒绝重操作，或低水位
# 事件风暴 —— 两者都难从串口反推，所以用可控堆值把边界钉在宿主测试里。
# mem_guard_tests.c 自带 heap_caps_* 强定义，不需要 heap_stub_impl.c。
add_executable(mem_guard_tests
    mem_guard_tests.c
    ../components/frame/frame_codec.c
)
target_include_directories(mem_guard_tests PRIVATE
    stubs
    ../components/frame
    ../main
)
target_compile_definitions(mem_guard_tests PRIVATE CONFIG_IDF_TARGET_ESP32S3=1)
target_compile_options(mem_guard_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function)
add_test(NAME mem_guard_tests COMMAND mem_guard_tests)

# WS-E transaction arena: fixed 4 KiB bump allocator backing the config
# transaction workspace (design §2).  The test includes the implementation
# header directly, mirroring how main/config_apply_transaction.c compiles it.
# No heap_stub_impl.c: the internal-RAM branch uses a static .bss block.
add_executable(config_tx_arena_tests
    config_tx_arena_tests.c
)
target_include_directories(config_tx_arena_tests PRIVATE
    ../main
    ../components/config_mgr
)
target_compile_options(config_tx_arena_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME config_tx_arena_tests COMMAND config_tx_arena_tests)

# WS-F phase-2 prototype: ConfigManifest as raw bytes + offset index.
# Host-only proof of the v3 storage model; not compiled into firmware.
add_executable(v3_manifest_view_tests
    v3_manifest_view_tests.c
    v3_manifest_view.c
    ../components/frame/frame_codec.c
)
target_include_directories(v3_manifest_view_tests PRIVATE
    .
    ../components/frame
    ../components/config_mgr
    stubs
)
target_compile_options(v3_manifest_view_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME v3_manifest_view_tests COMMAND v3_manifest_view_tests)

# WS-C PSRAM placement contract: prove prefer-PSRAM / fall-back-internal /
# both-exhausted-NULL for collector_mem.h.  The stub directory is listed
# BEFORE stubs/ because only it declares heap_caps_malloc/free; compiling with
# CONFIG_COLLECTOR_PSRAM=1 selects the firmware-side branch.
add_executable(collector_mem_tests
    collector_mem_tests.c
)
target_include_directories(collector_mem_tests PRIVATE
    collector_mem_stubs
    ../components/config_mgr
)
target_compile_options(collector_mem_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME collector_mem_tests COMMAND collector_mem_tests)

# V3-2a DataBatch(0x20)：契约 §2 的 7 条不变量 + 逐字节锚点。
#
# 为什么单列一个 target 而不是塞进现有用例：这个文件同时是**后端对锚的
# 参照实现**（v3-backend 的 handler_data_batch.go 用同一组 hex 常量），
# 它必须能独立编译、独立运行、独立失败，才能被两边共同引用。
add_executable(data_batch_codec_tests
    data_batch_codec_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_batch_codec.c
)
target_include_directories(data_batch_codec_tests PRIVATE
    ../components/frame
    ../components/msg_handler
)
target_compile_options(data_batch_codec_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME data_batch_codec_tests COMMAND data_batch_codec_tests)

# V3-2a 聚合路径（契约 §3）：直接编入 bus_worker.c 驱动 report_try_data_batch()。
#
# 与 bus_worker_report_tests 分开的理由：那份用例覆盖 0x03 路径的池/队列
# 语义，本份只覆盖"能力位门控 + 聚合决策"。两者共享 #include bus_worker.c
# 的模式，但断言集互不重叠，分开后失败定位更快。
add_executable(bus_worker_data_batch_tests bus_worker_data_batch_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_batch_codec.c
)
target_include_directories(bus_worker_data_batch_tests PRIVATE
    stubs
    stubs/rom
    ../components/bus_worker/include
    ../components/bus_dma/include
    ../components/bus_manager/include
    ../components/scheduler
    ../components/config_mgr
    ../components/dma_pool/include
    ../components/hw_profile/include
    ../components/frame
    ../components/msg_handler
)
target_compile_definitions(bus_worker_data_batch_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_worker_data_batch_tests PRIVATE
    -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_data_batch_tests COMMAND bus_worker_data_batch_tests)
# ---- 防回退门禁：诊断打印必须与门禁同口径（task-7，2026-10-06）----
#
# 为什么这条门禁必须存在：CTest 的其它 43 个用例**结构上不可能**发现这个缺陷。
# 裸 MALLOC_CAP_8BIT 只出现在诊断打印里 —— 它不参与任何判决，也不改变任何
# 返回值，所以一行日志把 s3p 的内部 RAM（~23 KiB）打成 PSRAM 总量（8.25 MB）
# 时，所有的运行时断言仍然全绿。这是一个**只有静态检查能覆盖**的缺陷类。
#
# 判据（为什么 grep 式检查就足够，不需要真机）：本仓只有
# sdkconfig.defaults.esp32s3psram 打开 CONFIG_SPIRAM_USE_MALLOC=y，而
# MALLOC_CAP_8BIT 之所以危险，正是因为它在该 profile 下悄悄变成"内部 + PSRAM"
# 的合计口径。因此规则"每个 MALLOC_CAP_8BIT 必须与 MALLOC_CAP_INTERNAL 或
# MALLOC_CAP_SPIRAM 出现在同一语句"在**所有** profile 下都成立，且与缺陷
# 一一对应 —— 静态可判，无需刷机。
#
# 两个 add_test 共用一份 runner，因此扫描逻辑只有一处实现：
#   1) 自测：对合成输入断言两条规则**真的会红**。否则正则写错时门禁会以
#      "全绿"的形式失效（这正是它要防的那类假绿）。
#   2) 全树扫描 main/ + components/ + host_tests/。
# 解释器：**configure 时解析成绝对路径**，ctest 不依赖调用 shell 的 PATH。
#
# 找不到 Python 时**不静默跳过** —— 那会以"没有这条测试"的形式留下一个洞，
# 而本门禁防的正是静默失效。改为注册一条必定失败的用例，把原因打出来：
# 红得响，但不阻塞其它用例。
find_program(EHOME_GATE_PYTHON NAMES python3 python)
set(EHOME_DIAG_SCAN "${CMAKE_CURRENT_SOURCE_DIR}/diag_heap_metric_scan.py")

if(EHOME_GATE_PYTHON)
    add_test(NAME diag_heap_metric_gate
        COMMAND "${EHOME_GATE_PYTHON}" "${EHOME_DIAG_SCAN}")
    set_tests_properties(diag_heap_metric_gate PROPERTIES
        FAIL_REGULAR_EXPRESSION "diag-metric gate FAILED")
    # 自测：断言两条规则**真的会红**。正则写错时门禁会以"全绿"形式失效，
    # 这条用例专门防那种假绿。
    add_test(NAME diag_heap_metric_gate_selftest
        COMMAND "${EHOME_GATE_PYTHON}" "${EHOME_DIAG_SCAN}" --self-test)
    set_tests_properties(diag_heap_metric_gate_selftest PROPERTIES
        FAIL_REGULAR_EXPRESSION "FAIL")
else()
    add_test(NAME diag_heap_metric_gate
        COMMAND "${CMAKE_COMMAND}" -E echo
            "diag-metric gate cannot run: no python3 found at configure time; "
            "install python3 and re-run cmake, or the diagnostic-metric regression "
            "gate is silently absent")
    set_tests_properties(diag_heap_metric_gate PROPERTIES WILL_FAIL TRUE)
endif()

# ---- L-02: 调用点去重（2026-10-06）----
#
# 为什么单独一条：transport_registry_tests.c 只钉住**判据函数**的语义，
# 变异自证发现把它改坏会红，但把**调用点**的去重分支去掉时它**仍然全绿** ——
# 也就是"修复本身"没有用例盯住。本用例补这个缺口，钉住"每失败帧 1 条 PF"
# 这一可观测后果（修复前是 2 条，实测 PF/NT = 1.996）。
add_executable(l02_publish_dedup_callsite_tests l02_publish_dedup_callsite_tests.c)
target_compile_options(l02_publish_dedup_callsite_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME l02_publish_dedup_callsite_tests COMMAND l02_publish_dedup_callsite_tests)
set_tests_properties(l02_publish_dedup_callsite_tests PROPERTIES
    FAIL_REGULAR_EXPRESSION "FAILED")

# ---- L-02 去重判据的回归锁（task-9，2026-10-06）----
#
# 直接编译**真实**的 components/transport/transport.c（不用替身），
# 锁住 transport_registry_has_type() 的语义 —— 它是 L-02 修复
# （"广播已含 MQTT 适配器时不再重复发布"）唯一的判据。
# 该判据若反向失效，后果是"静默丢消息"或"重复发布照旧"，两者在台架之外
# 都很难察觉，因此用宿主用例钉死。
add_executable(transport_registry_tests
    transport_registry_tests.c
    ../components/transport/transport.c
)
target_include_directories(transport_registry_tests PRIVATE
    stubs
    ../components/transport/include
)
target_compile_options(transport_registry_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME transport_registry_tests COMMAND transport_registry_tests)
# ---- 3.0 骨架：variant / link（设计文档「3.0-实施设计-骨架与增量」§2） ----
add_executable(variant_tests
    variant_tests.c
    ../components/variant/variant.c
)
target_include_directories(variant_tests PRIVATE ../components/variant/include)
target_compile_options(variant_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME variant_tests COMMAND variant_tests)

add_executable(link_tests
    link_tests.c
    ../components/link/link.c
)
target_include_directories(link_tests PRIVATE ../components/link/include)
target_compile_options(link_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME link_tests COMMAND link_tests)

add_executable(wire_tests
    wire_tests.c
    ../components/wire/wire.c
)
target_include_directories(wire_tests PRIVATE ../components/wire/include)
target_compile_options(wire_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME wire_tests COMMAND wire_tests)

add_executable(msgcodec_tests
    msgcodec_tests.c
    ../components/msgcodec/msgcodec.c
)
target_include_directories(msgcodec_tests PRIVATE ../components/msgcodec/include)
target_compile_options(msgcodec_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME msgcodec_tests COMMAND msgcodec_tests)

add_executable(dispatch_tests
    dispatch_tests.c
    ../components/dispatch/dispatch.c
)
target_include_directories(dispatch_tests PRIVATE ../components/dispatch/include)
target_compile_options(dispatch_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME dispatch_tests COMMAND dispatch_tests)

add_executable(net_policy_tests
    net_policy_tests.c
    ../components/netpolicy/net_policy.c
)
target_include_directories(net_policy_tests PRIVATE ../components/netpolicy/include)
target_compile_options(net_policy_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME net_policy_tests COMMAND net_policy_tests)

# D-03 回归门禁：TCP 启动必须可从 case 到达（防"不可达代码"复现）
find_package(Python3 COMPONENTS Interpreter REQUIRED)
add_test(NAME tcp_start_reachable
    COMMAND ${Python3_EXECUTABLE} ${CMAKE_CURRENT_SOURCE_DIR}/../tools/check_tcp_start_reachable.py)

# D-09 定帧契约（需要 esp 头 stub 才能宿主编译 transport.c）
add_executable(transport_framing_tests
    transport_framing_tests.c
    ../components/transport/transport.c
    ../components/netpolicy/net_policy.c
)
target_include_directories(transport_framing_tests PRIVATE
    ../components/transport/include
    ../components/netpolicy/include
    hello_stubs
)
target_compile_options(transport_framing_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME transport_framing_tests COMMAND transport_framing_tests)

# D-07 健康计数写入规则（纯函数，无需 stub）
add_executable(scheduler_health_tests
    scheduler_health_tests.c
    ../components/scheduler/scheduler_health.c
)
target_include_directories(scheduler_health_tests PRIVATE ../components/scheduler)
target_compile_options(scheduler_health_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME scheduler_health_tests COMMAND scheduler_health_tests)
