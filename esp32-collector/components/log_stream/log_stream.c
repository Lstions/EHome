/**
 * @file log_stream.c
 * @brief ESP32 native log capture and remote stream implementation.
 *
 * Architecture: IDF v6 esp_log link wrapper -> one bounded log_capture ring ->
 * log_tx_task -> MQTT MsgLogStream (0x1D). Explicit log_stream_emit() calls use
 * the same capture ring as native ESP_LOG calls.
 */

#include "log_stream.h"
#include "log_stream_codec.h"
#include "log_capture.h"
#include "log_capture_esp.h"
#include "frame_codec.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"
#include <stdarg.h>
#include <stdatomic.h>

#define TAG "LOG_STREAM"

#define LOG_BATCH_MAX       4
/* log_tx 任务栈（字节）。
 *
 * 2026-10-04 现场事故：S3 节点 30EDA0A9A808 以约 50 次/小时 复位，
 * 串口捕获到决定性证据：
 *
 *     ***ERROR*** A stack overflow in task log_tx has been detected.
 *     Backtrace: 0x40381871(panic_abort) 0x40381839(_esp_error_check_failed)
 *                0x420cd6f2(prvGetCurMaxSizeAllowSplit) 0x40382a8b(vTaskSwitchContext)
 *
 * 即：log_tx 的栈被击穿，触发 FreeRTOS 栈哨兵，直接 abort 重启。
 * 这与"堆不足"是两个不同的问题 —— 堆还有 24KB，但栈只有 1536 字节。
 *
 * 为什么 1536 不够（按 xtensa ABI 实测结构体尺寸计算）：
 *   log_tx_task 帧：entries[LOG_BATCH_MAX] = 4 x 24 = 96 字节
 *   log_stream_encode 帧：sub_buf[224] + 2 x frame_encoder_t + 局部变量 ≈ 260 字节
 *   再往下 publish() 是**最深的一层**：msg_handler_publish -> transport ->
 *   esp_mqtt_client_publish -> lwIP，这条链自身就要 1KB 以上。
 *   1536 连"本函数 + 一次 publish"都不够，必然溢出。
 *
 * 取值依据：本仓其他"会调用 publish"的任务用 4096（bus_worker 的 report_tx、
 * rx_task 都是 4096），mqtt_super 用 8192。这里取 4096，与同类任务对齐，
 * 而不是拍一个"看起来够大"的数。 */
#define LOG_TX_STACK        4096
#define LOG_TX_PRIO         2
#define LOG_TX_BUF_SIZE     768
#define RING_CAPACITY       4
#define LOG_TASK_EXITED_BIT BIT0
#define LOG_TASK_STOP_TIMEOUT_MS 1000
#define LOG_CAPTURE_USER_TIMEOUT_MS 100

typedef enum {
    LOG_STREAM_STOPPED = 0,
    LOG_STREAM_STARTING,
    LOG_STREAM_RUNNING,
    LOG_STREAM_STOPPING,
} log_stream_state_t;

#define LOG_STREAM_TX_CYCLE_MS 1000

/* Capture storage is static so a wrapper that was already in flight at detach
 * can never observe freed memory. log_capture_esp_detach() also waits for such
 * readers before stop returns or a subsequent start reinitializes the ring. */
static log_capture_t s_capture;
static log_capture_entry_t s_ring[RING_CAPACITY];
static log_capture_entry_t s_tx_batch[LOG_BATCH_MAX];
static uint8_t s_tx_buf[LOG_TX_BUF_SIZE];

static _Atomic(TaskHandle_t) s_task;
static atomic_uint s_state;
static atomic_uint s_capture_users;
static atomic_uint s_level;
/* s_seq is NOT atomic: it is only accessed by the single log_tx_task FreeRTOS
 * task during RUNNING. The STOPPED→STARTING→RUNNING state machine barrier ensures
 * s_seq is quiescent (no concurrent reader/writer) when it is reset to 0 in
 * log_stream_start_impl(), because stop waits for the TX task to fully exit
 * before start can proceed. This is a lifecycle barrier, not atomic access. */
static uint16_t s_seq;
static _Atomic(log_stream_publish_fn_t) s_publish;
static StaticEventGroup_t s_task_events_storage;
static EventGroupHandle_t s_task_events;

/* log_tx_task 的静态存储。
 *
 * 2026-10-04 实机根因（S3，满负载）：日志开关的配置**确实下发到了设备**
 * （串口可见 "LogStream config: enabled=1 level=3"），但紧接着就是
 *
 *	E LOG_STREAM: Failed to create log_tx_task
 *
 * 连打 6 次后放弃。原因是 xTaskCreate 需要从堆里分配 4096 字节栈 + TCB，
 * 而此时 5 条总线（3×UART + SPI2 + I2C0，都开了 DMA）、6 路 PWM、全部模板
 * 都已就绪，空闲堆只剩约 13.7KB —— 分配失败。
 *
 * 所以"打开日志开关就重启/无效"的真正机制不是配置没生效，而是**任务创建
 * 在满资源占用下必然失败**，且失败被静默降级成"日志不推"，开关看起来像没反应。
 *
 * 改为静态分配后，这条路径不再依赖运行时堆：
 *   - 创建不会因堆碎片/耗尽而失败；
 *   - 代价是 RAM 常驻（原本 xTaskCreate 也是按最大栈预留，量级相同）；
 *   - 与文件已有的 LOG_STREAM_OWNED_RAM_BYTES 门禁一致 —— 那个门禁
 *     本来就把 LOG_TX_STACK 计入"固件自有 RAM"，即代码早已假设它是静态的，
 *     只是实现用了动态创建，两者不一致。
 *
 * 注意：静态任务的栈/TCB 由调用方提供，任务结束必须用 vTaskDelete(NULL)
 * 归还 TCB（本文件已是如此），且不能重复创建 —— 上层 start/stop 状态机
 * 已保证同一时刻只有一个实例。
 */
static StaticTask_t s_log_tx_tcb;
static StackType_t s_log_tx_stack[LOG_TX_STACK / sizeof(StackType_t)];

/* Compile-time gate for firmware-owned known storage plus configured task stack.
 * Static ring/TX/control storage is resident even while disabled. This is only a
 * lower-bound accounting gate: dynamically allocated TCB, allocator metadata,
 * per-task TLS, and target runtime overhead require C6/S3 hardware measurement. */
/* 预算随 LOG_TX_STACK 上调（1536 -> 4096，见上面的栈溢出分析）。
 * 这是**上界门禁**：一旦有人再加静态缓冲或抬栈，必须同时复核这个数，
 * 否则门禁会在无人察觉时失去意义。 */
#define LOG_STREAM_OWNED_RAM_BUDGET_BYTES 6656U
#define LOG_STREAM_OWNED_RAM_BYTES ( \
    sizeof(s_capture) + sizeof(s_ring) + sizeof(s_tx_batch) + sizeof(s_tx_buf) + \
    LOG_TX_STACK + sizeof(s_task) + sizeof(s_state) + sizeof(s_capture_users) + \
    sizeof(s_seq) + sizeof(s_publish) + sizeof(s_task_events_storage) + \
    sizeof(s_task_events))
_Static_assert(LOG_STREAM_OWNED_RAM_BYTES <= LOG_STREAM_OWNED_RAM_BUDGET_BYTES,
               "log_stream firmware-owned static/stack/control RAM exceeds budget");

static uint8_t bounded_level(uint8_t level)
{
    return level > LOG_LEVEL_VERBOSE ? LOG_LEVEL_VERBOSE : level;
}

static void maybe_finish_stop(void)
{
    if (atomic_load_explicit(&s_task, memory_order_seq_cst) == NULL &&
        atomic_load_explicit(&s_capture_users, memory_order_seq_cst) == 0) {
        unsigned expected = LOG_STREAM_STOPPING;
        (void)atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STOPPED,
            memory_order_seq_cst, memory_order_seq_cst);
    }
}

static bool capture_user_enter(void)
{
    atomic_fetch_add_explicit(&s_capture_users, 1, memory_order_seq_cst);
    if (atomic_load_explicit(&s_state, memory_order_seq_cst) != LOG_STREAM_RUNNING) {
        atomic_fetch_sub_explicit(&s_capture_users, 1, memory_order_seq_cst);
        maybe_finish_stop();
        return false;
    }
    return true;
}

static void capture_user_leave(void)
{
    atomic_fetch_sub_explicit(&s_capture_users, 1, memory_order_seq_cst);
    maybe_finish_stop();
}

static void log_tx_task(void *pv)
{
    (void)pv;
    /* Creation can schedule this task on the other S3 core before start has
     * published its handle/state. The creator releases this one-shot gate only
     * after both are visible. */
    (void)ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
    while (atomic_load_explicit(&s_state, memory_order_acquire) == LOG_STREAM_RUNNING) {
        const TickType_t cycle_start = xTaskGetTickCount();
        size_t count = log_capture_drain(&s_capture, s_tx_batch, LOG_BATCH_MAX);
        if (count > 0) {
            log_stream_entry_t entries[LOG_BATCH_MAX];
            for (size_t i = 0; i < count; ++i) {
                entries[i] = (log_stream_entry_t){
                    .level = s_tx_batch[i].level,
                    .timestamp_us = s_tx_batch[i].timestamp_us,
                    .tag = s_tx_batch[i].tag,
                    .message = s_tx_batch[i].message,
                };
            }

            size_t encoded_len = 0;
            log_stream_publish_fn_t publish =
                atomic_load_explicit(&s_publish, memory_order_acquire);
            if (log_stream_encode(s_tx_buf, sizeof(s_tx_buf), &encoded_len,
                                  s_seq++, entries, count) == FRAME_OK &&
                publish != NULL) {
                /* MQTT publish paths log internally. Keep those diagnostics out
                 * of this same ring to prevent a publish -> log -> publish loop. */
                log_capture_suppress(&s_capture);
                publish(s_tx_buf, encoded_len);
                log_capture_resume(&s_capture);
            }
        }

        /* Enforce a minimum cycle from the start of the previous drain/publish.
         * This bounds log traffic even when publish itself returns quickly and
         * preserves MQTT task time for inbound control frames. */
        TickType_t wait_ticks = pdMS_TO_TICKS(LOG_STREAM_TX_CYCLE_MS);
        TickType_t elapsed = xTaskGetTickCount() - cycle_start;
        if (elapsed < wait_ticks) {
            wait_ticks -= elapsed;
        } else {
            wait_ticks = 1;
        }
        (void)ulTaskNotifyTake(pdTRUE, wait_ticks);
    }

    atomic_store_explicit(&s_task, NULL, memory_order_seq_cst);
    xEventGroupSetBits(s_task_events, LOG_TASK_EXITED_BIT);
    maybe_finish_stop();
    vTaskDelete(NULL);
}

void log_stream_set_publish_callback(log_stream_publish_fn_t publish)
{
    atomic_store_explicit(&s_publish, publish, memory_order_release);
}

esp_err_t log_stream_start(uint8_t level)
{
    level = bounded_level(level);
    unsigned expected = LOG_STREAM_STOPPED;
    if (!atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STARTING,
            memory_order_acq_rel, memory_order_acquire)) {
        if (expected == LOG_STREAM_RUNNING) {
            return log_stream_set_level(level);
        }
        /* STARTING/STOPPING are short, owned lifecycle transitions. A second
         * caller must not race initialization or resurrect a stopping task. */
        return ESP_ERR_INVALID_STATE;
    }

    if (s_task_events == NULL) {
        s_task_events = xEventGroupCreateStatic(&s_task_events_storage);
    }
    if (s_task_events == NULL ||
        atomic_load_explicit(&s_task, memory_order_acquire) != NULL) {
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        return ESP_FAIL;
    }

    /* Complete a previous bounded detach before reinitializing static storage. */
    if (!log_capture_esp_detach()) {
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        return ESP_FAIL;
    }

    xEventGroupClearBits(s_task_events, LOG_TASK_EXITED_BIT);
    log_capture_init(&s_capture, s_ring, RING_CAPACITY, level);
    atomic_store_explicit(&s_level, level, memory_order_release);
    s_seq = 0;
    log_capture_esp_attach(&s_capture);

    /* 静态创建：满负载下堆只有约 13.7KB 时 xTaskCreate 会失败，
     * 详见 s_log_tx_tcb 处的说明。xTaskCreateStatic 不分配堆。 */
    TaskHandle_t task = xTaskCreateStatic(log_tx_task, "log_tx",
                                          LOG_TX_STACK / sizeof(StackType_t),
                                          NULL, LOG_TX_PRIO,
                                          s_log_tx_stack, &s_log_tx_tcb);
    if (task == NULL) {
        (void)log_capture_esp_detach();
        atomic_store_explicit(&s_task, NULL, memory_order_release);
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        ESP_LOGE(TAG, "Failed to create log_tx_task");
        return ESP_FAIL;
    }
    atomic_store_explicit(&s_task, task, memory_order_release);
    atomic_store_explicit(&s_state, LOG_STREAM_RUNNING, memory_order_release);
    xTaskNotifyGive(task);

    ESP_LOGI(TAG, "Started (level=%u, ring=%u entries)", level, RING_CAPACITY);
    return ESP_OK;
}

esp_err_t log_stream_stop(void)
{
    unsigned expected = LOG_STREAM_RUNNING;
    if (!atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STOPPING,
            memory_order_acq_rel, memory_order_acquire)) {
        return expected == LOG_STREAM_STOPPED ? ESP_OK : ESP_ERR_INVALID_STATE;
    }

    if (!log_capture_esp_detach()) {
        /* Pointer is already detached. Static storage remains untouched and a
         * later start retries reader quiescence before reinitializing it. */
        ESP_LOGW(TAG, "native log readers did not quiesce before timeout");
    }

    const TickType_t capture_start = xTaskGetTickCount();
    TickType_t capture_timeout = pdMS_TO_TICKS(LOG_CAPTURE_USER_TIMEOUT_MS);
    if (capture_timeout == 0) {
        capture_timeout = 1;
    }
    while (atomic_load_explicit(&s_capture_users, memory_order_seq_cst) != 0) {
        if ((TickType_t)(xTaskGetTickCount() - capture_start) >= capture_timeout) {
            ESP_LOGW(TAG, "explicit log producers did not quiesce before timeout");
            break;
        }
        taskYIELD();
        vTaskDelay(1);
    }

    TaskHandle_t task = atomic_load_explicit(&s_task, memory_order_acquire);
    if (task != NULL) {
        xTaskNotifyGive(task);
        EventBits_t bits = xEventGroupWaitBits(
            s_task_events, LOG_TASK_EXITED_BIT, pdFALSE, pdTRUE,
            pdMS_TO_TICKS(LOG_TASK_STOP_TIMEOUT_MS));
        if ((bits & LOG_TASK_EXITED_BIT) == 0) {
            /* Never force-delete: the worker may be inside publish. It owns its
             * exit and will move STOPPING -> STOPPED after publish returns. */
            ESP_LOGW(TAG, "log_tx_task stop timed out; awaiting cooperative exit");
            return ESP_FAIL;
        }
        /* The exit bit is set after s_task becomes NULL. Complete the state
         * transition here as well so stop never returns while still STOPPING. */
        maybe_finish_stop();
    } else {
        maybe_finish_stop();
    }

    ESP_LOGI(TAG, "Stopped");
    return atomic_load_explicit(&s_state, memory_order_acquire) == LOG_STREAM_STOPPED
        ? ESP_OK : ESP_FAIL;
}

esp_err_t log_stream_set_level(uint8_t level)
{
    if (!capture_user_enter()) {
        return ESP_ERR_INVALID_STATE;
    }
    level = bounded_level(level);
    log_capture_set_level(&s_capture, level);
    atomic_store_explicit(&s_level, level, memory_order_release);
    capture_user_leave();
    log_stream_emit(LOG_LEVEL_INFO, TAG, "remote level set=%u", level);
    return ESP_OK;
}

bool log_stream_is_active(void)
{
    return atomic_load_explicit(&s_state, memory_order_acquire) == LOG_STREAM_RUNNING;
}

uint8_t log_stream_get_level(void)
{
    return (uint8_t)atomic_load_explicit(&s_level, memory_order_acquire);
}

void log_stream_emit(uint8_t level, const char *tag, const char *fmt, ...)
{
    if (tag == NULL || fmt == NULL || !capture_user_enter()) {
        return;
    }

    va_list args;
    va_start(args, fmt);
    (void)log_capture_pushv(&s_capture, level, (uint64_t)esp_timer_get_time(),
                            tag, fmt, args);
    va_end(args);
    capture_user_leave();
}
