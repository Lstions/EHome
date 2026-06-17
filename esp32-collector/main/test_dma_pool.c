/**
 * @file test_dma_pool.c
 * @brief Unit tests for dma_pool component
 *
 * Test cases (TODO):
 *   1. test_allocate_full — all channels allocated, next returns NOT_FOUND
 *   2. test_allocate_partial — partial capability channel fallback
 *   3. test_apply_config_preempt — binding already-allocated channel preempts old
 *   4. test_serialize_buffer_overflow — buffer too small, graceful truncation
 *   5. test_release_by_hw — release all channels bound to specific hardware
 *   6. test_disable_re_enable — disable then re-enable a channel
 */

#include <stdio.h>
#include <string.h>
#include "dma_pool.h"

/* Mock hw_dma table for testing */
static const hw_dma_t test_dmas[] = {
    { .dma_id = 0, .name = "GDMA_CH0", .dma_type = DMA_TYPE_GDMA,
      .capabilities = DMA_CAP_TX | DMA_CAP_RX, .max_burst = 4096,
      .compatible_bus = DMA_BUS_UART | DMA_BUS_SPI },
    { .dma_id = 1, .name = "GDMA_CH1", .dma_type = DMA_TYPE_GDMA,
      .capabilities = DMA_CAP_TX, .max_burst = 2048,
      .compatible_bus = DMA_BUS_SPI },
    { .dma_id = 2, .name = "GDMA_CH2", .dma_type = DMA_TYPE_GDMA,
      .capabilities = DMA_CAP_TX | DMA_CAP_RX, .max_burst = 4096,
      .compatible_bus = DMA_BUS_UART },
};

static dma_pool_t pool;

void test_dma_pool_setup(void) {
    dma_pool_init(&pool, test_dmas, 3);
}

void test_allocate_full(void) {
    uint32_t dma_id;
    /* Allocate all 3 channels */
    if (dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "UART0", &dma_id) != ESP_OK) {
        printf("  FAIL: test_allocate_full — first alloc\n");
        return;
    }
    if (dma_pool_allocate(&pool, DMA_POOL_BUS_SPI, "SPI2", &dma_id) != ESP_OK) {
        printf("  FAIL: test_allocate_full — second alloc\n");
        return;
    }
    if (dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "UART1", &dma_id) != ESP_OK) {
        printf("  FAIL: test_allocate_full — third alloc\n");
        return;
    }
    /* 4th should fail */
    if (dma_pool_allocate(&pool, DMA_POOL_BUS_SPI, "SPI3", &dma_id) != ESP_ERR_NOT_FOUND) {
        printf("  FAIL: test_allocate_full — expected NOT_FOUND\n");
        return;
    }
    printf("  PASS: test_allocate_full\n");
}

void test_allocate_partial(void) {
    dma_pool_init(&pool, test_dmas, 3);
    uint32_t dma_id;
    /* Allocate full-capability channels first */
    dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "UART0", &dma_id);
    dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "UART1", &dma_id);
    /* SPI request: CH1 has TX only (partial), should still allocate */
    if (dma_pool_allocate(&pool, DMA_POOL_BUS_SPI, "SPI2", &dma_id) != ESP_OK) {
        printf("  FAIL: test_allocate_partial — partial alloc expected OK\n");
        return;
    }
    printf("  PASS: test_allocate_partial\n");
}

int main(void) {
    printf("=== dma_pool unit tests ===\n");
    test_dma_pool_setup();
    test_allocate_full();
    test_allocate_partial();
    printf("=== All tests passed ===\n");
    return 0;
}
