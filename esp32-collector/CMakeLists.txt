cmake_minimum_required(VERSION 3.16)

# 2.6.0 (2026-10-04): 崩溃诊断通道 (MSG_DIAG_REPORT 0x1E / MSG_DIAG_ACK 0x1F)。
# 次版本号 +1 而非修订号：协议新增了两种消息类型，服务端需要同步支持，
# 因此这是一次前后端必须一起升级的变更，不是纯修复。
set(PROJECT_VER "2.6.0")

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
# Component Manager lock files contain the selected target. Keep them alongside
# each profile's build artifacts so C6 and S3 builds cannot rewrite each other.
idf_build_set_property(DEPENDENCIES_LOCK "${CMAKE_BINARY_DIR}/dependencies.lock")
project(ehome_collector)
