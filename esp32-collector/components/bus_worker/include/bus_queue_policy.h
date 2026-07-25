#ifndef BUS_QUEUE_POLICY_H
#define BUS_QUEUE_POLICY_H

#include <stdbool.h>
#include <stdint.h>

/* Maximum number of consecutive control commands before a ready sample gets
 * one turn.  The same policy is used by UART0/UART1/UART2/SPI/I2C workers. */
#define BUS_CONTROL_BURST_MAX 4U

typedef enum {
    BUS_QUEUE_DECISION_NONE = 0,
    BUS_QUEUE_DECISION_CONTROL,
    BUS_QUEUE_DECISION_SAMPLE,
} bus_queue_decision_t;

static inline bus_queue_decision_t bus_queue_choose(bool control_ready,
                                                     bool sample_ready,
                                                     uint8_t *control_burst)
{
    if (!control_burst) return BUS_QUEUE_DECISION_NONE;
    if (*control_burst >= BUS_CONTROL_BURST_MAX && sample_ready) {
        *control_burst = 0;
        return BUS_QUEUE_DECISION_SAMPLE;
    }
    if (control_ready) {
        if (*control_burst < BUS_CONTROL_BURST_MAX) (*control_burst)++;
        return BUS_QUEUE_DECISION_CONTROL;
    }
    if (sample_ready) {
        *control_burst = 0;
        return BUS_QUEUE_DECISION_SAMPLE;
    }
    return BUS_QUEUE_DECISION_NONE;
}

#endif
