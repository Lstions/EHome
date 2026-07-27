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
)
target_compile_options(channel_cmd_v2_decoder_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME channel_cmd_v2_decoder_tests COMMAND channel_cmd_v2_decoder_tests)

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
    ../components/hw_profile/hw_tables.c
    ../components/frame/frame_codec.c
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
)
target_compile_definitions(bus_worker_report_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_worker_report_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_report_tests COMMAND bus_worker_report_tests)

add_executable(bus_worker_rx_tests bus_worker_rx_tests.c
    ../components/frame/frame_codec.c
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
)
target_compile_definitions(bus_worker_rx_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(bus_worker_rx_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME bus_worker_rx_tests COMMAND bus_worker_rx_tests)

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
)
target_compile_definitions(handler_data_tests PRIVATE CONFIG_IDF_TARGET_ESP32C6=1)
target_compile_options(handler_data_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-function -Wno-unused-variable)
add_test(NAME handler_data_tests COMMAND handler_data_tests)

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

add_executable(config_apply_transaction_tests
    config_apply_transaction_tests.c
    ../main/config_apply_transaction.c
)
target_include_directories(config_apply_transaction_tests PRIVATE
    stubs
    ../main
    ../components/config_mgr
)
target_compile_options(config_apply_transaction_tests PRIVATE -Wall -Wextra -Werror)
add_test(NAME config_apply_transaction_tests COMMAND config_apply_transaction_tests)

add_executable(periph_transaction_gate_tests
    periph_transaction_gate_tests.c
    ../main/config_apply_transaction.c
    ../components/periph_owner/periph_owner.c
)
target_include_directories(periph_transaction_gate_tests PRIVATE
    stubs
    ../main
    ../components/config_mgr
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

add_executable(mqtt_event_tests mqtt_event_tests.c)
target_include_directories(mqtt_event_tests PRIVATE stubs)
target_compile_options(mqtt_event_tests PRIVATE -Wall -Wextra -Werror -Wno-unused-parameter)
add_test(NAME mqtt_event_tests COMMAND mqtt_event_tests)

add_executable(mqtt_recovery_tests mqtt_recovery_tests.c)
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
