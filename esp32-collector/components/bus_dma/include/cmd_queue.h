/**
 * @file cmd_queue.h
 * @brief Shared command queue types for bus_dma scheduler and main worker
 *
 * Used by both scheduler.c (producer) and main.c (consumer/worker task).
 * Replaces the inline uart_cmd_t definitions previously duplicated in both files.
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
    CMD_WRITE = 0,
    CMD_SAMPLE = 1,
} cmd_type_t;

/* === Bus command descriptor === */
typedef struct {
    uint32_t   request_id;
    uint32_t   channel_id;
    uint8_t    bus_type;        /* 1=UART, 2=I2C, 3=SPI */
    uint8_t    tx_data[CMD_TX_MAX];
    size_t     tx_len;
    uint32_t   read_size;
    uint32_t   timeout_ms;
    cmd_type_t type;
} bus_cmd_t;

/** Global command queue handle — created in main.c, used by scheduler + worker */
extern QueueHandle_t g_cmd_queue;

#ifdef __cplusplus
}
#endif

#endif /* CMD_QUEUE_H */
