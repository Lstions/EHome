#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "dma_pool.h"

/* Minimal FreeRTOS/ESP logging stubs for the host contract test. */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{
    (void)semaphore;
    (void)ticks;
    return 1;
}
int xSemaphoreGive(SemaphoreHandle_t semaphore)
{
    (void)semaphore;
    return 1;
}
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level;
    (void)tag;
    (void)format;
}

static int failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s\n", (message)); \
        failures++; \
    } \
} while (0)

static void test_uart_dma_is_exclusive(void)
{
    static const hw_dma_t table[] = {
        {.dma_id = 1, .name = "UHCI", .capabilities = DMA_CAP_TX | DMA_CAP_RX,
         .max_burst = 4095, .compatible_bus = DMA_BUS_UART},
    };
    dma_pool_t pool;
    dma_pool_init(&pool, table, 1);

    uint32_t first = UINT32_MAX;
    CHECK(dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "uart/UART0", &first) == ESP_OK,
          "first UART DMA request must allocate");

    uint32_t second = UINT32_MAX;
    CHECK(dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "uart/UART1", &second) == ESP_ERR_NOT_FOUND,
          "second UART DMA request must be rejected instead of sharing one channel");

    uint32_t same = UINT32_MAX;
    CHECK(dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "uart/UART0", &same) == ESP_OK &&
          same == first, "same controller lease must remain idempotent");

    CHECK(dma_pool_release_by_hw(&pool, "uart/UART0") == ESP_OK,
          "released UART lease must succeed");
    CHECK(dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "uart/UART1", &second) == ESP_OK,
          "released DMA channel must be reusable by the other UART");
}

static void test_prebound_dma_is_not_reassigned(void)
{
    static const hw_dma_t table[] = {
        {.dma_id = 1, .name = "UHCI", .capabilities = DMA_CAP_TX | DMA_CAP_RX,
         .max_burst = 4095, .compatible_bus = DMA_BUS_UART},
    };
    dma_pool_t pool;
    dma_pool_init(&pool, table, 1);
    CHECK(dma_pool_apply_config(&pool, 1, true, "uart/UART1") == ESP_OK,
          "pre-binding UART DMA must succeed");
    uint32_t dma_id = UINT32_MAX;
    CHECK(dma_pool_allocate(&pool, DMA_POOL_BUS_UART, "uart/UART0", &dma_id) == ESP_ERR_NOT_FOUND,
          "a pre-bound DMA channel must not be reassigned silently");
}

int main(void)
{
    test_uart_dma_is_exclusive();
    test_prebound_dma_is_not_reassigned();
    if (failures != 0) {
        fprintf(stderr, "%d DMA contract assertion(s) failed\n", failures);
        return 1;
    }
    puts("dma_pool_contract_tests: all tests passed");
    return 0;
}
