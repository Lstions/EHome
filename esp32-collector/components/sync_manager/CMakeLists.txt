idf_component_register(SRCS "sync_manager.c"
                       INCLUDE_DIRS "."
                       # ⭐ 2026-10-08（§194）：去掉 ehome_mqtt —— MQTT 已彻底移除。
                       # ⚠⚠ 同时补上 esp_timer：sync_manager.c:20 用了 esp_timer.h
                       #     （timer 句柄 + esp_timer_get_time），此前靠 ehome_mqtt
                       #     的传递 REQUIRES 活着。删除后立刻报
                       #     "fatal error: esp_timer.h: No such file or directory"。
                       #     ⇒ 本文件 :15 早就写过这个教训，这次真的应验了。
                       REQUIRES config_mgr nvs_flash rgb_led esp_timer)

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