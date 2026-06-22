/**
 * @file nvs_helper.h
 * @brief NVS helper functions - simplified wrappers for common NVS operations
 *
 * Provides convenience functions to reduce boilerplate in NVS read/write operations.
 * Uses ESP-IDF's built-in NVS API directly without session abstraction.
 */

#ifndef NVS_HELPER_H
#define NVS_HELPER_H

#include "nvs_flash.h"
#include "esp_err.h"
#include <string.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief Read a string value from NVS
 *
 * @param ns NVS namespace
 * @param key Key name
 * @param out_buf Output buffer
 * @param buf_size Buffer size
 * @return esp_err_t ESP_OK on success, ESP_ERR_NVS_NOT_FOUND if key doesn't exist
 */
static inline esp_err_t nvs_read_str(const char *ns, const char *key, 
                                     char *out_buf, size_t buf_size)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return err;
    }

    size_t required_size = buf_size;
    err = nvs_get_str(handle, key, out_buf, &required_size);
    nvs_close(handle);

    return err;
}

/**
 * @brief Write a string value to NVS
 *
 * @param ns NVS namespace
 * @param key Key name
 * @param value String value to write
 * @return esp_err_t ESP_OK on success
 */
static inline esp_err_t nvs_write_str(const char *ns, const char *key, const char *value)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        return err;
    }

    err = nvs_set_str(handle, key, value);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);

    return err;
}

/**
 * @brief Read a uint64 value from NVS
 *
 * @param ns NVS namespace
 * @param key Key name
 * @param out_value Output value pointer
 * @return esp_err_t ESP_OK on success, ESP_ERR_NVS_NOT_FOUND if key doesn't exist
 */
static inline esp_err_t nvs_read_u64(const char *ns, const char *key, uint64_t *out_value)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READONLY, &handle);
    if (err != ESP_OK) {
        return err;
    }

    err = nvs_get_u64(handle, key, out_value);
    nvs_close(handle);

    return err;
}

/**
 * @brief Write a uint64 value to NVS
 *
 * @param ns NVS namespace
 * @param key Key name
 * @param value Value to write
 * @return esp_err_t ESP_OK on success
 */
static inline esp_err_t nvs_write_u64(const char *ns, const char *key, uint64_t value)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        return err;
    }

    err = nvs_set_u64(handle, key, value);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);

    return err;
}

/**
 * @brief Delete a key from NVS
 *
 * @param ns NVS namespace
 * @param key Key name
 * @return esp_err_t ESP_OK on success, ESP_ERR_NVS_NOT_FOUND if key doesn't exist
 */
static inline esp_err_t nvs_delete_key(const char *ns, const char *key)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        return err;
    }

    err = nvs_erase_key(handle, key);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);

    return err;
}

/**
 * @brief Erase all keys in a namespace
 *
 * @param ns NVS namespace
 * @return esp_err_t ESP_OK on success
 */
static inline esp_err_t nvs_erase_all(const char *ns)
{
    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &handle);
    if (err != ESP_OK) {
        return err;
    }

    err = nvs_erase_all(handle);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);

    return err;
}

#ifdef __cplusplus
}
#endif

#endif /* NVS_HELPER_H */
