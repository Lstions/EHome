cmake_minimum_required(VERSION 3.16)

# 2.6.1 (2026-10-04): 两个现场复位缺陷修复。
#   1) log_tx 任务栈 1536 -> 4096。栈溢出（"A stack overflow in task log_tx"）
#      在 publish -> MQTT -> lwIP 这条链上击穿哨兵直接 abort。
#   2) bus_worker 命令队列 set 计数漂移：成员队列被直接 xQueueReceive，
#      绕过了 xQueueSelectFromSet，set 内部句柄计数只增不减，涨到 uxLength(24)
#      后触发 prvNotifyQueueSetContainer 断言 -> abort。
# 现场表现同为"约 50 次/小时复位"，S3 实测 8 分钟 0 复位。
# 2.6.0 (2026-10-04): 崩溃诊断通道 (MSG_DIAG_REPORT 0x1E / MSG_DIAG_ACK 0x1F)。
# 次版本号 +1 而非修订号：协议新增了两种消息类型，服务端需要同步支持，
# 因此这是一次前后端必须一起升级的变更，不是纯修复。
#
# ---- 版本注入（2026-11-19 起）------------------------------------------------
# 唯一真源是构建脚本传入的 -D FW_VERSION；缺省值只保证「直接 idf.py build 也能编」，
# 但它不是可发布产物：pack_firmware.py 会拒绝 0.0.0-dev。
# 落点要求（方案 §7.3 / 评审 A-5）：必须写在 include(project.cmake) 与 project()
# **之前** —— 写晚了镜像里仍是旧版本，而且无声。
# （上面 2.6.x 那段版本史自 2026-11-19 起只是历史：版本不再手写在 CMakeLists 里。）
set(FW_VERSION "" CACHE STRING "firmware version injected by build_firmware.sh")
if(FW_VERSION STREQUAL "")
    set(PROJECT_VER "0.0.0-dev")
else()
    set(PROJECT_VER "${FW_VERSION}")
endif()
if(NOT PROJECT_VER MATCHES "^[0-9]+\\.[0-9]+\\.[0-9]+$")
    message(FATAL_ERROR "PROJECT_VER=${PROJECT_VER} is not X.Y.Z; pass -DFW_VERSION=<x.y.z>")
endif()

# 诊断探针开关必须在这里（project() 之前）定义：main/CMakeLists.txt 的
# target_compile_definitions 只作用于 main 组件，而 EHOME_MEM_DIAG 还门控
# components/bus_manager 的 [busheap] 逐总线差分。只用 main 那份时，
# -DEHOME_MEM_DIAG=1 会**静默地**只打开一半探针（[bootheap] 有、[busheap] 没有）——
# 2026-10-06 实测就是这样（strings 查 ELF：[bootheap]=1、[busheap]=0）。
# 放在 project() 之前 + 用 CACHE 变量，所有组件才都看得到；默认 OFF，交付态不变。
set(EHOME_MEM_DIAG OFF CACHE BOOL "Enable boot/[busheap] heap milestone probes")
if(EHOME_MEM_DIAG)
    add_compile_definitions(EHOME_MEM_DIAG=1)
endif()

# task-34 ⭐ 归因工具（**诊断镜像专用**，默认 OFF）：
# 打开 ESP-IDF 的 heap trace，把每次分配连同**调用栈**记下来 ⇒ 直接点名
# "谁吃掉了 internal 连续块"，不再依赖"数值吻合"式推断。
#
# 为什么必须有它：本卡为同一个问题连续四次"算一个数 + 改一个候选"，四次全空
# （任务栈 / SSL_IN_CONTENT_LEN / SSL_OUT_CONTENT_LEN / MBEDTLS_DYNAMIC_BUFFER）。
# 其中第二次最说明问题：砍掉 8192 B 的 IN 上限，内部只回收 **284 B**
# ⇒ **改一个候选可以完全不动堆** ⇒ 枚举候选的信息量极低。
#
# ⚠ 它需要 HEAP_TRACING_STANDALONE（见 sdkconfig.defaults.diag），且会拖慢分配，
#   绝不能进交付配置。默认 OFF ⇒ 交付镜像与"10 个门禁"均不受影响。
set(EHOME_HEAP_TRACE OFF CACHE BOOL "Enable heap-trace allocation attribution (diagnostic only)")
if(EHOME_HEAP_TRACE)
    add_compile_definitions(EHOME_HEAP_TRACE=1)
endif()

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
# Component Manager lock files contain the selected target. Keep them alongside
# each profile's build artifacts so C6 and S3 builds cannot rewrite each other.
idf_build_set_property(DEPENDENCIES_LOCK "${CMAKE_BINARY_DIR}/dependencies.lock")
project(ehome_collector)

# ============================================================================
# 配置期断言：OTA 自动回滚开关必须是 y（M3a/T3a.1 配套，2026-11-18）
# ============================================================================
#
# 为什么在 CMake 配置期拦，而不是在 main.c 里写 #error：
#   · 配置期断言对**所有**构建入口生效（build_firmware.sh、裸 `idf.py build`、
#     CI 脚本），而 main.c 的 #error 只覆盖"真的编到那个文件"的路径；
#   · 它**早于编译**：不必等几分钟编译完才发现要重来；
#   · 它不受 host_tests 的 include 路径影响（host_tests 是独立 CMake 工程，
#     根本不经过本文件）。
#
# 为什么必须硬失败、且不留环境变量开关：
#   CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE 编进 **bootloader.bin**，而 OTA 只写
#   应用分区。缺了它，升级后即使新镜像启动即崩、即使应用在 30s 内没有确认，
#   引导器**也不会**退回旧分区 ⇒ "升级失败无法自动回滚"（设备卡在坏镜像上），
#   而构建日志、内存数字、其它门禁全部正常 —— 这是本仓"静默失效"家族里后果
#   最贵的一条：现场设备变砖，且事后看日志一切正常。
#
# 真实案例（2026-11-18 实测）：esp32-collector/sdkconfig 是 2026-10-04 生成的
#   陈旧派生配置，第 770 行是 "# CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE is not
#   set"；裸 `idf.py build` 读它 ⇒ 无回滚固件；`idf.py reconfigure` **也不会**
#   修（kconfgen 把"与已加载 defaults 不同的现值"当 user value，sdkconfig 胜
#   出）。build_firmware.sh 的 _guard_symbols 只保护"按脚本构建"这一条路，
#   本断言补上另一条（也是最容易被"我自己编一下试试"走的那条）。
if(NOT "${CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE}" STREQUAL "y")
    message(FATAL_ERROR
        "CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE 必须是 y（当前解析值：'${CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE}'）。\n"
        "这个开关编译进 bootloader.bin，而 OTA 只写应用分区：缺了它，升级后新镜像启动即崩、或应用 30s 内未确认时，引导器不会退回旧分区（设备卡在坏镜像上），而构建日志与其它门禁全部正常。\n"
        "本次配置：SDKCONFIG=${SDKCONFIG}\n"
        "          SDKCONFIG_DEFAULTS=${SDKCONFIG_DEFAULTS}\n"
        "该 sdkconfig 里此符号为 n 或缺失。典型原因：陈旧派生 sdkconfig 压过了 sdkconfig.defaults 的 y（实测 idf.py reconfigure 也不会修）。\n"
        "修复（任选其一）：\n"
        "  1) ./build_firmware.sh <profile>   —— 走带 defaults 链与守卫的正规路径（如 c6-n16 / s3-n16）\n"
        "  2) rm -f sdkconfig sdkconfig.old && idf.py build   —— 让 IDF 从 sdkconfig.defaults 重新生成\n"
        "  3) 或把上面 SDKCONFIG 文件里的该行改成 CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE=y\n"
        "本断言不提供关闭开关：它保护的是『升级失败能不能自动回退』——现场最需要、也最难从日志里发现的能力。\n")
endif()
