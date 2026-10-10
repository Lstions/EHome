/**
 * @file scheduler.c
 * @brief Channel Scheduler v3.1 — pure timer, all buses via unified command queue
 *
 * Every channel (UART / I2C / SPI) is sampled by posting a CMD_SAMPLE
 * descriptor to the injected command queue.  A single bus-worker task drains the queue
 * and performs the actual bus transactions, eliminating per-bus special
 * cases and the race-prone vTaskDelete in scheduler_stop.
 *
 * v2.3: channels with edge_devices use a three-level loop
 *       (channel → edge_device → command) with independent per-command timing.
 *       Channels without edge_devices fall back to the legacy template_ids[0] path.
 */

#include "scheduler.h"
#include "scheduler_queue_guard.h"
#include "scheduler_health.h"   /* D-07：健康计数的写入规则（宿主可测纯函数）*/
#include "config_mgr.h"
#include "collector_mem.h"
#include "cmd_queue.h"
#include "bus_dma.h"
#include "hw_tables.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/queue.h"
#include "driver/uart.h"
#include <string.h>
#include <inttypes.h>

#define TAG "SCHEDULER"
#define SCHED_CONTROL_QUEUE_RESERVE 2U

/* ── per-channel state (struct definition now in scheduler.h) ────
 *
 * On PSRAM models the whole SCHED_MAX_CHANNELS x sched_channel_t table
 * (S3: 5 x 748 B = 3,740 B) is purely CPU-accessed scheduler state, so it is
 * allocated from PSRAM at scheduler_init(); internal-only models keep the
 * static .bss array.  The sched_channel_t itself was narrowed by WS-C
 * (MAX_CHANNELS 8->5/4, bus_config 128->64). */
#define SCHED_CHANNELS_BYTES (SCHED_MAX_CHANNELS * sizeof(sched_channel_t))
#if COLLECTOR_MEM_PSRAM_ENABLED
static sched_channel_t *s_channels;
#else
static sched_channel_t s_channels[SCHED_MAX_CHANNELS];
#endif

/** True when the channel table can be touched.  Internal models always can;
 *  PSRAM models can once scheduler_init() allocated it. */
static inline bool sched_channels_ready(void)
{
#if COLLECTOR_MEM_PSRAM_ENABLED
    return s_channels != NULL;
#else
    return true;
#endif
}
static TaskHandle_t    s_task_handle;
static volatile bool   s_running;
static volatile bool   s_prepared;
static scheduler_queues_t s_queues;
static volatile uint32_t s_min_queue_spaces;
static scheduler_queue_metrics_t s_queue_metrics;

/* 调度任务的**静态**栈与 TCB（2026-10-05）。
 *
 * 为什么必须静态：配置事务的流程是 prepare(停调度任务) -> apply_scheduler(重建任务)，
 * 也就是**每次配置同步都会销毁再创建一个 4096 字节栈的任务**。
 * 反复 alloc/free 同尺寸块正是制造堆碎片的经典手法；一旦堆里没有 4096 的
 * **连续**块，xTaskCreatePinnedToCore 就失败 -> apply_scheduler 返回 ESP_FAIL
 * -> 整个配置事务回滚 -> 设备永久上报 success=false。
 *
 * 现场数据（2026-10-05，均 2.8.0 / IDF 6.1）：
 *     C6  free=77752  ->  config applied/in_sync   （正常）
 *     S3  free=25784  ->  config failed/failed      （每次同步都失败）
 * 串口证据：5 条 scheduler_add_channel 全部成功，紧接着
 *     E CFG_TX: config apply failed at apply_scheduler: ESP_FAIL (0xffffffff)
 * 因为 add_channel 的所有失败返回都在其日志**之前**，能打出日志即说明五路都成功，
 * 失败点只能是任务创建。
 *
 * 静态分配后任务创建**不再从堆取内存**，与堆碎片彻底解耦。
 * 这与本仓 2026-10-04 给 log_tx_task 做的修复是同一手法（那次堆只剩 13.7KB）。
 *
 * 注意 StackType_t 在 Xtensa 上是 4 字节，xTaskCreateStatic 的栈深度参数以
 * **字**为单位（xTaskCreate 用字节）—— 本仓已踩过这个坑，故显式做除法加断言。 */
#define SCHED_TASK_STACK_WORDS (SCHED_TASK_STACK / sizeof(StackType_t))
_Static_assert(SCHED_TASK_STACK % sizeof(StackType_t) == 0,
               "SCHED_TASK_STACK must be a whole number of StackType_t words");
static StackType_t  s_sched_stack[SCHED_TASK_STACK_WORDS];
static StaticTask_t s_sched_tcb;

enum {
    SCHED_Q_UART0 = 0,
    SCHED_Q_UART1 = 1,
    SCHED_Q_UART2 = 2,
    SCHED_Q_SPI = 3,
    SCHED_Q_I2C = 4,
};

static void scheduler_task(void *p);

/* ── queue dispatch: pick the right per-bus queue from bus_cmd_t ── */

/* Both helpers only read members inside BUS_CMD_COMMON (the shared prefix),
 * so they keep taking bus_cmd_t * for compatibility with the existing tests.
 * P2 (2026-10-10): the sample producers hold a sample_cmd_t, which is an
 * exact prefix of bus_cmd_t — they cast at the call site (see the two
 * producers).  Reading past the prefix through this pointer would be a bug,
 * so the helpers must never touch plan members. */
static QueueHandle_t dispatch_queue(const scheduler_queues_t *q, const bus_cmd_t *bcmd)
{
    switch (bcmd->bus_type) {
    case BUS_TYPE_UART:
        if (bcmd->uart_port == UART_NUM_0) return q->uart0_cmd_queue;
        if (bcmd->uart_port == UART_NUM_1) return q->uart1_cmd_queue;
        if (bcmd->uart_port == UART_NUM_2) return q->uart2_cmd_queue;
        return NULL;
    /* USB samples ride the UART0 worker pair: uart_cmd_loop resolves the target
     * context by channel_id, so no new worker or queue is required.  See the
     * matching routing comment in bus_manager. */
    case BUS_TYPE_USB:  return q->uart0_cmd_queue;
    case BUS_TYPE_SPI:  return q->spi_cmd_queue;
    case BUS_TYPE_I2C:  return q->i2c_cmd_queue;
    default:            return q->uart0_cmd_queue;
    }
}

/* A board may intentionally expose only a subset of bus workers.  Treat a
 * missing queue as fully available for global backpressure accounting; the
 * per-channel dispatch path below rejects a command whose own queue is absent. */
static UBaseType_t queue_spaces_or_depth(QueueHandle_t queue)
{
    return scheduler_queue_is_present(queue) ? uxQueueSpacesAvailable(queue) : CMD_QUEUE_DEPTH;
}

static UBaseType_t min_queue_spaces(UBaseType_t left, UBaseType_t right)
{
    return left < right ? left : right;
}

static int queue_metric_index(const bus_cmd_t *cmd)
{
    if (!cmd) return -1;
    switch (cmd->bus_type) {
    case BUS_TYPE_UART:
        if (cmd->uart_port == UART_NUM_0) return SCHED_Q_UART0;
        if (cmd->uart_port == UART_NUM_1) return SCHED_Q_UART1;
        if (cmd->uart_port == UART_NUM_2) return SCHED_Q_UART2;
        return -1;
    /* USB shares the UART0 worker, so it accounts under the UART0 queue metric
     * instead of a new index (which would change the metrics payload). */
    case BUS_TYPE_USB: return SCHED_Q_UART0;
    case BUS_TYPE_SPI: return SCHED_Q_SPI;
    case BUS_TYPE_I2C: return SCHED_Q_I2C;
    default: return -1;
    }
}

static UBaseType_t queue_metric_capacity(int index)
{
    return (index == SCHED_Q_SPI || index == SCHED_Q_I2C) ? 8U : 16U;
}

static void observe_queue_metrics(void)
{
    QueueHandle_t queues[SCHED_QUEUE_METRIC_COUNT] = {
        s_queues.uart0_cmd_queue, s_queues.uart1_cmd_queue,
        s_queues.uart2_cmd_queue, s_queues.spi_cmd_queue,
        s_queues.i2c_cmd_queue,
    };
    for (int i = 0; i < SCHED_QUEUE_METRIC_COUNT; i++) {
        UBaseType_t spaces = queue_spaces_or_depth(queues[i]);
        UBaseType_t capacity = queue_metric_capacity(i);
        s_queue_metrics.current_spaces[i] = (uint32_t)spaces;
        uint32_t used = spaces <= capacity ? (uint32_t)(capacity - spaces) : 0;
        if (used > s_queue_metrics.high_water_used[i])
            s_queue_metrics.high_water_used[i] = used;
    }
}

/* ── derive uart_port_t from bus_config bytes via hw_tables ── */
/* P3-7: Replaced static derive_uart_port with shared hw_derive_uart_port from hw_tables */

static uart_port_t derive_uart_port(const config_channel_t *ch)
{
    if (ch->bus_type != BUS_TYPE_UART || ch->bus_config_len < 2)
        return UART_NUM_0;  /* safe default */

    return hw_derive_uart_port(ch->bus_config[0], ch->bus_config[1], UART_NUM_1);
}

static uart_port_t route_uart_port(const config_channel_t *ch)
{
    if (ch && s_queues.uart_route) {
        uart_port_t leased = s_queues.uart_route(s_queues.route_ctx, ch->id);
        if (leased >= UART_NUM_0 && leased < UART_NUM_MAX)
            return leased;
    }
    return derive_uart_port(ch);
}

/* ── public API ──────────────────────────────────────────────────── */

void scheduler_init(void)
{
#if COLLECTOR_MEM_PSRAM_ENABLED
    if (s_channels == NULL) {
        s_channels = collector_mem_alloc_pref_psram(SCHED_CHANNELS_BYTES);
        if (s_channels == NULL) {
            ESP_LOGE(TAG, "scheduler channel table allocation failed (%u bytes)",
                     (unsigned)SCHED_CHANNELS_BYTES);
        }
    }
#endif
    if (sched_channels_ready()) {
        memset(s_channels, 0, SCHED_CHANNELS_BYTES);
    }
    s_running     = false;
    s_prepared    = false;
    s_task_handle = NULL;
    s_min_queue_spaces = CMD_QUEUE_DEPTH;
    memset(&s_queue_metrics, 0, sizeof(s_queue_metrics));
}

static bool queues_valid(const scheduler_queues_t *queues)
{
    return queues && (queues->uart0_cmd_queue || queues->uart1_cmd_queue ||
                      queues->uart2_cmd_queue ||
                      queues->spi_cmd_queue || queues->i2c_cmd_queue);
}

sched_err_t scheduler_prepare(const scheduler_queues_t *queues,
                              const config_manifest_t *manifest)
{
    if (s_task_handle || s_prepared) return SCHED_ERR_DUPLICATE;
    if (!queues_valid(queues) || !manifest || !sched_channels_ready()) {
        ESP_LOGE(TAG, "invalid queues or manifest, cannot prepare");
        return SCHED_ERR_INVALID;
    }
    s_queues = *queues;

    memset(s_channels, 0, SCHED_CHANNELS_BYTES);
    for (int i = 0; i < manifest->channel_count && i < MAX_CHANNELS; i++) {
        if (!manifest->channels[i].enabled) continue;
        sched_err_t err = scheduler_add_channel(&manifest->channels[i]);
        if (err != SCHED_OK) {
            memset(s_channels, 0, SCHED_CHANNELS_BYTES);
            return err;
        }
    }

    s_running = false;
    s_prepared = true;
    s_task_handle = xTaskCreateStaticPinnedToCore(
        scheduler_task, "scheduler", SCHED_TASK_STACK_WORDS, NULL,
        SCHED_TASK_PRIORITY, s_sched_stack, &s_sched_tcb, SCHED_TASK_CORE);
    if (s_task_handle == NULL) {
        /* 静态分配后这一路径理论上不可达（栈与 TCB 都在 .bss 中）。
         * 保留它作为兜底，并把内存实况打出来 —— 若真的触发，说明问题不在堆。 */
        ESP_LOGE(TAG, "scheduler task create FAILED (static): need %d bytes; "
                      "free=%u largest=%u min_ever=%u (internal) | free=%u largest=%u (total)",
                 SCHED_TASK_STACK,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
        s_prepared = false;
        memset(s_channels, 0, SCHED_CHANNELS_BYTES);
        return SCHED_ERR_NOT_INIT;
    }
    return SCHED_OK;
}

void scheduler_activate(void)
{
    if (s_prepared && s_task_handle) s_running = true;
}

sched_err_t scheduler_start_manifest(const scheduler_queues_t *queues,
                                     const config_manifest_t *manifest)
{
    sched_err_t err = scheduler_prepare(queues, manifest);
    if (err == SCHED_OK) scheduler_activate();
    return err;
}

sched_err_t scheduler_start(const scheduler_queues_t *queues)
{
    const config_manifest_t *cfg = config_mgr_get_manifest();
    return cfg ? scheduler_start_manifest(queues, cfg) : SCHED_ERR_INVALID;
}

sched_err_t scheduler_stop(void)
{
    s_running = false;
    s_prepared = false;

    if (s_task_handle) {
        /* Wait for the task to notice s_running==false and exit its loop. */
        for (int i = 0; i < 100 && eTaskGetState(s_task_handle) != eDeleted; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
        if (eTaskGetState(s_task_handle) != eDeleted) {
            ESP_LOGE(TAG, "scheduler task did not stop");
            return SCHED_ERR_BUS;
        }
        s_task_handle = NULL;
    }

    /* Now safe to clear channel state — no task is reading it. */
    if (!sched_channels_ready()) return SCHED_OK;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        s_channels[i].active = false;
    }
    return SCHED_OK;
}

/* v2.4: Lightweight pause — stops the task loop but preserves channel state.
 * Unlike scheduler_stop(), this does NOT clear s_channels[].active.
 * Caller must call scheduler_resume() to restart the task. */
sched_err_t scheduler_pause(void)
{
    s_running = false;
    s_prepared = false;
    if (s_task_handle) {
        for (int i = 0; i < 100 && eTaskGetState(s_task_handle) != eDeleted; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
        if (eTaskGetState(s_task_handle) != eDeleted) return SCHED_ERR_BUS;
        s_task_handle = NULL;
    }
    /* Channel state preserved — s_channels[].active untouched */
    return SCHED_OK;
}

/* v2.4: Resume after pause — recreates the task without reloading channels. */
sched_err_t scheduler_resume(const scheduler_queues_t *queues)
{
    if (s_task_handle) return SCHED_ERR_DUPLICATE;
    if (queues) {
        s_queues = *queues;
    }
    if (s_queues.uart0_cmd_queue == NULL && s_queues.uart1_cmd_queue == NULL &&
        s_queues.uart2_cmd_queue == NULL &&
        s_queues.spi_cmd_queue == NULL && s_queues.i2c_cmd_queue == NULL) {
        ESP_LOGE(TAG, "all queues are NULL, cannot resume");
        return SCHED_ERR_INVALID;
    }
    s_running = true;
    /* 同样用静态分配（见文件内 SCHED_TASK_STACK_WORDS 处的说明）。 */
    s_task_handle = xTaskCreateStaticPinnedToCore(
        scheduler_task, "scheduler", SCHED_TASK_STACK_WORDS, NULL,
        SCHED_TASK_PRIORITY, s_sched_stack, &s_sched_tcb, SCHED_TASK_CORE);
    if (s_task_handle == NULL) {
        ESP_LOGE(TAG, "scheduler task resume FAILED (static); free=%u largest=%u (internal)",
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT));
        s_running = false;
        return SCHED_ERR_NOT_INIT;
    }
    return SCHED_OK;
}

sched_err_t scheduler_add_channel(const config_channel_t *ch)
{
    if (!ch || !sched_channels_ready()) return SCHED_ERR_INVALID;

    int slot = -1;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == ch->id)
            return SCHED_ERR_DUPLICATE;
        if (!s_channels[i].active && slot < 0)
            slot = i;
    }
    if (slot < 0) return SCHED_ERR_FULL;

    memcpy(&s_channels[slot].config, ch, sizeof(config_channel_t));
    s_channels[slot].last_sequence    = 0;
    s_channels[slot].last_sample_time = 0;
    s_channels[slot].active           = true;
    s_channels[slot].error_count      = 0;
    s_channels[slot].skip_count       = 0;

    /* v2.3: initialise edge_device + command scheduler state */
    s_channels[slot].edge_device_count = 0;
    if (ch->edge_device_count > 0) {
        uint8_t count = ch->edge_device_count;
        if (count > MAX_EDGE_DEVICES_PER_CH)
            count = MAX_EDGE_DEVICES_PER_CH;
        s_channels[slot].edge_device_count = count;
        ESP_LOGI(TAG, "scheduler_add_channel: ch_id=%lu, edge_device_count=%d, ed[0].id=%lu",
                 (unsigned long)ch->id, ch->edge_device_count,
                 ch->edge_device_count > 0 ? (unsigned long)ch->edge_devices[0].edge_device_id : 0);

        for (int ed = 0; ed < count; ed++) {
            const config_edge_device_t *src = &ch->edge_devices[ed];
            sched_edge_device_t *dst = &s_channels[slot].edge_devices[ed];
            dst->edge_device_id = src->edge_device_id;
            dst->hardware_id    = src->hardware_id;
            dst->command_count  = 0;

            uint8_t cmd_count = src->command_count;
            if (cmd_count > MAX_COMMANDS_PER_DEVICE)
                cmd_count = MAX_COMMANDS_PER_DEVICE;
            dst->command_count = cmd_count;

            for (int ci = 0; ci < cmd_count; ci++) {
                dst->commands[ci].template_id  = src->commands[ci].template_id;
                dst->commands[ci].interval_ms  = src->commands[ci].interval_ms;
                dst->commands[ci].enabled      = src->commands[ci].enabled;
                dst->commands[ci].last_run_ms  = 0;
            }
        }
    }

    return SCHED_OK;
}

sched_err_t scheduler_remove_channel(uint32_t id)
{
    if (!sched_channels_ready()) return SCHED_ERR_NOT_FOUND;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == id) {
            s_channels[i].active = false;
            return SCHED_OK;
        }
    }
    return SCHED_ERR_NOT_FOUND;
}

/* v2.4: In-place update — only copies the config (interval_ms, template_ids, etc.)
 * without changing the active flag or runtime counters.  Used when bus-level
 * config hasn't changed and we don't want to lose the last_sample_time. */
sched_err_t scheduler_update_channel(const config_channel_t *ch)
{
    if (!ch || !sched_channels_ready()) return SCHED_ERR_INVALID;

    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == ch->id) {
            /* Preserve runtime state, overwrite config */
            TickType_t saved_last = s_channels[i].last_sample_time;
            uint32_t saved_seq = s_channels[i].last_sequence;
            uint32_t saved_err = s_channels[i].error_count;
            uint32_t saved_skip = s_channels[i].skip_count;

            memcpy(&s_channels[i].config, ch, sizeof(config_channel_t));
            s_channels[i].last_sample_time = saved_last;
            s_channels[i].last_sequence = saved_seq;
            s_channels[i].error_count = saved_err;
            s_channels[i].skip_count = saved_skip;

            /* Re-init edge_device state from new config */
            s_channels[i].edge_device_count = 0;
            if (ch->edge_device_count > 0) {
                uint8_t count = ch->edge_device_count;
                if (count > MAX_EDGE_DEVICES_PER_CH) count = MAX_EDGE_DEVICES_PER_CH;
                s_channels[i].edge_device_count = count;
                for (int ed = 0; ed < count; ed++) {
                    const config_edge_device_t *src = &ch->edge_devices[ed];
                    sched_edge_device_t *dst = &s_channels[i].edge_devices[ed];
                    dst->edge_device_id = src->edge_device_id;
                    dst->hardware_id    = src->hardware_id;
                    dst->command_count  = 0;
                    uint8_t cmd_count = src->command_count;
                    if (cmd_count > MAX_COMMANDS_PER_DEVICE) cmd_count = MAX_COMMANDS_PER_DEVICE;
                    dst->command_count = cmd_count;
                    for (int ci = 0; ci < cmd_count; ci++) {
                        dst->commands[ci].template_id  = src->commands[ci].template_id;
                        dst->commands[ci].interval_ms  = src->commands[ci].interval_ms;
                        dst->commands[ci].enabled      = src->commands[ci].enabled;
                        /* preserve last_run_ms for independent timing */
                    }
                }
            }
            return SCHED_OK;
        }
    }
    return SCHED_ERR_NOT_FOUND;
}

bool scheduler_is_running(void) { return s_running; }

uint8_t scheduler_get_channel_count(void)
{
    uint8_t c = 0;
    if (!sched_channels_ready()) return 0;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s_channels[i].active) c++;
    return c;
}

const scheduler_state_t *scheduler_get_state(void)
{
    static scheduler_state_t state;
    state.channels = s_channels;
    state.channel_count = sched_channels_ready() ? SCHED_MAX_CHANNELS : 0;
    return &state;
}

/* ── performance tracking ─────────────────────────────────────────── */

/* 2026-09-30 (defect 2, "statistics ownership migration"):
 * Find the per-command state that StatusReport reports as EdgeDeviceHealth.
 * handler_data.c reads sched_command_t.error_count, NOT the channel-level
 * s_channels[].error_count, so a health event that only bumps the channel
 * counter is invisible on the server.  Both notifiers must therefore update
 * the same per-command field the server reads.
 *
 * A zero/unknown key means the caller cannot address a command (legacy v1
 * channel, or a TX-level failure that happens before the RX path knows which
 * command is outstanding).  Only then do we fall back to the channel-level
 * counter so v1 backoff keeps working.
 *
 * Matching key is (channel, edge_device, template, index).  edge_device_id is
 * NOT optional: command_index is unique only *within* one device, so a channel
 * hosting two devices that share a template_id would otherwise let one
 * device's timeout be charged to the other, and the server would be told the
 * wrong device is faulted. */
static sched_command_t *sched_find_command(uint32_t channel_id,
                                           uint32_t edge_device_id,
                                           uint32_t command_template_id,
                                           uint8_t command_index)
{
    if (command_template_id == 0 || edge_device_id == 0) return NULL;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (!s_channels[i].active || s_channels[i].config.id != channel_id)
            continue;
        sched_channel_t *ch = &s_channels[i];
        for (int ed = 0; ed < ch->edge_device_count; ed++) {
            sched_edge_device_t *dev = &ch->edge_devices[ed];
            if (dev->edge_device_id != edge_device_id) continue;
            if (command_index >= dev->command_count) return NULL;
            if (dev->commands[command_index].template_id == command_template_id)
                return &dev->commands[command_index];
            return NULL;
        }
        return NULL;
    }
    return NULL;
}

static void sched_note_channel_error(uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == channel_id) {
            s_channels[i].error_count++;
            /* Cap error count to prevent overflow */
            if (s_channels[i].error_count > 100) {
                s_channels[i].error_count = 100;
            }
            break;
        }
    }
}

void scheduler_notify_channel_error(uint32_t channel_id)
{
    sched_note_channel_error(channel_id);
}

/* Record an RX/TX outcome against the exact command that was outstanding.
 *
 * The per-command counter is the one handler_data.c publishes as
 * EdgeDeviceHealth, so it is the only field that makes an unanswered sensor
 * visible to the server.  The channel-level counter is updated as well, so
 * the legacy v1 adaptive backoff keeps working exactly as before.
 *
 * Returns true when a per-command slot matched (the reported counter moved). */
bool scheduler_notify_command_outcome(uint32_t channel_id,
                                      uint32_t edge_device_id,
                                      uint32_t command_template_id,
                                      uint8_t command_index,
                                      bool success)
{
    bool reported = false;
    sched_command_t *scmd = sched_find_command(channel_id, edge_device_id,
                                               command_template_id, command_index);
    if (scmd) {
        /* D-07：设备层面的结果【才】可以推动健康计数 —— 走与背压路径
         * 同一个规则函数，保证"谁可以改这个字段"只有一个来源（P4）。 */
        scmd->error_count = sched_health_next(
            scmd->error_count,
            success ? SCHED_HEALTH_DEVICE_SUCCESS : SCHED_HEALTH_DEVICE_FAILURE);
        reported = true;
    }
    if (success) {
        /* Keep the legacy channel-level backoff/health semantics: a healthy
         * channel must not stay permanently backed off. */
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (s_channels[i].active && s_channels[i].config.id == channel_id) {
                if (s_channels[i].error_count > 0) s_channels[i].error_count--;
                s_channels[i].skip_count = 0;
                break;
            }
        }
    } else {
        sched_note_channel_error(channel_id);
    }
    return reported;
}

void scheduler_notify_channel_success(uint32_t channel_id)
{
    /* 0/0/0 = "no addressable command": channel-level bookkeeping only. */
    (void)scheduler_notify_command_outcome(channel_id, 0, 0, 0, true);
}

void scheduler_get_performance(scheduler_performance_t *out)
{
    if (!out) return;
    out->min_queue_spaces = s_min_queue_spaces;
    out->stack_high_water_words = s_task_handle
        ? (uint32_t)uxTaskGetStackHighWaterMark(s_task_handle) : 0;
}

void scheduler_get_queue_metrics(scheduler_queue_metrics_t *out)
{
    if (!out) return;
    memcpy(out, &s_queue_metrics, sizeof(*out));
}

/* ── scheduler helper functions ──────────────────────────────────── */

/**
 * @brief Schedule commands for a channel using v2 edge_device mode.
 * 
 * Iterates through all edge devices and their commands, checking timing
 * and sending commands to the queue.
 * 
 * @param ch Channel to schedule
 * @param now Current tick count
 * @param queue_pressure True if queue is nearly full
 * @param total_samples Pointer to sample counter
 * @param queue_full_count Pointer to queue-full counter
 */
static void schedule_v2_channel(sched_channel_t *ch, TickType_t now,
                                bool queue_pressure,
                                uint32_t *total_samples, uint32_t *queue_full_count)
{
    for (int ed = 0; ed < ch->edge_device_count; ed++) {
        sched_edge_device_t *dev = &ch->edge_devices[ed];
        for (int ci = 0; ci < dev->command_count; ci++) {
            sched_command_t *scmd = &dev->commands[ci];
            if (!scmd->enabled) continue;

            /* Independent timing check */
            if (now - scmd->last_run_ms < pdMS_TO_TICKS(scmd->interval_ms))
                continue;

            scmd->last_run_ms = now;

            /* Backpressure: skip if queue is nearly full */
            if (queue_pressure) {
                (*queue_full_count)++;
                continue;
            }

            /* Look up template for this command */
            const config_template_t *t = config_mgr_get_template(scmd->template_id);
            if (!t || t->write_data_len == 0) continue;

            /* Build a SAMPLE command.  P2 (2026-10-10): the type is
             * sample_cmd_t (the slim prefix).  Using the slim type here makes
             * it a compile error to ever set batch-plan members on a sample
             * command, and it drops this stack frame from 700 to 180 bytes. */
            sample_cmd_t bcmd = {
                .channel_id     = ch->config.id,
                .bus_type       = ch->config.bus_type,
                .tx_len         = t->write_data_len < CMD_TX_MAX ? t->write_data_len : CMD_TX_MAX,
                .delay_ms       = t->delay_ms > 0 ? t->delay_ms : 0,
                .read_size      = t->read_length,  /* P1-8: pass read_length for rx_task metadata */
                .edge_device_id = dev->edge_device_id,
                .command_template_id = scmd->template_id,
                .command_index  = (uint8_t)ci,
                .type           = CMD_SAMPLE,
            };
            memcpy(bcmd.tx_data, t->write_data, bcmd.tx_len);

            bcmd.uart_port = route_uart_port(&ch->config);
            /* sample_cmd_t is an exact prefix of bus_cmd_t (static-asserted in
             * cmd_queue.h), so this cast is layout-safe; the helpers only read
             * prefix members. */
            const bus_cmd_t *as_full = (const bus_cmd_t *)&bcmd;
            QueueHandle_t target_q = dispatch_queue(&s_queues, as_full);
            int metric_index = queue_metric_index(as_full);
            /* Keep a small per-bus reserve for on-demand control commands.
             * A congested SPI queue must not suppress UART0/UART1 sampling. */
            if (!scheduler_queue_is_present(target_q) ||
                uxQueueSpacesAvailable(target_q) <= SCHED_CONTROL_QUEUE_RESERVE ||
                xQueueSend(target_q, &bcmd, 0) != pdTRUE) {
                if (metric_index >= 0) {
                    if (!scheduler_queue_is_present(target_q) ||
                        uxQueueSpacesAvailable(target_q) <= SCHED_CONTROL_QUEUE_RESERVE)
                        s_queue_metrics.sample_skipped[metric_index]++;
                    else
                        s_queue_metrics.sample_rejected[metric_index]++;
                }
                (*queue_full_count)++;
                /* D-07 修复（2026-10-06）：本机队列满【不是】设备故障。
                 * 旧代码在这里 scmd->error_count++，而该字段是服务端读的
                 * 唯一健康量（handler_data.c 映射成 TIMEOUT/FAULT）——
                 * 于是 100 Hz 下必然发生的队列满（L-01c 实测 full=577~589）
                 * 会被上报成"现场传感器故障"。
                 * 现在：本机背压只进自己的计数；健康计数由规则函数统一管。
                 * 可观测性没有损失 —— 该拒绝早已计入
                 * s_queue_metrics.sample_rejected[]/sample_skipped[] 并上报。 */
                scmd->queue_full_count++;
                scmd->error_count = sched_health_next(
                    scmd->error_count, SCHED_HEALTH_LOCAL_BACKPRESSURE);
            } else {
                (*total_samples)++;
                /* 2026-09-30 (defect 2): do NOT clear error_count here.
                 * Handing bytes to the bus queue only proves the TX path
                 * accepted the request -- it says nothing about whether the
                 * sensor answered.  Clearing on enqueue made the counter that
                 * handler_data.c publishes (sched_command_t.error_count)
                 * return to 0 on every sample, so a device that never
                 * responded still reported error_code=0 for 7 days.
                 *
                 * The counter is now cleared only by an observed successful
                 * RX completion: scheduler_notify_command_outcome(..., true,
                 * ...) called from the rx_task idle-boundary path.  A
                 * command whose queue is always full still climbs via the
                 * branch above. */
            }
        }
    }
}

/**
 * @brief Schedule commands for a channel using v1 legacy template mode.
 * 
 * Uses the first template ID and applies adaptive backoff on errors.
 * 
 * @param ch Channel to schedule
 * @param now Current tick count
 * @param queue_pressure True if queue is nearly full
 * @param total_samples Pointer to sample counter
 * @param queue_full_count Pointer to queue-full counter
 */
static void schedule_v1_channel(sched_channel_t *ch, TickType_t now,
                                bool queue_pressure,
                                uint32_t *total_samples, uint32_t *queue_full_count)
{
    if (now - ch->last_sample_time < pdMS_TO_TICKS(ch->config.interval_ms))
        return;

    /* Adaptive backoff: if channel has errors, skip some samples */
    if (ch->error_count > 3) {
        ch->skip_count++;
        /* Exponential backoff: skip 2^min(error_count, 5) samples */
        uint32_t skip_threshold = (ch->error_count > 5) ? 32 :
                                  (1 << (ch->error_count - 3));
        if (ch->skip_count < skip_threshold) {
            return;
        }
        ch->skip_count = 0;
    }

    ch->last_sample_time = now;

    /* Backpressure: skip if queue is nearly full */
    if (queue_pressure) {
        (*queue_full_count)++;
        return;
    }

    /* Build a unified bus command for any bus type.
     * Only channels with templates need active TX (e.g. Modbus polling).
     * Channels without templates (e.g. GPS NMEA) are passive —
     * rx_task handles them. */
    /* P2 (2026-10-10): slim type for the same reasons as the V2 path above. */
    sample_cmd_t cmd = {
        .channel_id = ch->config.id,
        .bus_type   = ch->config.bus_type,
        .tx_len     = 0,
        .delay_ms   = 0,
        .type       = CMD_SAMPLE,
    };

    /* If the channel references a template, copy its TX payload and delay. */
    if (ch->config.template_count > 0) {
        const config_template_t *t = config_mgr_get_template(ch->config.template_ids[0]);
        if (t && t->write_data_len > 0) {
            cmd.tx_len = t->write_data_len < CMD_TX_MAX ? t->write_data_len : CMD_TX_MAX;
            memcpy(cmd.tx_data, t->write_data, cmd.tx_len);
            cmd.read_size = t->read_length;  /* P1-8: pass read_length for rx_task metadata */
            if (t->delay_ms > 0) {
                cmd.delay_ms = t->delay_ms;
            }
        }
    } else {
        /* No template — skip this channel.  rx_task handles passive
         * UART RX; SPI/I2C without a template have nothing to do. */
        return;
    }

    cmd.uart_port = route_uart_port(&ch->config);
    /* See the V2 producer above for why this prefix cast is safe. */
    const bus_cmd_t *as_full = (const bus_cmd_t *)&cmd;
    QueueHandle_t target_q = dispatch_queue(&s_queues, as_full);
    int metric_index = queue_metric_index(as_full);
    if (!scheduler_queue_is_present(target_q) ||
        uxQueueSpacesAvailable(target_q) <= SCHED_CONTROL_QUEUE_RESERVE ||
        xQueueSend(target_q, &cmd, 0) != pdTRUE) {
        if (metric_index >= 0) {
            if (!scheduler_queue_is_present(target_q) ||
                uxQueueSpacesAvailable(target_q) <= SCHED_CONTROL_QUEUE_RESERVE)
                s_queue_metrics.sample_skipped[metric_index]++;
            else
                s_queue_metrics.sample_rejected[metric_index]++;
        }
        (*queue_full_count)++;
    } else {
        (*total_samples)++;
    }
}

/* ── scheduler task (P3-1: dynamic tick — 1ms when fast channels active, 10ms otherwise) ─ */

static void scheduler_task(void *p)
{
    (void)p;
    TickType_t wake = xTaskGetTickCount();
    uint32_t queue_full_count = 0;
    uint32_t total_samples = 0;

    TickType_t prepared_at = xTaskGetTickCount();
    while (s_prepared && !s_running &&
           xTaskGetTickCount() - prepared_at < pdMS_TO_TICKS(SCHED_PREPARE_TIMEOUT_MS)) {
        vTaskDelay(pdMS_TO_TICKS(1));
    }
    if (!s_running) s_prepared = false;
    while (s_running) {
        /* P3-1: Dynamic tick — use 1ms when any channel needs <100ms interval,
         * otherwise use 10ms to save CPU.  Checked every iteration because
         * channel configuration may change at runtime. */
        bool has_fast_channel = false;
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active) continue;
            if (s_channels[i].config.interval_ms < 100) {
                has_fast_channel = true;
                break;
            }
            /* Also check per-command intervals in v2 edge_device channels */
            for (int ed = 0; ed < s_channels[i].edge_device_count; ed++) {
                for (int ci = 0; ci < s_channels[i].edge_devices[ed].command_count; ci++) {
                    if (s_channels[i].edge_devices[ed].commands[ci].interval_ms < 100) {
                        has_fast_channel = true;
                        break;
                    }
                }
                if (has_fast_channel) break;
            }
            if (has_fast_channel) break;
        }
        TickType_t tick_ms = has_fast_channel ? 1 : 10;
        vTaskDelayUntil(&wake, pdMS_TO_TICKS(tick_ms));
        TickType_t now = xTaskGetTickCount();

        /* Check queue depth for backpressure — use the busiest queue */
        UBaseType_t min_spaces = CMD_QUEUE_DEPTH;
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.uart0_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.uart1_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.uart2_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.spi_cmd_queue));
        min_spaces = min_queue_spaces(min_spaces, queue_spaces_or_depth(s_queues.i2c_cmd_queue));
        observe_queue_metrics();
        if (min_spaces < s_min_queue_spaces) s_min_queue_spaces = min_spaces;
        /* Pressure is evaluated at the destination queue at enqueue time;
         * global min pressure is retained only for diagnostics. */
        bool queue_pressure = false;

        /* Iterate through all active channels */
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s_channels[i].active || !s_channels[i].config.enabled)
                continue;
            sched_channel_t *ch = &s_channels[i];

            /* Dispatch to appropriate scheduling strategy */
            {
                static TickType_t s_path_log_time[SCHED_MAX_CHANNELS] = {0};
                TickType_t now2 = xTaskGetTickCount();
                if (s_path_log_time[i] == 0 || now2 - s_path_log_time[i] > pdMS_TO_TICKS(60000)) {
                    ESP_LOGI(TAG, "sched: ch=%lu edge_device_count=%d, choosing %s path",
                             (unsigned long)ch->config.id, ch->edge_device_count,
                             ch->edge_device_count > 0 ? "v2" : "v1");
                    s_path_log_time[i] = now2;
                }
            }
            if (ch->edge_device_count > 0) {
                schedule_v2_channel(ch, now, queue_pressure, &total_samples, &queue_full_count);
            } else {
                schedule_v1_channel(ch, now, queue_pressure, &total_samples, &queue_full_count);
            }
        }

        /* Periodic performance logging (every 10 seconds) */
        static uint32_t last_log = 0;
        if (now - last_log > pdMS_TO_TICKS(10000)) {
            if (total_samples > 0 || queue_full_count > 0) {
                ESP_LOGI(TAG, "Stats: samples=%" PRIu32 " full=%" PRIu32 " min_free=%d",
                         total_samples, queue_full_count, (int)min_spaces);
            }
            last_log = now;
            total_samples = 0;
            queue_full_count = 0;
        }
    }

    vTaskDelete(NULL);
}
