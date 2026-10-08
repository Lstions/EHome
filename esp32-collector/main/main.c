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
#include "report_stats.h"   /* D-14：注册栈余量 provider */
#include "msg_handler.h"
#include "msg_handler_hooks.h"   /* B2：钩子的唯一声明处（禁止弱符号）*/
#include "crash_diag.h"
#include "boot_guard.h"
#include "device_link_wiring.h"
#include "uplink_arbiter.h"   /* task-21：上行仲裁（两条门互斥）*/
#include "session_transport.h"  /* task-21：3.0 会话 → transport 适配 + 仲裁闸 */
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

/* ==== WS-G: 运行期内存水位门禁 + 低频 MemReport(0x21) ==== */

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

/* 编码并发布 MemReport(0x21)。帧很小（5 个 varint），栈缓冲 64B 足够，
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
         * "最小剩余字节"写入 mem_guard，并随 MemReport(0x21) 低频上报，
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
                /* ⭐ task-34：以下两个任务此前**不在采样名单里**。
                 *
                 * 3.0 的链路任务（8 KB 栈）漏得最要命：它是 3.0 引入的新任务，
                 * 水位既没被记录、也没进 MemReport ⇒ "3.0 到底占了多少栈"在真机上
                 * **无法回答**（只能靠猜），而本卡恰恰要求先测再改。
                 * 名单漏项本身就是一类静默缺陷：采样在跑、看着很全，
                 * 只是恰好漏掉了要观察的那个对象。 */
                "dev_link",
                /* S3 的 USB 数据泵（bus_dma.c:1189，栈 3072）—— 真实存在且常驻，
                 * 同理应当被观察。 */
                "usb_rx",
                /* ⚠ "uart0_dl"（bus_dma.c:169，栈 2048）**故意不加**：
                 * 它只在 USB 下载模式下创建，正常运行期不存在 ⇒ 加进来会
                 * 恒为 xTaskGetHandle==NULL，除了多一次无效查找没有任何信息。
                 * 写明理由是为了下一个人不必再查一遍"这是漏了还是有意为之"。
                 * 若将来该任务在正常启动路径上也被创建，必须加进来。 */
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

        /* ⚠ task-34 修正一处 P1 违规：**上行可用 != MQTT 已连接**。
         *
         * 这里原先三处都写成 "&& mem_mqtt_connected"（MQTT 连上才发 MemReport），
         * 注释也写着"等 MQTT 连上再发"。到 §7.3 P3（MQTT 下线、只留 3.0 TCP）时，
         * 这条路径**永远不会执行** ⇒ 内存/栈水位**再也到不了服务端**，
         * 而"栈峰值是多少"这个问题就会**再次变得不可回答**
         * （与 §138 同族：3.0 已就绪，判据却还挂在 MQTT 上）。
         *
         * 判据改用 transport_any_connected()：
         *   - 它回答的正是这里要问的问题：**任一**传输已连接（3.0 TCP 或 MQTT）；
         *   - 实现已存在（transport.h:183），**不新增第二个判据**（P4）；
         *   - 发送本身走 msg_handler_publish → 当前传输/广播，两条路都通。
         *
         * ⚠ 注意本修正**不改变** MemReport 的内容与频率，只改"什么时候允许发"。 */
        const bool mem_uplink_ready = transport_any_connected();
        bool mem_send = false;
        {
            static bool s_mem_report_initial_sent = false;
            if (!s_mem_report_initial_sent && mem_uplink_ready) {
                s_mem_report_initial_sent = true;
                mem_send = true;   /* 启动后首报（等任一路上行可用再发）*/
            }
        }
        if (mem_periodic && mem_uplink_ready) mem_send = true;    /* 60s 周期 */
        if (s_mem_low_event && mem_uplink_ready) {                /* 低内存事件 */
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

/* ---- sync 的"有没有可用上行"（P4：一处定义）----
 *
 * ⭐ 2026-10-08：sync_manager 原先**硬编码**判 `mqtt_client_is_connected_impl()`，
 * 于是无 MQTT 时 `sync_manager_request_sync()` 直接 return（只留一条 WARN）——
 * "周期 / 怀疑 / 无配置"三条**主动请求同步**的路径全部失效。
 *
 * 真机实测（C6 + P3 + MQTT 死地址，§164）：t=334 与 t=31493 两次请求都被那条挡住；
 * 配置之所以还能同步，是靠 **device_link 自己的握手 Hello** 兜住的，不是本模块的功劳。
 * ⇒ §7.3 P4（后端关 MQTT 监听）之后 `mqtt_client_is_connected_impl()` **永远 false**
 *   ⇒ 这三条路径**永久死掉且不报错**（只有一条 WARN）—— 静默死角。
 *
 * ⇒ 语义是"**任意一条上行可用**"：MQTT 挂了但 3.0 就绪时必须继续工作。
 * ⚠ 两个函数都走 `uplink_get_facts`（同一份事实）⇒ 不会与仲裁层的判定漂移（P4）。 */
static bool on_sync_uplink_available(void)
{
    return uplink_arbiter_tcp3_connected() || uplink_arbiter_mqtt_connected();
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
    /* 见 on_sync_uplink_available 的说明：不接这条，"主动请求同步"在 P4 后永久失效。 */
    sync_manager_register_uplink_available_cb(on_sync_uplink_available);
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
    /* task-21：两条门互斥的上行仲裁。
     *
     * ⚠⚠ **默认构建（CONFIG_EHOME_DEVICE_LINK_ENABLED=n）逐位不变** —— 这是硬约束：
     *   链路未启用时 s_link_enabled=false ⇒ uplink_gate_tcp3() 恒 false，
     *   且 MQTT 门退化为"只看自己连没连"（uplink_gate_mqtt 的 !link_enabled 分支）
     *   ⇒ **与 task-21 之前完全相同**，没有 3.0 transport、没有闸、没有切换。
     *   下面的 if 分支也保证注册路径都不同：未启用走原来的 mqtt_transport_register()。
     *
     * 为什么必须在 transport_manager_init() 之后：transport_register() 在未初始化时
     * 返回 ESP_ERR_INVALID_STATE（transport.c:31-36），会静默注册失败。
     *
     * 为什么用编译期常量而不是运行期查询：device_link_wiring.h:23-34 记录过教训 ——
     * devlink_wanted() 是运行期函数，静态分析看不到，于是"未启用"的构建仍会被误报。
     * 这里用 CONFIG_* 常量，让"启用与否"在编译期就可见。 */
#if defined(CONFIG_EHOME_DEVICE_LINK_ENABLED) && (CONFIG_EHOME_DEVICE_LINK_ENABLED == 1)
    uplink_arbiter_init(true);
    (void)uplink_mqtt_transport_register();   /* 门控版 MQTT 出口（不碰回调槽） */
#else
    mqtt_transport_register();                /* 默认构建：与今天逐位相同 */
#endif
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

    /* D-14（2026-10-06）：把"最小栈余量"的提供者注册给中立组件 report_stats。
     *
     * 为什么需要这一行：为了让 msg_handler 不再 REQUIRES bus_worker（破除依赖环），
     * 三个上报统计量搬到了 report_stats。其中 drop_count 与 queue_high_water
     * 是纯计数器，可以直接搬；而 min_stack_watermark 需要 **bus_worker 的任务句柄**
     * 按需计算（uxTaskGetStackHighWaterMark）⇒ 不能搬，只能由 bus_worker
     * 在此注册一个 provider，由 report_stats 转发。
     *
     * 不注册的后果：report_stats 会返回 UINT32_MAX（"无低水位"）。
     * 这**不会**造成假告警（0 才会），但会让上报帧里该字段**恒为无低水位** ——
     * 也就是丢失真实观测。所以这不是可选项。 */
    report_stats_set_stack_watermark_provider(bus_worker_get_min_stack_watermark);
    /* 启动期断言（P6：读一手事实，而不是假设刚才那行生效了）。
     * 未接上不会有假告警，但会让上报字段恒为"无低水位" —— 沉默地丢观测，
     * 所以这里必须【看得见】。 */
    if (!report_stats_has_stack_watermark_provider()) {
        ESP_LOGE(TAG, "report_stats stack-watermark provider NOT registered: "
                      "PerformanceReport will lose real stack observations");
    }
    bus_manager_set_write_rsp_cb(msg_handler_send_write_rsp);

    /* Inject OTA progress callback (eliminates ota → msg_handler cycle) */
    ota_set_progress_callback(msg_handler_send_ota_prog);

    /* Inject crash-record ack callback (v2.6): msg_handler cannot depend on
     * main, so the release-on-ACK handler is injected here. */
    msg_handler_set_diag_ack_cb(crash_diag_on_ack);

    /* 远程运维（0x22 重启 / 恢复出厂）的三个原语。
     * 注入点刻意放在传输启动之前：晚注入的话，一条早到的 0x22 会命中
     * "未注入 ⇒ 拒绝执行"分支，而操作员看到的是"设备没反应"。 */
    device_op_wiring_init();

    /* ⚠ 2026-10-07（heap tracing 实测）**本顺序是敏感的** —— 原注释的"顺序不敏感"已被否证。
     *
     * trace 窗口 = 一次 mTLS 连接，实测到的机制：
     *   · `device_link_wiring_init()` 建 devlink 任务后，该任务**立刻**开始连接
     *     （WiFi 此时已就绪）⇒ 进入 mbedTLS 握手；
     *   · 而 app_main 继续往下走到 `bus_worker_start()`，它要建 **7 个任务**
     *     （report_tx + rx_task + cmd_u0..2 + cmd_spi + cmd_i2c），
     *     **每个栈 4096 B、全在内部 RAM**（trace 里 7 条 4096 B 记录，调用栈指向
     *     `xTaskCreatePinnedToCore` ← `report_path_init`/`bus_worker_start`）；
     *   · 两者**并发** ⇒ 7×4096 的连续块需求与握手缓冲交错 ⇒ largest 塌到 7680。
     *
     * ⚠ **但"提前到链路之前"实测更差**（before_connect 的 largest 从 31744 掉到 12288）——
     *   提前分配只是让堆**先被切碎**。⇒ **顺序改变不了总量**：
     *   7×4096 栈 + TLS 缓冲之和本身就超出可用连续块。详见设计文档 §155。
     * 默认关闭（CONFIG_EHOME_DEVICE_LINK_ENABLED=n）⇒ 本段对默认构建无影响。 */
    device_link_wiring_init();

    /* task-21：把 3.0 会话注册成一条 transport，并注入**仲裁闸**。
     *
     * 为什么注册放在这里（而不是上面的 Transports 块）：
     *   session 由 device_link_wiring_init() 创建并**持有**；上面那块跑的时候
     *   device_link_wiring_session() 还是 NULL（未启用或尚未创建）。
     *   本函数内部有运行期早退（CONFIG=n 时直接 return）⇒ 这里取到 NULL 即
     *   "链路未启用"，静默跳过是**正确**的，不是失败。
     *
     * 为什么闸要单独注入：session_transport.c 是"纯判定 + 薄胶水"，让它直接
     * include 仲裁层会把 IDF 依赖带进 SESSION_TRANSPORT_HOST_TEST 构建，
     * 破坏那组宿主用例的可测性（见 session_transport.h 的说明）。
     *
     * ⚠ 不注册也不报错的原因：默认构建（CONFIG=n）本来就该**逐位不变**，
     *   报错会让默认构建变吵；而"启用了却拿不到 session"才是真异常，
     *   那种情况下面单独判并报错。 */
    {
        struct session *sess = device_link_wiring_session();
        if (sess != NULL) {
            transport_t *t3 = session_transport_create(sess);
            if (t3 != NULL) {
                session_transport_set_gate(uplink_arbiter_tcp3_connected);
            } else {
                ESP_LOGE(TAG, "3.0 transport 注册失败 ⇒ 上行仍走 MQTT（不双发，但没有 TCP 上行）");
            }
        }
    }

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
