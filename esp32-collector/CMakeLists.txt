cmake_minimum_required(VERSION 3.16)

# =============================================================================
# Multi-board partition table selection
# =============================================================================
# Set COLLECTOR_BOARD variant before configuring the project:
#   cmake -B build -DCOLLECTOR_BOARD=c6_4m    # ESP32-C6 4MB (default)
#   cmake -B build -DCOLLECTOR_BOARD=c6_8m    # ESP32-C6 8MB
#   cmake -B build -DCOLLECTOR_BOARD=c6_16m   # ESP32-C6 16MB
#   cmake -B build -DCOLLECTOR_BOARD=s3_8m    # ESP32-S3 8MB
#   cmake -B build -DCOLLECTOR_BOARD=s3_16m   # ESP32-S3 16MB
# Or override the CSV filename directly:
#   cmake -B build -DCOLLECTOR_PARTITION_TABLE=partitions_c6_16m.csv
#
# If neither is set, partitions.csv in the source tree is used as-is.

set(_pt_file "")
if(COLLECTOR_PARTITION_TABLE)
    # Direct filename override
    set(_pt_file "${COLLECTOR_PARTITION_TABLE}")
elseif(COLLECTOR_BOARD)
    # Named board variant
    set(_pt_file "partitions_${COLLECTOR_BOARD}.csv")
endif()

if(_pt_file)
    if(EXISTS "${CMAKE_CURRENT_SOURCE_DIR}/${_pt_file}")
        message(STATUS "[collector] Partition table: ${_pt_file}")
        file(COPY "${CMAKE_CURRENT_SOURCE_DIR}/${_pt_file}"
             DESTINATION "${CMAKE_CURRENT_SOURCE_DIR}"
             ONLY_IF_DIFFERENT)
    else()
        message(WARNING "[collector] Partition table not found: ${_pt_file}, using default")
    endif()
endif()

set(PROJECT_VER "2.5.5" CACHE STRING "Project version")

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(ehome_collector)
