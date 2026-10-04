idf_component_register(
    SRCS "bus_manager.c"
    INCLUDE_DIRS "include"
    REQUIRES bus_dma bus_worker config_mgr dma_pool hw_profile
)

# EHOME_FRAME_GUARD: 组件级栈帧上限门禁。
#
# 本组件允许的帧上限见下方阈值，理由是"调用方任务的栈已按此帧尺寸配置"。
# 门禁本身的价值：2026-10-04 的 status_task 栈溢出（PANIC in
# prvTaskCheckFreeStackSpace，task="status"）根因就是 handler_data.c 的
# uint8_t buf[1400]，而当时 -Wframe-larger-than 只加在 main/app_callbacks.c
# 一个文件上，那个帧从未被检查。
#
# 必须判断目标类型：header-only 组件注册出来是 INTERFACE 库，对它设 PRIVATE
# 选项会被 CMake 拒收（may only set INTERFACE properties on INTERFACE targets）。
get_target_property(_ehome_guard_type ${COMPONENT_LIB} TYPE)
if(NOT _ehome_guard_type STREQUAL "INTERFACE_LIBRARY")
    target_compile_options(${COMPONENT_LIB} PRIVATE "-Wframe-larger-than=3072")
endif()
