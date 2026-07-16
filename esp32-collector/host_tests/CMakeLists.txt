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
