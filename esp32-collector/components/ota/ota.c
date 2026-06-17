/**
 * @file ota.c
 * @brief OTA Upgrade Implementation
 */

#include "ota.h"
#include "msg_handler.h"
#include "esp_log.h"
#include "esp_ota_ops.h"
#include "esp_https_ota.h"
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

typedef enum {
    OTA_STATE_NONE       = 0,
    OTA_STATE_DOWNLOADING = 1,
    OTA_STATE_VERIFYING  = 2,
} ota_nvs_state_t;

static bool s_upgrading = false;
static char s_last_ota_id[64] = {0};

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

/* --- Public API --- */

void ota_init(void)
{
    s_upgrading = false;
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

void ota_confirm_valid(void)
{
    esp_err_t err = esp_ota_mark_app_valid_cancel_rollback();
    if (err == ESP_OK) {
        ESP_LOGI(TAG, "App marked valid, rollback cancelled");
    } else {
        ESP_LOGW(TAG, "mark_app_valid failed: %s", esp_err_to_name(err));
    }
    /* Clear NVS state — OTA fully completed */
    ota_nvs_set_state(OTA_STATE_NONE);
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

/**
 * @brief Build the esp_http_client_config_t based on Kconfig OTA security settings.
 *
 * - HTTPS + crt_bundle: uses Mozilla CA bundle (public Internet)
 * - HTTPS + custom cert: embeds a CA PEM for private/self-signed servers
 * - HTTPS + no verify: WARN log (not for production)
 * - HTTP: WARN log (no encryption)
 */
static esp_err_t build_ota_http_config(esp_http_client_config_t *cfg, const char *url)
{
    memset(cfg, 0, sizeof(*cfg));
    cfg->url = url;
    cfg->timeout_ms = 10000;

#if CONFIG_COLLECTOR_OTA_ALLOW_HTTP
    /* Development mode: allow plain HTTP (insecure) */
    cfg->transport_type = HTTP_TRANSPORT_OVER_TCP;
    ESP_LOGW(TAG, "OTA: HTTP allowed (development mode - INSECURE)");
    (void)url;  /* suppress unused warning */
    return ESP_OK;
#else
    /* Production: HTTPS with certificate verification */
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
    ESP_LOGW(TAG, "OTA: using plain HTTP (no encryption)");
#endif
    return ESP_OK;
#endif  /* CONFIG_COLLECTOR_OTA_ALLOW_HTTP */
}

/**
 * @brief Single OTA download+verify attempt. Returns ESP_OK on success.
 *        On failure caller may retry after resetting NVS state.
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

    msg_handler_send_ota_prog(ota_id, 0, 0, NULL);

    esp_err_t err;
    int total_bytes = 0;  // actual downloaded bytes, used for SHA256

    /* Runtime URL detection: http:// or https:// */
    bool is_https = (strncmp(url, "https://", 8) == 0);
    bool is_http  = (strncmp(url, "http://", 7) == 0);

    if (!is_http && !is_https) {
        ESP_LOGE(TAG, "Invalid URL scheme (must be http:// or https://)");
        return ESP_ERR_INVALID_ARG;
    }

#if CONFIG_COLLECTOR_OTA_ALLOW_HTTP
    if (is_http && !is_https) {
        ESP_LOGW(TAG, "Using plain HTTP (development mode)");
#else
    if (is_http && !is_https) {
        ESP_LOGE(TAG, "HTTP not allowed (CONFIG_COLLECTOR_OTA_ALLOW_HTTP=n)");
        return ESP_ERR_NOT_SUPPORTED;
#endif
        /* HTTP: direct esp_http_client */
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

        err = esp_http_client_open(client, 0);
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
                msg_handler_send_ota_prog(ota_id, 0, (uint8_t)pct, NULL);
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
        total_bytes = total;
    }
    else {
        /* HTTPS: use esp_https_ota high-level API */
        ESP_LOGI(TAG, "Using HTTPS with certificate verification");
        esp_http_client_config_t cli_cfg;
        build_ota_http_config(&cli_cfg, url);
        cli_cfg.timeout_ms = 30000;
        esp_https_ota_config_t ota_cfg = { .http_config = &cli_cfg };
        err = esp_https_ota(&ota_cfg);
        total_bytes = (int)size;
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "HTTPS OTA fail: %s (0x%x)", esp_err_to_name(err), err);
            return err;
        }
    }

    /* Write NVS: verifying */
    ota_nvs_set_state(OTA_STATE_VERIFYING);
    ESP_LOGI(TAG, "OTA image written, validating checksum...");

    const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
    if (update_partition == NULL) {
        ESP_LOGE(TAG, "No update partition found");
        return ESP_FAIL;
    }

    ESP_LOGI(TAG, "Computing SHA256 of '%s' (offset 0x%" PRIx32 ", %llu bytes)",
             update_partition->label, update_partition->address, (unsigned long long)total_bytes);

    uint8_t sha256_result[32] = {0};
    mbedtls_sha256_context sha256_ctx;
    mbedtls_sha256_init(&sha256_ctx);
    mbedtls_sha256_starts(&sha256_ctx, 0); /* 0 = SHA-256 */

    const int CHUNK = 4096;
    static uint8_t buf[4096];
    uint64_t remaining = total_bytes > 0 ? (uint64_t)total_bytes : size;
    uint32_t offset = 0;
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
        vTaskDelay(pdMS_TO_TICKS(1));  // yield to prevent watchdog
    }
    mbedtls_sha256_finish(&sha256_ctx, sha256_result);
    mbedtls_sha256_free(&sha256_ctx);

    if (!validate_firmware(checksum, sha256_result)) return ESP_FAIL;
    return ESP_OK;
}

/* Forward declaration */
static void ota_task_func(void *pvParameters);

typedef struct {
    char ota_id[64];
    char url[256];
    char checksum[128];
    char version[32];
    uint64_t size;
} ota_task_args_t;

void ota_start(const char *ota_id, const char *url, const char *checksum, uint64_t size, const char *version)
{
    ESP_LOGI(TAG, "=== ota_start called ===");
    ESP_LOGI(TAG, "  ota_id:    '%s'", ota_id ? ota_id : "(null)");
    ESP_LOGI(TAG, "  url:       '%s'", url ? url : "(null)");
    ESP_LOGI(TAG, "  checksum:  '%s'", checksum ? checksum : "(null)");
    ESP_LOGI(TAG, "  size:      %llu bytes", (unsigned long long)size);
    ESP_LOGI(TAG, "  version:   '%s'", version ? version : "(null)");

    if (s_upgrading) {
        ESP_LOGW(TAG, "OTA already in progress");
        return;
    }

    s_upgrading = true;
    ESP_LOGI(TAG, "Starting OTA: %s from %s (expect %llu bytes)", ota_id, url, (unsigned long long)size);

    // Copy args to heap for the task
    ota_task_args_t *args = malloc(sizeof(ota_task_args_t));
    if (!args) {
        ESP_LOGE(TAG, "malloc failed for OTA args");
        s_upgrading = false;
        return;
    }
    strncpy(args->ota_id, ota_id, sizeof(args->ota_id)-1);
    strncpy(args->url, url, sizeof(args->url)-1);
    strncpy(args->checksum, checksum, sizeof(args->checksum)-1);
    strncpy(args->version, version, sizeof(args->version)-1);
    args->size = size;

    // Run OTA in a dedicated task so mqtt_task can keep running
    // Increased stack from 8192 to 16384 for HTTP client buffer requirements
    ESP_LOGI(TAG, "Creating ota_task with 16KB stack...");
    BaseType_t ret = xTaskCreate(ota_task_func, "ota_task", 16384, args, 5, NULL);
    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create ota_task");
        free(args);
        s_upgrading = false;
    }
}

/* OTA task entry point */
static void ota_task_func(void *pvParameters)
{
    ota_task_args_t *args = (ota_task_args_t *)pvParameters;

    ESP_LOGI(TAG, "=== ota_task_func START ===");
    ESP_LOGI(TAG, "  args->ota_id:   '%s'", args->ota_id);
    ESP_LOGI(TAG, "  args->url:      '%s'", args->url);
    ESP_LOGI(TAG, "  args->checksum: '%s'", args->checksum);
    ESP_LOGI(TAG, "  args->version:  '%s'", args->version);
    ESP_LOGI(TAG, "  args->size:     %llu bytes", (unsigned long long)args->size);
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

        err = ota_try_download(args->ota_id, args->url, args->checksum, args->size, args->version);
        if (err == ESP_OK) {
            break;  /* success */
        }

        ESP_LOGW(TAG, "OTA attempt %d/%d failed", attempt + 1, OTA_MAX_RETRIES);
    }

    if (err != ESP_OK) {
        /* All retries exhausted */
        ota_nvs_set_state(OTA_STATE_NONE);
        msg_handler_send_ota_prog(args->ota_id, 3, 0, "Download failed after retries");
        free(args);
        s_upgrading = false;
        vTaskDelete(NULL);
        return;
    }

    /* Checksum OK — set boot partition and reboot */
    const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
    if (update_partition == NULL) {
        ESP_LOGE(TAG, "Cannot get update partition after OTA write");
        ota_nvs_set_state(OTA_STATE_NONE);
        msg_handler_send_ota_prog(args->ota_id, 3, 0, "Boot partition switch failed");
        free(args);
        s_upgrading = false;
        vTaskDelete(NULL);
        return;
    }

    err = esp_ota_set_boot_partition(update_partition);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "set_boot_partition failed: %s", esp_err_to_name(err));
        ota_nvs_set_state(OTA_STATE_NONE);
        msg_handler_send_ota_prog(args->ota_id, 3, 0, "Boot partition switch failed");
        free(args);
        s_upgrading = false;
        vTaskDelete(NULL);
        return;
    }

    ESP_LOGI(TAG, "Boot partition set to '%s' (0x%" PRIx32 "), restarting...",
             update_partition->label, update_partition->address);

    /* Clear NVS state */
    ota_nvs_set_state(OTA_STATE_NONE);

    msg_handler_send_ota_prog(args->ota_id, 1, 100, NULL);

    free(args);
    ESP_LOGI(TAG, "OTA success, restarting in 1s...");
    vTaskDelay(pdMS_TO_TICKS(1000));
    esp_restart();
}
bool ota_is_upgrading(void)
{
    return s_upgrading;
}
