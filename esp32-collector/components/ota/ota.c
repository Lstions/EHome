/**
 * @file ota.c
 * @brief OTA Upgrade Implementation
 */

#include "ota.h"
#include "esp_log.h"
#include "esp_ota_ops.h"
#include "esp_http_client.h"
#include "esp_partition.h"
#include "nvs_flash.h"
#include "nvs.h"
#if CONFIG_COLLECTOR_OTA_USE_HTTPS && CONFIG_COLLECTOR_OTA_VERIFY_CERT && CONFIG_COLLECTOR_OTA_CRT_BUNDLE
#include "esp_crt_bundle.h"
#endif
#include <string.h>
#include <stdlib.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_system.h"
#include "esp_heap_caps.h"
#include <ctype.h>

#define MBEDTLS_DECLARE_PRIVATE_IDENTIFIERS
#include "mbedtls/private/sha256.h"

#define TAG "OTA"

/* OTA NVS state machine
 *   0 = none (idle / completed)
 *   1 = downloading
 *   2 = verifying
 */
#define OTA_NVS_NAMESPACE "ota"
#define OTA_NVS_KEY_STATE   "ota_state"
#define OTA_NVS_KEY_VERSION "ota_version"
#define OTA_NVS_KEY_CHECKSUM "ota_checksum"
#define OTA_NVS_KEY_ID "replay_id"
#define OTA_NVS_KEY_URL "replay_url"
#define OTA_NVS_KEY_SIZE "replay_size"
#define OTA_NVS_KEY_SEQ "replay_seq"
#define OTA_NVS_KEY_STATUS "replay_status"
#define OTA_NVS_KEY_PCT "replay_pct"
#define OTA_NVS_KEY_ERROR "replay_error"

/* Stack for the OTA worker task, in bytes.
 *
 * ESP-IDF's xTaskCreate() takes the depth in BYTES, unlike vanilla FreeRTOS
 * which takes words (see freertos/task.h: "specified as the NUMBER OF BYTES.
 * Note that this differs from vanilla FreeRTOS").  So the value below is
 * passed straight through and the unit is not converted.
 *
 * Sizing rationale: the two 4 KB HTTP staging buffers are file-scope `static`
 * (they live in .bss, not on this stack), and the mbedTLS record buffers are
 * heap-allocated via mbedtls_ssl_setup() (ssl.h declares in_buf as a pointer).
 * 8 KB is therefore stack for the call chain only, and matches what ESP-IDF's
 * own advanced_https_ota example uses (xTaskCreate(..., 1024 * 8, ...)).
 * Halving it from 16 KB matters because that value is what a device must have
 * free before it can accept an OTA at all; on 2026-10-01 a 16 KB request
 * failed outright on ESP32-S3 with ~14 KB free. */
/* 4 KB（原 8KB，2026-10-05 实机测量后收紧）。
 *
 * 为什么敢降：本固件走**明文 HTTP**，两个 4KB 的下载暂存缓冲是 file-scope
 * static（在 .bss，不占这个栈），mbedTLS 的记录缓冲也由堆分配。
 * 因此这 8KB 里的"调用链"本身远用不到 8KB —— 8KB 是照抄 IDF
 * advanced_https_ota 示例（那条路径确实需要 TLS 握手栈）。
 *
 * 而 S3 的堆只有约 19KB 空闲，静态栈**常驻 .bss**：8KB 相当于拿走
 * 全部空闲的 42%。实测后果是 OTA 自己都跑不起来 ——
 *     free=9836 largest=3584  (OTA 启动时)
 *     free=2232 largest=1024  (下载中)
 *     E HTTP_CLIENT: Allocation failed (rx=2048 tx=1024, largest=1024)
 * 即"为了让 OTA 能启动而静态占的 8KB"，把 OTA 需要的 HTTP/lwIP
 * 缓冲挤掉了 —— 自己把自己饿死。降到 4KB 后总账才划算。
 *
 * 下面在升级成功路径上打印 uxTaskGetStackHighWaterMark，用实测确认余量；
 * 若余量不足会立刻在实测中暴露，而不是等到现场栈溢出。 */
#define OTA_TASK_STACK_BYTES 4096

/* OTA 任务的**静态**栈与 TCB（2026-10-05）。大小仍是 8KB，
 * 改的是"从哪来"：从堆分配改为静态分配。
 *
 * 为什么必须静态：xTaskCreate() 需要一整块**连续**堆内存（栈 + TCB）。
 * 现场实测（S3，2.8.0）：
 *     I OTA: Creating ota_task with 8192 byte stack: free=18464 largest=7680
 *     E OTA: Failed to create ota_task: need 8192 bytes contiguous
 * 即**总空闲 18KB 够、但没有 8192 的连续块** —— 碎片问题，不是总量问题。
 * 在只有十几 KB 堆的设备上碎片是常态：配置事务、UART 驱动、WiFi、
 * MQTT 重连都在反复分配/释放不同尺寸的块。只要 OTA 依赖"堆里恰好有
 * 这么一整块"，OTA 就变成**看运气** —— 而 OTA 是设备远程不可达时唯一的
 * 救命通道，不能看运气。
 *
 * 静态分配把这块内存放进 .bss，不参与堆的分配与碎片，
 * 于是"OTA 能否启动"与堆状态彻底解耦。代价是 8KB 常驻 .bss，
 * 换来"OTA 永远可用" —— 这个交换是值得的。
 *
 * 与本仓对 log_tx_task（2026-10-04）和 scheduler（2026-10-05）的修复同一手法，
 * 理由相同：**关键任务的成功不该取决于堆碎片**。
 *
 * 注意：StackType_t 在 Xtensa 上是 4 字节，而 xTaskCreateStatic 的栈深度
 * 以**字**为单位（xTaskCreate 用字节），故显式做除法并加编译期断言。
 * 重试升级时可能重复创建，用 s_ota_task 句柄做二次防护（s_upgrading
 * 已提供主要串行化）。 */
#define OTA_TASK_STACK_WORDS (OTA_TASK_STACK_BYTES / sizeof(StackType_t))
_Static_assert(OTA_TASK_STACK_BYTES % sizeof(StackType_t) == 0,
               "OTA_TASK_STACK_BYTES must be a whole number of StackType_t words");
static StackType_t  s_ota_stack[OTA_TASK_STACK_WORDS];
static StaticTask_t s_ota_tcb;
static TaskHandle_t s_ota_task;

typedef enum {
    OTA_STATE_NONE       = 0,
    OTA_STATE_DOWNLOADING = 1,
    OTA_STATE_VERIFYING  = 2,
} ota_nvs_state_t;

static bool s_upgrading = false;
static char s_last_ota_id[64] = {0};
static ota_cmd_t s_last_ota_cmd;
static bool s_have_last_ota_cmd = false;
static uint8_t s_last_progress_status;
static uint8_t s_last_progress_pct;
static char s_last_progress_error[96];
static portMUX_TYPE s_ota_cache_lock = portMUX_INITIALIZER_UNLOCKED;
static char s_download_ota_id[64] = {0};  /* set before download for progress reporting */

/* --- Progress callback injection (decouples from msg_handler) --- */
static ota_progress_cb_t s_progress_cb = NULL;
static esp_err_t ota_nvs_persist_replay(const ota_cmd_t *cmd);

void ota_set_progress_callback(ota_progress_cb_t cb)
{
    s_progress_cb = cb;
}

static void ota_report_progress(const char *ota_id, uint8_t status,
                                 uint8_t progress_pct, const char *error_msg)
{
    bool matches = false;
    ota_cmd_t snapshot = {0};
    taskENTER_CRITICAL(&s_ota_cache_lock);
    if (ota_id && strcmp(s_last_ota_id, ota_id) == 0) {
		matches = true;
        s_last_progress_status = status;
        s_last_progress_pct = progress_pct;
        snprintf(s_last_progress_error, sizeof(s_last_progress_error), "%s", error_msg ? error_msg : "");
		snapshot = s_last_ota_cmd;
    }
    taskEXIT_CRITICAL(&s_ota_cache_lock);
	if (matches && snapshot.ota_id[0]) (void)ota_nvs_persist_replay(&snapshot);
    if (s_progress_cb) {
        s_progress_cb(ota_id, status, progress_pct, error_msg);
    }
}

void ota_replay_last_progress(const char *ota_id)
{
    uint8_t status = 0, pct = 0;
    char error[sizeof(s_last_progress_error)] = {0};
    bool matches;
    taskENTER_CRITICAL(&s_ota_cache_lock);
    matches = ota_id && strcmp(s_last_ota_id, ota_id) == 0;
    if (matches) { status = s_last_progress_status; pct = s_last_progress_pct; memcpy(error, s_last_progress_error, sizeof(error)); }
    taskEXIT_CRITICAL(&s_ota_cache_lock);
    if (matches && s_progress_cb) s_progress_cb(ota_id, status, pct, error[0] ? error : NULL);
}

/* --- NVS helpers --- */

static esp_err_t ota_nvs_set_state(ota_nvs_state_t state)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(OTA_NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "NVS open failed: %s", esp_err_to_name(err));
        return err;
    }
    err = nvs_set_u8(handle, OTA_NVS_KEY_STATE, (uint8_t)state);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "NVS set state %d failed: %s", state, esp_err_to_name(err));
    } else {
        ESP_LOGI(TAG, "NVS ota_state <- %d", state);
    }
    return err;
}

static esp_err_t ota_nvs_set_meta(const char *version, const char *checksum)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(OTA_NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) return err;
    if (version && version[0]) {
        nvs_set_str(handle, OTA_NVS_KEY_VERSION, version);
    }
    if (checksum && checksum[0]) {
        nvs_set_str(handle, OTA_NVS_KEY_CHECKSUM, checksum);
    }
    err = nvs_commit(handle);
    nvs_close(handle);
    return err;
}

static esp_err_t ota_nvs_persist_replay(const ota_cmd_t *cmd)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(OTA_NVS_NAMESPACE, NVS_READWRITE, &handle);
    if (err != ESP_OK) return err;
    err = nvs_set_str(handle, OTA_NVS_KEY_ID, cmd->ota_id);
    if (err == ESP_OK) err = nvs_set_str(handle, OTA_NVS_KEY_URL, cmd->firmware_url);
    if (err == ESP_OK) err = nvs_set_str(handle, OTA_NVS_KEY_CHECKSUM, cmd->checksum);
    if (err == ESP_OK) err = nvs_set_str(handle, OTA_NVS_KEY_VERSION, cmd->version);
    if (err == ESP_OK) err = nvs_set_u64(handle, OTA_NVS_KEY_SIZE, cmd->size_bytes);
    if (err == ESP_OK) err = nvs_set_u32(handle, OTA_NVS_KEY_SEQ, cmd->sequence);
    if (err == ESP_OK) err = nvs_set_u8(handle, OTA_NVS_KEY_STATUS, s_last_progress_status);
    if (err == ESP_OK) err = nvs_set_u8(handle, OTA_NVS_KEY_PCT, s_last_progress_pct);
    if (err == ESP_OK) err = nvs_set_str(handle, OTA_NVS_KEY_ERROR, s_last_progress_error);
    if (err == ESP_OK) err = nvs_commit(handle);
    nvs_close(handle);
    return err;
}

static void ota_nvs_load_replay(void)
{
    nvs_handle_t handle;
    if (nvs_open(OTA_NVS_NAMESPACE, NVS_READONLY, &handle) != ESP_OK) return;
    ota_cmd_t loaded = {0};
    size_t id_len = sizeof(loaded.ota_id), url_len = sizeof(loaded.firmware_url);
    size_t checksum_len = sizeof(loaded.checksum), version_len = sizeof(loaded.version);
    size_t error_len = sizeof(s_last_progress_error);
    esp_err_t err = nvs_get_str(handle, OTA_NVS_KEY_ID, loaded.ota_id, &id_len);
    if (err == ESP_OK) err = nvs_get_str(handle, OTA_NVS_KEY_URL, loaded.firmware_url, &url_len);
    if (err == ESP_OK) err = nvs_get_str(handle, OTA_NVS_KEY_CHECKSUM, loaded.checksum, &checksum_len);
    if (err == ESP_OK) err = nvs_get_str(handle, OTA_NVS_KEY_VERSION, loaded.version, &version_len);
    if (err == ESP_OK) err = nvs_get_u64(handle, OTA_NVS_KEY_SIZE, &loaded.size_bytes);
    if (err == ESP_OK) err = nvs_get_u32(handle, OTA_NVS_KEY_SEQ, &loaded.sequence);
    if (err == ESP_OK) err = nvs_get_u8(handle, OTA_NVS_KEY_STATUS, &s_last_progress_status);
    if (err == ESP_OK) err = nvs_get_u8(handle, OTA_NVS_KEY_PCT, &s_last_progress_pct);
    if (err == ESP_OK) err = nvs_get_str(handle, OTA_NVS_KEY_ERROR, s_last_progress_error, &error_len);
    nvs_close(handle);
    if (err == ESP_OK && loaded.ota_id[0] && loaded.sequence) {
        s_last_ota_cmd = loaded; s_have_last_ota_cmd = true;
        snprintf(s_last_ota_id, sizeof(s_last_ota_id), "%s", loaded.ota_id);
    }
}

/* --- Public API --- */

void ota_init(void)
{
    s_upgrading = false;
    ota_nvs_load_replay();
    ESP_LOGI(TAG, "OTA initialized");
}

bool ota_is_duplicate(const char *ota_id)
{
    if (s_last_ota_id[0] && strcmp(s_last_ota_id, ota_id) == 0) {
        return true;
    }
    strncpy(s_last_ota_id, ota_id, sizeof(s_last_ota_id) - 1);
    s_last_ota_id[sizeof(s_last_ota_id) - 1] = '\0';
    return false;
}

ota_cmd_class_t ota_classify_cmd(const ota_cmd_t *cmd)
{
    if (!cmd) return OTA_CMD_COLLISION;
    if (s_have_last_ota_cmd && strcmp(s_last_ota_cmd.ota_id, cmd->ota_id) == 0) {
        bool exact = strcmp(s_last_ota_cmd.firmware_url, cmd->firmware_url) == 0 &&
                     strcmp(s_last_ota_cmd.checksum, cmd->checksum) == 0 &&
                     strcmp(s_last_ota_cmd.version, cmd->version) == 0 &&
                     s_last_ota_cmd.size_bytes == cmd->size_bytes &&
                     s_last_ota_cmd.sequence == cmd->sequence;
        return exact ? OTA_CMD_EXACT_REPLAY : OTA_CMD_COLLISION;
    }
    if (s_upgrading) return OTA_CMD_BUSY;
    s_last_progress_status = 0;
    s_last_progress_pct = 0;
    s_last_progress_error[0] = '\0';
    if (ota_nvs_persist_replay(cmd) != ESP_OK) return OTA_CMD_BUSY;
    s_last_ota_cmd = *cmd;
    s_have_last_ota_cmd = true;
    strncpy(s_last_ota_id, cmd->ota_id, sizeof(s_last_ota_id) - 1);
    s_last_ota_id[sizeof(s_last_ota_id) - 1] = '\0';
    return OTA_CMD_NEW;
}

void ota_forget_duplicate(const char *ota_id)
{
    if (ota_id && strcmp(s_last_ota_id, ota_id) == 0) {
        s_last_ota_id[0] = '\0';
        memset(&s_last_ota_cmd, 0, sizeof(s_last_ota_cmd));
        s_have_last_ota_cmd = false;
		nvs_handle_t handle;
		if (nvs_open(OTA_NVS_NAMESPACE, NVS_READWRITE, &handle) == ESP_OK) {
			nvs_erase_key(handle, OTA_NVS_KEY_ID);
			nvs_erase_key(handle, OTA_NVS_KEY_URL);
			nvs_erase_key(handle, OTA_NVS_KEY_SIZE);
			nvs_erase_key(handle, OTA_NVS_KEY_SEQ);
			nvs_erase_key(handle, OTA_NVS_KEY_STATUS);
			nvs_erase_key(handle, OTA_NVS_KEY_PCT);
			nvs_erase_key(handle, OTA_NVS_KEY_ERROR);
			nvs_commit(handle);
			nvs_close(handle);
		}
    }
}

uint8_t ota_get_nvs_state(void)
{
    nvs_handle_t handle;
    uint8_t state = OTA_STATE_NONE;
    if (nvs_open(OTA_NVS_NAMESPACE, NVS_READONLY, &handle) == ESP_OK) {
        nvs_get_u8(handle, OTA_NVS_KEY_STATE, &state);
        nvs_close(handle);
    }
    return state;
}

esp_err_t ota_confirm_valid(void)
{
    const esp_partition_t *running = esp_ota_get_running_partition();
    esp_ota_img_states_t state;
    esp_err_t state_err = running ? esp_ota_get_state_partition(running, &state) : ESP_ERR_INVALID_STATE;
    if (state_err == ESP_OK && state == ESP_OTA_IMG_VALID) {
        /* The bootloader transition already succeeded on an earlier attempt;
         * only durable recovery-state cleanup still needs retrying. */
        return ota_nvs_set_state(OTA_STATE_NONE);
    }
    esp_err_t err = esp_ota_mark_app_valid_cancel_rollback();
    if (err == ESP_OK) {
        ESP_LOGI(TAG, "App marked valid, rollback cancelled");
		err = ota_nvs_set_state(OTA_STATE_NONE);
		if (err != ESP_OK) ESP_LOGE(TAG, "Failed to clear OTA recovery state: %s", esp_err_to_name(err));
    } else {
        ESP_LOGW(TAG, "mark_app_valid failed: %s", esp_err_to_name(err));
    }
    return err;
}

void ota_mark_invalid_rollback(ota_rollback_trigger_t trigger)
{
    const char *reason = (trigger == OTA_ROLLBACK_ON_BOOT_FAIL) 
        ? "boot validation failed" 
        : "manual rollback";
    
    ESP_LOGW(TAG, "Marking app invalid and triggering rollback + reboot (reason: %s)", reason);
    
    /* Clear NVS state before rollback */
    ota_nvs_set_state(OTA_STATE_NONE);
    
    esp_err_t err = esp_ota_mark_app_invalid_rollback_and_reboot();
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "mark_app_invalid_rollback_and_reboot failed: %s", esp_err_to_name(err));
    }
}

/**
 * @brief Convert binary SHA256 hash to hex string
 */
static void sha256_to_hex(const uint8_t *hash, char *hex_out)
{
    static const char hex_chars[] = "0123456789abcdef";
    for (int i = 0; i < 32; i++) {
        hex_out[i * 2]     = hex_chars[(hash[i] >> 4) & 0x0F];
        hex_out[i * 2 + 1] = hex_chars[hash[i] & 0x0F];
    }
    hex_out[64] = '\0';
}

/**
 * @brief Validate firmware SHA256 checksum against expected value
 * @param expected_checksum Hex-encoded SHA256 string from server (64 chars)
 * @param computed_hash    Binary SHA256 hash computed from firmware
 * @return true if checksums match, false otherwise
 */
static bool validate_firmware(const char *expected_checksum, const uint8_t *computed_hash)
{
    if (expected_checksum == NULL || expected_checksum[0] == '\0') {
        ESP_LOGW(TAG, "No checksum provided, skipping validation");
        return true;  /* Allow OTA without checksum for backward compatibility */
    }

    char computed_hex[65];
    sha256_to_hex(computed_hash, computed_hex);

    ESP_LOGI(TAG, "Expected checksum: %s", expected_checksum);
    ESP_LOGI(TAG, "Computed checksum: %s", computed_hex);

    if (strcasecmp(expected_checksum, computed_hex) != 0) {
        ESP_LOGE(TAG, "SHA256 checksum mismatch! Firmware rejected.");
        return false;
    }

    ESP_LOGI(TAG, "SHA256 checksum verified OK");
    return true;
}

/* Forward declarations for refactored OTA functions */
static esp_err_t ota_download_http(const char *url, uint32_t *out_total_bytes);
static esp_err_t ota_verify(const char *expected_checksum,
                            uint32_t total_bytes, uint64_t expected_size);

/**
 * @brief Build the esp_http_client_config_t based on URL scheme and Kconfig settings.
 *
 * Unified for both HTTP and HTTPS:
 * - HTTPS + crt_bundle: uses Mozilla CA bundle (public Internet)
 * - HTTPS + custom cert: embeds a CA PEM for private/self-signed servers
 * - HTTPS + no verify: WARN log (not for production)
 * - HTTP: allowed only when CONFIG_COLLECTOR_OTA_ALLOW_HTTP is set
 *
 * @param cfg      Output client config (caller owns the struct)
 * @param url      Firmware download URL
 * @param is_https true if URL scheme is https://, false for http://
 */
static esp_err_t build_ota_http_config(esp_http_client_config_t *cfg,
                                       const char *url, bool is_https)
{
    memset(cfg, 0, sizeof(*cfg));
    cfg->url = url;
    cfg->timeout_ms = 10000;

    if (!is_https) {
        /* Plain HTTP path */
#if CONFIG_COLLECTOR_OTA_ALLOW_HTTP
        ESP_LOGW(TAG, "OTA: using plain HTTP (development mode - INSECURE)");
        return ESP_OK;
#else
        ESP_LOGE(TAG, "HTTP not allowed (CONFIG_COLLECTOR_OTA_ALLOW_HTTP=n)");
        return ESP_ERR_NOT_SUPPORTED;
#endif
    }

    /* HTTPS path: configure certificate verification */
#if CONFIG_COLLECTOR_OTA_USE_HTTPS
    #if CONFIG_COLLECTOR_OTA_VERIFY_CERT
        #if CONFIG_COLLECTOR_OTA_CRT_BUNDLE
            cfg->crt_bundle_attach = esp_crt_bundle_attach;
            ESP_LOGI(TAG, "OTA HTTPS: verify with Mozilla CA bundle");
        #elif CONFIG_COLLECTOR_OTA_CUSTOM_CERT
            extern const uint8_t server_cert_pem_start[] asm("_binary_ca_pem_start");
            extern const uint8_t server_cert_pem_end[]   asm("_binary_ca_pem_end");
            cfg->cert_pem = (const char *)server_cert_pem_start;
            ESP_LOGI(TAG, "OTA HTTPS: verify with custom CA cert (%d bytes)",
                     (int)(server_cert_pem_end - server_cert_pem_start));
        #endif
        /*
         * Set expected CN if configured (non-empty string).
         * Kconfig default is "", so we check at runtime.
         */
        if (CONFIG_COLLECTOR_OTA_EXPECTED_CN[0] != '\0') {
            cfg->common_name = CONFIG_COLLECTOR_OTA_EXPECTED_CN;
            ESP_LOGI(TAG, "OTA HTTPS: expecting CN=%s", CONFIG_COLLECTOR_OTA_EXPECTED_CN);
        }
    #else
        ESP_LOGW(TAG, "OTA HTTPS: certificate verification DISABLED - not for production");
    #endif
#else
    ESP_LOGW(TAG, "OTA: HTTPS URL but CONFIG_COLLECTOR_OTA_USE_HTTPS not set");
#endif
    return ESP_OK;
}

/**
 * @brief Download firmware image via HTTP or HTTPS.
 * @return ESP_OK on success, ESP_FAIL/esp_err_t on failure.
 *         On success, *out_total_bytes is set to the number of bytes written.
 */
static esp_err_t ota_download(const char *url, uint64_t expected_size,
                              uint32_t *out_total_bytes)
{
    esp_err_t err;

    /* Partition safety check: ensure update partition is not the running partition. */
    const esp_partition_t *running_part = esp_ota_get_running_partition();
    const esp_partition_t *update_part_check = esp_ota_get_next_update_partition(NULL);
    if (update_part_check == NULL) {
        ESP_LOGE(TAG, "No OTA update partition found");
        return ESP_FAIL;
    }
    if (update_part_check->address == running_part->address) {
        ESP_LOGE(TAG, "OTA target partition '%s' (0x%" PRIx32 ") is the running partition! Aborting.",
                 update_part_check->label, update_part_check->address);
        return ESP_FAIL;
    }
    ESP_LOGI(TAG, "OTA partition check OK: running=0x%" PRIx32 " update=0x%" PRIx32,
             running_part->address, update_part_check->address);

    bool is_https = (strncmp(url, "https://", 8) == 0);
    bool is_http  = (strncmp(url, "http://", 7) == 0);

    if (!is_http && !is_https) {
        ESP_LOGE(TAG, "Invalid URL scheme (must be http:// or https://)");
        return ESP_ERR_INVALID_ARG;
    }

    if (is_http && !is_https) {
#if !CONFIG_COLLECTOR_OTA_ALLOW_HTTP
        ESP_LOGE(TAG, "HTTP not allowed (CONFIG_COLLECTOR_OTA_ALLOW_HTTP=n)");
        return ESP_ERR_NOT_SUPPORTED;
#else
        ESP_LOGW(TAG, "Using plain HTTP (development mode)");
        err = ota_download_http(url, out_total_bytes);
#endif
    } else {
        /* HTTPS path: use esp_http_client with certificate verification */
        ESP_LOGI(TAG, "Using HTTPS with certificate verification");
        esp_http_client_config_t cli_cfg;
        build_ota_http_config(&cli_cfg, url, true);
        cli_cfg.timeout_ms = 30000;

        esp_http_client_handle_t client = esp_http_client_init(&cli_cfg);
        if (client == NULL) {
            ESP_LOGE(TAG, "HTTPS client init FAILED");
            return ESP_FAIL;
        }

        err = esp_http_client_open(client, 0);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "HTTPS open FAILED: %s", esp_err_to_name(err));
            esp_http_client_cleanup(client);
            return err;
        }

        int cl = esp_http_client_fetch_headers(client);
        int sc = esp_http_client_get_status_code(client);
        ESP_LOGI(TAG, "HTTPS %d, content-length=%d", sc, cl);

        if (sc != 200) {
            ESP_LOGE(TAG, "HTTPS server returned error %d", sc);
            esp_http_client_close(client);
            esp_http_client_cleanup(client);
            return ESP_FAIL;
        }

        const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
        if (update_partition == NULL) {
            ESP_LOGE(TAG, "No OTA partition found");
            esp_http_client_close(client);
            esp_http_client_cleanup(client);
            return ESP_FAIL;
        }

        esp_ota_handle_t ota_handle;
        err = esp_ota_begin(update_partition, OTA_SIZE_UNKNOWN, &ota_handle);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "esp_ota_begin failed: %s", esp_err_to_name(err));
            esp_http_client_close(client);
            esp_http_client_cleanup(client);
            return err;
        }

        int total = 0, last_pct = -1;
        uint8_t *rx_buf = malloc(4096);
        if (!rx_buf) {
            ESP_LOGE(TAG, "Failed to allocate receive buffer");
            esp_ota_end(ota_handle);
            esp_http_client_close(client);
            esp_http_client_cleanup(client);
            return ESP_ERR_NO_MEM;
        }

        int n;
        while ((n = esp_http_client_read(client, (char *)rx_buf, 4096)) > 0) {
            err = esp_ota_write(ota_handle, rx_buf, n);
            if (err != ESP_OK) {
                ESP_LOGE(TAG, "esp_ota_write failed at %d bytes: %s", total, esp_err_to_name(err));
                free(rx_buf);
                esp_ota_end(ota_handle);
                esp_http_client_close(client);
                esp_http_client_cleanup(client);
                return err;
            }
            total += n;
            int pct = cl > 0 ? (total * 100 / cl) : 0;
            if (pct != last_pct && pct % 10 == 0) {
                ESP_LOGI(TAG, "Downloaded %d%%", pct);
                ota_report_progress(s_download_ota_id, 0, (uint8_t)pct, NULL);
                last_pct = pct;
            }
        }

        free(rx_buf);
        esp_http_client_close(client);
        esp_http_client_cleanup(client);

        err = esp_ota_end(ota_handle);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "esp_ota_end failed: %s", esp_err_to_name(err));
            return err;
        }

        *out_total_bytes = (uint32_t)total;
        ESP_LOGI(TAG, "HTTPS OTA written %d bytes", total);
    }

    if (err != ESP_OK) {
        ESP_LOGE(TAG, "OTA download failed: %s", esp_err_to_name(err));
    }
    return err;
}

/**
 * @brief HTTP download path (development mode).
 */
static esp_err_t ota_download_http(const char *url, uint32_t *out_total_bytes)
{
    esp_http_client_config_t cli_cfg = {0};
    cli_cfg.url = url;
    cli_cfg.timeout_ms = 30000;
    /* HTTP 客户端内部缓冲：**1024/512（原 2048/1024，更早是 8192）。**
     *
     * 这两个值由 esp_http_client_init() 各 malloc 成一块**连续**内存，
     * 因此能否分配成功取决于"最大连续块"而不是总空闲量：
     *   - 8192 时曾在 S3 上出现"33880 字节空闲但没有 8KB 连续块"而失败；
     *   - 2048 时实测 OTA 期间 largest free block 只有 1024，
     *     直接报 "HTTP_CLIENT: Allocation failed"。
     *
     * 1024/512 配合下面的 4KB 静态读缓冲足够：HTTP 头很小，正文由 read()
     * 循环反复取，缓冲大小只影响系统调用次数、不影响正确性。
     * 降这两个值是为了让 **OTA 在 S3 的碎片化堆上仍有可用的连续块**。 */
    cli_cfg.buffer_size = 1024;
    cli_cfg.buffer_size_tx = 512;

    esp_http_client_handle_t client = esp_http_client_init(&cli_cfg);
    if (client == NULL) {
        /* esp_http_client_init() returns NULL when its internal TX/RX buffers
         * cannot be allocated. Report the sizes and the largest free block so a
         * heap-fragmentation failure is distinguishable from a bad URL -- the
         * raw "Allocation failed" from IDF does not say which. */
        ESP_LOGE(TAG, "HTTP client init FAILED (rx=%u tx=%u, free=%u, "
                      "largest free block=%u)",
                 (unsigned)cli_cfg.buffer_size, (unsigned)cli_cfg.buffer_size_tx,
                 (unsigned)esp_get_free_heap_size(),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT));
        return ESP_FAIL;
    }

    esp_err_t err = esp_http_client_open(client, 0);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "HTTP open FAILED: %s", esp_err_to_name(err));
        esp_http_client_cleanup(client);
        return err;
    }

    int cl = esp_http_client_fetch_headers(client);
    int sc = esp_http_client_get_status_code(client);
    ESP_LOGI(TAG, "HTTP %d, content-length=%d", sc, cl);

    if (sc != 200) {
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
        return ESP_FAIL;
    }

    const esp_partition_t *part = esp_ota_get_next_update_partition(NULL);
    if (part == NULL) {
        ESP_LOGE(TAG, "No OTA partition found");
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
        return ESP_FAIL;
    }

    esp_ota_handle_t handle = 0;
    err = esp_ota_begin(part, OTA_WITH_SEQUENTIAL_WRITES, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "esp_ota_begin FAILED: %s", esp_err_to_name(err));
        esp_http_client_close(client);
        esp_http_client_cleanup(client);
        return err;
    }

    int total = 0, last_pct = -1;
    static uint8_t rx[4096];
    int n;
    while ((n = esp_http_client_read(client, (char *)rx, sizeof(rx))) > 0) {
        err = esp_ota_write(handle, rx, n);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "ota_write FAILED at %d bytes", total);
            esp_ota_end(handle);
            esp_http_client_close(client);
            esp_http_client_cleanup(client);
            return err;
        }
        total += n;
        int pct = cl > 0 ? (total * 100 / cl) : 0;
        if (pct != last_pct && pct % 10 == 0) {
            ESP_LOGI(TAG, "Downloaded %d%%", pct);
            ota_report_progress(s_download_ota_id, 0, (uint8_t)pct, NULL);
            last_pct = pct;
        }
    }

    esp_http_client_close(client);
    esp_http_client_cleanup(client);

    if (total == 0) {
        ESP_LOGE(TAG, "Zero bytes downloaded");
        esp_ota_end(handle);
        return ESP_FAIL;
    }

    err = esp_ota_end(handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "esp_ota_end FAILED: %s", esp_err_to_name(err));
        return err;
    }

    ESP_LOGI(TAG, "HTTP OTA written %d bytes", total);
    *out_total_bytes = (uint32_t)total;
    return ESP_OK;
}

/**
 * @brief Verify firmware SHA256 checksum against expected value.
 * @param expected_checksum Hex-encoded SHA256 string from server.
 * @param total_bytes       Number of bytes written to the update partition.
 * @param expected_size     Expected firmware size from server command.
 * @return ESP_OK if checksum matches or no checksum provided.
 */
static esp_err_t ota_verify(const char *expected_checksum,
                            uint32_t total_bytes, uint64_t expected_size)
{
    ESP_LOGI(TAG, "OTA image written, validating checksum...");

    const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
    if (update_partition == NULL) {
        ESP_LOGE(TAG, "No update partition found for verification");
        return ESP_FAIL;
    }

    ESP_LOGI(TAG, "Computing SHA256 of '%s' (offset 0x%" PRIx32 ", %llu bytes)",
             update_partition->label, update_partition->address,
             (unsigned long long)total_bytes);

    uint8_t sha256_result[32] = {0};
    mbedtls_sha256_context sha256_ctx;
    mbedtls_sha256_init(&sha256_ctx);
    mbedtls_sha256_starts(&sha256_ctx, 0);

    const int CHUNK = 4096;
    static uint8_t buf[4096];
    uint64_t remaining = total_bytes > 0 ? (uint64_t)total_bytes : expected_size;
    uint32_t offset = 0;
    int chunk_count = 0;
    esp_err_t err;

    while (remaining > 0) {
        size_t tr = (remaining > CHUNK) ? CHUNK : (size_t)remaining;
        err = esp_partition_read(update_partition, offset, buf, tr);
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "part read @%lu: %s", offset, esp_err_to_name(err));
            mbedtls_sha256_free(&sha256_ctx);
            return err;
        }
        mbedtls_sha256_update(&sha256_ctx, buf, tr);
        offset += tr; remaining -= tr;
        chunk_count++;
        if (chunk_count % 16 == 0) {
            ESP_LOGI(TAG, "SHA256 progress: %lu/%llu bytes (%d%%)",
                     (unsigned long)offset, (unsigned long long)total_bytes,
                     total_bytes > 0 ? (int)(offset * 100 / total_bytes) : 0);
        }
        vTaskDelay(pdMS_TO_TICKS(10));
        taskYIELD();
    }
    mbedtls_sha256_finish(&sha256_ctx, sha256_result);
    mbedtls_sha256_free(&sha256_ctx);

    if (!validate_firmware(expected_checksum, sha256_result)) {
        return ESP_FAIL;
    }
    return ESP_OK;
}

/**
 * @brief Single OTA download+verify attempt. Returns ESP_OK on success.
 */
static esp_err_t ota_try_download(const char *ota_id, const char *url,
                                  const char *checksum, uint64_t size,
                                  const char *version)
{
    ESP_LOGI(TAG, "=== ota_try_download START ===");
    ESP_LOGI(TAG, "  URL: '%s'", url);
    ESP_LOGI(TAG, "  Expected size: %llu bytes", (unsigned long long)size);
    ESP_LOGI(TAG, "  Checksum: '%s'", checksum ? checksum : "(none)");
    ESP_LOGI(TAG, "  Version: '%s'", version ? version : "(none)");

    /* Write NVS: downloading */
    ota_nvs_set_state(OTA_STATE_DOWNLOADING);
    ota_nvs_set_meta(version, checksum);
    snprintf(s_download_ota_id, sizeof(s_download_ota_id), "%s", ota_id);
    ota_report_progress(ota_id, 0, 0, NULL);

    uint32_t total_bytes = 0;
    esp_err_t err = ota_download(url, size, &total_bytes);
    if (err != ESP_OK) {
        return err;
    }

    /* Write NVS: verifying */
    ota_nvs_set_state(OTA_STATE_VERIFYING);

    return ota_verify(checksum, total_bytes, size);
}

/* Forward declarations */
static void ota_task_func(void *pvParameters);

/* OTA start function */
esp_err_t ota_start(const ota_cmd_t *cmd)
{
    if (!cmd) {
        ESP_LOGE(TAG, "ota_start: NULL command");
        return ESP_ERR_INVALID_ARG;
    }

    if (s_upgrading) {
        ESP_LOGW(TAG, "OTA already in progress");
        free((void *)cmd);
        return ESP_ERR_INVALID_STATE;
    }

    s_upgrading = true;
    ESP_LOGI(TAG, "Starting OTA: %s from %s (expect %llu bytes)",
             cmd->ota_id, cmd->firmware_url, (unsigned long long)cmd->size_bytes);

    /* Run OTA in a dedicated task so mqtt_task can keep running.
     * cmd is passed directly — ota_task_func takes ownership and will free it. */
    /* 必须同时打**最大连续块**，只打 free heap 会误导。
     *
     * xTaskCreate() 需要一整块**连续**内存（约 8192 字节栈 + TCB），
     * 而堆的总空闲量可能够、却因为碎片没有 8192 的连续块而失败。
     * 2026-10-01 的真实故障正是这一类：请求 16KB 栈而只有 14.3KB 空闲；
     * 2026-10-05 修配置事务内存问题时又实测到配置刚结束时
     * free=9648 但 largest 只有 7680 —— 小于 8192，此时 OTA 必然起不来，
     * 而只看 free heap 会让人以为"还有 9.6KB，应该够"。
     *
     * 因此这里把 free / largest / min_ever 一并打出来，
     * 让"OTA 起不来"能一眼区分为总量不足还是碎片所致。 */
    ESP_LOGI(TAG, "Creating ota_task with %u byte stack: free=%u largest=%u min_ever=%u "
                  "(internal free=%u largest=%u)",
             (unsigned)OTA_TASK_STACK_BYTES,
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT),
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
             (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
    /* 静态创建：栈与 TCB 都在 .bss，**不从堆分配、也不受堆碎片影响**。
     * 这正是本次要修的缺陷 —— 原实现要 8192 连续堆块，实测只有 7680，
     * 于是 OTA 完全起不来（见文件上方 s_ota_stack 处的完整说明）。
     * 返回即句柄，失败为 NULL（与 xTaskCreate 的 pdPASS 语义不同）。 */
    s_ota_task = xTaskCreateStatic(ota_task_func, "ota_task",
                                   OTA_TASK_STACK_WORDS, (void *)cmd, 5,
                                   s_ota_stack, &s_ota_tcb);
    if (s_ota_task == NULL) {
        /* Report the failure instead of returning silently.
         *
         * This is load-bearing, not cosmetic: the server's SendOtaCommand()
         * treats "no OtaProg within 30s" as a retryable condition and only
         * fails the task after 3 attempts. A silent return here therefore
         * costs the operator 90+ seconds and yields the generic message
         * "no ack after 3 attempts", which points at the network rather than
         * at the device. Observed 2026-10-01 on 30EDA0A9A808, where the real
         * cause was a 16 KB stack request against 14.3 KB of free heap.
         *
         * ota_id must still match s_last_ota_id for ota_report_progress() to
         * forward the callback, which ota_classify_cmd() already set. */
        ESP_LOGE(TAG, "Failed to create ota_task (static): need %u bytes .bss "
                      "(this should be unreachable); free=%u largest=%u min_ever=%u",
                 (unsigned)OTA_TASK_STACK_BYTES,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT));
        ota_report_progress(cmd->ota_id, 3, 0, "Insufficient heap to start OTA task");
        free((void *)cmd);
        s_upgrading = false;
        return ESP_ERR_NO_MEM;
    }
    return ESP_OK;
}

/* OTA task entry point */
static void ota_task_func(void *pvParameters)
{
    ota_cmd_t *cmd = (ota_cmd_t *)pvParameters;

    ESP_LOGI(TAG, "=== ota_task_func START ===");
    ESP_LOGI(TAG, "  cmd->ota_id:      '%s'", cmd->ota_id);
    ESP_LOGI(TAG, "  cmd->firmware_url:'%s'", cmd->firmware_url);
    ESP_LOGI(TAG, "  cmd->checksum:    '%s'", cmd->checksum);
    ESP_LOGI(TAG, "  cmd->version:     '%s'", cmd->version);
    ESP_LOGI(TAG, "  cmd->size_bytes:  %llu bytes", (unsigned long long)cmd->size_bytes);
    ESP_LOGI(TAG, "  Free heap: %u bytes", (unsigned int)esp_get_free_heap_size());

    #define OTA_MAX_RETRIES 3
    static const int retry_delay_s[OTA_MAX_RETRIES] = {0, 2, 4};

    esp_err_t err = ESP_FAIL;
    for (int attempt = 0; attempt < OTA_MAX_RETRIES; attempt++) {
        if (attempt > 0) {
            ESP_LOGI(TAG, "OTA retry %d/%d after %ds", attempt + 1, OTA_MAX_RETRIES, retry_delay_s[attempt]);
            vTaskDelay(pdMS_TO_TICKS(retry_delay_s[attempt] * 1000));
            /* Reset NVS state before retry */
            ota_nvs_set_state(OTA_STATE_NONE);
        }

        err = ota_try_download(cmd->ota_id, cmd->firmware_url, cmd->checksum, cmd->size_bytes, cmd->version);
        if (err == ESP_OK) {
            break;  /* success */
        }

        ESP_LOGW(TAG, "OTA attempt %d/%d failed", attempt + 1, OTA_MAX_RETRIES);
    }

    if (err != ESP_OK) {
        /* All retries exhausted */
        ota_nvs_set_state(OTA_STATE_NONE);
        ota_report_progress(cmd->ota_id, 3, 0, "Download failed after retries");
        free(cmd);
        s_upgrading = false;
        /* 先清句柄再删除：vTaskDelete(NULL) 不返回，清必须在前。
         * 静态任务的栈/TCB 不会被释放（它们在 .bss，见 s_ota_stack 的说明），
         * 因此下一次升级可以安全复用同一组缓冲。 */
        s_ota_task = NULL;
        vTaskDelete(NULL);
        return;
    }

    /* Checksum OK — set boot partition and reboot */
    const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
    if (update_partition == NULL) {
        ESP_LOGE(TAG, "Cannot get update partition after OTA write");
        ota_nvs_set_state(OTA_STATE_NONE);
        ota_report_progress(cmd->ota_id, 3, 0, "Boot partition switch failed");
        free(cmd);
        s_upgrading = false;
        /* 先清句柄再删除：vTaskDelete(NULL) 不返回，清必须在前。
         * 静态任务的栈/TCB 不会被释放（它们在 .bss，见 s_ota_stack 的说明），
         * 因此下一次升级可以安全复用同一组缓冲。 */
        s_ota_task = NULL;
        vTaskDelete(NULL);
        return;
    }

    err = esp_ota_set_boot_partition(update_partition);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to set boot partition: %s", esp_err_to_name(err));
        ota_nvs_set_state(OTA_STATE_NONE);
        ota_report_progress(cmd->ota_id, 3, 0, "Boot partition switch failed");
        free(cmd);
        s_upgrading = false;
        /* 先清句柄再删除：vTaskDelete(NULL) 不返回，清必须在前。
         * 静态任务的栈/TCB 不会被释放（它们在 .bss，见 s_ota_stack 的说明），
         * 因此下一次升级可以安全复用同一组缓冲。 */
        s_ota_task = NULL;
        vTaskDelete(NULL);
        return;
    }

    ESP_LOGI(TAG, "Boot partition set to next partition");
    /* 一次性测量：OTA 任务的真实栈用量与剩余堆。
     *
     * 为什么需要：OTA_TASK_STACK_BYTES 从 16KB 降到 8KB 是照着 IDF 示例
     * 抄的量级，**从未按本固件的实测用量论证**。而 8KB 常驻 .bss 会永久
     * 拿走 8KB 堆 —— 在只有约 19KB 空闲堆的 S3 上这是很大的代价，
     * 实测会导致配置事务后堆再次紧张（MQTT 上报 tcp_write errno=11）。
     * 因此先量真实用量，再决定能否进一步收紧。
     *
     * uxTaskGetStackHighWaterMark 返回**历史最小剩余**（字节），
     * 即"离栈溢出最近的时刻还差多少"——这正是选栈大小需要的数。 */
    {
        UBaseType_t hw = uxTaskGetStackHighWaterMark(NULL);
        ESP_LOGI(TAG, "[otamem] ota_task stack: total=%u high_water_free=%u used≈%u",
                 (unsigned)OTA_TASK_STACK_BYTES, (unsigned)(hw * sizeof(StackType_t)),
                 (unsigned)(OTA_TASK_STACK_BYTES - hw * sizeof(StackType_t)));
        ESP_LOGI(TAG, "[otamem] heap now: free=%u largest=%u min_ever=%u",
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_8BIT),
                 (unsigned)heap_caps_get_minimum_free_size(MALLOC_CAP_8BIT));
    }
    ota_nvs_set_state(OTA_STATE_VERIFYING);
    ota_report_progress(cmd->ota_id, 1, 100, NULL);

    free(cmd);
    ESP_LOGI(TAG, "Rebooting in 1 second...");
    vTaskDelay(pdMS_TO_TICKS(1000));
    esp_restart();
}

bool ota_is_upgrading(void)
{
    return s_upgrading;
}
