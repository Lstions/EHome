/**
 * @file uart0_boot.h
 * @brief UART0 Boot Mode Manager — dual-use: data serial vs firmware download
 *
 * UART0 serves two roles:
 *   1. Normal operation: user data serial port (DMA enabled by default)
 *   2. Firmware download: ROM bootloader UART0 download mode
 *
 * Download mode entry methods:
 *   - Hardware: Hold BOOT(GPIO0) + press RESET → ROM bootloader enters download
 *   - Software: uart0_boot_enter_download() → RTC GPIO hold GPIO0 low + restart
 *   - Boot window: On startup, if BOOT is held, skip UART0 driver init
 *
 * Console/logs always go through USB Serial/JTAG (not UART0).
 * Works on both ESP32-S3 and ESP32-C6.
 */

#ifndef UART0_BOOT_H
#define UART0_BOOT_H

#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief Initialize UART0 boot mode manager.
 *
 * MUST be called BEFORE any UART0 driver installation.
 * Checks BOOT button (GPIO0) state:
 *   - If held: UART0 reserved for download, enters wait loop (LED fast blink)
 *   - If released: UART0 available for data use
 *
 * @return true  if UART0 is available for data use
 * @return false if UART0 is reserved for firmware download mode
 */
bool uart0_boot_init(void);

/**
 * @brief Enter UART0 firmware download mode.
 *
 * Pulls GPIO0 low, enables RTC GPIO hold so it persists across reset,
 * then calls esp_restart(). ROM bootloader detects GPIO0 low and enters
 * UART0 serial download mode automatically.
 *
 * This function does not return.
 */
void uart0_boot_enter_download(void) __attribute__((noreturn));

/**
 * @brief Check if UART0 is available for data use.
 * @return true if UART0 can be used as data serial port
 */
bool uart0_boot_is_uart0_available(void);

#ifdef __cplusplus
}
#endif

#endif /* UART0_BOOT_H */
