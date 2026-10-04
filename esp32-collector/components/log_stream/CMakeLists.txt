idf_component_register(
    SRCS "log_stream.c" "log_capture.c" "log_capture_esp.c" "log_stream_codec.c"
    INCLUDE_DIRS "include"
    REQUIRES "frame" "esp_system" "esp_timer" "log"
)

# IDF v6 exposes esp_log(config, tag, format, ...) before UART formatting. Add
# the wrapper to the project link options directly: a component archive's
# INTERFACE library item is not guaranteed to reach the final application link.
idf_build_set_property(LINK_OPTIONS "-Wl,--wrap=esp_log" APPEND)

# EHOME_FRAME_GUARD: 组件级栈帧上限门禁。
# 见 2026-10-04 status_task 栈溢出（prvTaskCheckFreeStackSpace）：根因帧在
# components/msg_handler/handler_data.c 的 uint8_t buf[1400]，而当时门禁只覆盖
# main/app_callbacks.c 一个文件，所以从未被检查。这里扩到所有编译型组件。
#
# 必须判断目标类型：header-only 组件注册出来是 INTERFACE 库，对它设 PRIVATE
# 选项会被 CMake 拒收（may only set INTERFACE properties on INTERFACE targets）。
get_target_property(_ehome_guard_type ${COMPONENT_LIB} TYPE)
if(NOT _ehome_guard_type STREQUAL "INTERFACE_LIBRARY")
    target_compile_options(${COMPONENT_LIB} PRIVATE "-Wframe-larger-than=1024")
endif()