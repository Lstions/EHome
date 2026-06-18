/**
 * @file cmd_queue.h
 * @brief Shared command queue types for bus_dma scheduler and worker tasks
 *
 * Used by scheduler.c (producer), cmd_task (consumer), and rx_task (UART listener).
 */

#ifndef CMD_QUEUE_H
#define CMD_QUEUE_H

#include <stdint.h>
#include <stddef.h>
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Queue sizing === */
#define CMD_QUEUE_DEPTH  16
#define CMD_TX_MAX       128

/* === Command types === */
typedef enum {
    CMD_WRITE = 0,    /* WriteCommand from backend (TX only, no RX wait) */
    CMD_SAMPLE = 1,   /* Periodic sample from scheduler (TX + optional delay) */
} cmd_type_t;

/* === Bus command descriptor ===
 *
 * UART channels:  cmd_task does TX only; rx_task handles RX independently.
 * SPI/I2C channels: cmd_task does full transact (TX+RX atomic).
 *
 * delay_ms: for CMD_SAMPLE, milliseconds to wait between TX and letting
 *           rx_task pick up the response.  0 = no delay.
 *           Not used for CMD_WRITE (WriteCommand returns immediately).
 */
typedef struct {
    uint32_t   request_id;                    /* Correlates WriteResponse/DataReport */
    uint32_t   channel_id;
    uint8_t    bus_type;                      /* 1=UART, 2=I2C, 3=SPI */
    uint8_t    tx_data[CMD_TX_MAX];
    size_t     tx_len;
    uint32_t   delay_ms;                      /* TX→RX delay (sample only) */
    cmd_type_t type;
} bus_cmd_t;

#ifdef __cplusplus
}
#endif

#endif /* CMD_QUEUE_H */
