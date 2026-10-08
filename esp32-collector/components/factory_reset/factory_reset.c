#include "factory_reset.h"
/* ⭐ 2026-10-08：白名单抽到 factory_reset_namespaces.[ch]（P4 单一来源 + 宿主可测）。
 * 原先它是本文件里的 static 数组 ⇒ 宿主引用不到 ⇒ 本文件长期零覆盖，
 * 而 tests/ 下那份"测试"只能**复制**一份名单（且与生产不同，见 §166.4）。 */
#include "factory_reset_namespaces.h"
#include "rgb_led.h"
#include "nvs_flash.h"
#include "nvs.h"
#include "esp_system.h"
#include "esp_log.h"
#include "driver/gpio.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define TAG "FACTORY_RESET"
static bool s_in_progress = false;

/* 白名单已移到 factory_reset_namespaces.c —— **唯一来源**（P4）。
 * 下面两个宏只是本文件内的短别名，避免改动大量调用点；
 * ⚠ 不要在这里再写一份名单：那正是本次修掉的形态（两份定义必然漂移）。 */
#define NVS_NAMESPACES       FACTORY_RESET_NAMESPACES
#define NVS_NAMESPACE_COUNT  FACTORY_RESET_NAMESPACE_COUNT

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

/* 长按判定参数 */
#define HOLD_TIME_MS      5000
#define POLL_INTERVAL_MS  100

/* 恢复出厂只在上电后的这个窗口内响应（毫秒）。
 *
 * 为什么需要窗口（2026-10-04 现场事故的第二道防线）：
 * 原来长按判定在上电后的**任何时刻**都生效，于是"GPIO0 被某个外设拉低"
 * 就等于"用户一直按着 BOOT 键"，设备会在运行中随时把自己恢复出厂。
 * 本次事故就是 PWM0 配到 GPIO0（duty 3%）造成的：每 8.8s 擦一次 NVS 并重启。
 *
 * 限定在启动窗口内，是沿用 ESP 系设备的通行做法（ROM 下载模式、多数
 * bootloader 的按键语义都只在启动瞬间采样）：真正的"我要恢复出厂"操作
 * 是"按住 BOOT 再上电"，本来就发生在启动时刻；而运行期间把引脚拉低的
 * 干扰（外设误配、总线串扰）不再能被解释成用户意图。
 *
 * 取 10s：足够覆盖启动到 WiFi/MQTT 连接完成的整个阶段，让运维有从容的操作
 * 时间；又短于"设备进入稳定运行"的时刻，之后引脚干扰不再触发擦除。
 * 窗口外仍然**持续监控并在日志里提示**（见 warned_low_pin），
 * 这样"按键没反应"与"按键根本没被检测到"在日志上可以区分。 */
#define BOOT_WINDOW_MS    10000

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

    ESP_LOGI(TAG, "Monitoring BOOT button (GPIO%d); only a hold within the first "
             "%d ms after boot triggers factory reset",
             BOOT_BUTTON_GPIO, BOOT_WINDOW_MS);

    const TickType_t start_tick = xTaskGetTickCount();
    bool warned_low_pin = false;
    /* 窗口是否已关闭。只在本任务里读写，故用局部变量而非文件级状态。 */
    bool window_elapsed = false;

    while (1) {
        /* 窗口判定：只在启动后的 BOOT_WINDOW_MS 内接受长按。 */
        /* 用 pdTICKS_TO_MS 而不是手写 * portTICK_PERIOD_MS：它是 IDF 的官方换算宏，
         * 在 1000Hz（本工程 CONFIG_FREERTOS_HZ=1000，即 1 tick = 1ms）下就是恒等，
         * 若将来有人改 tick 频率此处不必跟着改。减法本身对 TickType_t 回绕是安全的
         * （无符号运算），不要改成比较绝对 tick。 */
        const uint32_t up_ms = (uint32_t)pdTICKS_TO_MS(xTaskGetTickCount() - start_tick);
        const bool in_window = (up_ms < BOOT_WINDOW_MS);
        if (!in_window && !window_elapsed) {
            window_elapsed = true;
            ESP_LOGI(TAG, "BOOT window closed at %u ms uptime; the button is no "
                     "longer armed for factory reset", (unsigned)up_ms);
        }

        /* Wait for button press (active low) */
        if (gpio_get_level(BOOT_BUTTON_GPIO) == 0) {
            /* 窗口外读到低电平：不再触发擦除，但必须留痕迹 ——
             * "引脚被拉低"是硬件/配置异常的早期信号，静默忽略会让我们
             * 在下次事故里再一次把它误判成"用户按了键"。
             * 只在第一次打印，避免持续低电平把日志刷满。 */
            if (!in_window) {
                if (!warned_low_pin) {
                    warned_low_pin = true;
                    ESP_LOGW(TAG, "GPIO%d is LOW after the boot window; ignoring it "
                             "as a factory-reset request. Something is driving this "
                             "pin — check the GPIO/PWM configuration for this node.",
                             BOOT_BUTTON_GPIO);
                }
            } else {
                int held_ms = 0;
                while (gpio_get_level(BOOT_BUTTON_GPIO) == 0 &&
                       held_ms < HOLD_TIME_MS) {
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
                    ESP_LOGW(TAG, "FACTORY RESET triggered! Erasing NVS namespaces...");

                    rgb_led_set_state(LED_STATE_FACTORY_RESET);

                    /* Erase only whitelisted NVS namespaces */
                    for (size_t i = 0; i < NVS_NAMESPACE_COUNT; i++) {
                        nvs_handle_t handle;
                        esp_err_t err = nvs_open(NVS_NAMESPACES[i], NVS_READWRITE, &handle);
                        if (err == ESP_OK) {
                            err = nvs_erase_all(handle);
                            nvs_commit(handle);
                            nvs_close(handle);
                            if (err == ESP_OK) {
                                ESP_LOGI(TAG, "Erased namespace: %s", NVS_NAMESPACES[i]);
                            } else {
                                ESP_LOGW(TAG, "Failed to erase %s: %s",
                                         NVS_NAMESPACES[i], esp_err_to_name(err));
                            }
                        } else if (err != ESP_ERR_NVS_NOT_FOUND) {
                            ESP_LOGW(TAG, "Failed to open %s: %s",
                                     NVS_NAMESPACES[i], esp_err_to_name(err));
                        }
                    }

                    ESP_LOGW(TAG, "NVS namespaces erased. Rebooting in 2s...");
                    vTaskDelay(pdMS_TO_TICKS(2000));
                    esp_restart();
                }
            }
        }

        /* 窗口内 100ms 轮询（长按计时靠它累积）；窗口外降到 500ms：
         * 运行期只需要"发现异常并提示"，没必要一直高频采样。 */
        vTaskDelay(pdMS_TO_TICKS(window_elapsed ? 500 : POLL_INTERVAL_MS));
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
    ESP_LOGW(TAG, "FACTORY RESET via command! Erasing NVS namespaces...");
    rgb_led_set_state(LED_STATE_FACTORY_RESET);
    
    /* Erase only whitelisted NVS namespaces */
    for (size_t i = 0; i < NVS_NAMESPACE_COUNT; i++) {
        nvs_handle_t handle;
        esp_err_t err = nvs_open(NVS_NAMESPACES[i], NVS_READWRITE, &handle);
        if (err == ESP_OK) {
            err = nvs_erase_all(handle);
            nvs_commit(handle);
            nvs_close(handle);
            if (err == ESP_OK) {
                ESP_LOGI(TAG, "Erased namespace: %s", NVS_NAMESPACES[i]);
            } else {
                ESP_LOGW(TAG, "Failed to erase %s: %s", NVS_NAMESPACES[i], esp_err_to_name(err));
            }
        } else if (err != ESP_ERR_NVS_NOT_FOUND) {
            ESP_LOGW(TAG, "Failed to open %s: %s", NVS_NAMESPACES[i], esp_err_to_name(err));
        }
    }
    
    ESP_LOGW(TAG, "NVS namespaces erased. Rebooting in 2s...");
    vTaskDelay(pdMS_TO_TICKS(2000));
    esp_restart();
}
