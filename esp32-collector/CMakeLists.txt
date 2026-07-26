cmake_minimum_required(VERSION 3.16)

set(PROJECT_VER "2.5.21")

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
# Component Manager lock files contain the selected target. Keep them alongside
# each profile's build artifacts so C6 and S3 builds cannot rewrite each other.
idf_build_set_property(DEPENDENCIES_LOCK "${CMAKE_BINARY_DIR}/dependencies.lock")
project(ehome_collector)
