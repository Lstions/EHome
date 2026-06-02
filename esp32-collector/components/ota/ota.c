/**
 * @file ota.c
 * @brief OTA Upgrade Implementation
 */

#include "ota.h"
#include "msg_handler.h"
#include "esp_log.h"
#include "esp_ota_ops.h"
#include "esp_https_ota.h"
#include "esp_partition.h"
#include <string.h>
#include <ctype.h>

#define TAG "OTA"

static bool s_upgrading = false;

void ota_init(void)
{
    s_upgrading = false;
    ESP_LOGI(TAG, "OTA initialized");
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

void ota_start(const char *ota_id, const char *url, const char *checksum, uint64_t size, const char *version)
{
    (void)size;
    (void)version;

    if (s_upgrading) {
        ESP_LOGW(TAG, "OTA already in progress");
        return;
    }

    s_upgrading = true;
    ESP_LOGI(TAG, "Starting OTA: %s from %s", ota_id, url);

    esp_http_client_config_t client_config = {
        .url = url,
        .cert_pem = NULL,
        .timeout_ms = 10000,
        .skip_cert_common_name_check = true,
    };

    esp_https_ota_config_t ota_config = {
        .http_config = &client_config,
    };

    msg_handler_send_ota_prog(ota_id, 0, 0, NULL);

    /* Use the incremental OTA API for progress tracking */
    esp_https_ota_handle_t handle = NULL;
    esp_err_t err = esp_https_ota_begin(&ota_config, &handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "OTA begin failed: %s", esp_err_to_name(err));
        msg_handler_send_ota_prog(ota_id, 2, 0, "OTA begin failed");
        s_upgrading = false;
        return;
    }

    int last_pct = 0;

    while (1) {
        err = esp_https_ota_perform(handle);
        if (err != ESP_OK) {
            break;
        }

        int total_read = esp_https_ota_get_image_len_read(handle);
        int image_size = esp_https_ota_get_image_size(handle);

        /* Report progress */
        if (image_size > 0) {
            int pct = (total_read * 100) / image_size;
            if (pct != last_pct && pct % 10 == 0) {
                last_pct = pct;
                msg_handler_send_ota_prog(ota_id, 0, pct, NULL);
            }
        }
    }

    if (err != ESP_ERR_HTTPS_OTA_IN_PROGRESS) {
        ESP_LOGE(TAG, "OTA download failed: %s", esp_err_to_name(err));
        esp_https_ota_abort(handle);
        msg_handler_send_ota_prog(ota_id, 2, 0, "Download failed");
        s_upgrading = false;
        return;
    }

    /* Download complete. Get the update partition and compute SHA256. */
    const esp_partition_t *update_partition = esp_ota_get_next_update_partition(NULL);
    if (update_partition == NULL) {
        ESP_LOGE(TAG, "No update partition found");
        esp_https_ota_abort(handle);
        msg_handler_send_ota_prog(ota_id, 2, 0, "No update partition");
        s_upgrading = false;
        return;
    }

    int image_len = esp_https_ota_get_image_len_read(handle);
    ESP_LOGI(TAG, "Downloaded %d bytes to partition '%s'", image_len, update_partition->label);

    /* Compute SHA256 of the downloaded firmware in the update partition */
    uint8_t sha256_result[32];
    ESP_LOGI(TAG, "Computing SHA256 of partition '%s' at offset 0x%" PRIx32,
             update_partition->label, update_partition->address);

    err = esp_partition_get_sha256(update_partition, sha256_result);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to compute SHA256: %s", esp_err_to_name(err));
        esp_https_ota_abort(handle);
        msg_handler_send_ota_prog(ota_id, 2, 0, "SHA256 compute failed");
        s_upgrading = false;
        return;
    }

    /* Validate checksum */
    if (!validate_firmware(checksum, sha256_result)) {
        esp_https_ota_abort(handle);
        msg_handler_send_ota_prog(ota_id, 2, 0, "Checksum mismatch");
        s_upgrading = false;
        return;
    }

    /* Checksum OK, finish OTA */
    err = esp_https_ota_finish(handle);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "OTA finish failed: %s", esp_err_to_name(err));
        msg_handler_send_ota_prog(ota_id, 2, 0, "OTA finish failed");
        s_upgrading = false;
        return;
    }

    msg_handler_send_ota_prog(ota_id, 1, 100, NULL);
    ESP_LOGI(TAG, "OTA complete, restarting...");
    esp_restart();
}

bool ota_is_upgrading(void)
{
    return s_upgrading;
}
