#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "legacy_write_guard.h"

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s:%d: %s\n", __FILE__, __LINE__, message); \
        return 1; \
    } \
} while (0)

int main(void)
{
    uint8_t payload[129] = {0};
    CHECK(legacy_write_args_valid(1, payload, 128, 256, 30000, 128),
          "valid boundary command rejected");
    CHECK(!legacy_write_args_valid(0, payload, 1, 0, 1000, 128),
          "channel zero accepted");
    CHECK(!legacy_write_args_valid(1, payload, 129, 0, 1000, 128),
          "oversized TX accepted");
    CHECK(!legacy_write_args_valid(1, NULL, 1, 0, 1000, 128),
          "missing payload pointer accepted");
    CHECK(!legacy_write_args_valid(1, payload, 1, 257, 1000, 128),
          "oversized RX accepted");
    CHECK(!legacy_write_args_valid(1, payload, 1, 1, 0, 128),
          "zero timeout accepted");
    CHECK(!legacy_write_args_valid(1, payload, 1, 1, 30001, 128),
          "oversized timeout accepted");
    CHECK(legacy_write_route_valid(LEGACY_WRITE_BUS_UART, 0), "UART0 rejected");
    CHECK(legacy_write_route_valid(LEGACY_WRITE_BUS_UART, 1), "UART1 rejected");
    CHECK(legacy_write_route_valid(LEGACY_WRITE_BUS_UART, 2), "UART2 rejected");
    CHECK(!legacy_write_route_valid(LEGACY_WRITE_BUS_UART, 3), "invalid UART route accepted");
    CHECK(legacy_write_route_valid(LEGACY_WRITE_BUS_I2C, 0), "I2C rejected");
    CHECK(legacy_write_route_valid(LEGACY_WRITE_BUS_SPI, 0), "SPI rejected");
    CHECK(!legacy_write_route_valid(4, 0), "unsupported bus accepted");
    puts("legacy_write_guard_tests: all tests passed");
    return 0;
}
