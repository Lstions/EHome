/**
 * @file dma_pool.h
 * @brief DMA Resource Manager — manages DMA channel allocation per chip
 *
 * DMA channels are first-class resources. Each chip (S3, C6, future chips)
 * defines its DMA channels as a static const hw_dma_t table in hw_profile.
 * This module provides runtime allocation/release/serialize on top of that table.
 *
 * Design: no dependency on hw_profile — table passed at init time.
 * hw_profile includes this header for the hw_dma_t type definition.
 */

#ifndef DMA_POOL_H
#define DMA_POOL_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits === */
#define DMA_POOL_MAX_CHANNELS  8
#define DMA_NAME_MAX           16
#define DMA_BOUND_MAX          16

/* === DMA channel state === */
#define DMA_STATE_FREE       0
#define DMA_STATE_ALLOCATED  1
#define DMA_STATE_DISABLED   2

/* === DMA channel capabilities (bit mask) === */
#define DMA_CAP_TX     (1 << 0)
#define DMA_CAP_RX     (1 << 1)
#define DMA_CAP_BURST  (1 << 2)

/* === Compatible bus types (bit mask) === */
#define DMA_BUS_UART  (1 << 0)
#define DMA_BUS_I2C   (1 << 1)
#define DMA_BUS_SPI   (1 << 2)

/* === DMA type === */
#define DMA_TYPE_GDMA  0

/* === bus_type values (must match bus_dma.h BUS_TYPE_*) === */
#define DMA_POOL_BUS_UART  1
#define DMA_POOL_BUS_I2C   2
#define DMA_POOL_BUS_SPI   3

/* === Static DMA channel descriptor (defined per-chip in hw_profile.c) === */
typedef struct {
    uint32_t dma_id;
    const char *name;         /* "GDMA_CH0" */
    uint8_t  dma_type;        /* DMA_TYPE_GDMA */
    uint8_t  capabilities;    /* DMA_CAP_TX | DMA_CAP_RX */
    uint32_t max_burst;       /* Max burst size (bytes) */
    uint8_t  compatible_bus;  /* DMA_BUS_UART | DMA_BUS_SPI */
} hw_dma_t;

/* === Runtime DMA channel info === */
typedef struct {
    uint32_t dma_id;
    char     name[DMA_NAME_MAX];
    uint8_t  dma_type;
    uint8_t  capabilities;
    uint32_t max_burst;
    uint8_t  state;                /* DMA_STATE_* */
    char     bound_to[DMA_BOUND_MAX]; /* "UART1" / "SPI2" / "" */
    uint8_t  compatible_bus;
} dma_channel_info_t;

/* === DMA pool === */
typedef struct dma_pool_t {
    dma_channel_info_t channels[DMA_POOL_MAX_CHANNELS];
    uint8_t  count;
    SemaphoreHandle_t mutex;
} dma_pool_t;

/* === Public API === */

/**
 * @brief Initialize DMA pool from a static hw_dma_t table.
 * @param pool   Pool to initialize (caller-allocated)
 * @param table  Static hw_dma_t array (from hw_profile)
 * @param count  Number of entries in table
 */
void dma_pool_init(dma_pool_t *pool, const hw_dma_t *table, int count);

/**
 * @brief Auto-allocate a DMA channel compatible with bus_type.
 * @return ESP_OK or ESP_ERR_NOT_FOUND (caller degrades to polled)
 */
esp_err_t dma_pool_allocate(dma_pool_t *pool, uint8_t bus_type,
                             const char *hw_id, uint32_t *out_dma_id);

/** @brief Release a DMA channel by dma_id. */
void dma_pool_release(dma_pool_t *pool, uint32_t dma_id);

/** @brief Release all channels bound to a specific hardware resource. */
void dma_pool_release_by_hw(dma_pool_t *pool, const char *hw_id);

/** @brief Apply user DMA config (enable/disable/bind from DmaChannelConfig). */
esp_err_t dma_pool_apply_config(dma_pool_t *pool, uint32_t dma_id,
                                 bool enabled, const char *bind_to);

/**
 * @brief Serialize pool state as raw field-8 entries for ResourceReport.
 * @return Number of bytes written to buf
 */
size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size);

/* No global getter. Callers receive dma_pool_t* via app_state_t->dma_pool (DIP). */

#ifdef __cplusplus
}
#endif

#endif /* DMA_POOL_H */
