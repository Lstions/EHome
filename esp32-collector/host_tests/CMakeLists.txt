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
