#ifndef DRIVER_UART_H
#define DRIVER_UART_H

#include <stdint.h>

typedef int uart_port_t;
#define UART_NUM_0 0
#define UART_NUM_1 1
#define UART_NUM_2 2
#define UART_NUM_MAX 3

/* UART event types */
typedef enum {
    UART_DATA = 0,
    UART_BREAK = 1,
    UART_BUFFER_FULL = 2,
    UART_FIFO_OVF = 3,
    UART_FRAME_ERR = 4,
    UART_PARITY_ERR = 5,
    UART_EVENT_MAX = 6,
    UART_PATTERN_DET = 7,
} uart_event_type_t;

typedef struct {
    uart_event_type_t type;
    size_t size;
    uint8_t *data;
} uart_event_t;

/* Minimal stubs for UART driver functions referenced by bus_worker.c */
static inline void uart_flush_input(uart_port_t port) { (void)port; }

#endif

