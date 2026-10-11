/*
 * firmware_manifest_crosslang.c -- ConfigManifest(0x04) 跨语言对锚的**固件侧解码器**。
 *
 * 为什么需要它（S0 的存在理由）
 * ---------------------------
 * 后端 Go 的 encodeConfigManifest() 与固件 C 的 parse_manifest() 是**两套独立实现**。
 * 两端各自的单测都绿，但**没有任何东西证明它们互相能懂** ——
 * 这正是 S0 向量要拦的"两端同时自洽却互相不通"。
 * 0x04 恰好是：最复杂载荷、配置事务这条"项目历史上最危险的路径"、
 * 3.0 配置同步的关键路径。
 *
 * 本程序做**一件事**：把一份 Go 编码出来的 0x04 载荷，喂给**生产解码器**
 * config_mgr_stage_manifest()，然后把解出来的字段逐条打到 stdout，
 * 由 Go 侧逐字段比对。**不重编码、不修数据、不做任何"测试专用旁路"** ——
 * 走的就是真机构建里那条路（config_mgr.c 原样编入，见 CMakeLists 的
 * firmware_manifest_crosslang target）。
 *
 * 用法:  firmware_manifest_crosslang <payload-hex-file>
 * 退出码: 0 = 解码成功且已打印字段; 1 = 解码失败; 2 = 用法/IO 错误
 *
 * 输出约定（Go 侧按此解析，改这里必须同步改 manifest_crosslang_test.go）:
 *   MANIFEST_ID=<str>
 *   SYNC_ID=<str>
 *   TEMPLATE_COUNT=<n>
 *   T<i>.ID=<n>
 *   T<i>.WRITE_DATA=<hex>          （空 -> "-"）
 *   T<i>.READ_LENGTH=<n>
 *   T<i>.DELAY_MS=<n>
 *   CHANNEL_COUNT=<n>
 *   C<i>.ID=<n>
 *   C<i>.HARDWARE_ID=<n>
 *   C<i>.INTERVAL_MS=<n>
 *   C<i>.ENABLED=<0|1>
 *   C<i>.BUS_TYPE=<n>
 *   C<i>.BUS_CONFIG=<hex>          （空 -> "-"）
 *   C<i>.DMA_ENABLED=<0|1>
 *   LOG_STREAM_ENABLED=<0|1>
 *   LOG_STREAM_LEVEL=<n>
 */

#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "config_mgr.h"
#include "frame_codec.h"
#include "nvs_flash.h"
#include "freertos/semphr.h"

/* ---- 宿主桩：与 host_tests/config_manifest_pwm_tests.c 同一套（不新增语义） ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }
const char *esp_err_to_name(esp_err_t err) { (void)err; return "host"; }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{ (void)semaphore; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t semaphore) { (void)semaphore; return 1; }
esp_err_t nvs_open(const char *name, int mode, nvs_handle_t *handle)
{ (void)name; (void)mode; (void)handle; return ESP_FAIL; }
esp_err_t nvs_get_u64(nvs_handle_t h, const char *k, uint64_t *v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_get_str(nvs_handle_t h, const char *k, char *v, size_t *l)
{ (void)h; (void)k; (void)v; (void)l; return ESP_FAIL; }
esp_err_t nvs_set_u64(nvs_handle_t h, const char *k, uint64_t v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_set_str(nvs_handle_t h, const char *k, const char *v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_erase_key(nvs_handle_t h, const char *k) { (void)h; (void)k; return ESP_FAIL; }
esp_err_t nvs_commit(nvs_handle_t h) { (void)h; return ESP_FAIL; }
void nvs_close(nvs_handle_t h) { (void)h; }

/* ---- 十六进制读取 ---- */
static int hex_nibble(int c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* 把文件里的十六进制（允许空白/换行）解到 buf；返回字节数，-1 表示错误。 */
static long read_hex_file(const char *path, uint8_t *buf, size_t cap)
{
    FILE *f = fopen(path, "rb");
    if (f == NULL) { fprintf(stderr, "cannot open %s\n", path); return -1; }
    size_t n = 0;
    int hi = -1;
    int ch;
    while ((ch = fgetc(f)) != EOF) {
        if (ch == ' ' || ch == '\n' || ch == '\r' || ch == '\t') continue;
        int v = hex_nibble(ch);
        if (v < 0) { fprintf(stderr, "bad hex char %c\n", ch); fclose(f); return -1; }
        if (hi < 0) { hi = v; }
        else {
            if (n >= cap) { fprintf(stderr, "payload too big for %zu\n", cap); fclose(f); return -1; }
            buf[n++] = (uint8_t)((hi << 4) | v);
            hi = -1;
        }
    }
    fclose(f);
    if (hi >= 0) { fprintf(stderr, "odd number of hex digits\n"); return -1; }
    return (long)n;
}

static void print_hex(const uint8_t *p, size_t n)
{
    if (n == 0) { printf("-"); return; }
    for (size_t i = 0; i < n; i++) printf("%02x", p[i]);
}

int main(int argc, char **argv)
{
    if (argc != 2) {
        fprintf(stderr, "usage: %s <payload-hex-file>\n", argv[0]);
        return 2;
    }

    /* 与真机同一入口。宿主上 NVS 桩恒失败，config_mgr_init 不依赖 NVS。 */
    config_mgr_init();

    static uint8_t payload[8192];
    long len = read_hex_file(argv[1], payload, sizeof(payload));
    if (len <= 0) {
        fprintf(stderr, "read payload failed (len=%ld)\n", len);
        return 2;
    }
    fprintf(stderr, "read %ld bytes, first=%02x\n", len, payload[0]);

    /* ★ 生产解码器。没有旁路、没有测试专用分支。 */
    if (!config_mgr_stage_manifest(payload, (size_t)len)) {
        fprintf(stderr, "config_mgr_stage_manifest FAILED (decode error)\n");
        printf("DECODE=FAIL\n");
        return 1;
    }
    const config_manifest_t *m = config_mgr_get_staged_manifest();
    if (m == NULL) {
        fprintf(stderr, "staged manifest is NULL after successful stage\n");
        printf("DECODE=FAIL\n");
        return 1;
    }
    printf("DECODE=OK\n");

    printf("MANIFEST_ID=%s\n", m->manifest_id);
    printf("SYNC_ID=%s\n", m->sync_id);

    printf("TEMPLATE_COUNT=%u\n", (unsigned)m->template_count);
    for (uint8_t i = 0; i < m->template_count && i < MAX_TEMPLATES; i++) {
        const config_template_t *t = &m->templates[i];
        printf("T%u.ID=%u\n", (unsigned)i, (unsigned)t->id);
        printf("T%u.WRITE_DATA=", (unsigned)i); print_hex(t->write_data, t->write_data_len); printf("\n");
        printf("T%u.READ_LENGTH=%u\n", (unsigned)i, (unsigned)t->read_length);
        printf("T%u.DELAY_MS=%u\n", (unsigned)i, (unsigned)t->delay_ms);
    }

    printf("CHANNEL_COUNT=%u\n", (unsigned)m->channel_count);
    for (uint8_t i = 0; i < m->channel_count && i < MAX_CHANNELS; i++) {
        const config_channel_t *c = &m->channels[i];
        printf("C%u.ID=%u\n", (unsigned)i, (unsigned)c->id);
        printf("C%u.HARDWARE_ID=%u\n", (unsigned)i, (unsigned)c->hardware_id);
        printf("C%u.INTERVAL_MS=%u\n", (unsigned)i, (unsigned)c->interval_ms);
        printf("C%u.ENABLED=%d\n", (unsigned)i, c->enabled ? 1 : 0);
        printf("C%u.BUS_TYPE=%u\n", (unsigned)i, (unsigned)c->bus_type);
        printf("C%u.BUS_CONFIG=", (unsigned)i); print_hex(c->bus_config, c->bus_config_len); printf("\n");
        printf("C%u.DMA_ENABLED=%d\n", (unsigned)i,
               config_channel_get_dma_enabled(c) ? 1 : 0);
    }

    printf("LOG_STREAM_ENABLED=%d\n", m->log_stream_enabled ? 1 : 0);
    printf("LOG_STREAM_LEVEL=%u\n", (unsigned)m->log_stream_level);
    return 0;
}
