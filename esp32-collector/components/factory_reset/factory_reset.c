#include "factory_reset.h"
#include "rgb_led.h"
#include "nvs_flash.h"
#include "esp_system.h"
#include "esp_log.h"
#include "driver/gpio.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define TAG "FACTORY_RESET"
static bool s_in_progress = false;

/* BOOT button GPIO differs by chip:
 *   S3: GPIO0 (standard BOOT pin, also used for ROM download mode)
 *   C6: GPIO9 (on most C6 dev boards, e.g. ESP32-C6-DevKitC-1) */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define BOOT_BUTTON_GPIO  0
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  #define BOOT_BUTTON_GPIO  9
#else
  #define BOOT_BUTTON_GPIO  0
#endif

#define HOLD_TIME_MS      5000
#define POLL_INTERVAL_MS  100

static void factory_reset_task(void *arg)
{
    (void)arg;

    /* Configure BOOT button as input with pull-up */
    gpio_config_t io_conf = {
        .pin_bit_mask = (1ULL << BOOT_BUTTON_GPIO),
        .mode = GPIO_MODE_INPUT,
        .pull_up_en = GPIO_PULLUP_ENABLE,
        .pull_down_en = GPIO_PULLDOWN_DISABLE,
        .intr_type = GPIO_INTR_DISABLE,
    };
    gpio_config(&io_conf);

    ESP_LOGI(TAG, "Monitoring BOOT button (GPIO%d) for factory reset", BOOT_BUTTON_GPIO);

    while (1) {
        /* Wait for button press (active low) */
        if (gpio_get_level(BOOT_BUTTON_GPIO) == 0) {
            int held_ms = 0;
            while (gpio_get_level(BOOT_BUTTON_GPIO) == 0 && held_ms < HOLD_TIME_MS) {
                vTaskDelay(pdMS_TO_TICKS(POLL_INTERVAL_MS));
                held_ms += POLL_INTERVAL_MS;

                /* Visual feedback: blink faster as hold progresses */
                if (held_ms > 1000 && held_ms % 500 == 0) {
                    ESP_LOGW(TAG, "Factory reset in %d/%d ms", held_ms, HOLD_TIME_MS);
                    rgb_led_set_state(LED_STATE_FACTORY_RESET);
                }
            }

            if (held_ms >= HOLD_TIME_MS) {
                s_in_progress = true;
                ESP_LOGW(TAG, "FACTORY RESET triggered! Erasing NVS...");

                rgb_led_set_state(LED_STATE_FACTORY_RESET);

                /* Erase NVS partition */
                nvs_flash_erase();
                nvs_flash_init();

                ESP_LOGW(TAG, "NVS erased. Rebooting in 2s...");
                vTaskDelay(pdMS_TO_TICKS(2000));
                esp_restart();
            }
        }

        vTaskDelay(pdMS_TO_TICKS(200));
    }
}

void factory_reset_init(void)
{
    xTaskCreate(factory_reset_task, "factory_reset", 3072, NULL, 3, NULL);
}

bool factory_reset_in_progress(void)
{
    return s_in_progress;
}

void factory_reset_trigger(void)
{
    s_in_progress = true;
    ESP_LOGW(TAG, "FACTORY RESET via command! Erasing NVS...");
    rgb_led_set_state(LED_STATE_FACTORY_RESET);
    nvs_flash_erase();
    nvs_flash_init();
    ESP_LOGW(TAG, "NVS erased. Rebooting in 2s...");
    vTaskDelay(pdMS_TO_TICKS(2000));
    esp_restart();
}
