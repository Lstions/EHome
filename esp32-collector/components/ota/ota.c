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
    cli_cfg.buffer_size = 8192;
    cli_cfg.buffer_size_tx = 1024;

    esp_http_client_handle_t client = esp_http_client_init(&cli_cfg);
    if (client == NULL) {
        ESP_LOGE(TAG, "HTTP client init FAILED");
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
    ESP_LOGI(TAG, "Creating ota_task with 16KB stack...");
    BaseType_t ret = xTaskCreate(ota_task_func, "ota_task", 16384, (void *)cmd, 5, NULL);
    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create ota_task");
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
        vTaskDelete(NULL);
        return;
    }

    ESP_LOGI(TAG, "Boot partition set to next partition");
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
