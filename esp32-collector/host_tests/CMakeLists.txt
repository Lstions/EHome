cmake_minimum_required(VERSION 3.16)
project(ehome_collector_host_tests C)

enable_testing()
set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)

add_executable(protocol_log_tests
    protocol_log_tests.c
    ../components/frame/frame_codec.c
    ../components/msg_handler/data_report_codec.c
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
