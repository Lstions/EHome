#ifndef DRIVER_UART_H
#define DRIVER_UART_H

#include <stdint.h>
#include <stddef.h>
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"

typedef int uart_port_t;
#define UART_NUM_0 0
#define UART_NUM_1 1
#define UART_NUM_2 2
#define UART_NUM_MAX 3

/* SOC UART capabilities — target-dependent */
#ifdef CONFIG_IDF_TARGET_ESP32C6
  #define SOC_UART_LP_NUM 1
  #define SOC_UART_HP_NUM 2
#else
  #define SOC_UART_LP_NUM 0
  #define SOC_UART_HP_NUM 3
#endif

typedef int lp_uart_sclk_t;
#define UART_SCLK_DEFAULT     0
#define LP_UART_SCLK_DEFAULT  0

typedef enum { UART_DATA_8_BITS = 3 } uart_word_length_t;
typedef enum { UART_PARITY_DISABLE = 0 } uart_parity_t;
typedef enum { UART_STOP_BITS_1 = 1 } uart_stop_bits_t;
typedef enum { UART_HW_FLOWCTRL_DISABLE = 0 } uart_hw_flowcontrol_t;

typedef struct {
    int baud_rate;
    uart_word_length_t data_bits;
    uart_parity_t parity;
    uart_stop_bits_t stop_bits;
    uart_hw_flowcontrol_t flow_ctrl;
#if SOC_UART_LP_NUM >= 1
    lp_uart_sclk_t lp_source_clk;
#else
    int source_clk;
#endif
} uart_config_t;

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

/* Driver function declarations — implementations provided by test files */
esp_err_t uart_param_config(uart_port_t port, const uart_config_t *config);
esp_err_t uart_set_pin(uart_port_t port, int tx, int rx, int rts, int cts);
esp_err_t uart_driver_install(uart_port_t port, int rx_buf, int tx_buf,
                              int queue_size, QueueHandle_t *queue, int flags);
esp_err_t uart_driver_delete(uart_port_t port);
int uart_write_bytes(uart_port_t port, const char *data, size_t len);
int uart_read_bytes(uart_port_t port, void *buf, uint32_t length, TickType_t ticks);
esp_err_t uart_wait_tx_done(uart_port_t port, TickType_t ticks);
esp_err_t uart_set_rx_timeout(uart_port_t port, uint8_t tout);

/* Minimal inline stubs for functions referenced by bus_worker.c */
static inline void uart_flush_input(uart_port_t port) { (void)port; }

#endif
