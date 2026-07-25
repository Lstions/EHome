#include <stdint.h>
#include <stdio.h>

#include "bus_queue_policy.h"

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s:%d: %s\n", __FILE__, __LINE__, message); \
        return 1; \
    } \
} while (0)

int main(void)
{
    uint8_t burst = 0;
    CHECK(bus_queue_choose(true, true, &burst) == BUS_QUEUE_DECISION_CONTROL && burst == 1,
          "control must win the first turn");
    CHECK(bus_queue_choose(true, true, &burst) == BUS_QUEUE_DECISION_CONTROL && burst == 2,
          "control burst count did not advance");
    CHECK(bus_queue_choose(true, true, &burst) == BUS_QUEUE_DECISION_CONTROL && burst == 3,
          "control burst count did not advance twice");
    CHECK(bus_queue_choose(true, true, &burst) == BUS_QUEUE_DECISION_CONTROL && burst == 4,
          "control burst maximum was reached incorrectly");
    CHECK(bus_queue_choose(true, true, &burst) == BUS_QUEUE_DECISION_SAMPLE && burst == 0,
          "ready sample must get a turn after four controls");
    CHECK(bus_queue_choose(false, true, &burst) == BUS_QUEUE_DECISION_SAMPLE && burst == 0,
          "sample must run when control is empty");
    CHECK(bus_queue_choose(true, false, &burst) == BUS_QUEUE_DECISION_CONTROL && burst == 1,
          "control must run when sample is empty");
    CHECK(bus_queue_choose(false, false, &burst) == BUS_QUEUE_DECISION_NONE,
          "empty queues must not produce a command");
    puts("bus_queue_policy_tests: all tests passed");
    return 0;
}
