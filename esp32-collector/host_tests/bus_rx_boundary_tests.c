#include <stdio.h>

#include "bus_rx_boundary.h"

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s:%d: %s\n", __FILE__, __LINE__, message); \
        return 1; \
    } \
} while (0)

int main(void)
{
    CHECK(bus_rx_boundary_length(0, false, false, 0) == 0,
          "empty input must not emit a report");
    CHECK(bus_rx_boundary_length(511, false, false, 0) == 0,
          "short passive input must wait for the fixed boundary");
    CHECK(bus_rx_boundary_length(512, false, false, 0) == 512,
          "passive input must emit at the fixed boundary");
    CHECK(bus_rx_boundary_length(99, true, false, 100) == 0,
          "explicit read_size must not complete early");
    CHECK(bus_rx_boundary_length(150, true, false, 100) == 100,
          "explicit read_size must complete exactly at its length");
    CHECK(bus_rx_boundary_length(512, true, false, 0) == 512,
          "length-less pending input must use the common block boundary");
    CHECK(bus_rx_boundary_length(512, true, true, 0) == 0,
          "unknown ChannelCmdV2 length must not emit a partial final");
    puts("bus_rx_boundary_tests: all tests passed");
    return 0;
}
