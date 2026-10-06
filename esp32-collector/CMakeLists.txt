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
set(PROJECT_VER "2.8.0")

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

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
# Component Manager lock files contain the selected target. Keep them alongside
# each profile's build artifacts so C6 and S3 builds cannot rewrite each other.
idf_build_set_property(DEPENDENCIES_LOCK "${CMAKE_BINARY_DIR}/dependencies.lock")
project(ehome_collector)
