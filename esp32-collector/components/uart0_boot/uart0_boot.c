/**
 * @file uart0_boot.c
 * @brief UART0 Boot Mode Manager implementation
 *
 * Manages the dual-use of UART0: normal data serial port vs firmware download.
 *
 * On ESP32-S3:  UART0 download port = TX43/RX44, strapping pin = GPIO0
 * On ESP32-C6:  UART0 download port = TX16/RX17, strapping pin = GPIO8
 *
 * C6 constraint: GPIO8 is shared with RGB LED (WS2812), so GPIO hold
 * cannot be used. Instead, C6 uses an NVS flag: set flag, reboot,
 * check flag on next boot to enter download wait mode.
 */

#include "uart0_boot.h"
#include "rgb_led.h"
#include "driver/gpio.h"
#include "esp_log.h"
#include "esp_system.h"
#include "nvs_flash.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define TAG "UART0_BOOT"

/* Per-chip BOOT/strapping GPIO */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define BOOT_STRAP_GPIO  0   /* S3 strapping pin */
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  #define BOOT_STRAP_GPIO  8   /* C6 strapping pin (shared with RGB LED) */
#else
  #define BOOT_STRAP_GPIO  0
#endif

#define BOOT_CHECK_DELAY_MS 50
#define DOWNLOAD_LED_BLINK_MS 250
#define NVS_NS "boot"
#define NVS_KEY_DL_FLAG "dl_flag"

static bool s_uart0_available = true;

/* ----------------------------------------------------------------
 *  Download mode wait task
 * ---------------------------------------------------------------- */
static void download_wait_task(void *arg)
{
    (void)arg;
    ESP_LOGW(TAG, "Download mode: UART0 reserved — connect esptool to UART0");
#ifdef CONFIG_IDF_TARGET_ESP32S3
    ESP_LOGI(TAG, "  S3 UART0 pins: TX=43, RX=44 (strapping: GPIO0)");
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
    ESP_LOGI(TAG, "  C6 UART0 pins: TX=16, RX=17 (strapping: GPIO8)");
#endif

    while (1) {
        rgb_led_set_state(LED_STATE_FACTORY_RESET);
        vTaskDelay(pdMS_TO_TICKS(DOWNLOAD_LED_BLINK_MS));

#ifdef CONFIG_IDF_TARGET_ESP32S3
        /* S3: check if BOOT (GPIO0) released */
        if (gpio_get_level(BOOT_STRAP_GPIO) != 0) {
            ESP_LOGI(TAG, "BOOT released — clearing NVS flag, restarting");
            /* Clear NVS download flag */
            nvs_handle_t h;
            if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
                nvs_set_u8(h, NVS_KEY_DL_FLAG, 0);
                nvs_commit(h);
                nvs_close(h);
            }
            vTaskDelay(pdMS_TO_TICKS(500));
            esp_restart();
        }
#else
        /* C6: no BOOT button check possible (GPIO8 = LED).
         * User must physically reset. We just wait. */
        vTaskDelay(pdMS_TO_TICKS(DOWNLOAD_LED_BLINK_MS));
#endif
    }
}

/* ----------------------------------------------------------------
 *  Public API
 * ---------------------------------------------------------------- */

bool uart0_boot_init(void)
{
    bool dl_requested = false;

#ifdef CONFIG_IDF_TARGET_ESP32S3
    /* S3: check BOOT button (GPIO0) directly */
    gpio_config_t io_conf = {
        .pin_bit_mask = (1ULL << BOOT_STRAP_GPIO),
        .mode         = GPIO_MODE_INPUT,
        .pull_up_en   = GPIO_PULLUP_ENABLE,
        .pull_down_en = GPIO_PULLDOWN_DISABLE,
        .intr_type    = GPIO_INTR_DISABLE,
    };
    gpio_config(&io_conf);
    vTaskDelay(pdMS_TO_TICKS(BOOT_CHECK_DELAY_MS));

    if (gpio_get_level(BOOT_STRAP_GPIO) == 0) {
        ESP_LOGW(TAG, "BOOT button held — entering download mode");
        dl_requested = true;
    }

    /* Also check NVS flag (set by software trigger) */
    if (!dl_requested) {
        nvs_handle_t h;
        if (nvs_open(NVS_NS, NVS_READONLY, &h) == ESP_OK) {
            uint8_t flag = 0;
            if (nvs_get_u8(h, NVS_KEY_DL_FLAG, &flag) == ESP_OK && flag) {
                dl_requested = true;
                /* Clear the flag so next normal boot works */
                nvs_close(h);
                nvs_open(NVS_NS, NVS_READWRITE, &h);
                nvs_set_u8(h, NVS_KEY_DL_FLAG, 0);
                nvs_commit(h);
                nvs_close(h);
            } else {
                nvs_close(h);
            }
        }
    }

#elif defined(CONFIG_IDF_TARGET_ESP32C6)
    /* C6: GPIO8 is shared with RGB LED, cannot use as input.
     * Only check NVS flag for download mode. */
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) == ESP_OK) {
        uint8_t flag = 0;
        if (nvs_get_u8(h, NVS_KEY_DL_FLAG, &flag) == ESP_OK && flag) {
            dl_requested = true;
            nvs_close(h);
            /* Clear flag */
            nvs_open(NVS_NS, NVS_READWRITE, &h);
            nvs_set_u8(h, NVS_KEY_DL_FLAG, 0);
            nvs_commit(h);
            nvs_close(h);
        } else {
            nvs_close(h);
        }
    }
#endif

    if (dl_requested) {
        s_uart0_available = false;
        xTaskCreate(download_wait_task, "uart0_dl", 2048, NULL, 5, NULL);
        return false;
    }

    ESP_LOGI(TAG, "UART0 available for data use (console on USB)");
    s_uart0_available = true;
    return true;
}

void uart0_boot_enter_download(void)
{
    ESP_LOGW(TAG, "Entering UART0 download mode via software restart...");

#ifdef CONFIG_IDF_TARGET_ESP32S3
    /*
     * S3 strategy: Pull GPIO0 low + RTC hold + restart.
     * ROM bootloader sees GPIO0=low → enters UART0 download.
     */
    gpio_set_direction(BOOT_STRAP_GPIO, GPIO_MODE_OUTPUT);
    gpio_set_level(BOOT_STRAP_GPIO, 0);
    gpio_hold_en(BOOT_STRAP_GPIO);

    /* Also set NVS flag as backup (for the wait task to detect) */
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
        nvs_set_u8(h, NVS_KEY_DL_FLAG, 1);
        nvs_commit(h);
        nvs_close(h);
    }

#elif defined(CONFIG_IDF_TARGET_ESP32C6)
    /*
     * C6 strategy: NVS flag only (GPIO8 = LED, cannot hold).
     * Set flag, reboot. On next boot, uart0_boot_init reads flag
     * and enters download wait mode. User then connects esptool.
     * NOTE: ROM bootloader won't auto-enter download mode.
     * User must manually hold BOOT (GPIO8) + press RESET for
     * true ROM download. The NVS flag puts firmware in wait mode
     * so UART0 is not grabbed by bus_dma.
     */
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) == ESP_OK) {
        nvs_set_u8(h, NVS_KEY_DL_FLAG, 1);
        nvs_commit(h);
        nvs_close(h);
    }
    ESP_LOGW(TAG, "C6: NVS flag set. ROM download requires physical BOOT+RESET.");
#endif

    vTaskDelay(pdMS_TO_TICKS(100));
    ESP_LOGW(TAG, "Restarting...");
    esp_restart();

    while (1) { vTaskDelay(pdMS_TO_TICKS(1000)); }
}

bool uart0_boot_is_uart0_available(void)
{
    return s_uart0_available;
}
