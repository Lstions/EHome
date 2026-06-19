/**
 * @file hw_tables.c
 * @brief Static hardware resource tables — per-target pin tables
 *
 * Extracted from hw_profile.c to separate data tables from encoding logic.
 * Supports ESP32-S3 and ESP32-C6 via CONFIG_IDF_TARGET_* conditionals.
 *
 * S3: 3 UART (all DMA), 2 I2C, 2 SPI, 12 GPIO, 5 ADC, 5 GDMA
 * C6: 2 UART (DMA),    1 I2C, 1 SPI, 8 GPIO,  3 ADC, 3 GDMA
 */

#include "hw_tables.h"

/* ================================================================
 *  Static Hardware Profile — per-target pin tables
 *
 *  RESERVED PINS (do NOT assign to user peripherals):
 *    S3: GPIO19=USB_D-, GPIO20=USB_D+, GPIO48=RGB LED
 *    C6: GPIO12=USB_D-, GPIO13=USB_D+, GPIO8=RGB LED
 *
 *  S3 UART0: TX=43 RX=44 (ROM bootloader download port)
 *  S3 UART1: TX=4  RX=5  (general purpose, avoids USB 19/20)
 *  S3 UART2: TX=1  RX=2  (general purpose)
 *  C6 UART0: TX=16 RX=17 (ROM bootloader download port)
 *  C6 UART1: TX=20 RX=21 (general purpose, avoids USB 12/13)
 *
 *  All HP UARTs on both chips support DMA (flags = 0x01).
 * ================================================================ */

#ifdef CONFIG_IDF_TARGET_ESP32S3

/* S3 USB pins: GPIO19=USB_D-, GPIO20=USB_D+ (RESERVED for USB Serial/JTAG) */
const hw_uart_t hw_uarts[HW_UART_COUNT] = {
    { .id = "UART0", .port = 0, .default_tx_pin = 43, .default_rx_pin = 44,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, ROM download port */
    { .id = "UART1", .port = 1, .default_tx_pin = 4,  .default_rx_pin = 5,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, avoids USB pins 19/20 */
    { .id = "UART2", .port = 2, .default_tx_pin = 1,  .default_rx_pin = 2,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, general purpose */
};

const hw_i2c_t hw_i2cs[HW_I2C_COUNT] = {
    { .id = "I2C0", .port = 0, .default_sda = 8,  .default_scl = 9,
      .max_freq_hz = 1000000, .flags = 0x01 },
    { .id = "I2C1", .port = 1, .default_sda = 47, .default_scl = 48,
      .max_freq_hz = 1000000, .flags = 0x01 },
};

const hw_spi_t hw_spis[HW_SPI_COUNT] = {
    { .id = "SPI2", .port = 2, .default_mosi = 11, .default_miso = 13,
      .default_sclk = 12, .default_cs = 10, .max_freq_hz = 80000000,
      .flags = 0x01 },
    { .id = "SPI3", .port = 3, .default_mosi = 35, .default_miso = 37,
      .default_sclk = 36, .default_cs = 34, .max_freq_hz = 80000000,
      .flags = 0x01 },
};

const hw_gpio_t hw_gpios[HW_GPIO_COUNT] = {
    { .id = "GPIO0",  .pin = 0  },
    { .id = "GPIO1",  .pin = 1  },
    { .id = "GPIO2",  .pin = 2  },
    { .id = "GPIO3",  .pin = 3  },
    { .id = "GPIO4",  .pin = 4  },
    { .id = "GPIO5",  .pin = 5  },
    { .id = "GPIO6",  .pin = 6  },
    { .id = "GPIO7",  .pin = 7  },
    { .id = "GPIO8",  .pin = 8  },
    { .id = "GPIO15", .pin = 15 },
    { .id = "GPIO16", .pin = 16 },
    { .id = "GPIO17", .pin = 17 },
};

const hw_adc_t hw_adcs[HW_ADC_COUNT] = {
    { .id = "ADC1_CH0", .unit = 1, .channel = 0, .pin = 1,  .max_bits = 12 },
    { .id = "ADC1_CH1", .unit = 1, .channel = 1, .pin = 2,  .max_bits = 12 },
    { .id = "ADC1_CH2", .unit = 1, .channel = 2, .pin = 3,  .max_bits = 12 },
    { .id = "ADC1_CH3", .unit = 1, .channel = 3, .pin = 4,  .max_bits = 12 },
    { .id = "ADC1_CH4", .unit = 1, .channel = 4, .pin = 5,  .max_bits = 12 },
};

/* S3: 5 GDMA channels (CH0-4), all general purpose TX+RX, UART+I2C+SPI compatible */
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .name = "GDMA_CH0", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x07 },  /* UART|I2C|SPI */
    { .dma_id = 1, .name = "GDMA_CH1", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x07 },
    { .dma_id = 2, .name = "GDMA_CH2", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x07 },
    { .dma_id = 3, .name = "GDMA_CH3", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x07 },
    { .dma_id = 4, .name = "GDMA_CH4", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x07 },
};

#elif defined(CONFIG_IDF_TARGET_ESP32C6)

/* C6 USB pins: GPIO12=USB_D-, GPIO13=USB_D+ (RESERVED for USB Serial/JTAG) */
const hw_uart_t hw_uarts[HW_UART_COUNT] = {
    { .id = "UART0", .port = 0, .default_tx_pin = 16, .default_rx_pin = 17,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, ROM download port */
    { .id = "UART1", .port = 1, .default_tx_pin = 20, .default_rx_pin = 21,
      .max_baud = 5000000, .flags = 0x01 },  /* DMA, avoids USB pins 12/13 */
};

const hw_i2c_t hw_i2cs[HW_I2C_COUNT] = {
    { .id = "I2C0", .port = 0, .default_sda = 21, .default_scl = 22,
      .max_freq_hz = 1000000, .flags = 0x00 },  /* C6 I2C: no DMA support */
};

const hw_spi_t hw_spis[HW_SPI_COUNT] = {
    { .id = "SPI2", .port = 2, .default_mosi = 23, .default_miso = 19,
      .default_sclk = 18, .default_cs = 5, .max_freq_hz = 40000000,
      .flags = 0x01 },
};

const hw_gpio_t hw_gpios[HW_GPIO_COUNT] = {
    { .id = "GPIO0", .pin = 0 },
    { .id = "GPIO1", .pin = 1 },
    { .id = "GPIO2", .pin = 2 },
    { .id = "GPIO3", .pin = 3 },
    { .id = "GPIO4", .pin = 4 },
    { .id = "GPIO5", .pin = 5 },
    { .id = "GPIO6", .pin = 6 },
    { .id = "GPIO7", .pin = 7 },
};

const hw_adc_t hw_adcs[HW_ADC_COUNT] = {
    { .id = "ADC1_CH0", .unit = 1, .channel = 0, .pin = 0, .max_bits = 12 },
    { .id = "ADC1_CH1", .unit = 1, .channel = 1, .pin = 1, .max_bits = 12 },
    { .id = "ADC1_CH2", .unit = 1, .channel = 2, .pin = 2, .max_bits = 12 },
};

/* C6: 3 GDMA channel pairs (TX+RX).  Only CH1 is UART-capable because
 * UART0/UART1 share a single UHCI interface — at most one UART can use
 * DMA at any time.  CH0 and CH2 are reserved for SPI / other peripherals. */
const hw_dma_t hw_dmas[HW_DMA_COUNT] = {
    { .dma_id = 0, .name = "GDMA_CH0", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
    { .dma_id = 1, .name = "GDMA_CH1", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x05 },  /* UART|SPI */
    { .dma_id = 2, .name = "GDMA_CH2", .dma_type = 0,
      .capabilities = 0x03, .max_burst = 4095, .compatible_bus = 0x04 },  /* SPI only */
};

#else
  #error "Unsupported IDF target"
#endif
