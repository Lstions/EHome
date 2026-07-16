/**
 * @file dma_pool.c
 * @brief DMA Resource Manager implementation
 *
 * No dependency on hw_profile — table passed at init time.
 */

#include "dma_pool.h"
#include "frame_codec.h"
#include "esp_log.h"
#include <string.h>

#define TAG "DMA_POOL"

/* ----------------------------------------------------------------
 *  Init: copy static hw_dma_t table into runtime pool
 * ---------------------------------------------------------------- */

void dma_pool_init(dma_pool_t *pool, const hw_dma_t *table, int count)
{
    memset(pool, 0, sizeof(*pool));
    pool->mutex = xSemaphoreCreateMutex();
    if (pool->mutex == NULL) {
        ESP_LOGE(TAG, "Failed to create DMA pool mutex!");
        pool->count = 0;  /* Mark pool as invalid */
        return;
    }

    if (count > DMA_POOL_MAX_CHANNELS) {
        ESP_LOGW(TAG, "DMA table count %d > pool max %d, truncating",
                 count, DMA_POOL_MAX_CHANNELS);
        count = DMA_POOL_MAX_CHANNELS;
    }
    pool->count = (uint8_t)count;

    for (int i = 0; i < count; i++) {
        dma_channel_info_t *ch = &pool->channels[i];
        ch->dma_id = table[i].dma_id;
        strncpy(ch->name, table[i].name, DMA_NAME_MAX - 1);
        ch->name[DMA_NAME_MAX - 1] = '\0';
        ch->dma_type = table[i].dma_type;
        ch->capabilities = table[i].capabilities;
        ch->max_burst = table[i].max_burst;
        ch->state = DMA_STATE_FREE;
        ch->bound_to[0] = '\0';
        ch->compatible_bus = table[i].compatible_bus;

        ESP_LOGI(TAG, "  DMA ch%d: %s cap=0x%02X bus=0x%02X burst=%lu",
                 ch->dma_id, ch->name, ch->capabilities,
                 ch->compatible_bus, (unsigned long)ch->max_burst);
    }

    ESP_LOGI(TAG, "Initialized %d DMA channels", pool->count);
}

/* ----------------------------------------------------------------
 *  Allocate
 * ---------------------------------------------------------------- */

esp_err_t dma_pool_allocate(dma_pool_t *pool, uint8_t bus_type,
                             const char *hw_id, uint32_t *out_dma_id)
{
    if (!pool || !pool->mutex || !hw_id || !out_dma_id) return ESP_ERR_INVALID_ARG;

    uint8_t bus_mask = 0;
    switch (bus_type) {
    case DMA_POOL_BUS_UART: bus_mask = DMA_BUS_UART; break;
    case DMA_POOL_BUS_I2C:  bus_mask = DMA_BUS_I2C;  break;
    case DMA_POOL_BUS_SPI:  bus_mask = DMA_BUS_SPI;  break;
    default: return ESP_ERR_INVALID_ARG;
    }

    xSemaphoreTake(pool->mutex, portMAX_DELAY);

    /* 1. Already bound? */
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == DMA_STATE_ALLOCATED &&
            strcmp(pool->channels[i].bound_to, hw_id) == 0) {
            *out_dma_id = pool->channels[i].dma_id;
            xSemaphoreGive(pool->mutex);
            return ESP_OK;
        }
    }

    /* 1b. Pre-allocated by apply_config with different hw_id?
     *
     * At C6 boot: dma_pool_apply_config binds CH1 to "uart/UART1"
     * (from user DMA toggle).  Later bus_manager_setup_from_manifest()
     * calls dma_pool_allocate("uart/UART1") — the exact match in step 1
     * succeeds, no need for step 1b.
     *
     * Step 1b handles the case where a different UART requests DMA
     * after the first one already took it (C6 UHCI sharing).  Since
     * only CH1 is UART-compatible (hw_tables.c), step 1b reuses the
     * already-allocated CH1 for the new requester.  This naturally
     * enforces the hardware constraint documented in
     * ESP32-C6 TRM v1.2 Ch.4 pp.122-123: UART0/UART1 share one
     * UHCI slot on the GDMA peri-select matrix.
     *
     * bound_to format: "bus/hw_id" (e.g. "uart/UART1"). */
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == DMA_STATE_ALLOCATED &&
            (pool->channels[i].compatible_bus & bus_mask) &&
            pool->channels[i].bound_to[0] != '\0') {
            const char *slash = strchr(pool->channels[i].bound_to, '/');
            if (slash) {
                /* bind_to = "busType/hwId" — check bus type matches */
                size_t bus_len = slash - pool->channels[i].bound_to;
                const char *bus_names[] = {"uart", "i2c", "spi"};
                for (int b = 0; b < 3; b++) {
                    if (bus_type == b + 1 &&
                        bus_len == strlen(bus_names[b]) &&
                        strncasecmp(pool->channels[i].bound_to, bus_names[b], bus_len) == 0) {
                        *out_dma_id = pool->channels[i].dma_id;
                        /* Save old bound_to for logging before overwriting */
                        char old_bound[DMA_BOUND_MAX];
                        strncpy(old_bound, pool->channels[i].bound_to, DMA_BOUND_MAX - 1);
                        old_bound[DMA_BOUND_MAX - 1] = '\0';
                        /* Update bound_to to the canonical hw_id */
                        strncpy(pool->channels[i].bound_to, hw_id, DMA_BOUND_MAX - 1);
                        pool->channels[i].bound_to[DMA_BOUND_MAX - 1] = '\0';
                        xSemaphoreGive(pool->mutex);
                        ESP_LOGI(TAG, "Alloc %s: reuse %s (was bound %s)",
                                 hw_id, pool->channels[i].name, old_bound);
                        return ESP_OK;
                    }
                }
            }
        }
    }

    /* 2. Free channel: compatible + TX+RX */
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == DMA_STATE_FREE &&
            (pool->channels[i].compatible_bus & bus_mask) &&
            (pool->channels[i].capabilities & (DMA_CAP_TX | DMA_CAP_RX))
               == (DMA_CAP_TX | DMA_CAP_RX)) {
            pool->channels[i].state = DMA_STATE_ALLOCATED;
            strncpy(pool->channels[i].bound_to, hw_id, DMA_BOUND_MAX - 1);
            pool->channels[i].bound_to[DMA_BOUND_MAX - 1] = '\0';
            *out_dma_id = pool->channels[i].dma_id;
            xSemaphoreGive(pool->mutex);
            ESP_LOGI(TAG, "Alloc %s -> %s", pool->channels[i].name, hw_id);
            return ESP_OK;
        }
    }

    /* 3. Fallback: any free compatible */
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == DMA_STATE_FREE &&
            (pool->channels[i].compatible_bus & bus_mask)) {
            pool->channels[i].state = DMA_STATE_ALLOCATED;
            strncpy(pool->channels[i].bound_to, hw_id, DMA_BOUND_MAX - 1);
            pool->channels[i].bound_to[DMA_BOUND_MAX - 1] = '\0';
            *out_dma_id = pool->channels[i].dma_id;
            xSemaphoreGive(pool->mutex);
            ESP_LOGI(TAG, "Alloc %s -> %s (partial)", pool->channels[i].name, hw_id);
            return ESP_OK;
        }
    }

    xSemaphoreGive(pool->mutex);
    ESP_LOGW(TAG, "No DMA for %s (bus=%u)", hw_id, bus_type);
    return ESP_ERR_NOT_FOUND;
}

/* ----------------------------------------------------------------
 *  Release
 * ---------------------------------------------------------------- */

esp_err_t dma_pool_release(dma_pool_t *pool, uint32_t dma_id)
{
    if (!pool || !pool->mutex) return ESP_ERR_INVALID_ARG;
    if (xSemaphoreTake(pool->mutex, portMAX_DELAY) != pdTRUE) return ESP_ERR_TIMEOUT;
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].dma_id == dma_id &&
            pool->channels[i].state == DMA_STATE_ALLOCATED) {
            ESP_LOGI(TAG, "Release %s (was %s)",
                     pool->channels[i].name, pool->channels[i].bound_to);
            pool->channels[i].state = DMA_STATE_FREE;
            pool->channels[i].bound_to[0] = '\0';
            break;
        }
    }
    xSemaphoreGive(pool->mutex);
    return ESP_OK;
}

esp_err_t dma_pool_release_by_hw(dma_pool_t *pool, const char *hw_id)
{
    if (!pool || !pool->mutex || !hw_id) return ESP_ERR_INVALID_ARG;
    if (xSemaphoreTake(pool->mutex, portMAX_DELAY) != pdTRUE) return ESP_ERR_TIMEOUT;
    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].state == DMA_STATE_ALLOCATED &&
            strcmp(pool->channels[i].bound_to, hw_id) == 0) {
            ESP_LOGI(TAG, "Release %s (unbind %s)", pool->channels[i].name, hw_id);
            pool->channels[i].state = DMA_STATE_FREE;
            pool->channels[i].bound_to[0] = '\0';
        }
    }
    xSemaphoreGive(pool->mutex);
    return ESP_OK;
}

/* ----------------------------------------------------------------
 *  Apply user config
 * ---------------------------------------------------------------- */

esp_err_t dma_pool_apply_config(dma_pool_t *pool, uint32_t dma_id,
                                 bool enabled, const char *bind_to)
{
    if (!pool || !pool->mutex) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(pool->mutex, portMAX_DELAY);

    for (int i = 0; i < pool->count; i++) {
        if (pool->channels[i].dma_id != dma_id) continue;

        if (!enabled) {
            if (pool->channels[i].state == DMA_STATE_ALLOCATED) {
                ESP_LOGW(TAG, "Disable %s (was %s)",
                         pool->channels[i].name, pool->channels[i].bound_to);
                pool->channels[i].bound_to[0] = '\0';
            }
            pool->channels[i].state = DMA_STATE_DISABLED;
            xSemaphoreGive(pool->mutex);
            return ESP_OK;
        }

        if (pool->channels[i].state == DMA_STATE_DISABLED) {
            pool->channels[i].state = DMA_STATE_FREE;
            ESP_LOGI(TAG, "Re-enable %s", pool->channels[i].name);
        }

        if (bind_to && bind_to[0] != '\0') {
            pool->channels[i].bound_to[0] = '\0';
            pool->channels[i].state = DMA_STATE_FREE;
            /* Preempt other channels bound to same hw */
            for (int j = 0; j < pool->count; j++) {
                if (j != i && pool->channels[j].state == DMA_STATE_ALLOCATED &&
                    strcmp(pool->channels[j].bound_to, bind_to) == 0) {
                    ESP_LOGW(TAG, "Preempt %s from %s",
                             pool->channels[j].name, bind_to);
                    pool->channels[j].state = DMA_STATE_FREE;
                    pool->channels[j].bound_to[0] = '\0';
                }
            }
            pool->channels[i].state = DMA_STATE_ALLOCATED;
            strncpy(pool->channels[i].bound_to, bind_to, DMA_BOUND_MAX - 1);
            pool->channels[i].bound_to[DMA_BOUND_MAX - 1] = '\0';
            ESP_LOGI(TAG, "Bind %s -> %s (user)", pool->channels[i].name, bind_to);
        }

        xSemaphoreGive(pool->mutex);
        return ESP_OK;
    }

    xSemaphoreGive(pool->mutex);
    return ESP_ERR_NOT_FOUND;
}

esp_err_t dma_pool_snapshot_state(dma_pool_t *pool, dma_pool_state_t *out)
{
    if (!pool || !pool->mutex || !out) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(pool->mutex, portMAX_DELAY);
    memcpy(out->channels, pool->channels, sizeof(out->channels));
    out->count = pool->count;
    xSemaphoreGive(pool->mutex);
    return ESP_OK;
}

esp_err_t dma_pool_restore_state(dma_pool_t *pool, const dma_pool_state_t *state)
{
    if (!pool || !pool->mutex || !state || state->count > DMA_POOL_MAX_CHANNELS)
        return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(pool->mutex, portMAX_DELAY);
    memcpy(pool->channels, state->channels, sizeof(pool->channels));
    pool->count = state->count;
    xSemaphoreGive(pool->mutex);
    return ESP_OK;
}

esp_err_t dma_pool_reset_runtime(dma_pool_t *pool)
{
    if (!pool || !pool->mutex) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(pool->mutex, portMAX_DELAY);
    for (int i = 0; i < pool->count; i++) {
        pool->channels[i].state = DMA_STATE_FREE;
        pool->channels[i].bound_to[0] = '\0';
    }
    xSemaphoreGive(pool->mutex);
    return ESP_OK;
}

/* ----------------------------------------------------------------
 *  Serialize for ResourceReport field 8
 * ---------------------------------------------------------------- */

static size_t encode_one_channel(uint8_t *out, size_t cap,
                                  const dma_channel_info_t *ch)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);
    if (frame_encode_varint(&enc, 1, ch->dma_id) != FRAME_OK) return 0;
    if (frame_encode_string(&enc, 2, ch->name) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 3, ch->dma_type) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 4, ch->capabilities) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 5, ch->max_burst) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 6, ch->state) != FRAME_OK) return 0;
    if (frame_encode_string(&enc, 7, ch->bound_to) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 8, ch->compatible_bus) != FRAME_OK) return 0;
    return frame_encoder_size(&enc);
}

size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size)
{
    if (!pool || !pool->mutex || !buf || buf_size == 0) return 0;
    
    size_t pos = 0;
    uint8_t sub_buf[128];

    xSemaphoreTake(pool->mutex, portMAX_DELAY);
    for (int i = 0; i < pool->count; i++) {
        size_t sub_len = encode_one_channel(sub_buf, sizeof(sub_buf),
                                             &pool->channels[i]);
        if (sub_len <= 1) continue;  /* skip empty or failed encode */
        size_t payload_len = sub_len - 1;

        size_t hdr = (payload_len < 128) ? 2 : 3;  /* tag + 1 or 2 byte length */
        if (pos + hdr + payload_len > buf_size) break;
        buf[pos++] = 0x42;  /* field 8, wire type 2 */
        if (payload_len < 128) {
            buf[pos++] = (uint8_t)payload_len;
        } else {
            buf[pos++] = (uint8_t)((payload_len & 0x7F) | 0x80);
            buf[pos++] = (uint8_t)(payload_len >> 7);
        }
        memcpy(&buf[pos], sub_buf + 1, payload_len);
        pos += payload_len;
    }
    xSemaphoreGive(pool->mutex);
    return pos;
}


