/**
 * @file crash_diag.c
 * @brief 崩溃诊断：复位原因 + 异常捕获 + NVS 持久化 + 上报确认后释放
 *
 * 实现红线（panic 上下文约束）
 * ---------------------------
 * crash_diag_capture_from_panic() 运行在 **panic 上下文**：flash cache 可能
 * 已关闭、中断可能已禁用、栈可能已损坏。该函数因此只做三件事：
 *   (a) 读寄存器和栈内存   (b) 写 RTC_NOINIT SRAM   (c) 写两个 volatile
 * **绝不**调用 ESP_LOG*、nvs_*、malloc、esp_timer_get_time 或任何可能触碰
 * flash 的函数 —— 否则会把唯一的崩溃证据也毁掉。
 *
 * 因此"崩溃时刻的 uptime"不能用 esp_timer_get_time() 现取（它需要 flash 中
 * 的定时器状态）；改为由 panic 挂钩的**调用方**在进入本函数前用
 * crash_diag_note_uptime() 传入（见 main.c 的 wrap 实现），该调用发生在
 * panic 早期、flash 仍可读。
 */

#include "crash_diag.h"

#include <string.h>
#include <stdio.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "esp_attr.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_random.h"
#include "nvs.h"
#include "nvs_flash.h"

#include "frame_codec.h"
#include "msg_handler.h"
#include "app_state.h"

static const char *TAG = "CRASH_DIAG";

#define NVS_NS_CRASH   "crashdiag"
#define NVS_KEY_COUNT  "count"

/* 最多保留的未确认记录条数。8 * ~120B ≈ 1KB，远小于 nvs 分区。 */
#define CRASH_DIAG_MAX_RECORDS 8

#define CRASH_RTC_MAGIC 0x0BADF00DUL
#define CRASH_REC_MAGIC 0x43444147UL /* "CDAG" */

/* ---- RTC_NOINIT 槽：panic 时写，复位后读 ---- */

typedef struct {
    uint32_t magic;
    uint32_t crc;
    uint32_t seq;
    int32_t  reset_reason;
    int32_t  core;
    int32_t  exception;
    uint32_t pc;
    uint32_t exccause;
    uint32_t uptime_sec;
    char     task_name[16];
    char     reboot_reason[CRASH_DIAG_REASON_MAX];
    uint32_t stack[CRASH_DIAG_STACK_WORDS];
} crash_rtc_slot_t;

/*
 * RTC_NOINIT：内容跨复位保留（RTC_DATA_ATTR 会被 bootloader 清零，不适用）。
 */
static RTC_NOINIT_ATTR crash_rtc_slot_t s_rtc;

static int  s_reset_reason = 0;
static bool s_inited = false;
/* 由 wrap 在 panic 早期填入，供 capture 使用（capture 自己不能读定时器） */
static volatile uint32_t s_panic_uptime_sec = 0;
static volatile bool     s_panic_uptime_valid = false;

/* ---- CRC32 (IEEE, 便于服务端独立复算) ---- */

static uint32_t crc32_update(uint32_t crc, uint8_t byte)
{
    crc ^= byte;
    for (int b = 0; b < 8; b++) {
        uint32_t mask = (uint32_t)(-(int32_t)(crc & 1u));
        crc = (crc >> 1) ^ (0xEDB88320u & mask);
    }
    return crc;
}

static uint32_t crc32_span(const uint8_t *data, size_t len)
{
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < len; i++) {
        crc = crc32_update(crc, data[i]);
    }
    return ~crc;
}

/* CRC over the slot with the crc field itself skipped. */
static uint32_t rtc_slot_crc(const crash_rtc_slot_t *slot)
{
    const uint8_t *base = (const uint8_t *)slot;
    const size_t skip_off = offsetof(crash_rtc_slot_t, crc);
    const size_t skip_len = sizeof(uint32_t);
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < sizeof(crash_rtc_slot_t); i++) {
        if (i >= skip_off && i < skip_off + skip_len) {
            continue;
        }
        crc = crc32_update(crc, base[i]);
    }
    return ~crc;
}

/* ---- NVS 记录 ---- */

typedef struct {
    uint32_t record_id;
    uint32_t magic;
    uint32_t crc;          /* CRC over bytes [0, offsetof(crc)) */
    uint32_t reset_reason;
    uint32_t core;
    uint32_t exception;
    uint32_t pc;
    uint32_t exccause;
    uint32_t uptime_sec;
    char     task_name[16];
    char     fw_version[16];
    char     reboot_reason[CRASH_DIAG_REASON_MAX];
    uint32_t stack_words;
    uint32_t stack[CRASH_DIAG_STACK_WORDS];
} crash_record_t;

static void record_key(uint8_t idx, char *out, size_t n)
{
    snprintf(out, n, "c%u", (unsigned)idx);
}

/* 记录条数。命名冲突规避：与 NVS_KEY_COUNT 语义一致。 */
static esp_err_t nvs_read_count(uint8_t *out)
{
    nvs_handle_t h;
    *out = 0;
    if (nvs_open(NVS_NS_CRASH, NVS_READONLY, &h) != ESP_OK) {
        return ESP_ERR_NVS_NOT_FOUND;
    }
    esp_err_t err = nvs_get_u8(h, NVS_KEY_COUNT, out);
    nvs_close(h);
    if (err != ESP_OK) {
        *out = 0;
    }
    return err;
}

static esp_err_t nvs_write_record(uint8_t idx, const crash_record_t *rec)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_NS_CRASH, NVS_READWRITE, &h);
    if (err != ESP_OK) {
        return err;
    }
    char key[8];
    record_key(idx, key, sizeof(key));
    err = nvs_set_blob(h, key, rec, sizeof(*rec));
    if (err == ESP_OK) {
        uint8_t count = 0;
        if (nvs_get_u8(h, NVS_KEY_COUNT, &count) != ESP_OK) {
            count = 0;
        }
        if ((uint16_t)idx + 1 > count) {
            count = (uint8_t)(idx + 1);
            err = nvs_set_u8(h, NVS_KEY_COUNT, count);
        }
        if (err == ESP_OK) {
            err = nvs_commit(h);
        }
    }
    nvs_close(h);
    return err;
}

static esp_err_t nvs_read_record(uint8_t idx, crash_record_t *out)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS_CRASH, NVS_READONLY, &h) != ESP_OK) {
        return ESP_ERR_NVS_NOT_FOUND;
    }
    char key[8];
    record_key(idx, key, sizeof(key));
    size_t len = sizeof(*out);
    esp_err_t err = nvs_get_blob(h, key, out, &len);
    nvs_close(h);
    if (err != ESP_OK || len != sizeof(*out)) {
        return ESP_ERR_INVALID_SIZE;
    }
    return ESP_OK;
}

/* 删除槽 0 并把后续记录前移，保持"最旧在槽 0"的连续性。 */
static esp_err_t nvs_drop_oldest(void)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(NVS_NS_CRASH, NVS_READWRITE, &h);
    if (err != ESP_OK) {
        return err;
    }
    uint8_t count = 0;
    if (nvs_get_u8(h, NVS_KEY_COUNT, &count) != ESP_OK || count == 0) {
        nvs_close(h);
        return ESP_ERR_NVS_NOT_FOUND;
    }
    char key[8];
    /* 前移 */
    for (uint8_t i = 1; i < count; i++) {
        crash_record_t tmp;
        size_t len = sizeof(tmp);
        char src[8];
        record_key(i, src, sizeof(src));
        if (nvs_get_blob(h, src, &tmp, &len) != ESP_OK) {
            continue;
        }
        record_key((uint8_t)(i - 1), key, sizeof(key));
        (void)nvs_set_blob(h, key, &tmp, sizeof(tmp));
    }
    record_key((uint8_t)(count - 1), key, sizeof(key));
    (void)nvs_erase_key(h, key);
    (void)nvs_set_u8(h, NVS_KEY_COUNT, (uint8_t)(count - 1));
    err = nvs_commit(h);
    nvs_close(h);
    return err;
}

/* ---- 公开 API ---- */

int crash_diag_reset_reason(void)
{
    return s_reset_reason;
}

const char *crash_diag_reset_reason_str(int reason)
{
    switch (reason) {
    case ESP_RST_UNKNOWN:    return "UNKNOWN";
    case ESP_RST_POWERON:    return "POWERON";
    case ESP_RST_EXT:        return "EXT_PIN";
    case ESP_RST_SW:         return "SOFTWARE";
    case ESP_RST_PANIC:      return "PANIC";
    case ESP_RST_INT_WDT:    return "INT_WDT";
    case ESP_RST_TASK_WDT:   return "TASK_WDT";
    case ESP_RST_WDT:        return "OTHER_WDT";
    case ESP_RST_DEEPSLEEP:  return "DEEPSLEEP";
    case ESP_RST_BROWNOUT:   return "BROWNOUT";
    case ESP_RST_SDIO:       return "SDIO";
    case ESP_RST_USB:        return "USB";
    case ESP_RST_JTAG:       return "JTAG";
    case ESP_RST_EFUSE:      return "EFUSE";
    case ESP_RST_PWR_GLITCH: return "PWR_GLITCH";
    default:                 return "?";
    }
}

const char *crash_diag_last_reboot_reason(void)
{
    return s_rtc.reboot_reason;
}

void crash_diag_mark_reboot_reason(const char *reason)
{
    if (reason == NULL) {
        s_rtc.reboot_reason[0] = '\0';
        return;
    }
    strlcpy(s_rtc.reboot_reason, reason, sizeof(s_rtc.reboot_reason));
}

/*
 * 由 wrap 在 panic 早期调用。此时 flash 尚可读，取一次 uptime 供 capture 用。
 * 若取值失败（返回 0），capture 记 0，不阻塞。
 */
void crash_diag_note_panic_uptime(uint32_t uptime_sec)
{
    s_panic_uptime_sec = uptime_sec;
    s_panic_uptime_valid = true;
}

void crash_diag_capture_from_panic(int core, int exception, uintptr_t pc,
                                   const void *frame)
{
    /* 只碰 RTC SRAM。顺序：元数据 -> 栈 -> CRC（CRC 必须最后）。 */
    s_rtc.magic = CRASH_RTC_MAGIC;
    s_rtc.seq++;
    s_rtc.reset_reason = (int32_t)ESP_RST_PANIC;
    s_rtc.core = core;
    s_rtc.exception = exception;
    s_rtc.pc = (uint32_t)pc;
    s_rtc.exccause = 0;
    s_rtc.uptime_sec = s_panic_uptime_valid ? s_panic_uptime_sec : 0;
    /*
     * 任务名：pcTaskGetName 只读 TCB（SRAM），在 panic 上下文是安全的，
     * 不触碰 flash。现场实测该字段曾为空 —— 空任务名会让"崩在哪个任务里"
     * 重新变成一个要靠猜的问题，所以这里必须真正填上。
     */
    {
        const char *tn = pcTaskGetName(NULL);
        if (tn != NULL) {
            size_t i = 0;
            for (; i + 1 < sizeof(s_rtc.task_name) && tn[i] != '\0'; i++) {
                s_rtc.task_name[i] = tn[i];
            }
            s_rtc.task_name[i] = '\0';
        } else {
            s_rtc.task_name[0] = '\0';
        }
    }
    /* reboot_reason 保留：主动重启标记与崩溃无冲突，且能看出"崩在哪个动作里" */

    /*
     * 栈窗口。优先用调用方给的异常帧（XtExcFrame 的前两个字是 pc/ps，
     * 其后是通用寄存器），退化为当前帧地址。
     */
    if (frame != NULL) {
        const volatile uint32_t *f = (const volatile uint32_t *)frame;
        for (int i = 0; i < CRASH_DIAG_STACK_WORDS; i++) {
            s_rtc.stack[i] = f[i];
        }
    } else {
        const volatile uint32_t *sp =
            (const volatile uint32_t *)__builtin_frame_address(0);
        for (int i = 0; i < CRASH_DIAG_STACK_WORDS; i++) {
            s_rtc.stack[i] = sp[i];
        }
    }

    s_rtc.crc = rtc_slot_crc(&s_rtc);
}

esp_err_t crash_diag_init(void)
{
    s_reset_reason = (int)esp_reset_reason();
    s_inited = true;

    bool have_crash = (s_rtc.magic == CRASH_RTC_MAGIC);
    if (have_crash) {
        uint32_t expect = rtc_slot_crc(&s_rtc);
        if (expect != s_rtc.crc) {
            ESP_LOGW(TAG, "RTC crash slot CRC mismatch (stored %08X computed %08X) — discarding",
                     (unsigned)s_rtc.crc, (unsigned)expect);
            have_crash = false;
        }
    }

    esp_err_t ret = ESP_ERR_NOT_FOUND;

    if (have_crash) {
        crash_record_t rec;
        memset(&rec, 0, sizeof(rec));
        rec.magic = CRASH_REC_MAGIC;
        rec.record_id = ((uint32_t)esp_random() & 0x7FFFFFFFu) | 0x80000000u;
        rec.reset_reason = (uint32_t)s_rtc.reset_reason;
        rec.core = (uint32_t)s_rtc.core;
        rec.exception = (uint32_t)s_rtc.exception;
        rec.pc = s_rtc.pc;
        rec.exccause = s_rtc.exccause;
        rec.uptime_sec = s_rtc.uptime_sec;
        memcpy(rec.task_name, s_rtc.task_name, sizeof(rec.task_name));
        memcpy(rec.reboot_reason, s_rtc.reboot_reason, sizeof(rec.reboot_reason));
        strlcpy(rec.fw_version, get_firmware_version(), sizeof(rec.fw_version));
        rec.stack_words = CRASH_DIAG_STACK_WORDS;
        memcpy(rec.stack, s_rtc.stack, sizeof(rec.stack));
        rec.crc = crc32_span((const uint8_t *)&rec, offsetof(crash_record_t, crc));

        uint8_t count = 0;
        (void)nvs_read_count(&count);
        if (count >= CRASH_DIAG_MAX_RECORDS) {
            ESP_LOGW(TAG, "NVS full (%u records), dropping oldest", (unsigned)count);
            (void)nvs_drop_oldest();
            count = CRASH_DIAG_MAX_RECORDS - 1;
        }
        esp_err_t werr = nvs_write_record(count, &rec);
        if (werr != ESP_OK) {
            ESP_LOGE(TAG, "persist crash record failed: %s", esp_err_to_name(werr));
        } else {
            ESP_LOGW(TAG, "CRASH captured: reset_reason=%d(%s) core=%d exc=%d pc=%08X uptime=%us",
                     (int)rec.reset_reason, crash_diag_reset_reason_str((int)rec.reset_reason),
                     (int)rec.core, (int)rec.exception,
                     (unsigned)rec.pc, (unsigned)rec.uptime_sec);
            ret = ESP_OK;
        }
    }

    /* 清空 RTC 槽，避免下次启动重复上报同一条崩溃。
     * 注意：reboot_reason 已被复制进 NVS 记录，这里一并清掉是安全的。 */
    memset(&s_rtc, 0, sizeof(s_rtc));

    ESP_LOGI(TAG, "reset_reason=%d(%s) last_reboot_reason=\"%s\" pending_crashes=%d",
             s_reset_reason, crash_diag_reset_reason_str(s_reset_reason),
             "", (int)crash_diag_pending_count());
    return ret;
}

int crash_diag_pending_count(void)
{
    uint8_t count = 0;
    (void)nvs_read_count(&count);
    return (int)count;
}

esp_err_t crash_diag_report_pending(void)
{
    uint8_t count = 0;
    (void)nvs_read_count(&count);

    frame_encoder_t enc;
    uint8_t buf[384];
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DIAG_REPORT);

    if (count == 0) {
        /* BOOT 报告：不占 NVS，record_id=0 */
        (void)frame_encode_varint(&enc, 1, 0);
        (void)frame_encode_varint(&enc, 2, CRASH_DIAG_TYPE_BOOT);
        (void)frame_encode_varint(&enc, 3, (uint64_t)(uint32_t)s_reset_reason);
        (void)frame_encode_varint(&enc, 4, (uint64_t)app_state_get_uptime_sec());
        (void)frame_encode_string(&enc, 5, crash_diag_reset_reason_str(s_reset_reason));
        (void)frame_encode_varint(&enc, 6, 0);
        (void)frame_encode_string(&enc, 13, get_firmware_version());
        (void)frame_encode_string(&enc, 14, "");
        msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
        ESP_LOGI(TAG, "reported BOOT diag: reset_reason=%d(%s)",
                 s_reset_reason, crash_diag_reset_reason_str(s_reset_reason));
        return ESP_OK;
    }

    crash_record_t rec;
    if (nvs_read_record(0, &rec) != ESP_OK || rec.magic != CRASH_REC_MAGIC) {
        ESP_LOGW(TAG, "pending count=%u but record 0 unreadable", (unsigned)count);
        return ESP_ERR_INVALID_STATE;
    }
    /* CRC 校验：宁可不报，也不上报一条被截断/损坏的记录去误导排查 */
    uint32_t expect = crc32_span((const uint8_t *)&rec, offsetof(crash_record_t, crc));
    if (expect != rec.crc) {
        ESP_LOGE(TAG, "crash record %08X CRC mismatch (stored %08X computed %08X) — erasing",
                 (unsigned)rec.record_id, (unsigned)rec.crc, (unsigned)expect);
        (void)nvs_drop_oldest();
        return ESP_ERR_INVALID_CRC;
    }

    (void)frame_encode_varint(&enc, 1, rec.record_id);
    (void)frame_encode_varint(&enc, 2, CRASH_DIAG_TYPE_CRASH);
    (void)frame_encode_varint(&enc, 3, rec.reset_reason);
    (void)frame_encode_varint(&enc, 4, rec.uptime_sec);
    (void)frame_encode_string(&enc, 5, crash_diag_reset_reason_str((int)rec.reset_reason));
    (void)frame_encode_varint(&enc, 6, rec.crc);
    (void)frame_encode_varint(&enc, 7, rec.core);
    (void)frame_encode_varint(&enc, 8, rec.exception);
    (void)frame_encode_varint(&enc, 9, rec.pc);
    (void)frame_encode_varint(&enc, 10, rec.exccause);
    (void)frame_encode_bytes(&enc, 11, (const uint8_t *)rec.task_name,
                             strnlen(rec.task_name, sizeof(rec.task_name)));
    (void)frame_encode_bytes(&enc, 12, (const uint8_t *)rec.stack,
                             rec.stack_words * sizeof(uint32_t));
    (void)frame_encode_string(&enc, 13, rec.fw_version);
    (void)frame_encode_string(&enc, 14, rec.reboot_reason);

    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
    ESP_LOGW(TAG, "reported CRASH diag: id=%08X pc=%08X exc=%d core=%d reason=%d fw=%s reboot=%s",
             (unsigned)rec.record_id, (unsigned)rec.pc, (int)rec.exception,
             (int)rec.core, (int)rec.reset_reason, rec.fw_version, rec.reboot_reason);
    return ESP_OK;
}

void crash_diag_on_ack(uint32_t record_id, bool accepted)
{
    if (record_id == 0) {
        return; /* BOOT 报告不占 NVS */
    }
    crash_record_t rec;
    if (nvs_read_record(0, &rec) != ESP_OK) {
        ESP_LOGW(TAG, "ack id=%08X but no stored record", (unsigned)record_id);
        return;
    }
    if (rec.record_id != record_id) {
        /* 服务端确认的不是最旧那条：可能是重复 ACK。忽略，不误删。 */
        ESP_LOGW(TAG, "ack id=%08X != oldest %08X — ignoring",
                 (unsigned)record_id, (unsigned)rec.record_id);
        return;
    }
    if (!accepted) {
        /* 服务端明确说没存下：保留记录，等下次重试。 */
        ESP_LOGW(TAG, "record %08X rejected by server (accepted=false) — keeping for retry",
                 (unsigned)record_id);
        return;
    }
    if (nvs_drop_oldest() == ESP_OK) {
        ESP_LOGI(TAG, "crash record %08X acknowledged and released, %d remaining",
                 (unsigned)record_id, crash_diag_pending_count());
    } else {
        ESP_LOGE(TAG, "failed to release crash record %08X", (unsigned)record_id);
    }
}
