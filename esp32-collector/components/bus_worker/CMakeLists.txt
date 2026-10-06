idf_component_register(
    SRCS "bus_worker.c"
    INCLUDE_DIRS "include"
    # msg_handler: V3-2a 需要 hello_get_server_caps()（handler_hello.h，轻量）
    # 与 data_batch_encode()（data_batch_codec.h）。msg_handler 反向 REQUIRES
    # bus_worker（handler_data.c 读上报计数器），ESP-IDF 的 component manager
    # 会记录依赖环并按字典序选择静态库重复解析顺序（build.cmake:399 明确处理）。
    REQUIRES bus_dma scheduler esp_timer esp_hw_support msg_handler report_stats
)

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