#include <stdio.h>

#include "bus_lease_policy.h"

static int failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        failures++; \
    } \
} while (0)

static void test_compatible_channel_keeps_lease(void)
{
    CHECK(bus_lease_select_controller(1, true, 0) == 1,
          "compatible channel must keep its current controller");
    CHECK(bus_lease_select_controller(0, true, 1) == 0,
          "UART0 is a valid lease and must not be treated as absent");
}

static void test_incompatible_channel_uses_new_plan(void)
{
    CHECK(bus_lease_select_controller(1, false, 0) == 0,
          "changed pins/baud must use the new planner result");
    CHECK(bus_lease_select_controller(-1, true, 0) == 0,
          "missing lease must use the planner result");
}

int main(void)
{
    test_compatible_channel_keeps_lease();
    test_incompatible_channel_uses_new_plan();
    if (failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", failures);
        return 1;
    }
    puts("bus_lease_policy_tests: all tests passed");
    return 0;
}
