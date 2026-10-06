/**
 * @file main.c
 * @brief EHomeSystem ESP32-C6 v2.4 — Unified Bus DMA + Resource Reporting
 *
 * app_main() is the sole entry point.  All business logic lives in:
 *   app_state / app_callbacks / bus_manager / hello_handshake / bus_worker
 */

#include "app_state.h"
#include "app_callbacks.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "bus_worker.h"
#include "msg_handler.h"
#include "msg_handler_hooks.h"   /* B2：钩子的唯一声明处（禁止弱符号）*/
#include "crash_diag.h"
#include "boot_guard.h"
#include "mem_guard.h"
#include "msg_handler_internal.h"
#include "config_mgr.h"
#include "scheduler.h"
#include "sync_manager.h"
#include "ota.h"
#include "rgb_led.h"
#include "factory_reset.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"
#include "bus_dma.h"
#include "log_stream.h"
#include "esp_heap_caps.h"
#include "esp_system.h"
#ifdef CONFIG_DEBUG_TCP_ENABLED
#include "ehome_tcp.h"
#endif
#include "nvs_flash.h"
#include "esp_ota_ops.h"
#include "esp_log.h"
#include "esp_task_wdt.h"   /* 8.3: Task watchdog initialization */

/* RGB LED pin differs by board: S3=GPIO48, C6=GPIO8 */
#ifdef CONFIG_IDF_TARGET_ESP32S3
  #define BOARD_LED_GPIO  48
#elif defined(CONFIG_IDF_TARGET_ESP32C6)
  #define BOARD_LED_GPIO  8
#else
  #define BOARD_LED_GPIO  8
#endif

#define TAG "EHOME"

/* 启动里程碑堆探针（2026-10-05）。
 *
 * 目的：S3 有 211+21+32 KiB 内部 RAM，但配置事务开始时只剩约 13KB 可用，
 * 事务内 apply_buses 又要约 12KB，把堆压到 844 字节，导致 MQTT 上报时
 * lwIP 分配 pbuf 失败（tcp_write errno=11 -> esp-mqtt 判定致命 -> 掉线）。
 *
 * 关键约束：**MQTT 未来要启用 TLS**，所以不能靠裁 mbedTLS / SSL 缓冲来省内存
 * —— TLS 会**增加**内存需求（TLS 上下文通常 16~40KB）。
 * 因此必须先搞清 258KB 内部 RAM 究竟被谁占用：只有找到"真正的过量预留"，
 * 才有空间既让当前明文 MQTT 稳定，又为将来的 TLS 留出余量。
 * 下面在每个子系统初始化后打印 free/largest，按步骤差分即可定位。 */
/* 启动里程碑堆探针只在 EHOME_MEM_DIAG 定义时编译进来。
 *
 * 2026-10-05 定位"配置事务后 MQTT 因内存不足掉线"时，这组探针是决定性的：
 * 它按步骤差分出 apply_buses 单独吃掉约 12KB，并证明 WiFi 启动另吃 63KB。
 * 但它在正常启动时会打 8 行日志，属于诊断噪声，因此默认关闭。
 * 需要时在 main/CMakeLists.txt 加 target_compile_definitions(main PRIVATE EHOME_MEM_DIAG=1)。
 * 注意：失败路径上的内存打印（bus_dma/scheduler/mqtt）**不在此开关内**，
 * 它们只在出错时各打一行，必须保持常开，否则下次同类故障又要重新反推。
 *
 * 口径（2026-10-06，task-7）：下面每个堆打印都**显式并上
 * MALLOC_CAP_INTERNAL**，且 internal 一律在前。开了 CONFIG_SPIRAM_USE_MALLOC
 * 的 s3p 上，裸 MALLOC_CAP_8BIT 是"内部 RAM + PSRAM"的合计：
 *   - largest 跨两个堆取最大值 ⇒ 恒等于 PSRAM 的 8.25 MB 连续块；
 *   - 而同一时刻门禁（mem_guard，2026-10-05 已修为内部口径）看的是 23,552 B。
 * 读数比判决大 350 倍 —— "看着还有 8 MB，下一个 malloc 就失败"正是原始缺陷
 * 报告抱怨的误导信号，所以读数必须与判决同口径。
 *
 * 本文件**两个口径都打**（internal + total）：internal 是门禁判决与"按步骤
 * 差分找消费者"用的口径，total 只在 PSRAM 型号上提供"内部紧、PSRAM 宽裕"的
 * 对照。只打 internal 的同类点各有理由，见 config_apply_transaction.c 与
 * bus_dma.c 的注释。
 *
 * 防回退：host_tests/diag_heap_metric_scan.py 全树断言"8BIT 必须与 INTERNAL
 * 同一语句出现"，任何一处退回裸 8BIT 都会让 ctest 变红。 */
#ifdef EHOME_MEM_DIAG
static void log_boot_heap(const char *stage)
{
    ESP_LOGI(TAG, "[bootheap] %-22s free=%u largest=%u min_ever=%u (internal) | "
                  "free=%u largest=%u min_ever=%u (total)",
             stage,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT));
}

/* 60 s 栈采样后的一行堆总览，同样双口径（理由同上）。 */
static void log_stack_heap(void)
{
    ESP_LOGI(TAG, "[stack] ---- heap free=%u largest=%u (internal) | "
                  "free=%u largest=%u (total) ----",
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
}
#else
static void log_boot_heap(const char *stage) { (void)stage; }
#endif


/* status_task 的栈。6144 在 2026-10-04 被证明不够：设备在压测中两次以
 * reset_reason=4(PANIC) 崩溃，pc 落在 FreeRTOS 的 prvTaskCheckFreeStackSpace
 * （栈溢出检测点），crash_diag 记下的 task 名就是 "status"。
 *
 * 为什么 6144 不够：status_task 每秒调用 msg_handler_send_status，而该函数有
 * uint8_t buf[1400] 的栈上帧（components/msg_handler/handler_data.c:107），
 * 之后还要走 msg_handler_publish_checked → MQTT → lwIP，这条链本身有 1KB 以上
 * 开销 —— 与 log_tx 那次栈溢出是同一形态（见 log_stream.c 的同类修复）。
 * 1400 + 发布链 + 调用者帧，6144 的余量过薄。
 *
 * 取 8192：与 OTA 任务同量级（ota.c 的 OTA_TASK_STACK_BYTES），
 * 给 buf[1400] + 发布链留约 2 倍余量。
 *
 * ⚠ 这只是把余量做够，**没有**消除根因：msg_handler_send_status 仍把 1400 字节
 * 放在栈上。main/CMakeLists.txt 的 -Wframe-larger-than 门禁此前只作用于
 * app_callbacks.c 这一个文件，所以这个 1400 字节帧从未被门禁检查过；
 * handler_data.c 属于 msg_handler 组件，完全在门禁视野之外。 */
/* **由 8192 降到 5120（2026-10-05，实测依据）**。
 *
 * 依据：uxTaskGetStackHighWaterMark() 实测峰值 3996 字节（未用 4196）。
 * 5120 保留 1124 字节余量，相对峰值约 1.28 倍 —— 之所以不按 2 倍给，
 * 是因为这个任务的栈需求主要来自 buf[1400] 这一个固定帧，属于**已知常量**
 * 而非随负载增长的量；下面的警告也说明该帧已被门禁盯着。
 *
 * 为什么值得收：S3 的 14 个任务栈标称合计约 64KB（占内部 RAM 近四分之一），
 * 而配置事务后只剩 844 字节可用堆，导致 MQTT 上报时 lwIP 分配 pbuf 失败、
 * 连接被判定致命而断开。且 MQTT 未来要启用 TLS（上下文 16~40KB），
 * 现有余量完全不够 —— 只能先把无谓预留压下去。 */
#define STATUS_TASK_STACK 5120

/* StatusReport 上报周期 (毫秒)。1s 是节点离线可见时延预算的一部分：
 * 最坏 = 1s(本周期) + 3s(服务端 NodeOfflineThreshold) + 1s(服务端检测 ticker) = 5s。
 * 改这里必须同步 backend/internal/offlinedetector/offlinedetector.go 的
 * FirmwareStatusReportPeriod 常量与预算注释，否则指标会被静默破坏。
 * 关于 uptime：status_task 取的是真实单调时钟（app_state_uptime_sec_now），
 * 不是按本周期自增，所以把 5s 压到 1s 不会让 uptime 失真。 */
#define STATUS_REPORT_PERIOD_MS 1000

static bool s_ota_pending_verify = false;

/* ==== WS-G: 运行期内存水位门禁 + 低频 MemReport(0x20) ==== */

/* 低内存事件只置标志，发布动作留到 status_task 且仅在 MQTT 已连接时执行。
 * 原因：low callback 由 mem_guard_poll() 在 status_task 上下文同步调用，
 * 在其中直接 publish 会让"内存已低"的路径再走 AT 分配/网络栈，正是要避免的。 */
static volatile bool s_mem_low_event = false;

static void mem_guard_low_memory_cb(size_t free_bytes, size_t largest_bytes)
{
    s_mem_low_event = true;
    /* 失败路径上的内存打印必须常开（方法论 §3）：每类事件一行，成本可忽略，
     * 缺了它下次故障又要从零反推。 */
    ESP_LOGW(TAG, "[memguard] low memory: free=%u largest=%u floor=%u; scheduling MemReport",
             (unsigned)free_bytes, (unsigned)largest_bytes,
             (unsigned)mem_guard_floor_bytes());
}

/* 编码并发布 MemReport(0x20)。帧很小（5 个 varint），栈缓冲 64B 足够，
 * 不引入新的堆分配；编码失败只记一行，不阻塞主循环。 */
static void send_mem_report(void)
{
    uint8_t buf[64];
    size_t n = mem_guard_encode_report(buf, sizeof(buf));
    if (n == 0) {
        ESP_LOGW(TAG, "MemReport encode failed");
        return;
    }
    msg_handler_publish(buf, n);
}

/* ---- status_task — still in main.c (single-loop, minimal dependency) ---- */

static void status_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    while (1) {
        /* uptime 必须取真实单调时钟，不能按上报周期自增：
         * 若改成每循环 +1，StatusReport field 1（单位=秒）就会变成
         * "真实运行秒数 / STATUS_REPORT_PERIOD_MS" 的比例值——周期从 5s
         * 收到 1s 后这个偏差会放大 5 倍。取 app_state_uptime_sec_now()
         * 与周期无关，所以收紧周期不会让 uptime 失真。 */
        s->uptime_sec = app_state_uptime_sec_now();
        /* 刷新启动熔断的"本次存活多久"。只写 RTC_NOINIT（几次 SRAM 写，零 flash 代价），
         * 所以可以放在这个周期任务里随手调用。不要放进 NVS —— 每 1s 写一次会磨损 flash。
         * 见 main/boot_guard.h 的说明。 */
        boot_guard_tick();
        /* WiFi 主动链路探针（2026-10-05）。
         *
         * 为什么不能只靠 WIFI_EVENT_STA_DISCONNECTED：实测出现过"驱动以为还连着、
         * 实际 L2 已经不在"的静默失联 —— 串口 0 条 WiFi 事件、0 条重连日志，
         * 而服务端 ping 100% 丢包、ARP 无表项，固件却一路正常运行。
         * 没有事件就没有重试，所以必须有一个主动探针。
         *
         * 放在这里（1s 周期）而不是新建任务：探针自带 5s 节流，
         * 无需额外栈与内存。 */
        /* 传入应用层信号：MQTT 是否已连接。
         * WiFi 自述 CONNECTED 而 MQTT 连不上，正是静默失联的特征组合 ——
         * 只看 WiFi 驱动状态是发现不了的（驱动缓存会说"一切正常"）。 */
        /* ---- 运行期水位门禁 + 每 60s 任务栈采样/内存上报（WS-G，2026-10-05）----
         *
         * 动机（沿用原诊断探针）：S3 的 14 个任务栈全部走堆分配（xTaskCreate），
         * 标称合计约 64KB，是配置事务（12KB）的 5 倍。压缩栈不能靠猜：
         * 栈不足表现为随机踩踏，比内存不足更难定位；必须先用
         * uxTaskGetStackHighWaterMark() 测真实峰值。
         *
         * 与原探针的区别：现在**不只在 EHOME_MEM_DIAG 下打日志**，而是把
         * "最小剩余字节"写入 mem_guard，并随 MemReport(0x20) 低频上报，
         * 使服务端能看到跨重启趋势（原探针默认编译不进来，服务端什么都看不到）。
         *
         * 单位：uxTaskGetStackHighWaterMark 返回**字**（FreeRTOS 约定），
         * 乘 sizeof(StackType_t) 才是字节。 */
        bool mem_periodic = false;
        {
            static uint32_t s_mem_report_tick = 0;
            if (++s_mem_report_tick >= 60) {
                s_mem_report_tick = 0;
                mem_periodic = true;
            }
        }
        if (mem_periodic) {
            static const char *names[] = {
                "status", "sync", "rx_task", "cmd_u0", "cmd_u1", "cmd_u2",
                "cmd_spi", "cmd_i2c", "report_tx", "mqtt_super",
                "hello_super", "periph_worker", "periph_rsp", "rgb_led",
                "factory_reset",
            };
            size_t min_stack_free = (size_t)-1;
            for (size_t i = 0; i < sizeof(names) / sizeof(names[0]); i++) {
                TaskHandle_t h = xTaskGetHandle(names[i]);
                if (h == NULL) continue;
                size_t hw = (size_t)uxTaskGetStackHighWaterMark(h) * sizeof(StackType_t);
                if (hw < min_stack_free) min_stack_free = hw;
#ifdef EHOME_MEM_DIAG
                ESP_LOGI(TAG, "[stack] %-14s high_water=%u bytes free (unused)",
                         names[i], (unsigned)hw);
#endif
            }
            if (min_stack_free != (size_t)-1) {
                mem_guard_set_min_stack_high_water(min_stack_free);
            }
#ifdef EHOME_MEM_DIAG
            log_stack_heap();
#endif
        }

        /* 1Hz 轮询水位；低于 floor 时回调置 s_mem_low_event（迟滞见 mem_guard.c）。 */
        mem_guard_poll();
        bool mem_mqtt_connected = mqtt_client_is_connected_impl();
        bool mem_send = false;
        {
            static bool s_mem_report_initial_sent = false;
            if (!s_mem_report_initial_sent && mem_mqtt_connected) {
                s_mem_report_initial_sent = true;
                mem_send = true;   /* 启动后首报（等 MQTT 连上再发） */
            }
        }
        if (mem_periodic && mem_mqtt_connected) mem_send = true;  /* 60s 周期 */
        if (s_mem_low_event && mem_mqtt_connected) {              /* 低内存事件 */
            s_mem_low_event = false;
            mem_send = true;
        }
        if (mem_send) send_mem_report();
        (void)wifi_mgr_check_liveness(mqtt_client_is_connected_impl());
        if (mqtt_client_is_connected_impl()) {
            esp_err_t status_err = msg_handler_send_status(
                s->uptime_sec, "online",
                (config_mgr_get_manifest() ? config_mgr_get_manifest()->channel_count : 0),
                scheduler_get_state());
			if (s->ota_need_confirm && status_err == ESP_OK) {
				if (ota_confirm_valid() == ESP_OK) s->ota_need_confirm = false;
			}
		}
        vTaskDelay(pdMS_TO_TICKS(STATUS_REPORT_PERIOD_MS));
    }
}

/* ---- sync send_hello callback ---- */

static void on_sync_send_hello(void)
{
    (void)hello_handshake_request_sync();
}

/* ---- Weak-symbol bridges for msg_handler callbacks ---- */

void on_write_cmd_received(uint32_t rid, uint32_t ch,
                           const uint8_t *d, size_t l, uint32_t rs,
                           uint32_t edge_device_id, uint32_t rx_timeout_ms)
{
    bus_manager_on_write_cmd(&app_state_get()->bus_runtime, rid, ch, d, l, rs,
                             edge_device_id, rx_timeout_ms);
}

const char *channel_cmd_v2_current_boot_id(void)
{
    return app_state_get()->boot_id;
}

uint64_t channel_cmd_v2_current_time_ms(void)
{
    return msg_handler_get_current_server_time_ms();
}

bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot)
{
    if (!cmd) return false;
    return bus_manager_on_channel_cmd_v2(&app_state_get()->bus_runtime, cmd->channel_id,
        cmd->tx_data, cmd->tx_len, cmd->read_size, cmd->rx_timeout_ms,
        cmd->post_tx_delay_ms, cmd->plan_data, cmd->plan_len, cmd->plan_step_count, slot);
}

void on_query_resources_received(const char *request_id)
{
    (void)request_id;
}

/* ---- Modbus CRC16 helper ---- */

static uint16_t modbus_crc16(const uint8_t *data, size_t len)
{
    uint16_t crc = 0xFFFF;
    for (size_t i = 0; i < len; i++) {
        crc ^= data[i];
        for (int j = 0; j < 8; j++) {
            if (crc & 0x0001) {
                crc = (crc >> 1) ^ 0xA001;
            } else {
                crc >>= 1;
            }
        }
    }
    return crc;
}

/* ---- Modbus scan callback (overrides weak in handler_writecmd.c) ---- */

/* B2（2026-10-06）：on_scan_req_received 原先**只有**一个 weak 空实现
 * （在 handler_writecmd.c 里，注释写"implemented in main.c"）——
 * 但 main.c **从来没有实现过它**。实测确认：全仓搜不到任何强定义。
 *
 * 也就是说：扫描请求这条路径一直是**静默无操作**的 ——
 * 编译通过、链接通过、测试通过，功能却不存在。
 * 这正是弱符号最坏的形态：它把"没实现"伪装成"已实现"。
 *
 * 现在给出一个**显式的**强实现：链接器能确认它存在，
 * 而运行期每条请求都会留下一条 WARN，让"未实现"变成可见事实。
 * 真要支持该功能时，替换本函数体即可（声明见 msg_handler_hooks.h）。 */
void on_scan_req_received(const char *request_id, uint32_t hardware_id)
{
    ESP_LOGW(TAG, "on_scan_req_received: NOT IMPLEMENTED (request_id=%s hardware_id=%u) "
                  "-- scan requests are acknowledged but never executed",
             request_id ? request_id : "(null)", (unsigned)hardware_id);
    /* 有意不发送任何成功回执：避免把"没做"报成"做完了"。 */
}

void on_modbus_scan_req_received(const char *request_id,
    uint32_t start_addr, uint32_t end_addr, uint32_t timeout_ms)
{
    ESP_LOGI("MODBUS_SCAN", "Scanning addresses %lu-%lu, timeout=%lums",
             (unsigned long)start_addr, (unsigned long)end_addr, (unsigned long)timeout_ms);

    app_state_t *s = app_state_get();

    /* Find first UART channel */
    bus_dma_ctx_t *uart_ctx = NULL;
    uint32_t channel_id = 0;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s->bus_ctx[i].initialized && s->bus_ctx[i].bus_type == BUS_TYPE_UART) {
            uart_ctx = &s->bus_ctx[i];
            channel_id = s->bus_ch[i];
            break;
        }
    }

    if (!uart_ctx) {
        ESP_LOGE("MODBUS_SCAN", "No UART channel found");
        msg_handler_send_scan_rpt(request_id, 0, false, NULL, 0);
        return;
    }

    /* Scan each address */
    uint32_t found[256];
    uint8_t found_count = 0;

    for (uint32_t addr = start_addr; addr <= end_addr && addr <= 247; addr++) {
        /* Build Modbus 03 command: read holding register
         * [addr] [03] [00] [00] [00] [01] [CRC_L] [CRC_H] */
        uint8_t cmd[8];
        cmd[0] = (uint8_t)addr;
        cmd[1] = 0x03;
        cmd[2] = 0x00;
        cmd[3] = 0x00;
        cmd[4] = 0x00;
        cmd[5] = 0x01;
        uint16_t crc = modbus_crc16(cmd, 6);
        cmd[6] = crc & 0xFF;
        cmd[7] = (crc >> 8) & 0xFF;

        /* Send */
        esp_err_t err = bus_dma_write(uart_ctx, cmd, sizeof(cmd));
        if (err != ESP_OK) {
            continue;
        }

        /* Wait for response */
        uint8_t rx[256];
        size_t rx_len = 0;
        TickType_t start_tick = xTaskGetTickCount();
        while ((xTaskGetTickCount() - start_tick) < pdMS_TO_TICKS(timeout_ms)) {
            rx_len = bus_dma_read(uart_ctx, rx, sizeof(rx));
            if (rx_len >= 5) { /* Modbus response minimum 5 bytes */
                if (rx[0] == addr && rx[1] == 0x03) {
                    found[found_count++] = addr;
                    ESP_LOGI("MODBUS_SCAN", "Found device at addr %lu", (unsigned long)addr);
                }
                break;
            }
            vTaskDelay(pdMS_TO_TICKS(10));
        }
    }

    /* Send result */
    msg_handler_send_scan_rpt(request_id, channel_id, true, found, found_count);
    ESP_LOGI("MODBUS_SCAN", "Scan complete: %d devices found", found_count);
}

/* ================================================================== */
/*  app_main                                                          */
/* ================================================================== */

void app_main(void)
{
    ESP_LOGI(TAG, "EHomeSystem v%s unified bus DMA", get_firmware_version());

    /* ---- NVS ---- */
    esp_err_t r = nvs_flash_init();
    if (r == ESP_ERR_NVS_NO_FREE_PAGES || r == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        r = nvs_flash_init();
    }
    ESP_ERROR_CHECK(r);

    /* ---- OTA: boot validation ---- */
    /* Check if running partition is pending verification (OTA rollback protection).
     * If so, run self-checks before confirming the new firmware as valid.
     * Self-checks: WiFi connected + MQTT connected + first StatusReport sent.
     * If any check fails 3 times → mark invalid → rollback to previous partition. */
    {
        esp_ota_img_states_t ota_state;
        const esp_partition_t *running = esp_ota_get_running_partition();
        if (esp_ota_get_state_partition(running, &ota_state) == ESP_OK) {
            if (ota_state == ESP_OTA_IMG_PENDING_VERIFY) {
                ESP_LOGW(TAG, "OTA: running partition is PENDING_VERIFY — boot validation active");
                /* Validation happens in app_callbacks only after WiFi, MQTT,
                 * and the first StatusReport have all succeeded. Do not cancel
                 * rollback here. */
				s_ota_pending_verify = true;
            }
        }
        /* A non-zero download NVS state is recovery metadata, not proof that
         * the running image passed boot validation. Preserve it for OTA
         * recovery logic and never cancel rollback from here. */
        if (ota_get_nvs_state() != 0) {
            ESP_LOGW(TAG, "OTA: NVS state=%d (power-loss recovery pending)", ota_get_nvs_state());
        }
    }

    /* ---- App state (singleton — node_id from MAC, cmd_queue, spinlock) ---- */
    app_state_t *s = app_state_init();
	s->ota_need_confirm = s_ota_pending_verify;

    /* ---- Crash diagnostics (v2.6) ----
     * 必须在 nvs_flash_init() 之后：把上次 panic 留在 RTC_NOINIT 里的记录
     * 搬进 NVS。设备此前完全不记录复位原因，导致"反复重启"无法定性；
     * 这一步让每次启动都带一个可上报的原因。 */
    crash_diag_init();

    /* ---- 启动熔断（v2.7，2026-10-04）----
     * 必须在 nvs_flash_init() 之后、各子系统启动之前：它要根据"上一次启动存活了多久"
     * 决定本次是否进入安全模式，而安全模式会影响后续子系统（尤其是日志上报）的选择。
     *
     * 为什么需要：实测满负载 + 日志上传会让设备每 20~110 秒崩一次，而日志配置随
     * ConfigManifest 持久化、重启后重新应用 -> 无限重启循环，只能人工接触设备恢复。
     * 详见 main/boot_guard.h。 */
    if (boot_guard_init()) {
        ESP_LOGE(TAG, "BOOT_GUARD: entering safe mode: %s", boot_guard_safe_mode_reason());
    }
    boot_guard_start_watchdog();

    /* ---- UART0 boot mode check (MUST be before any UART0 driver install) ---- */
    /* If BOOT held at startup, UART0 reserved for download — task blocks here */
    if (!bus_dma_uart0_boot_init()) {
        ESP_LOGW(TAG, "UART0 in download mode, normal init skipped");
        /* bus_dma spawns a wait task; we just spin */
        while (1) { vTaskDelay(pdMS_TO_TICKS(1000)); }
    }

    log_boot_heap("after app_state");

    /* ---- Subsystem init (dma_pool already initialized in app_state_init) ---- */
    config_mgr_init();
    log_boot_heap("after config_mgr");
    
    /* DIP: inject dma_pool into components that need it */
    config_mgr_set_dma_pool(s->dma_pool);
    msg_handler_set_dma_pool(s->dma_pool);

    /* Server is single source of truth — no NVS config load at boot.
     * Config will arrive via ConfigManifest after Hello handshake. */
    
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_sync_send_hello);
    msg_handler_init();
    log_boot_heap("after msg_handler");
    /* Create the long-lived Hello supervisor before MQTT can start. */
    hello_handshake_start(s);
    log_stream_set_publish_callback(msg_handler_publish);
    if (handler_periph_init() != ESP_OK) {
        ESP_LOGE(TAG, "Peripheral handler initialization failed; restarting");
        crash_diag_mark_reboot_reason("periph_init_failed");
        esp_restart();
    }   /* v3.0: GPIO/PWM peripheral control queues + tasks */
    log_boot_heap("after periph");
    ota_init();
    scheduler_init();
    log_boot_heap("after ota+sched");

    /* ---- Transports ---- */
    mqtt_client_init();
    transport_manager_init();
    mqtt_transport_register();
    log_boot_heap("after mqtt_init");

    /* ---- WiFi + callbacks ---- */
    wifi_mgr_register_state_cb(on_wifi_state_cb, s);
    mqtt_client_register_state_cb(on_mqtt_state_cb, s);
    mqtt_client_register_ready_cb(on_mqtt_ready_cb, s);
    mqtt_client_register_transport_cb(on_mqtt_transport_cb, s);
    mqtt_client_register_owner_wake_cb(on_mqtt_owner_wake_cb, s);
    mqtt_client_register_msg_cb(on_mqtt_msg_cb, s);
    mqtt_client_set_node_id(s->node_id);

    rgb_led_init(BOARD_LED_GPIO);
    rgb_led_start();
    factory_reset_init();

    log_boot_heap("before wifi");
    wifi_mgr_init();
    wifi_mgr_start();
    log_boot_heap("after wifi_start");

    /* ---- Background tasks ---- */
    /* StatusReport now carries runtime queue/performance metrics and nested
     * channel health.  Its bounded encoder buffers live on this call stack;
     * keep a dedicated budget so a valid report cannot trip the stack guard
     * while UART RX workers are active. */
    mem_guard_register_low_cb(mem_guard_low_memory_cb);
    xTaskCreate(status_task, "status", STATUS_TASK_STACK, (void *)s, 3, NULL);
    xTaskCreate(sync_manager_periodic_task, "sync", 3072, NULL, 2, NULL);
    /* Inject msg_handler callbacks into bus_worker and bus_manager (eliminates extern) */
    bus_worker_set_callbacks(msg_handler_send_write_rsp, msg_handler_send_data_report);
    bus_worker_set_channel_cmd_v2_final_cb(handler_channel_cmd_v2_complete);
    /* D-06（2026-10-06）：DataBatch(0x20) 编码器【显式】注入。
     * 此前靠 bus_worker.c 里的 __attribute__((weak)) 默认实现，
     * 生效与否取决于链接顺序，且宿主测试走另一套符号表 ——
     * 弱定义一旦生效，聚合静默退化为逐样本 0x03 而【没有测试会红】。
     * 现在与上面两行同一风格：依赖方向单向（main -> 双方），一步到位。 */
    bus_worker_set_data_batch_cb(msg_handler_send_data_batch);
    bus_manager_set_write_rsp_cb(msg_handler_send_write_rsp);

    /* Inject OTA progress callback (eliminates ota → msg_handler cycle) */
    ota_set_progress_callback(msg_handler_send_ota_prog);

    /* Inject crash-record ack callback (v2.6): msg_handler cannot depend on
     * main, so the release-on-ACK handler is injected here. */
    msg_handler_set_diag_ack_cb(crash_diag_on_ack);

    /* 8.3: Initialize task watchdog — 10 second timeout, panic on timeout
     * ESP-IDF v6.0 CONFIG_ESP_TASK_WDT_INIT=1 auto-initializes TWDT (5s)
     * before app_main. We must deinit first, then reinit with our config. */
    esp_task_wdt_deinit();
    esp_task_wdt_config_t wdt_config = {
        .timeout_ms = 10000,
        .idle_core_mask = 0,
        .trigger_panic = true,
    };
    ESP_ERROR_CHECK(esp_task_wdt_init(&wdt_config));

    bus_worker_start(&s->bus_runtime);

#ifdef CONFIG_DEBUG_TCP_ENABLED
    /* TCP transport (parallel with MQTT, debug only) */
    ESP_LOGI(TAG, "Starting TCP transport on port %d", CONFIG_DEBUG_TCP_PORT);
    tcp_transport_config_t tcp_cfg = {
        .port        = CONFIG_DEBUG_TCP_PORT,
        .max_clients = 4,
    };
    s->tcp_transport = tcp_transport_create(&tcp_cfg);
    if (s->tcp_transport) {
        s->tcp_transport->msg_cb = on_transport_msg_cb;
        s->tcp_transport->msg_cb_ctx = s;
        s->tcp_transport->state_cb = on_transport_state_cb;
        s->tcp_transport->state_cb_ctx = NULL;
        s->tcp_transport->ops->init(s->tcp_transport, NULL);
        transport_register(s->tcp_transport);

        if (wifi_mgr_get_state() == WIFI_MGR_CONNECTED
            && s->tcp_transport->state != TRANSPORT_CONNECTED
            && s->tcp_transport->ops->start) {
            s->tcp_transport->ops->start(s->tcp_transport);
        }
    }
#endif

    ESP_LOGI(TAG, "Init done, node=%s", s->node_id);
}
