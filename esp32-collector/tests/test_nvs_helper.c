/**
 * @file test_nvs_helper.c
 * @brief Unit tests for NVS helper functions (Step 5)
 *
 * Tests: nvs_helper wrapper functions for simplified NVS operations
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include "nvs_flash.h"
#include "nvs.h"

/* Test counters */
static int tests_run = 0;
static int tests_passed = 0;

#define TEST_ASSERT(cond, msg) do { \
    tests_run++; \
    if (cond) { \
        printf("  ✓ %s\n", msg); \
        tests_passed++; \
    } else { \
        printf("  ✗ %s\n", msg); \
    } \
} while(0)

/* Helper function to check if NVS is initialized */
static bool nvs_initialized = false;

static esp_err_t ensure_nvs_init(void) {
    if (nvs_initialized) return ESP_OK;
    
    esp_err_t err = nvs_flash_init();
    if (err == ESP_ERR_NVS_NO_FREE_PAGES || err == ESP_ERR_NVS_NEW_VERSION_FOUND) {
        ESP_ERROR_CHECK(nvs_flash_erase());
        err = nvs_flash_init();
    }
    
    if (err == ESP_OK) {
        nvs_initialized = true;
    }
    return err;
}

/* Test: nvs_read_str - read string value */
void test_nvs_read_str(void) {
    printf("\n[test_nvs_read_str]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write test data */
    nvs_handle_t handle;
    err = nvs_open("test_ns", NVS_READWRITE, &handle);
    TEST_ASSERT(err == ESP_OK, "NVS namespace opened");
    
    err = nvs_set_str(handle, "test_key", "test_value");
    TEST_ASSERT(err == ESP_OK, "String written to NVS");
    
    err = nvs_commit(handle);
    nvs_close(handle);
    TEST_ASSERT(err == ESP_OK, "NVS committed");
    
    /* Read back using helper */
    char buffer[64];
    size_t len = sizeof(buffer);
    err = nvs_read_str("test_ns", "test_key", buffer, &len);
    TEST_ASSERT(err == ESP_OK, "String read successfully");
    TEST_ASSERT(strcmp(buffer, "test_value") == 0, "String value matches");
    TEST_ASSERT(len == strlen("test_value") + 1, "Length is correct (including null terminator)");
}

/* Test: nvs_write_str - write string value */
void test_nvs_write_str(void) {
    printf("\n[test_nvs_write_str]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write using helper */
    err = nvs_write_str("test_ns2", "new_key", "new_value");
    TEST_ASSERT(err == ESP_OK, "String written via helper");
    
    /* Read back directly */
    nvs_handle_t handle;
    err = nvs_open("test_ns2", NVS_READONLY, &handle);
    TEST_ASSERT(err == ESP_OK, "NVS namespace opened");
    
    char buffer[64];
    size_t len = sizeof(buffer);
    err = nvs_get_str(handle, "new_key", buffer, &len);
    TEST_ASSERT(err == ESP_OK, "String read directly");
    TEST_ASSERT(strcmp(buffer, "new_value") == 0, "String value matches");
    
    nvs_close(handle);
}

/* Test: nvs_read_blob - read binary data */
void test_nvs_read_blob(void) {
    printf("\n[test_nvs_read_blob]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write binary data */
    uint8_t test_data[] = {0x01, 0x02, 0x03, 0x04, 0x05};
    nvs_handle_t handle;
    err = nvs_open("test_ns3", NVS_READWRITE, &handle);
    TEST_ASSERT(err == ESP_OK, "NVS namespace opened");
    
    err = nvs_set_blob(handle, "blob_key", test_data, sizeof(test_data));
    TEST_ASSERT(err == ESP_OK, "Blob written to NVS");
    
    err = nvs_commit(handle);
    nvs_close(handle);
    TEST_ASSERT(err == ESP_OK, "NVS committed");
    
    /* Read back using helper */
    uint8_t buffer[16];
    size_t len = sizeof(buffer);
    err = nvs_read_blob("test_ns3", "blob_key", buffer, &len);
    TEST_ASSERT(err == ESP_OK, "Blob read successfully");
    TEST_ASSERT(len == sizeof(test_data), "Blob length matches");
    TEST_ASSERT(memcmp(buffer, test_data, len) == 0, "Blob data matches");
}

/* Test: nvs_write_blob - write binary data */
void test_nvs_write_blob(void) {
    printf("\n[test_nvs_write_blob]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write binary data using helper */
    uint8_t test_data[] = {0xAA, 0xBB, 0xCC, 0xDD};
    err = nvs_write_blob("test_ns4", "blob_key2", test_data, sizeof(test_data));
    TEST_ASSERT(err == ESP_OK, "Blob written via helper");
    
    /* Read back directly */
    nvs_handle_t handle;
    err = nvs_open("test_ns4", NVS_READONLY, &handle);
    TEST_ASSERT(err == ESP_OK, "NVS namespace opened");
    
    uint8_t buffer[16];
    size_t len = sizeof(buffer);
    err = nvs_get_blob(handle, "blob_key2", buffer, &len);
    TEST_ASSERT(err == ESP_OK, "Blob read directly");
    TEST_ASSERT(len == sizeof(test_data), "Blob length matches");
    TEST_ASSERT(memcmp(buffer, test_data, len) == 0, "Blob data matches");
    
    nvs_close(handle);
}

/* Test: nvs_erase_key - erase specific key */
void test_nvs_erase_key(void) {
    printf("\n[test_nvs_erase_key]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write test data */
    err = nvs_write_str("test_ns5", "key_to_erase", "value1");
    TEST_ASSERT(err == ESP_OK, "First key written");
    
    err = nvs_write_str("test_ns5", "key_to_keep", "value2");
    TEST_ASSERT(err == ESP_OK, "Second key written");
    
    /* Erase one key using helper */
    err = nvs_erase_key("test_ns5", "key_to_erase");
    TEST_ASSERT(err == ESP_OK, "Key erased via helper");
    
    /* Verify erased key is gone */
    char buffer[64];
    size_t len = sizeof(buffer);
    err = nvs_read_str("test_ns5", "key_to_erase", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Erased key not found");
    
    /* Verify other key still exists */
    len = sizeof(buffer);
    err = nvs_read_str("test_ns5", "key_to_keep", buffer, &len);
    TEST_ASSERT(err == ESP_OK, "Other key still exists");
    TEST_ASSERT(strcmp(buffer, "value2") == 0, "Other key value unchanged");
}

/* Test: nvs_erase_namespace - erase entire namespace */
void test_nvs_erase_namespace(void) {
    printf("\n[test_nvs_erase_namespace]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write multiple keys */
    err = nvs_write_str("test_ns6", "key1", "value1");
    TEST_ASSERT(err == ESP_OK, "First key written");
    
    err = nvs_write_str("test_ns6", "key2", "value2");
    TEST_ASSERT(err == ESP_OK, "Second key written");
    
    err = nvs_write_str("test_ns6", "key3", "value3");
    TEST_ASSERT(err == ESP_OK, "Third key written");
    
    /* Erase entire namespace using helper */
    err = nvs_erase_namespace("test_ns6");
    TEST_ASSERT(err == ESP_OK, "Namespace erased via helper");
    
    /* Verify all keys are gone */
    char buffer[64];
    size_t len = sizeof(buffer);
    
    err = nvs_read_str("test_ns6", "key1", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Key1 not found");
    
    len = sizeof(buffer);
    err = nvs_read_str("test_ns6", "key2", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Key2 not found");
    
    len = sizeof(buffer);
    err = nvs_read_str("test_ns6", "key3", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Key3 not found");
}

/* Test: Error handling - read non-existent key */
void test_nvs_read_nonexistent(void) {
    printf("\n[test_nvs_read_nonexistent]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    char buffer[64];
    size_t len = sizeof(buffer);
    
    /* Try to read non-existent key */
    err = nvs_read_str("nonexistent_ns", "nonexistent_key", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Non-existent namespace returns NOT_FOUND");
    
    /* Write a key, then try to read different key */
    err = nvs_write_str("test_ns7", "existing_key", "value");
    TEST_ASSERT(err == ESP_OK, "Key written");
    
    len = sizeof(buffer);
    err = nvs_read_str("test_ns7", "nonexistent_key", buffer, &len);
    TEST_ASSERT(err == ESP_ERR_NVS_NOT_FOUND, "Non-existent key returns NOT_FOUND");
}

/* Test: Error handling - buffer too small */
void test_nvs_buffer_too_small(void) {
    printf("\n[test_nvs_buffer_too_small]\n");
    
    esp_err_t err = ensure_nvs_init();
    TEST_ASSERT(err == ESP_OK, "NVS initialized");
    
    /* Write a long string */
    err = nvs_write_str("test_ns8", "long_key", "this_is_a_very_long_value_that_exceeds_buffer");
    TEST_ASSERT(err == ESP_OK, "Long string written");
    
    /* Try to read with small buffer */
    char buffer[10];
    size_t len = sizeof(buffer);
    err = nvs_read_str("test_ns8", "long_key", buffer, &len);
    
    /* Should return ESP_ERR_NVS_INVALID_LENGTH and set len to required size */
    TEST_ASSERT(err == ESP_ERR_NVS_INVALID_LENGTH, "Small buffer returns INVALID_LENGTH");
    TEST_ASSERT(len > sizeof(buffer), "Required length is reported");
}

int main(void) {
    printf("=== NVS Helper Function Tests ===\n");
    
    test_nvs_read_str();
    test_nvs_write_str();
    test_nvs_read_blob();
    test_nvs_write_blob();
    test_nvs_erase_key();
    test_nvs_erase_namespace();
    test_nvs_read_nonexistent();
    test_nvs_buffer_too_small();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d/%d\n", tests_passed, tests_run);
    
    return (tests_passed == tests_run) ? 0 : 1;
}
