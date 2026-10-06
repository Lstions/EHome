/**
 * @file bus_dma.c
 * @brief Unified Bus DMA Engine for ESP-IDF v6
 *
 * Supports UART, SPI, and I2C with dynamic DMA on/off switch.
 * - UART DMA:  driver_install with ring buffers + rx_timeout gap detection
 * - UART non-DMA: smaller driver ring, still consumed through the same event
 *   queue and worker path
 * - SPI DMA:   spi_bus_initialize with SPI_DMA_CH_AUTO
 * - SPI polled: spi_bus_initialize with SPI_DMA_DISABLED
 * - I2C:        new master API for both DMA settings; the planner rejects
 *               DMA requests on profiles without I2C DMA capability so the
 *               flag cannot alter repeated-START/STOP wire semantics
 */

#include "bus_dma.h"
#include "rgb_led.h"
#include "hw_tables.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_heap_caps.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"
#include "driver/gpio.h"
#include "nvs_flash.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#if SOC_USB_SERIAL_JTAG_SUPPORTED
#include "driver/usb_serial_jtag.h"
#include "driver/usb_serial_jtag_vfs.h"
#endif
#include <string.h>

#define TAG "BUS_DMA"

/* ------------------------------------------------------------------ */
/*  UART0 Boot Mode Manager (merged from uart0_boot component)        */
/*                                                                    */
/*  Manages the dual-use of UART0: normal data serial port vs         */
/*  firmware download.                                                */
/*                                                                    */
/*  On ESP32-S3:  UART0 download port = TX43/RX44, strap = GPIO0     */
/*  On ESP32-C6:  UART0 download port = TX16/RX17, strap = GPIO8     */
/*                                                                    */
/*  C6 constraint: GPIO8 is shared with RGB LED (WS2812), so GPIO    */
/*  hold cannot be used. Instead, C6 uses an NVS flag: set flag,     */
/*  reboot, check flag on next boot to enter download wait mode.     */
/* ------------------------------------------------------------------ */

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

bool bus_dma_uart0_boot_init(void)
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

void bus_dma_uart0_enter_download(void)
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
     * Set flag, reboot. On next boot, bus_dma_uart0_boot_init reads flag
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

bool bus_dma_uart0_is_available(void)
{
    return s_uart0_available;
}

/* ------------------------------------------------------------------ */
/*  UART0 availability: depends on console output target               */
/*  If console is on USB Serial/JTAG, UART0 is free for data use.     */
/*  If console is on UART0, we must skip it.                          */
/*                                                                    */
/*  When UART0 is available (console on USB-JTAG), we allow it to be  */
/*  used as a data channel.  The first UART channel gets UART_NUM_0,  */
/*  the second gets UART_NUM_1, etc.  This lets users connect an     */
/*  external USB-serial adapter to the UART0 boot pins (C6: GPIO16/17)*/
/*  and use it for sensor communication.                              */
/*                                                                    */
/*  When UART0 is NOT available (console on UART0), we skip it and    */
/*  start from UART1.                                                 */
/* ------------------------------------------------------------------ */
#if defined(CONFIG_ESP_CONSOLE_USB_SERIAL_JTAG) || defined(CONFIG_ESP_CONSOLE_USB_CDC)
  #define UART0_START_INDEX  0   /* Console on USB — UART0 available for data */
#else
  #define UART0_START_INDEX  1   /* Console on UART0 — skip UART0 (reserved)  */
#endif

/* GPIO pin max varies by chip: S3=48, C6=30 */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define GPIO_PIN_MAX  48
  /* Reserved pins — USB Serial/JTAG and RGB LED */
  #define RESERVED_PIN_USB_DN  19
  #define RESERVED_PIN_USB_DP  20
  #define RESERVED_PIN_LED     48
#else
  #define GPIO_PIN_MAX  30
  /* Reserved pins — USB Serial/JTAG and RGB LED */
  #define RESERVED_PIN_USB_DN  12
  #define RESERVED_PIN_USB_DP  13
  #define RESERVED_PIN_LED      8
#endif

/* Check if a pin is reserved (USB or LED) */
static inline bool is_pin_reserved(int pin)
{
    return pin == RESERVED_PIN_USB_DN ||
           pin == RESERVED_PIN_USB_DP ||
           pin == RESERVED_PIN_LED;
}

/* ------------------------------------------------------------------ */
/*  Helpers                                                           */
/* ------------------------------------------------------------------ */

static inline uint32_t read_be32(const uint8_t *p)
{
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8)  |  (uint32_t)p[3];
}

/* ------------------------------------------------------------------ */
/*  UART init / transact / deinit (with port sharing)                 */
/* ------------------------------------------------------------------ */

/* Shared UART port registry */
#define MAX_UART_PORTS 3
/* UART 驱动事件队列深度。**由 32 降到 8（2026-10-05 实机测量）**。
 *
 * 这一步是 S3 内存问题的真正大头。逐总线实测（bus_manager 的 [busheap] 探针）：
 *
 *     ch=2 UART0  12948 -> 9848   -3100
 *     ch=4 UART1   9848 -> 6768   -3080
 *     ch=5 UART2   6768 -> 3672   -3096     3 路 UART 合计 ~9.3KB
 *     ch=6 SPI2    3672 -> 2320   -1352
 *     ch=7 I2C0    2320 ->  896   -1424
 *
 * 每路 UART 约 3.1KB，而缓冲区（512+512）只占其中约 1KB。差额来自 IDF 的
 * uart_alloc_driver_obj()（components/esp_driver_uart/src/uart.c:1962）：
 * 每路要分配 **9 个独立堆对象** —— uart_obj_t 本体、rx_data_buf
 * （UART_HW_FIFO_LEN × sizeof(uint32_t) = 128×4 = **512 字节**）、
 * event_queue、tx/rx 两个 ringbuf、以及 5 个 mutex/semaphore。
 *
 * event_queue ≈ 深度 × sizeof(uart_event_t) ≈ 32×12 ≈ 400 字节。深度 32 是
 * 历史值，从未按实际消费速率论证过：事件由 rx_task 通过 queue set 持续排空，
 * 稳态下队列里通常只有个位数条目。8 仍能吸收突发，但省下约 300 字节/路
 * —— 在只有 876 字节可用堆的现场，这个量级是决定性的。
 *
 * 注意：这里**不是**靠缩小 RX/TX 缓冲换内存。RX 缓冲已另行由 1024 降到 512
 * （仍远大于 128 字节硬件 FIFO），本条是进一步去掉队列深度的过量预留。 */
#define UART_EVENT_QUEUE_DEPTH 8

/* UART 驱动缓冲区。DMA 路径用 512（原为 1024）；非 DMA 路径 256 不变。
 * 降低的理由见 uart_driver_install 调用处的大段说明：S3 只有约 7~8KB
 * 可用堆时，3 路 UART 各占 1024+1024 会让第三路 uart_driver_install
 * 失败，导致整个配置事务回滚。512 仍远大于硬件 FIFO（128 字节）。 */
#define UART_DRV_RX_BUFFER       512U
#define UART_DRV_TX_BUFFER       512U
#define UART_DRV_RX_BUFFER_SMALL 256U
#define UART_DRV_TX_BUFFER_SMALL 256U

typedef struct {
    uart_port_t port;
    int tx_pin;
    int rx_pin;
    uint32_t baud;
    uint32_t ref_count;  /* Number of logical channels currently leasing this port */
    QueueHandle_t event_queue;
    /* WS-E install-once lifecycle:
     *   installed: the IDF driver object for this controller exists and is
     *              kept across manifest rebuilds (that is the whole point —
     *              re-installing it cost ~3 KB of contiguous heap per UART).
     *   leased:    at least one logical channel currently owns it.  A port
     *              with installed && !leased is idle but still resident; only
     *              bus_dma_uart_teardown() frees it (called by the manager's
     *              prune step after a successful apply, or explicitly). */
    bool installed;
    bool leased;
} uart_port_entry_t;

static uart_port_entry_t s_uart_ports[MAX_UART_PORTS];
static bool s_uart_registry_initialized = false;

static void uart_registry_init(void)
{
    if (!s_uart_registry_initialized) {
        memset(s_uart_ports, 0, sizeof(s_uart_ports));
        for (int i = 0; i < MAX_UART_PORTS; i++) {
            s_uart_ports[i].port = UART_NUM_MAX;  /* Invalid marker */
        }
        s_uart_registry_initialized = true;
    }
}

/* Find existing UART port with matching config */
static uart_port_entry_t *uart_find_port(int tx_pin, int rx_pin, uint32_t baud)
{
    uart_registry_init();
    for (int i = 0; i < MAX_UART_PORTS; i++) {
        if (s_uart_ports[i].port != UART_NUM_MAX &&
            s_uart_ports[i].tx_pin == tx_pin &&
            s_uart_ports[i].rx_pin == rx_pin &&
            s_uart_ports[i].baud == baud) {
            return &s_uart_ports[i];
        }
    }
    return NULL;
}

/* Allocate new UART port entry — skip UART0 if console is on UART0 */
static uart_port_entry_t *uart_alloc_port(void)
{
    uart_registry_init();
    for (int i = UART0_START_INDEX; i < MAX_UART_PORTS; i++) {
        if (s_uart_ports[i].port == UART_NUM_MAX) {
            return &s_uart_ports[i];
        }
    }
    return NULL;
}

/* Find the entry for a concrete controller, installed or not.  uart_find_port()
 * above matches by pin/baud and therefore cannot answer "is controller N still
 * installed after its last channel was released?". */
static uart_port_entry_t *uart_find_installed(uart_port_t port)
{
    uart_registry_init();
    for (int i = 0; i < MAX_UART_PORTS; i++) {
        if (s_uart_ports[i].port == port && s_uart_ports[i].installed) {
            return &s_uart_ports[i];
        }
    }
    return NULL;
}

/* First installed but unleased entry (used when pins do not map to a fixed
 * controller and no fresh slot is free).  Reusing an idle resident driver
 * avoids a pointless teardown+install churn on the next apply. */
static uart_port_entry_t *uart_find_idle_installed(void)
{
    uart_registry_init();
    for (int i = UART0_START_INDEX; i < MAX_UART_PORTS; i++) {
        if (s_uart_ports[i].port != UART_NUM_MAX &&
            s_uart_ports[i].installed && !s_uart_ports[i].leased) {
            return &s_uart_ports[i];
        }
    }
    return NULL;
}

/* ------------------------------------------------------------------ */
/*  UART lifecycle (WS-E)                                              */
/*                                                                     */
/*  install-once contract:                                             */
/*    - uart_driver_install() runs at most once per controller for the */
/*      life of the process.  A manifest change that only moves pins,  */
/*      baud or DMA preference reconfigures the live driver via        */
/*      uart_param_config()/uart_set_pin()/uart_set_baudrate().        */
/*    - bus_dma_deinit() releases the *lease*, it does not delete the  */
/*      driver.  bus_dma_uart_teardown() is the only path that frees a */
/*      controller, and the manager calls it only after a successful   */
/*      apply for ports no longer leased (channel removed / controller */
/*      moved), or explicitly for whole-runtime teardown.              */
/*                                                                     */
/*  Why: reinstalling 3 UART drivers per transaction cost ~9.3 KB of   */
/*  contiguous heap (3 x ~3.1 KB; IDF allocates 9 objects per driver,  */
/*  esp_driver_uart/src/uart.c:1962).  On the S3 field unit that left  */
/*  free=876 / largest=832, which is what broke the transaction.       */
/* ------------------------------------------------------------------ */

/* Build the IDF config used by both install and reconfigure.  Keeping one
 * builder guarantees a live driver and a freshly installed one end in the
 * same state. */
static uart_config_t uart_make_config(uart_port_t port, uint32_t baud)
{
    uart_config_t uart_cfg = {
        .baud_rate  = (int)baud,
        .data_bits  = UART_DATA_8_BITS,
        .parity     = UART_PARITY_DISABLE,
        .stop_bits  = UART_STOP_BITS_1,
        .flow_ctrl  = UART_HW_FLOWCTRL_DISABLE,
#if SOC_UART_LP_NUM >= 1
        .lp_source_clk = (port >= SOC_UART_HP_NUM)
                         ? LP_UART_SCLK_DEFAULT
                         : (lp_uart_sclk_t)UART_SCLK_DEFAULT,
#else
        .source_clk = UART_SCLK_DEFAULT,
#endif
    };
    return uart_cfg;
}

/* True when the controller has fixed IOs and uart_set_pin() must be skipped
 * (LP_UART on C6). */
static bool uart_has_fixed_pins(uart_port_t port)
{
    for (int i = 0; i < HW_UART_COUNT; i++) {
        if (hw_uarts[i].port == port) return hw_uart_is_lp(&hw_uarts[i]);
    }
    return false;
}

/* Apply pin/baud changes to an already-installed driver.
 *
 * On failure the entry's OLD pin/baud are written back so the caller's
 * rollback has a consistent controller to hand back to the previous manifest
 * (the IDF setters are transactional per call; restoring is idempotent).
 * A write-back failure is logged but not escalated: the caller is already on
 * the error path and will rebuild from old_manifest. */
static esp_err_t uart_reconfigure(uart_port_entry_t *entry, int tx_pin, int rx_pin,
                                  uint32_t baud)
{
    const uart_port_t port = entry->port;
    const int old_tx = entry->tx_pin;
    const int old_rx = entry->rx_pin;
    const uint32_t old_baud = entry->baud;

    /* Let an in-flight byte leave the FIFO before remapping pins.  Callers run
     * this with bus_worker suspended, so this is a bounded drain, not a wait
     * on new traffic. */
    (void)uart_wait_tx_done(port, pdMS_TO_TICKS(100));

    uart_config_t uart_cfg = uart_make_config(port, baud);
    esp_err_t r = uart_param_config(port, &uart_cfg);
    if (r == ESP_OK && !uart_has_fixed_pins(port)) {
        r = uart_set_pin(port, tx_pin, rx_pin, -1, -1);
    }
    if (r == ESP_OK) {
        r = uart_set_baudrate(port, baud);
    }

    if (r != ESP_OK) {
        ESP_LOGE(TAG, "UART%d reconfigure failed: %s (tx=%d rx=%d baud=%lu); restoring",
                 (int)port, esp_err_to_name(r), tx_pin, rx_pin, (unsigned long)baud);
        if (!uart_has_fixed_pins(port)) {
            (void)uart_set_pin(port, old_tx, old_rx, -1, -1);
        }
        (void)uart_set_baudrate(port, old_baud);
        return r;
    }

    entry->tx_pin = tx_pin;
    entry->rx_pin = rx_pin;
    entry->baud = baud;
    ESP_LOGI(TAG, "UART%d reused/reconfigured (TX=%d RX=%d baud=%lu)",
             (int)port, tx_pin, rx_pin, (unsigned long)baud);
    return ESP_OK;
}

/* Install a brand-new driver and register the entry.  Only reached from
 * uart_port_acquire() when no resident driver can serve the request. */
static esp_err_t uart_install_new(bus_dma_ctx_t *ctx, int tx_pin, int rx_pin,
                                  uint32_t baud)
{
    const uart_port_t port = ctx->cfg.uart.port;

#if SOC_UART_LP_NUM >= 1
    /* LP_UART does not support DMA — force polled mode */
    if (uart_has_fixed_pins(port) && ctx->dma_enabled) {
        ESP_LOGW(TAG, "LP_UART port %d does not support DMA, forcing polled mode", (int)port);
        ctx->dma_enabled = false;
    }
#endif

    uart_config_t uart_cfg = uart_make_config(port, baud);
    esp_err_t r = uart_param_config(port, &uart_cfg);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "uart_param_config failed: %s", esp_err_to_name(r));
        return r;
    }

    if (!uart_has_fixed_pins(port)) {
        r = uart_set_pin(port, tx_pin, rx_pin, -1, -1);
        if (r != ESP_OK) {
            ESP_LOGE(TAG, "uart_set_pin failed: %s", esp_err_to_name(r));
            return r;
        }
    } else {
        ESP_LOGI(TAG, "LP_UART port %d: using fixed pins (skip uart_set_pin)", (int)port);
    }

    /* Both DMA and non-DMA paths use the same driver event queue.  DMA only
     * changes the underlying buffer sizes; it must not change the public
     * receive flow or route one UART through a different worker.
     *
     * ── 为什么缓冲区从 1024 降到 512（2026-10-05，实机现场）───────────
     *
     * S3（30EDA0A9A808）的 3 路 UART 每路要 1024+1024 字节驱动缓冲，
     * 外加 32 深的 uart_event_t 队列。三路合计约 7.2KB，而 S3 在做配置
     * 事务时可用堆只有约 7~8KB —— 于是第 3 路必然失败：
     *
     *     E uart: UART driver malloc error
     *     E BUS_DMA: uart_driver_install failed: ESP_FAIL
     *     E BUS_MGR: ch=5 init failed: ESP_FAIL
     *     E CFG_TX: config apply failed at apply_buses: ESP_FAIL
     *     E CALLBACK: Rejecting ConfigManifest transaction: result=2
     *
     * 后果是**整个配置事务回滚**、设备持续上报 success=false；重试时因
     * 分配顺序不同偶尔能成功（现场观察到"首次失败、第二次成功"）。
     *
     * 512 仍远大于 UART 硬件 FIFO（S3/C6 均为 128 字节），且 RX 由
     * rx_task 持续搬运、不依赖缓冲区做大块缓存，因此不影响协议时序。
     * 三路合计由约 7.2KB 降到约 5.1KB，为配置事务腾出必要余量。
     *
     * WS-E 起冷安装只在 preinstall/首笔配置时发生一次；后续 manifest 变更
     * 走 uart_reconfigure()，不再重复这 ~9.3KB 峰值。 */
    QueueHandle_t event_queue = NULL;
    size_t rx_buffer_size = ctx->dma_enabled ? UART_DRV_RX_BUFFER : UART_DRV_RX_BUFFER_SMALL;
    size_t tx_buffer_size = ctx->dma_enabled ? UART_DRV_TX_BUFFER : UART_DRV_TX_BUFFER_SMALL;
    r = uart_driver_install(port, rx_buffer_size, tx_buffer_size,
                            UART_EVENT_QUEUE_DEPTH, &event_queue, 0);
    if (r != ESP_OK) {
        /* 把内存实况一并打出来。2026-10-05 的现场只有一句裸的
         * "UART driver malloc error"，无法判断是"总量不够"还是"碎片"，
         * 定位成本很高；这次直接把 free / largest / min-ever 记下来。
         *
         * 口径：internal（2026-10-06，task-7）。UART 驱动要的是**内部 RAM**
         * 的连续块（DMA 描述符与 ring buffer 不能放 PSRAM），门禁读的也是它。
         * 这里**只留 internal**：这行是分配失败的现场快照，读它就是为了知道
         * 门禁为什么放行/拒绝，而 total 在 s3p 上恒为 PSRAM 的 8.25 MB ——
         * 报它反而复现了那个原始误导信号。加 (internal) 后缀是必需的：
         * 数字从 8.25 MB 变成 ~23 KB，没有标记会让人以为内存一夜之间少了 300 倍。 */
        ESP_LOGE(TAG, "uart_driver_install failed: %s (uart%d rx=%u tx=%u q=%d; "
                      "free=%u largest=%u min_ever=%u (internal))",
                 esp_err_to_name(r), (int)port,
                 (unsigned)rx_buffer_size, (unsigned)tx_buffer_size,
                 UART_EVENT_QUEUE_DEPTH,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT));
        return r;
    }

    uart_port_entry_t *entry = uart_alloc_port();
    if (entry == NULL) {
        ESP_LOGE(TAG, "UART port registry full");
        uart_driver_delete(port);
        return ESP_ERR_NO_MEM;
    }

    entry->port = port;
    entry->tx_pin = tx_pin;
    entry->rx_pin = rx_pin;
    entry->baud = baud;
    entry->ref_count = 0;
    entry->event_queue = event_queue;
    entry->installed = true;
    entry->leased = false;

    ctx->uart_event_queue = event_queue;
    /* A short hardware RX timeout improves event granularity.  The worker
     * still applies its protocol-independent idle fallback. */
    uart_set_rx_timeout(port, 4);

    ESP_LOGI(TAG, "UART%d %s installed (TX=%d RX=%d baud=%lu)",
             (int)port, ctx->dma_enabled ? "DMA" : "polled",
             tx_pin, rx_pin, (unsigned long)baud);
    return ESP_OK;
}

/* Select the controller for a not-yet-installed request, preserving the
 * historical policy (preferred controller wins; profile pins derive a port;
 * otherwise first free).  Split out so both acquire() and preinstall() share
 * exactly one place where a controller id is chosen. */
static esp_err_t uart_select_port(int tx_pin, int rx_pin,
                                  int32_t preferred_controller,
                                  uart_port_t *out_port)
{
    uart_port_t port_num = preferred_controller >= 0
        ? (uart_port_t)preferred_controller
        : hw_derive_uart_port(tx_pin, rx_pin, UART_NUM_MAX);

    if (preferred_controller >= 0 && port_num >= UART_NUM_MAX) {
        ESP_LOGE(TAG, "preferred UART controller %ld is unavailable", (long)preferred_controller);
        return ESP_ERR_NOT_SUPPORTED;
    }
    if (preferred_controller >= 0 && port_num == UART_NUM_0 &&
        !bus_dma_uart0_is_available()) {
        ESP_LOGE(TAG, "preferred UART0 controller is reserved");
        return ESP_ERR_NOT_SUPPORTED;
    }

    if (port_num < UART_NUM_MAX) {
        for (int i = 0; i < MAX_UART_PORTS; i++) {
            if (s_uart_ports[i].port == port_num) {
                ESP_LOGE(TAG, "UART%d is already allocated to a different pin/baud config", port_num);
                return ESP_ERR_INVALID_STATE;
            }
        }
    } else {
        /* No fixed mapping: prefer an idle resident driver over a fresh port,
         * then the first free slot (skip UART0 only when it is the console). */
        uart_port_entry_t *idle = uart_find_idle_installed();
        if (idle != NULL) {
            *out_port = idle->port;
            return ESP_OK;
        }
        for (int i = UART0_START_INDEX; i < MAX_UART_PORTS; i++) {
            if (s_uart_ports[i].port == UART_NUM_MAX) {
                uart_port_t candidate = (uart_port_t)(UART_NUM_0 + i);
                if (candidate >= UART_NUM_MAX) break;
                port_num = candidate;
                break;
            }
        }
    }

    if (port_num >= UART_NUM_MAX) {
        ESP_LOGE(TAG, "No available UART port (all %d in use or exceeds chip limit)", MAX_UART_PORTS);
        return ESP_ERR_NO_MEM;
    }
    *out_port = port_num;
    return ESP_OK;
}

/* Acquire (install or reconfigure) a controller and take a lease on it. */
static esp_err_t uart_port_acquire(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 6) return ESP_ERR_INVALID_SIZE;

    int tx_pin  = cfg[0];
    int rx_pin  = cfg[1];
    uint32_t baud = read_be32(&cfg[2]);

    /* ── TODO(DMA): ESP32-S3 的三个 UART 共享一组 UHCI，同一时刻只能有一个用 DMA ──
     *
     * 硬件事实（用户 2026-10-05 提供的官方依据）：
     *   · ESP32-S3 的 UART0/UART1/UART2 通过 UHCI（主机控制接口）共享一组
     *     DMA TX/RX 通道；UHCI_UART_SEL 用于从多个 UART 中**选择一个**连到
     *     UHCI，UHCI_UART1_CE 决定是否把 UHCI 接到 UART1。
     *   · 因此任一时刻**只有一个 UART 能占用这组共享 DMA 资源**；
     *     需要多路时只能"分时复用"或"混合模式"（一个 DMA + 其余中断/轮询）。
     *   · 官方另提醒：UART DMA 与**蓝牙 HCI 共享硬件**，不要与 BLE HCI 同时用。
     *
     * 当前代码的实际情况（**重要，避免误解**）：本组件对 UART 走的是
     * **中断驱动**（uart_driver_install + 事件队列 + 512B 驱动缓冲），
     * 并未调用任何 UHCI API（全仓 `uhci_*` 零命中）。因此 `dma_enabled`
     * 对 UART 的**唯一实际作用**是选择驱动缓冲大小（见下方 rx/tx_buffer_size），
     * **不构成 UHCI 争用**，也就不是现场 TX 无输出的原因（已实测排除：
     * 同一节点上 UART0 正常、UART1 全无波形，而两者 dma_enabled 都是 true）。
     *
     * 待办（用户要求先记 TODO，不阻塞当前调试）：
     *   1. 若将来真的启用 UART DMA（uhci_*），**必须**在这里加互斥：
     *      同一时刻只允许一个 UART 持有 UHCI，并在配置校验阶段就拒绝
     *      "多个 UART 同时 DMA"的 manifest（而不是运行期静默失败）。
     *   2. 目标形态按用户决定：**混合模式** —— 三个 UART 都"支持" DMA，
     *      但任一时刻只有一个真正启用；其余走中断/轮询。
     *   3. 若将来引入 BLE 配网/BLE OTA，需同时保证 BLE HCI 与 UART DMA
     *      不并存（同一套共享硬件）。
     *
     * 相关位置：components/hw_profile/hw_tables.c 的 hw_dmas/hw_uarts 能力表、
     * components/dma_pool/dma_pool.c 的分配互斥。 */

    /* Validate pins (S3: 0-48, C6: 0-30) */
    if (tx_pin < 0 || tx_pin > GPIO_PIN_MAX || rx_pin < 0 || rx_pin > GPIO_PIN_MAX) {
        ESP_LOGE(TAG, "UART invalid pins: TX=%d RX=%d (must be 0-%d)", tx_pin, rx_pin, GPIO_PIN_MAX);
        return ESP_ERR_INVALID_ARG;
    }
    /* Reject reserved pins (USB/LED) to prevent hardware conflicts */
    if (is_pin_reserved(tx_pin) || is_pin_reserved(rx_pin)) {
        ESP_LOGE(TAG, "UART pin conflict: TX=%d RX=%d (reserved: USB_D-=%d USB_D+=%d LED=%d)",
                 tx_pin, rx_pin, RESERVED_PIN_USB_DN, RESERVED_PIN_USB_DP, RESERVED_PIN_LED);
        return ESP_ERR_INVALID_ARG;
    }

    /* A resident driver whose pins/baud match is the hot path: reconfigure is
     * a no-op and no driver object is touched. */
    uart_port_entry_t *entry = uart_find_port(tx_pin, rx_pin, baud);
    if (entry != NULL && entry->installed) {
        if (ctx->preferred_controller >= 0 &&
            entry->port != (uart_port_t)ctx->preferred_controller) {
            ESP_LOGE(TAG, "UART config is already leased by UART%d, preferred UART%ld requested",
                     (int)entry->port, (long)ctx->preferred_controller);
            return ESP_ERR_INVALID_STATE;
        }
        ctx->cfg.uart.port   = entry->port;
        ctx->cfg.uart.baud   = baud;
        ctx->cfg.uart.tx_pin = tx_pin;
        ctx->cfg.uart.rx_pin = rx_pin;
        ctx->uart_event_queue = entry->event_queue;
        entry->leased = true;
        entry->ref_count++;
        ESP_LOGI(TAG, "UART%d %s reused (TX=%d RX=%d baud=%lu)",
                 (int)entry->port, ctx->dma_enabled ? "DMA" : "polled",
                 tx_pin, rx_pin, (unsigned long)baud);
        return ESP_OK;
    }

    /* An installed controller with a fixed mapping (preferred) but different
     * pins/baud: reconfigure it in place instead of delete+install.  A
     * controller already leased to another channel is NOT reusable — the old
     * allocator rejected that too ("already allocated to a different
     * pin/baud config"), and silently remapping it would break the running
     * channel. */
    if (ctx->preferred_controller >= 0) {
        entry = uart_find_installed((uart_port_t)ctx->preferred_controller);
        if (entry != NULL && (entry->leased || entry->ref_count > 0)) {
            ESP_LOGE(TAG, "UART%d is already leased to a different pin/baud config",
                     (int)entry->port);
            return ESP_ERR_INVALID_STATE;
        }
        if (entry != NULL) {
            ctx->cfg.uart.port    = entry->port;
            ctx->cfg.uart.baud    = baud;
            ctx->cfg.uart.tx_pin  = tx_pin;
            ctx->cfg.uart.rx_pin  = rx_pin;
            ctx->cfg.uart.turnaround_us = 0;
            ctx->uart_event_queue = entry->event_queue;
#if SOC_UART_LP_NUM >= 1
            if (uart_has_fixed_pins(entry->port) && ctx->dma_enabled) {
                ctx->dma_enabled = false;
            }
#endif
            esp_err_t r = uart_reconfigure(entry, tx_pin, rx_pin, baud);
            if (r != ESP_OK) return r;
            entry->leased = true;
            entry->ref_count++;
            return ESP_OK;
        }
    }

    /* Fresh install.  uart_select_port() rejects a port already occupied by a
     * different pin/baud pair, so a slot here is guaranteed free or idle. */
    uart_port_t port = UART_NUM_MAX;
    esp_err_t r = uart_select_port(tx_pin, rx_pin, ctx->preferred_controller, &port);
    if (r != ESP_OK) return r;

    /* If select_port picked an idle installed entry, reconfigure it instead of
     * leaving a stale mapping behind. */
    entry = uart_find_installed(port);
    if (entry != NULL) {
        ctx->cfg.uart.port   = entry->port;
        ctx->cfg.uart.baud   = baud;
        ctx->cfg.uart.tx_pin = tx_pin;
        ctx->cfg.uart.rx_pin = rx_pin;
        ctx->uart_event_queue = entry->event_queue;
        r = uart_reconfigure(entry, tx_pin, rx_pin, baud);
        if (r != ESP_OK) return r;
        entry->leased = true;
        entry->ref_count++;
        return ESP_OK;
    }

    ctx->cfg.uart.port   = port;
    ctx->cfg.uart.baud   = baud;
    ctx->cfg.uart.tx_pin = tx_pin;
    ctx->cfg.uart.rx_pin = rx_pin;
    ctx->uart_event_queue = NULL;

    r = uart_install_new(ctx, tx_pin, rx_pin, baud);
    if (r != ESP_OK) return r;

    entry = uart_find_installed(port);
    if (entry == NULL) {
        /* uart_install_new registered it; reaching here means the registry
         * was corrupted.  Delete the driver rather than leak it. */
        ESP_LOGE(TAG, "UART%d installed but missing from registry", (int)port);
        uart_driver_delete(port);
        return ESP_FAIL;
    }
    entry->leased = true;
    entry->ref_count++;
    ESP_LOGI(TAG, "UART port ref_count=%lu", (unsigned long)entry->ref_count);
    return ESP_OK;
}

/* Preinstall a controller without taking a lease.  Used before a transaction
 * suspends the workers so the one-time ~3 KB/driver allocation happens at a
 * gated, explicitly measured point instead of inside apply_buses. */
esp_err_t bus_dma_uart_preinstall(uint8_t tx_pin, uint8_t rx_pin, uint32_t baud,
                                  bool dma_enabled, int32_t preferred_controller,
                                  uart_port_t *out_port)
{
    if (tx_pin > GPIO_PIN_MAX || rx_pin > GPIO_PIN_MAX ||
        is_pin_reserved((int)tx_pin) || is_pin_reserved((int)rx_pin)) {
        return ESP_ERR_INVALID_ARG;
    }

    /* INSTALL ONLY.  Pin/baud changes are deliberately NOT applied here.
     *
     * This step exists to pay the one-time driver allocation before the
     * transaction suspends workers.  Reconfiguring has zero heap cost, and at
     * this moment the OLD manifest still holds its leases (cleanup runs inside
     * apply_buses), so touching a live controller's pins would disturb a
     * channel that is still registered.  acquire() reconfigures later, after
     * cleanup has released those leases. */

    /* Same pins/baud already resident?  Nothing to install. */
    uart_port_entry_t *entry = uart_find_port((int)tx_pin, (int)rx_pin, baud);
    if (entry != NULL && entry->installed) {
        if (out_port) *out_port = entry->port;
        return ESP_OK;
    }

    /* The planner's controller is already installed under different
     * pins/baud: acquire() will reconfigure it; nothing to install. */
    if (preferred_controller >= 0) {
        entry = uart_find_installed((uart_port_t)preferred_controller);
        if (entry != NULL) {
            if (out_port) *out_port = entry->port;
            return ESP_OK;
        }
    }

    /* Any other installed-but-idle controller could serve custom pins.  The
     * acquire path prefers it too (uart_select_port), so only an actual free
     * slot needs a fresh install. */
    entry = uart_find_idle_installed();
    if (entry != NULL) {
        if (out_port) *out_port = entry->port;
        return ESP_OK;
    }

    /* Fresh install on a stack-only context: no bus_dma_ctx_t is published, so
     * the controller ends up installed but unleased.  Resource preflight and
     * the memory gate are the caller's responsibility (bus_manager does both
     * before any teardown). */
    bus_dma_ctx_t probe;
    memset(&probe, 0, sizeof(probe));
    probe.dma_enabled = dma_enabled;
    probe.preferred_controller = preferred_controller;

    uart_port_t port = UART_NUM_MAX;
    esp_err_t r = uart_select_port((int)tx_pin, (int)rx_pin,
                                   preferred_controller, &port);
    if (r != ESP_OK) return r;

    entry = uart_find_installed(port);
    if (entry != NULL) {          /* idle reuse, selected above by policy */
        if (out_port) *out_port = entry->port;
        return ESP_OK;
    }

    probe.cfg.uart.port = port;
    r = uart_install_new(&probe, (int)tx_pin, (int)rx_pin, baud);
    if (r != ESP_OK) return r;
    if (out_port) *out_port = port;
    return ESP_OK;
}

/* Release a controller's driver.  Only valid when installed && !leased; the
 * manager calls this after a successful apply for ports the new manifest no
 * longer uses (or explicitly for whole-runtime teardown).  Releasing a leased
 * port would yank the event queue out from under a live worker. */
esp_err_t bus_dma_uart_teardown(uart_port_t port)
{
    uart_port_entry_t *entry = uart_find_installed(port);
    if (entry == NULL) return ESP_OK;   /* already gone: idempotent */
    if (entry->leased || entry->ref_count > 0) {
        ESP_LOGW(TAG, "UART%d teardown refused: still leased (ref=%lu)",
                 (int)port, (unsigned long)entry->ref_count);
        return ESP_ERR_INVALID_STATE;
    }

    esp_err_t r = uart_driver_delete(port);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "UART%d driver delete failed: %s", (int)port, esp_err_to_name(r));
        return r;
    }
    entry->port = UART_NUM_MAX;
    entry->event_queue = NULL;
    entry->installed = false;
    entry->leased = false;
    entry->ref_count = 0;
    ESP_LOGI(TAG, "UART%d driver torn down", (int)port);
    return ESP_OK;
}

/* ==== UART: independent TX (fire-and-forget) ==== */
/* ==== UART: independent TX (fire-and-forget) ==== */

static esp_err_t uart_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    uart_port_t port = ctx->cfg.uart.port;

    if (data && len > 0) {
        int w = uart_write_bytes(port, (const char *)data, len);
        if (w < 0) {
            ESP_LOGW(TAG, "UART write failed");
            return ESP_FAIL;
        }
        uart_wait_tx_done(port, pdMS_TO_TICKS(100));
    }
    return ESP_OK;
}

/* ==== UART: independent RX (event-owned, non-blocking read) ==== */

static size_t uart_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size)
{
    uart_port_t port = ctx->cfg.uart.port;

    /*
     * DMA path: the DMA ring buffer captures data autonomously.
     * Non-blocking read picks up whatever has accumulated.
     *
     * Polled path: uart_read_bytes with timeout=0 returns FIFO content
     * immediately.  No flush — flush would discard useful data.
     */
    size_t total = 0;
    int n = uart_read_bytes(port, buf, buf_size, 0);  /* timeout=0: non-blocking */
    if (n > 0) {
        total = (size_t)n;
        /* Drain remaining bytes if DMA ring has more (quick gap detection) */
        while (total < buf_size) {
            n = uart_read_bytes(port, buf + total, buf_size - total, pdMS_TO_TICKS(2));
            if (n <= 0) break;
            total += (size_t)n;
        }
    }
    return total;
}

/* Release this channel's lease on its controller.
 *
 * WS-E install-once: the driver object stays resident.  Actually freeing it is
 * bus_dma_uart_teardown()'s job, and the manager only calls that for ports the
 * new manifest no longer leases.  Keeping the driver alive here is what makes
 * a manifest rebuild cheap: the next apply reconfigures instead of
 * reinstalling (~3 KB contiguous per UART). */
static esp_err_t uart_deinit(bus_dma_ctx_t *ctx)
{
    uart_port_entry_t *port_entry = uart_find_installed(ctx->cfg.uart.port);
    if (port_entry == NULL) {
        /* No resident driver: tolerate teardown of an already-uninstalled port
         * (idempotent), but surface it because it usually means bookkeeping is
         * out of sync. */
        ESP_LOGW(TAG, "UART%d lease release with no installed driver", (int)ctx->cfg.uart.port);
        return ESP_OK;
    }

    if (port_entry->ref_count > 0) {
        port_entry->ref_count--;
    }
    if (port_entry->ref_count == 0) {
        port_entry->leased = false;
    }
    ESP_LOGI(TAG, "UART%d lease released, ref_count=%lu (driver stays installed)",
             (int)ctx->cfg.uart.port, (unsigned long)port_entry->ref_count);
    return ESP_OK;
}

/* ------------------------------------------------------------------ */
/*  USB Serial/JTAG as a DATA bus                                      */
/*                                                                     */
/*  The C6 internal USB endpoint is a virtual serial port whose rate is */
/*  set by the host, so there is no baud/pin to configure and no UART   */
/*  controller to lease.  We surface it through the SAME public shape   */
/*  as UART so that bus_worker's stream RX path (event -> read ring ->  */
/*  idle/read_size boundary) works unchanged.                           */
/*                                                                     */
/*  Two IDF facts this implementation depends on:                       */
/*   1. usb_serial_jtag_driver_install() may be called even when the    */
/*      console is USB-Serial-JTAG; it does not check CONFIG_ESP_CONSOLE.*/
/*      (verified: no CONFIG_ESP_CONSOLE reference in usb_serial_jtag.c) */
/*   2. usb_serial_jtag_vfs_use_driver() then routes console TX/RX      */
/*      through that driver, so logs keep working on the same endpoint.  */
/* ------------------------------------------------------------------ */

#if SOC_USB_SERIAL_JTAG_SUPPORTED

/* Reader task: the IDF driver has no event queue (unlike uart_driver_install).
 * rx_task waits on a FreeRTOS queue set built from per-channel event queues, so
 * we synthesise uart_event_t notifications from a dedicated pump.  This keeps a
 * single RX wait path in bus_worker instead of teaching it a second one. */
#define USB_TX_BUFFER_SIZE    512
#define USB_RX_BUFFER_SIZE    1024
#define USB_PUMP_TASK_STACK   3072
#define USB_PUMP_TASK_PRIO    9
#define USB_PUMP_READ_CHUNK   256
#define USB_PUMP_IDLE_MS      10   /* poll period when idle (~1 kHz task rate is avoided) */

/* The IDF USB-Serial-JTAG driver has no event queue, so we synthesise
 * uart_event_t notifications with a pump task.  CRITICAL: the pump must also be
 * the place the bytes come to rest, because usb_serial_jtag_read_bytes() CONSUMES
 * them.  UART's contract is "notify, leave the payload in the ring"; if the pump
 * consumed the bytes and only posted a notification, rx_task's later
 * bus_dma_read() would find an empty ring and every response would look like a
 * timeout.  So the pump appends into a ring buffer and bus_dma_read() drains
 * that buffer instead of the driver. */
typedef struct {
    QueueHandle_t   event_queue;   /* synthesised uart_event_t sink */
    volatile bool   stop;
    TaskHandle_t    task;
    uint8_t        *scratch;
    /* Bytes already taken from the driver, waiting for rx_task.  Single producer
     * (pump) / single consumer (rx_task) => the two counters are sufficient; no
     * lock is needed because only the consumer advances rx_tail and only the
     * producer advances rx_head. */
    uint8_t        *ring;
    volatile size_t ring_head;
    volatile size_t ring_tail;
} usb_bus_runtime_t;

#define USB_RING_SIZE 4096

static usb_bus_runtime_t s_usb;
static portMUX_TYPE      s_usb_lock = portMUX_INITIALIZER_UNLOCKED;

/* Copy bytes into the hand-off ring, dropping only when rx_task has fallen a
 * full ring behind (which the worker's idle/read_size boundary would treat as
 * overflow anyway). */
static size_t usb_ring_push(usb_bus_runtime_t *u, const uint8_t *src, size_t n)
{
    size_t written = 0;
    while (written < n) {
        size_t head = u->ring_head;
        size_t tail = u->ring_tail;
        size_t used = (head >= tail) ? (head - tail) : (USB_RING_SIZE - tail + head);
        if (used >= USB_RING_SIZE) break;          /* full: drop the remainder */
        size_t space = USB_RING_SIZE - used;
        size_t chunk = n - written;
        if (chunk > space) chunk = space;
        for (size_t k = 0; k < chunk; k++) {
            u->ring[(head + k) % USB_RING_SIZE] = src[written + k];
        }
        u->ring_head = (head + chunk) % USB_RING_SIZE;
        written += chunk;
    }
    return written;
}

static void usb_pump_task(void *arg)
{
    usb_bus_runtime_t *u = (usb_bus_runtime_t *)arg;
    while (!u->stop) {
        int n = usb_serial_jtag_read_bytes(u->scratch, USB_PUMP_READ_CHUNK, 0);
        if (n > 0) {
            size_t total = (size_t)n;
            while (total < USB_PUMP_READ_CHUNK) {
                int m = usb_serial_jtag_read_bytes(u->scratch + total,
                                                   USB_PUMP_READ_CHUNK - total, 0);
                if (m <= 0) break;
                total += (size_t)m;
            }
            /* Park the bytes where bus_dma_read() can find them, THEN notify.
             * Order matters: the consumer must never observe an event for data
             * that is not yet in the ring. */
            size_t kept = usb_ring_push(u, u->scratch, total);
            if (kept == 0) {
                ESP_LOGW(TAG, "USB ring full, dropped %u bytes", (unsigned)total);
                continue;
            }
            uart_event_t ev = {0};
            ev.type = UART_DATA;
            ev.size = (uint16_t)(kept > 0xFFFF ? 0xFFFF : kept);
            /* Non-blocking send: the payload is already parked, and the worker
             * also polls the ring on its idle tick, so a dropped notification
             * delays but never loses data. */
            (void)xQueueSend(u->event_queue, &ev, 0);
        } else {
            vTaskDelay(pdMS_TO_TICKS(USB_PUMP_IDLE_MS));
        }
    }
    vTaskDelete(NULL);
}

static esp_err_t usb_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    /* No pins, no baud: the only thing we could validate is "config is ignored".
     * Accept any length (including 0) so the platform does not need to invent
     * placeholder bytes -- see bus_dma.h for why this type has no bus_config. */
    (void)cfg;
    (void)len;

    taskENTER_CRITICAL(&s_usb_lock);
    bool already = (s_usb.task != NULL);
    taskEXIT_CRITICAL(&s_usb_lock);

    QueueHandle_t q = NULL;
    if (!already) {
        q = xQueueCreate(UART_EVENT_QUEUE_DEPTH, sizeof(uart_event_t));
        if (!q) {
            ESP_LOGE(TAG, "USB: event queue alloc failed");
            return ESP_ERR_NO_MEM;
        }
        usb_serial_jtag_driver_config_t ucfg = USB_SERIAL_JTAG_DRIVER_CONFIG_DEFAULT();
        ucfg.tx_buffer_size = USB_TX_BUFFER_SIZE;
        ucfg.rx_buffer_size = USB_RX_BUFFER_SIZE;
        esp_err_t r = usb_serial_jtag_driver_install(&ucfg);
        if (r != ESP_OK) {
            ESP_LOGE(TAG, "USB: driver install failed: %s", esp_err_to_name(r));
            vQueueDelete(q);
            return r;
        }
        /* Route the console through the driver so log output and sensor traffic
         * share the endpoint instead of fighting over the raw FIFO. */
        usb_serial_jtag_vfs_use_driver();

        uint8_t *scratch = malloc(USB_PUMP_READ_CHUNK);
        uint8_t *ring    = malloc(USB_RING_SIZE);
        if (!scratch || !ring) {
            free(scratch);
            free(ring);
            usb_serial_jtag_driver_uninstall();
            vQueueDelete(q);
            return ESP_ERR_NO_MEM;
        }

        taskENTER_CRITICAL(&s_usb_lock);
        s_usb.event_queue = q;
        s_usb.scratch     = scratch;
        s_usb.ring        = ring;
        s_usb.ring_head   = 0;
        s_usb.ring_tail   = 0;
        s_usb.stop        = false;
        taskEXIT_CRITICAL(&s_usb_lock);

        if (xTaskCreate(usb_pump_task, "usb_rx", USB_PUMP_TASK_STACK, &s_usb,
                        USB_PUMP_TASK_PRIO, &s_usb.task) != pdPASS) {
            ESP_LOGE(TAG, "USB: pump task create failed");
            usb_serial_jtag_driver_uninstall();
            free(scratch);
            free(ring);
            vQueueDelete(q);
            s_usb.event_queue = NULL;
            s_usb.scratch = NULL;
            s_usb.ring = NULL;
            s_usb.task = NULL;
            return ESP_ERR_NO_MEM;
        }
    } else {
        q = s_usb.event_queue;
    }

    ctx->uart_event_queue = q;   /* shared stream-bus naming, see bus_dma.h */
    ctx->cfg.usb.ref_count++;

    ESP_LOGI(TAG, "USB data channel init (ref=%lu, console shares endpoint)",
             (unsigned long)ctx->cfg.usb.ref_count);
    return ESP_OK;
}

static esp_err_t usb_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    (void)ctx;
    if (!data || len == 0) return ESP_OK;
    int w = usb_serial_jtag_write_bytes(data, len, pdMS_TO_TICKS(100));
    if (w < 0) {
        ESP_LOGW(TAG, "USB write failed");
        return ESP_FAIL;
    }
    /* Best-effort flush so a Modbus-style turnaround sees the frame on the wire
     * before the reply window opens.  A timeout here is not fatal: the bytes are
     * queued and will be transmitted. */
    (void)usb_serial_jtag_wait_tx_done(pdMS_TO_TICKS(100));
    return ESP_OK;
}

/* Drain the pump's hand-off ring.  Reading the IDF driver here would race the
 * pump and, worse, return nothing because the pump already took the bytes. */
static size_t usb_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size)
{
    (void)ctx;
    size_t total = 0;
    while (total < buf_size) {
        size_t head = s_usb.ring_head;
        size_t tail = s_usb.ring_tail;
        if (head == tail) break;                    /* empty */
        size_t avail = (head > tail) ? (head - tail) : (USB_RING_SIZE - tail + head);
        size_t chunk = buf_size - total;
        if (chunk > avail) chunk = avail;
        for (size_t k = 0; k < chunk; k++) {
            buf[total + k] = s_usb.ring[(tail + k) % USB_RING_SIZE];
        }
        s_usb.ring_tail = (tail + chunk) % USB_RING_SIZE;
        total += chunk;
    }
    return total;
}

static esp_err_t usb_deinit(bus_dma_ctx_t *ctx)
{
    if (ctx->cfg.usb.ref_count > 0) ctx->cfg.usb.ref_count--;
    if (ctx->cfg.usb.ref_count > 0) return ESP_OK;   /* still leased */

    taskENTER_CRITICAL(&s_usb_lock);
    s_usb.stop = true;
    QueueHandle_t q = s_usb.event_queue;
    uint8_t *scratch = s_usb.scratch;
    s_usb.event_queue = NULL;
    s_usb.task = NULL;
    taskEXIT_CRITICAL(&s_usb_lock);

    /* Let the pump observe stop before we tear the driver down underneath it. */
    vTaskDelay(pdMS_TO_TICKS(USB_PUMP_IDLE_MS * 3));

    usb_serial_jtag_driver_uninstall();
    if (q) vQueueDelete(q);
    if (scratch) free(scratch);
    uint8_t *ring = s_usb.ring;
    s_usb.ring = NULL;
    if (ring) free(ring);
    s_usb.scratch = NULL;

    ESP_LOGI(TAG, "USB data channel deinit");
    return ESP_OK;
}

#endif /* SOC_USB_SERIAL_JTAG_SUPPORTED */

/* ------------------------------------------------------------------ */
/*  SPI init / transact / deinit (with bus sharing)                   */
/* ------------------------------------------------------------------ */

/* Shared SPI bus registry */
#define MAX_SPI_BUSES 4

typedef struct {
    spi_host_device_t host;
    int mosi_pin;
    int miso_pin;
    int sclk_pin;
    bool dma_enabled;
    uint32_t ref_count;  /* Number of devices using this bus */
} spi_bus_entry_t;

static spi_bus_entry_t s_spi_buses[MAX_SPI_BUSES];
static bool s_spi_registry_initialized = false;

static void spi_registry_init(void)
{
    if (!s_spi_registry_initialized) {
        memset(s_spi_buses, 0, sizeof(s_spi_buses));
        for (int i = 0; i < MAX_SPI_BUSES; i++) {
            s_spi_buses[i].host = SPI_HOST_MAX;  /* Invalid marker */
        }
        s_spi_registry_initialized = true;
    }
}

/* Find existing SPI bus with matching config */
static spi_bus_entry_t *spi_find_bus(int mosi, int miso, int sclk, bool dma)
{
    spi_registry_init();
    for (int i = 0; i < MAX_SPI_BUSES; i++) {
        if (s_spi_buses[i].host != SPI_HOST_MAX &&
            s_spi_buses[i].mosi_pin == mosi &&
            s_spi_buses[i].miso_pin == miso &&
            s_spi_buses[i].sclk_pin == sclk &&
            s_spi_buses[i].dma_enabled == dma) {
            return &s_spi_buses[i];
        }
    }
    return NULL;
}

/* Allocate new SPI bus entry */
static spi_bus_entry_t *spi_alloc_bus(void)
{
    spi_registry_init();
    for (int i = 0; i < MAX_SPI_BUSES; i++) {
        if (s_spi_buses[i].host == SPI_HOST_MAX) {
            return &s_spi_buses[i];
        }
    }
    return NULL;
}

static esp_err_t spi_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 6) return ESP_ERR_INVALID_SIZE;

    int cs_pin     = cfg[0];
    uint8_t mode   = cfg[1];
    uint32_t freq  = read_be32(&cfg[2]);

    /* SPI bus pins - parse from config if provided, otherwise use defaults */
    int mosi_pin = -1;  /* Default: use board default */
    int miso_pin = -1;
    int sclk_pin = -1;

    /* Parse MOSI/MISO/SCLK from config if len >= 9 */
    if (len >= 9) {
        mosi_pin = cfg[6];
        miso_pin = cfg[7];
        sclk_pin = cfg[8];
    }

    ESP_LOGI(TAG, "SPI config: CS=%d, mode=%d, freq=%lu, MOSI=%d, MISO=%d, SCLK=%d, dma=%d",
             cs_pin, mode, (unsigned long)freq, mosi_pin, miso_pin, sclk_pin, ctx->dma_enabled);

    /* Reject reserved pins (USB/LED) to prevent hardware conflicts */
    if (cs_pin >= 0 && is_pin_reserved(cs_pin)) {
        ESP_LOGE(TAG, "SPI pin conflict: CS=%d (reserved: USB/LED)", cs_pin);
        return ESP_ERR_INVALID_ARG;
    }
    if (mosi_pin >= 0 && is_pin_reserved(mosi_pin)) {
        ESP_LOGE(TAG, "SPI pin conflict: MOSI=%d (reserved: USB/LED)", mosi_pin);
        return ESP_ERR_INVALID_ARG;
    }
    if (miso_pin >= 0 && is_pin_reserved(miso_pin)) {
        ESP_LOGE(TAG, "SPI pin conflict: MISO=%d (reserved: USB/LED)", miso_pin);
        return ESP_ERR_INVALID_ARG;
    }
    if (sclk_pin >= 0 && is_pin_reserved(sclk_pin)) {
        ESP_LOGE(TAG, "SPI pin conflict: SCLK=%d (reserved: USB/LED)", sclk_pin);
        return ESP_ERR_INVALID_ARG;
    }

    bool forced_host = ctx->preferred_controller >= 0;
    spi_host_device_t preferred_host = forced_host
        ? (spi_host_device_t)ctx->preferred_controller
        : hw_derive_spi_host(mosi_pin, miso_pin, sclk_pin, SPI_HOST_MAX);
    if (forced_host && preferred_host >= SPI_HOST_MAX) {
        ESP_LOGE(TAG, "preferred SPI controller %ld is unavailable",
                 (long)ctx->preferred_controller);
        return ESP_ERR_NOT_SUPPORTED;
    }
    ctx->cfg.spi.host = preferred_host < SPI_HOST_MAX ? preferred_host : SPI2_HOST;
    ctx->cfg.spi.cs_pin = cs_pin;
    ctx->cfg.spi.freq   = freq;
    ctx->cfg.spi.mode   = mode;
    ctx->cfg.spi.mosi_pin = mosi_pin;
    ctx->cfg.spi.miso_pin = miso_pin;
    ctx->cfg.spi.sclk_pin = sclk_pin;

    /* Check if bus with same config already exists */
    spi_bus_entry_t *bus_entry = spi_find_bus(mosi_pin, miso_pin, sclk_pin, ctx->dma_enabled);
    if (bus_entry && forced_host && bus_entry->host != preferred_host) {
        ESP_LOGE(TAG, "SPI pin bus is leased by SPI%d, preferred SPI%d requested",
                 bus_entry->host, preferred_host);
        return ESP_ERR_INVALID_STATE;
    }

    if (bus_entry == NULL) {
        /* Find available SPI host */
        spi_host_device_t host = preferred_host;
        bool preferred_busy = false;
        if (host < SPI_HOST_MAX) {
            for (int i = 0; i < MAX_SPI_BUSES; i++) {
                if (s_spi_buses[i].host == host) {
                    preferred_busy = true;
                    break;
                }
            }
        }
        if (forced_host && preferred_busy) {
            ESP_LOGE(TAG, "preferred SPI%d controller is already occupied",
                     preferred_host);
            return ESP_ERR_INVALID_STATE;
        }
        if (host >= SPI_HOST_MAX || preferred_busy) {
            host = SPI_HOST_MAX;
            for (spi_host_device_t candidate = SPI2_HOST;
                 candidate < SPI_HOST_MAX; candidate++) {
                bool busy = false;
                for (int i = 0; i < MAX_SPI_BUSES; i++) {
                    if (s_spi_buses[i].host == candidate) {
                        busy = true;
                        break;
                    }
                }
                if (!busy) {
                    host = candidate;
                    break;
                }
            }
        }
        if (host >= SPI_HOST_MAX) return ESP_ERR_NOT_SUPPORTED;

        /* Default bus pins — caller may override via SPI pin config */
        spi_bus_config_t bus_cfg = {
            .mosi_io_num   = mosi_pin,
            .miso_io_num   = miso_pin,
            .sclk_io_num   = sclk_pin,
            .quadwp_io_num = -1,
            .quadhd_io_num = -1,
            .max_transfer_sz = 256,
        };

        esp_err_t r = spi_bus_initialize(host, &bus_cfg,
                                         ctx->dma_enabled ? SPI_DMA_CH_AUTO
                                                          : SPI_DMA_DISABLED);
        if (r != ESP_OK) {
            ESP_LOGE(TAG, "spi_bus_initialize failed: %s", esp_err_to_name(r));
            return r;
        }

        /* Register in shared bus table */
        bus_entry = spi_alloc_bus();
        if (bus_entry == NULL) {
            ESP_LOGE(TAG, "SPI bus registry full");
            spi_bus_free(host);
            return ESP_ERR_NO_MEM;
        }

        bus_entry->host = host;
        bus_entry->mosi_pin = mosi_pin;
        bus_entry->miso_pin = miso_pin;
        bus_entry->sclk_pin = sclk_pin;
        bus_entry->dma_enabled = ctx->dma_enabled;
        bus_entry->ref_count = 0;

        ctx->cfg.spi.host = host;

        ESP_LOGI(TAG, "SPI%d %s bus init (MOSI=%d MISO=%d SCLK=%d)",
                 host, ctx->dma_enabled ? "DMA" : "polled",
                 mosi_pin, miso_pin, sclk_pin);
    } else {
        /* Reuse existing bus */
        ctx->cfg.spi.host = bus_entry->host;
        ESP_LOGI(TAG, "SPI%d %s bus reused (MOSI=%d MISO=%d SCLK=%d)",
                 bus_entry->host, ctx->dma_enabled ? "DMA" : "polled",
                 mosi_pin, miso_pin, sclk_pin);
    }

    /* Add device to bus */
    spi_device_interface_config_t dev_cfg = {
        .clock_speed_hz = (int)freq,
        .mode           = mode,
        .spics_io_num   = cs_pin,
        .queue_size     = 1,
    };

    esp_err_t r = spi_bus_add_device(ctx->cfg.spi.host, &dev_cfg, &ctx->cfg.spi.dev);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "spi_bus_add_device failed: %s", esp_err_to_name(r));
        /* Only free bus if we just created it (ref_count == 0) */
        if (bus_entry->ref_count == 0) {
            spi_bus_free(ctx->cfg.spi.host);
            bus_entry->host = SPI_HOST_MAX;
        }
        return r;
    }

    bus_entry->ref_count++;
    ESP_LOGI(TAG, "SPI %s device init (CS=%d mode=%d freq=%lu) ref_count=%lu",
             ctx->dma_enabled ? "DMA" : "polled",
             cs_pin, mode, (unsigned long)freq, (unsigned long)bus_entry->ref_count);
    return ESP_OK;
}

/* Byte bound on the scratch frame used for a full-duplex write-then-read.
 * The frame is tx_len + rx_size bytes: every caller is bounded by
 * CMD_TX_MAX == 128 (legacy_write_guard.h) and read_size <= 256, and
 * spi_i2c_cmd_loop clamps the capture to its own 256-byte buffer.  The bound
 * is explicit so a future CMD_TX_MAX/read_size change fails loudly at the
 * source instead of silently truncating a request. */
#define SPI_FD_FRAME_MAX 384

static esp_err_t spi_transact(bus_dma_ctx_t *ctx,
                               const uint8_t *tx, size_t tx_len,
                               uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    *rx_len = 0;

    const bool rx_wanted = (rx != NULL && rx_size > 0);
    const bool tx_wanted = (tx != NULL && tx_len > 0);

    if (!rx_wanted && !tx_wanted) return ESP_ERR_INVALID_ARG;

    spi_transaction_t t = { 0 };

    if (tx_wanted && rx_wanted) {
        /* Full-duplex write-then-read, the shape every SPI sensor read uses
         * (e.g. template 644: 1 register-pointer byte, 6 response bytes).
         *
         * SPI clocks one bit in and one bit out per clock, so a slave cannot
         * answer a command while that command is still being shifted in.  The
         * response therefore occupies the clocks AFTER the command, and the
         * single-CS frame that carries it is (tx_len + rx_size) bytes: the
         * command first, then rx_size neutral clocks that shift the answer
         * back.  That is also exactly what i2c_master_transmit_receive() does
         * on the I2C side (write, repeated START, read), so both transactional
         * buses now describe a read-after-write the same way.
         *
         * The previous code asked for length = tx_len and rxlength = rx_size in
         * one full-duplex transaction.  Whenever the response was longer than
         * the request (8->9 for template 643, 1->6 for 644) IDF rejected the
         * frame before emitting a clock:
         *     E spi_master: check_trans_valid(1119):
         *       rx length > tx length in full duplex mode
         * (esp_driver_spi/src/gpspi/spi_master.c:1119, because the peripheral
         * has no way to clock in more than it clocks out in full duplex), so
         * CMD_SPI counted err++ on 100% of samples.
         *
         * Splitting this into two spi_device_transmit() calls would raise CS
         * between the command and the response and reset the slave's command
         * state machine on most sensors, so it must stay ONE transaction: the
         * MOSI tail is padded with 0x00 (neutral, not stale stack) and the
         * whole frame is captured, then the response is sliced from the tail.
         * IDF copies RX from bit 0 of the frame (spi_hal_fetch_result()), so
         * the first tx_len captured bytes are the slave's pre-response output
         * and are discarded; rx[0..rx_size) is the answer. */
        size_t frame = tx_len + rx_size;
        if (frame > SPI_FD_FRAME_MAX) return ESP_ERR_INVALID_ARG;

        uint8_t scratch_tx[SPI_FD_FRAME_MAX];
        uint8_t scratch_rx[SPI_FD_FRAME_MAX];
        memcpy(scratch_tx, tx, tx_len);
        memset(scratch_tx + tx_len, 0x00, rx_size);

        t.length    = (uint32_t)frame * 8;
        t.tx_buffer = scratch_tx;
        t.rxlength  = (uint32_t)frame * 8;   /* capture the whole frame */
        t.rx_buffer = scratch_rx;

        esp_err_t r = spi_device_transmit(ctx->cfg.spi.dev, &t);
        if (r != ESP_OK) return r;

        memcpy(rx, scratch_rx + tx_len, rx_size);
        *rx_len = rx_size;
        return ESP_OK;
    }

    /* Symmetric (rx_size <= tx_len) and single-direction frames are already a
     * legal full-duplex transaction: rxlength <= length holds, so the caller's
     * own buffers can be used directly. */
    t.length    = (uint32_t)tx_len * 8;          /* bits */
    t.tx_buffer = tx;
    t.rxlength  = rx_size * 8;
    t.rx_buffer = rx;

    /* If TX only, don't request RX */
    if (!rx_wanted) {
        t.rxlength  = 0;
        t.rx_buffer = NULL;
    }
    /* If RX only, don't send TX */
    if (!tx_wanted) {
        t.length    = (uint32_t)rx_size * 8;
        t.tx_buffer = NULL;
    }

    esp_err_t r = spi_device_transmit(ctx->cfg.spi.dev, &t);
    if (r != ESP_OK) return r;

    *rx_len = (t.rxlength > 0) ? (t.rxlength / 8) : 0;
    return ESP_OK;
}

static esp_err_t spi_deinit(bus_dma_ctx_t *ctx)
{
    spi_bus_entry_t *bus_entry = spi_find_bus(ctx->cfg.spi.mosi_pin,
                                               ctx->cfg.spi.miso_pin,
                                               ctx->cfg.spi.sclk_pin,
                                               ctx->dma_enabled);
    if (ctx->cfg.spi.dev) {
        esp_err_t err = spi_bus_remove_device(ctx->cfg.spi.dev);
        if (err != ESP_OK) return err;
        ctx->cfg.spi.dev = NULL;
        if (bus_entry && bus_entry->ref_count > 0) {
            bus_entry->ref_count--;
            ESP_LOGI(TAG, "SPI bus ref_count=%lu", (unsigned long)bus_entry->ref_count);
        }
    }
    /* A prior free attempt may have failed after device removal. Keep the
     * zero-ref registry entry intact so this call retries the driver release. */
    if (bus_entry && bus_entry->ref_count == 0) {
        esp_err_t err = spi_bus_free(ctx->cfg.spi.host);
        if (err != ESP_OK) return err;
        bus_entry->host = SPI_HOST_MAX;
        ESP_LOGI(TAG, "SPI bus freed");
    }
    return ESP_OK;
}

/* ------------------------------------------------------------------ */
/*  I2C init / transact / deinit (with bus sharing)                   */
/* ------------------------------------------------------------------ */

/* Shared I2C bus registry */
#define MAX_I2C_BUSES 4

typedef struct {
    i2c_master_bus_handle_t bus_handle;
    i2c_port_t port;
    int sda_pin;
    int scl_pin;
    uint32_t ref_count;  /* Number of devices using this bus */
} i2c_bus_entry_t;

static i2c_bus_entry_t s_i2c_buses[MAX_I2C_BUSES];
static bool s_i2c_registry_initialized = false;

static void i2c_registry_init(void)
{
    if (!s_i2c_registry_initialized) {
        memset(s_i2c_buses, 0, sizeof(s_i2c_buses));
        s_i2c_registry_initialized = true;
    }
}

/* Find existing I2C bus with matching pins */
static i2c_bus_entry_t *i2c_find_bus(int sda, int scl)
{
    i2c_registry_init();
    for (int i = 0; i < MAX_I2C_BUSES; i++) {
        if (s_i2c_buses[i].bus_handle != NULL &&
            s_i2c_buses[i].sda_pin == sda &&
            s_i2c_buses[i].scl_pin == scl) {
            return &s_i2c_buses[i];
        }
    }
    return NULL;
}

/* Allocate new I2C bus entry */
static i2c_bus_entry_t *i2c_alloc_bus(void)
{
    i2c_registry_init();
    for (int i = 0; i < MAX_I2C_BUSES; i++) {
        if (s_i2c_buses[i].bus_handle == NULL) {
            return &s_i2c_buses[i];
        }
    }
    return NULL;
}

static esp_err_t i2c_init(bus_dma_ctx_t *ctx, const uint8_t *cfg, size_t len)
{
    if (len < 7) return ESP_ERR_INVALID_SIZE;

    int sda       = cfg[0];
    int scl       = cfg[1];
    uint8_t addr  = cfg[2];
    uint32_t freq = read_be32(&cfg[3]);

    /* Validate pins (S3: 0-48, C6: 0-30) */
    if (sda < 0 || sda > GPIO_PIN_MAX || scl < 0 || scl > GPIO_PIN_MAX) {
        ESP_LOGE(TAG, "I2C invalid pins: SDA=%d SCL=%d (must be 0-%d)", sda, scl, GPIO_PIN_MAX);
        return ESP_ERR_INVALID_ARG;
    }
    /* Reject reserved pins (USB/LED) to prevent hardware conflicts */
    if (is_pin_reserved(sda) || is_pin_reserved(scl)) {
        ESP_LOGE(TAG, "I2C pin conflict: SDA=%d SCL=%d (reserved: USB_D-=%d USB_D+=%d LED=%d)",
                 sda, scl, RESERVED_PIN_USB_DN, RESERVED_PIN_USB_DP, RESERVED_PIN_LED);
        return ESP_ERR_INVALID_ARG;
    }

    if (sda == scl) {
        ESP_LOGE(TAG, "I2C SDA and SCL cannot be the same pin: %d", sda);
        return ESP_ERR_INVALID_ARG;
    }

    /* Validate I2C address (7-bit: 0x08-0x77) */
    if (addr < 0x08 || addr > 0x77) {
        ESP_LOGW(TAG, "I2C address 0x%02X may be reserved", addr);
    }

    ctx->cfg.i2c.addr    = addr;
    ctx->cfg.i2c.freq    = freq;
    ctx->cfg.i2c.sda_pin = sda;
    ctx->cfg.i2c.scl_pin = scl;

    /* Check if bus with same pins already exists */
    i2c_bus_entry_t *bus_entry = i2c_find_bus(sda, scl);
    if (bus_entry && ctx->preferred_controller >= 0 &&
        bus_entry->port != (i2c_port_t)ctx->preferred_controller) {
        ESP_LOGE(TAG, "I2C pin bus is leased by I2C%d, preferred I2C%ld requested",
                 bus_entry->port, (long)ctx->preferred_controller);
        return ESP_ERR_INVALID_STATE;
    }
    
    if (bus_entry == NULL) {
        /* Only a new pin pair consumes a hardware I2C controller.  Existing
         * devices on the same SDA/SCL bus are valid shared-bus leases. */
        int active_count = 0;
        for (int i = 0; i < MAX_I2C_BUSES; i++)
            if (s_i2c_buses[i].bus_handle != NULL) active_count++;
        if (active_count >= HW_I2C_COUNT) {
            ESP_LOGE(TAG, "I2C bus limit reached: %d active, HW_I2C_COUNT=%d",
                     active_count, HW_I2C_COUNT);
            return ESP_ERR_NOT_SUPPORTED;
        }

        /* Create new I2C bus */
        bool forced_port = ctx->preferred_controller >= 0;
        i2c_port_t port = forced_port
            ? (i2c_port_t)ctx->preferred_controller
            : hw_derive_i2c_port(sda, scl, I2C_NUM_MAX);
        if (forced_port && port >= I2C_NUM_MAX) {
            ESP_LOGE(TAG, "preferred I2C controller %ld is unavailable",
                     (long)ctx->preferred_controller);
            return ESP_ERR_NOT_SUPPORTED;
        }
        if (port >= I2C_NUM_MAX) {
            for (int p = 0; p < HW_I2C_COUNT; p++) {
                bool busy = false;
                for (int j = 0; j < MAX_I2C_BUSES; j++) {
                    if (s_i2c_buses[j].bus_handle != NULL &&
                        s_i2c_buses[j].port == (i2c_port_t)hw_i2cs[p].port) {
                        busy = true;
                        break;
                    }
                }
                if (!busy) {
                    port = (i2c_port_t)hw_i2cs[p].port;
                    break;
                }
            }
        }
        if (port >= I2C_NUM_MAX) return ESP_ERR_NOT_SUPPORTED;
        bool forced_port_busy = false;
        if (forced_port) {
            for (int i = 0; i < MAX_I2C_BUSES; i++) {
                if (s_i2c_buses[i].bus_handle != NULL &&
                    s_i2c_buses[i].port == port) {
                    forced_port_busy = true;
                    break;
                }
            }
        }
        if (forced_port_busy) {
            ESP_LOGE(TAG, "preferred I2C%d controller is already occupied",
                     port);
            return ESP_ERR_INVALID_STATE;
        }
        ctx->cfg.i2c.port = port;
        i2c_master_bus_config_t bus_cfg = {
            .i2c_port        = port,
            .sda_io_num      = sda,
            .scl_io_num      = scl,
            .clk_source      = I2C_CLK_SRC_DEFAULT,
            .glitch_ignore_cnt = 7,
            .flags.enable_internal_pullup = true,
        };

        esp_err_t r = i2c_new_master_bus(&bus_cfg, &ctx->cfg.i2c.bus_handle);
        if (r != ESP_OK) {
            ESP_LOGE(TAG, "i2c_new_master_bus failed (SDA=%d SCL=%d): %s", 
                     sda, scl, esp_err_to_name(r));
            return r;
        }

        /* Register in shared bus table */
        bus_entry = i2c_alloc_bus();
        if (bus_entry == NULL) {
            ESP_LOGE(TAG, "I2C bus registry full");
            i2c_del_master_bus(ctx->cfg.i2c.bus_handle);
            return ESP_ERR_NO_MEM;
        }
        
        bus_entry->bus_handle = ctx->cfg.i2c.bus_handle;
        bus_entry->port = port;
        bus_entry->sda_pin = sda;
        bus_entry->scl_pin = scl;
        bus_entry->ref_count = 0;
        
        ESP_LOGI(TAG, "I2C bus created (SDA=%d SCL=%d)", sda, scl);
    } else {
        /* Reuse existing bus */
        ctx->cfg.i2c.bus_handle = bus_entry->bus_handle;
        ctx->cfg.i2c.port = bus_entry->port;
        ESP_LOGI(TAG, "I2C bus reused (SDA=%d SCL=%d)", sda, scl);
    }

    /* Add device to bus */
    i2c_device_config_t dev_cfg = {
        .dev_addr_length = I2C_ADDR_BIT_LEN_7,
        .device_address  = addr,
        .scl_speed_hz    = freq,
    };

    esp_err_t r = i2c_master_bus_add_device(ctx->cfg.i2c.bus_handle, &dev_cfg, &ctx->cfg.i2c.dev_handle);
    if (r != ESP_OK) {
        ESP_LOGE(TAG, "i2c_master_bus_add_device failed (addr=0x%02X): %s", 
                 addr, esp_err_to_name(r));
        /* Only delete bus if we just created it (ref_count == 0) */
        if (bus_entry->ref_count == 0) {
            i2c_del_master_bus(ctx->cfg.i2c.bus_handle);
            bus_entry->bus_handle = NULL;
        }
        ctx->cfg.i2c.bus_handle = NULL;
        return r;
    }

    bus_entry->ref_count++;
    
    ESP_LOGI(TAG, "I2C %s init (SDA=%d SCL=%d addr=0x%02X freq=%lu) ref_count=%lu",
             ctx->dma_enabled ? "DMA" : "std",
             sda, scl, addr, (unsigned long)freq, (unsigned long)bus_entry->ref_count);
    return ESP_OK;
}

static esp_err_t i2c_transact(bus_dma_ctx_t *ctx,
                               const uint8_t *tx, size_t tx_len,
                               uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    *rx_len = 0;
    int tmo = 50;  /* I2C needs a timeout for ACK — 50ms is sufficient */

    i2c_master_dev_handle_t dev = ctx->cfg.i2c.dev_handle;
    if (dev == NULL) return ESP_ERR_INVALID_STATE;

    esp_err_t r;
    if (tx && tx_len > 0 && rx && rx_size > 0) {
        /* Wire semantics are independent of the optional DMA allocation:
         * every read-after-write uses one repeated-START transaction.  DMA
         * only changes how the driver moves bytes, never what the peripheral
         * observes on SDA/SCL. */
        r = i2c_master_transmit_receive(dev, tx, tx_len, rx, rx_size, tmo);
        if (r == ESP_OK) *rx_len = rx_size;
    } else if (tx && tx_len > 0) {
        r = i2c_master_transmit(dev, tx, tx_len, tmo);
    } else if (rx && rx_size > 0) {
        r = i2c_master_receive(dev, rx, rx_size, tmo);
        if (r == ESP_OK) *rx_len = rx_size;
    } else {
        r = ESP_ERR_INVALID_ARG;
    }
    return r;
}

static esp_err_t i2c_deinit(bus_dma_ctx_t *ctx)
{
    i2c_bus_entry_t *bus_entry = i2c_find_bus(ctx->cfg.i2c.sda_pin,
                                               ctx->cfg.i2c.scl_pin);
    if (ctx->cfg.i2c.dev_handle) {
        esp_err_t err = i2c_master_bus_rm_device(ctx->cfg.i2c.dev_handle);
        if (err != ESP_OK) return err;
        ctx->cfg.i2c.dev_handle = NULL;
        if (bus_entry && bus_entry->ref_count > 0) {
            bus_entry->ref_count--;
            ESP_LOGI(TAG, "I2C device removed, ref_count=%lu",
                     (unsigned long)bus_entry->ref_count);
        }
    }
    if (bus_entry && bus_entry->ref_count == 0 && ctx->cfg.i2c.bus_handle) {
        esp_err_t err = i2c_del_master_bus(ctx->cfg.i2c.bus_handle);
        if (err != ESP_OK) return err;
        ctx->cfg.i2c.bus_handle = NULL;
        bus_entry->bus_handle = NULL;
        ESP_LOGI(TAG, "I2C bus deleted (SDA=%d SCL=%d)",
                 ctx->cfg.i2c.sda_pin, ctx->cfg.i2c.scl_pin);
    }
    return ESP_OK;
}

/* ------------------------------------------------------------------ */
/*  GPIO init / write / read / deinit (no sharing, no DMA)            */
/* ------------------------------------------------------------------ */
/*  Public API                                                        */
/* ------------------------------------------------------------------ */

static esp_err_t bus_dma_init_internal(bus_dma_ctx_t *ctx, uint8_t bus_type,
                                       bool dma_enabled, const uint8_t *config,
                                       size_t config_len, int32_t controller_id)
{
    if (ctx == NULL || config == NULL) return ESP_ERR_INVALID_ARG;

    memset(ctx, 0, sizeof(*ctx));
    ctx->bus_type    = bus_type;
    ctx->dma_enabled = dma_enabled;
    ctx->preferred_controller = controller_id;

    ctx->tx_mutex = xSemaphoreCreateMutex();
    if (ctx->tx_mutex == NULL) return ESP_ERR_NO_MEM;

    esp_err_t r;
    switch (bus_type) {
        case BUS_TYPE_UART: r = uart_port_acquire(ctx, config, config_len); break;
        case BUS_TYPE_SPI:  r = spi_init(ctx, config, config_len);  break;
        case BUS_TYPE_I2C:  r = i2c_init(ctx, config, config_len);  break;
#if SOC_USB_SERIAL_JTAG_SUPPORTED
        case BUS_TYPE_USB:  r = usb_init(ctx, config, config_len);  break;
#endif
        default:
            ESP_LOGE(TAG, "Unknown bus type: %d", bus_type);
            r = ESP_ERR_NOT_SUPPORTED;
            break;
    }

    if (r != ESP_OK) {
        vSemaphoreDelete(ctx->tx_mutex);
        ctx->tx_mutex = NULL;
        return r;
    }

    ctx->initialized = true;
    return ESP_OK;
}

esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len)
{
    return bus_dma_init_internal(ctx, bus_type, dma_enabled, config,
                                 config_len, -1);
}

esp_err_t bus_dma_init_preferred(bus_dma_ctx_t *ctx, uint8_t bus_type,
                                 bool dma_enabled, const uint8_t *config,
                                 size_t config_len, int32_t controller_id)
{
    if (controller_id < -1) return ESP_ERR_INVALID_ARG;
    return bus_dma_init_internal(ctx, bus_type, dma_enabled, config,
                                 config_len, controller_id);
}

/* ==================================================================
 *  Public API
 * ================================================================== */

/* ---- UART: independent TX ---- */
esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    if (ctx == NULL || !ctx->initialized) return ESP_ERR_INVALID_ARG;
    if (ctx->bus_type != BUS_TYPE_UART && ctx->bus_type != BUS_TYPE_USB)
        return ESP_ERR_NOT_SUPPORTED;

    if (xSemaphoreTake(ctx->tx_mutex, pdMS_TO_TICKS(100)) != pdTRUE)
        return ESP_ERR_TIMEOUT;

    esp_err_t r;
#if SOC_USB_SERIAL_JTAG_SUPPORTED
    if (ctx->bus_type == BUS_TYPE_USB) r = usb_write(ctx, data, len);
    else
#endif
    r = uart_write(ctx, data, len);
    xSemaphoreGive(ctx->tx_mutex);
    return r;
}

/* ---- UART: independent RX (non-blocking) ---- */
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size)
{
    if (ctx == NULL || !ctx->initialized) return 0;
    if (ctx->bus_type != BUS_TYPE_UART && ctx->bus_type != BUS_TYPE_USB) return 0;

    /* No mutex needed for RX: both readers are thread-safe at the driver level
     * and rx_task is the sole consumer. */
#if SOC_USB_SERIAL_JTAG_SUPPORTED
    if (ctx->bus_type == BUS_TYPE_USB) return usb_read(ctx, buf, buf_size);
#endif
    return uart_read(ctx, buf, buf_size);
}

esp_err_t bus_dma_flush_input(const bus_dma_ctx_t *ctx)
{
    if (ctx == NULL || !ctx->initialized) return ESP_ERR_INVALID_ARG;
    switch (ctx->bus_type) {
    case BUS_TYPE_UART:
        return uart_flush_input(ctx->cfg.uart.port);
#if SOC_USB_SERIAL_JTAG_SUPPORTED
    case BUS_TYPE_USB:
        /* Discard the hand-off ring.  Draining the IDF driver here would race
         * the pump (and is meaningless once the pump owns the bytes). */
        s_usb.ring_tail = s_usb.ring_head;
        return ESP_OK;
#endif
    default:
        return ESP_ERR_NOT_SUPPORTED;
    }
}

/* Stream buses (UART and USB) both deliver uart_event_t notifications through
 * this accessor, so bus_worker discovers event queues from the runtime instead
 * of keeping a fixed transport table. */
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *ctx)
{
    if (ctx == NULL || !ctx->initialized) return NULL;
    if (ctx->bus_type != BUS_TYPE_UART && ctx->bus_type != BUS_TYPE_USB)
        return NULL;
    return ctx->uart_event_queue;
}

/* ---- SPI / I2C: transactional ---- */
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len,
                           uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    if (ctx == NULL || !ctx->initialized || rx_len == NULL)
        return ESP_ERR_INVALID_ARG;

    *rx_len = 0;

    if (xSemaphoreTake(ctx->tx_mutex, pdMS_TO_TICKS(1000)) != pdTRUE)
        return ESP_ERR_TIMEOUT;

    esp_err_t r;
    switch (ctx->bus_type) {
        case BUS_TYPE_SPI:
            r = spi_transact(ctx, tx, tx_len, rx, rx_size, rx_len);
            break;
        case BUS_TYPE_I2C:
            r = i2c_transact(ctx, tx, tx_len, rx, rx_size, rx_len);
            break;
        default:
            r = ESP_ERR_NOT_SUPPORTED;
            break;
    }

    xSemaphoreGive(ctx->tx_mutex);
    return r;
}

esp_err_t bus_dma_deinit(bus_dma_ctx_t *ctx)
{
    if (ctx == NULL) return ESP_ERR_INVALID_ARG;
    if (!ctx->initialized) return ESP_OK;

    esp_err_t err;
    switch (ctx->bus_type) {
        case BUS_TYPE_UART: err = uart_deinit(ctx); break;
        case BUS_TYPE_SPI:  err = spi_deinit(ctx);  break;
        case BUS_TYPE_I2C:  err = i2c_deinit(ctx);  break;
#if SOC_USB_SERIAL_JTAG_SUPPORTED
        case BUS_TYPE_USB:  err = usb_deinit(ctx);  break;
#endif
        default: return ESP_ERR_NOT_SUPPORTED;
    }
    if (err != ESP_OK) return err;

    if (ctx->tx_mutex) {
        vSemaphoreDelete(ctx->tx_mutex);
        ctx->tx_mutex = NULL;
    }

    ctx->initialized = false;
    ctx->uart_event_queue = NULL;
    ESP_LOGI(TAG, "Bus type=%d deinit", ctx->bus_type);
    return ESP_OK;
}
