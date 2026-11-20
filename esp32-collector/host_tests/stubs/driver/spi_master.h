#ifndef HOST_TEST_DRIVER_SPI_MASTER_H
#define HOST_TEST_DRIVER_SPI_MASTER_H

#include <stdint.h>
#include <stddef.h>
#include "esp_err.h"

typedef int spi_host_device_t;
/* Match ESP-IDF's spi_host_device_t: SPI1_HOST=0, SPI2_HOST=1.
 * hw_spis[].port remains the human port number (2), and bus_manager maps it
 * to this enum; keeping the stub at 2 hid that mapping bug in host tests. */
#define SPI1_HOST 0
#define SPI2_HOST 1
#define SPI3_HOST 2  /* not present on C6; valid as an out-of-range sentinel */
#define SPI_HOST_MAX 2

#define SPI_DMA_CH_AUTO    1
#define SPI_DMA_DISABLED   0

typedef void *spi_device_handle_t;

typedef struct {
    uint32_t flags;
    int spics_io_num;
    int8_t mode;
    uint32_t clock_speed_hz;
    int input_delay_ns;
    int8_t queue_size;
} spi_device_interface_config_t;

typedef struct {
    int mosi_io_num;
    int miso_io_num;
    int sclk_io_num;
    int spics_io_num;
    int quadwp_io_num;
    int quadhd_io_num;
    int max_transfer_sz;
    uint32_t flags;
    uint32_t intr_flags;
} spi_bus_config_t;

typedef struct {
    uint32_t flags;
    size_t length;
    size_t rxlength;
    const void *tx_buffer;
    void *rx_buffer;
} spi_transaction_t;

/* Driver function declarations — implementations provided by test files */
esp_err_t spi_bus_initialize(spi_host_device_t host, const spi_bus_config_t *cfg, int dma);
esp_err_t spi_bus_free(spi_host_device_t host);
esp_err_t spi_bus_add_device(spi_host_device_t host,
                             const spi_device_interface_config_t *cfg,
                             spi_device_handle_t *handle);
esp_err_t spi_bus_remove_device(spi_device_handle_t handle);
esp_err_t spi_device_transmit(spi_device_handle_t handle, spi_transaction_t *trans);

#endif
