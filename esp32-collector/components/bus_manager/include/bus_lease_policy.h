#ifndef BUS_LEASE_POLICY_H
#define BUS_LEASE_POLICY_H

#include <stdbool.h>
#include <stdint.h>

/* A planner may reuse a previous physical controller only when the logical
 * channel's bus configuration is still compatible.  This tiny policy is kept
 * free of ESP-IDF types so the lease invariant can be tested on the host. */
static inline int32_t bus_lease_select_controller(int32_t current_controller,
                                                   bool compatible,
                                                   int32_t fallback_controller)
{
    return compatible && current_controller >= 0
        ? current_controller : fallback_controller;
}

#endif
