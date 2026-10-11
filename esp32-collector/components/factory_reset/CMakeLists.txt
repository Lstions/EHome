idf_component_register(SRCS "factory_reset.c" "factory_reset_namespaces.c"
                       INCLUDE_DIRS "include"
                       REQUIRES esp_driver_gpio nvs_flash rgb_led freertos)

# ⚠ factory_reset_namespaces.c **刻意不依赖任何 IDF 头**（纯数据 + 纯判定）
# ⇒ 它可以直接进宿主测试（见 host_tests/factory_reset_namespaces_tests.c）。
# 这是把白名单从 factory_reset.c 抽出来的**主要目的**：让"擦哪些命名空间"
# 这个破坏性判定可被宿主 100% 覆盖，而不是藏在依赖 nvs_flash 的胶水里。

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