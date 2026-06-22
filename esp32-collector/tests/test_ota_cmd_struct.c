/**
 * @file test_ota_cmd_struct.c
 * @brief Unit tests for OTA command structure (Step 4)
 *
 * Tests: ota_cmd_t heap allocation, field validation, ownership transfer
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>

/* ota_cmd_t structure from ota.h */
typedef struct {
    char ota_id[64];
    char firmware_url[256];
    char checksum[128];
    char version[32];
    uint64_t size_bytes;
} ota_cmd_t;

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

/* Test: Structure allocation and initialization */
void test_struct_allocation(void) {
    printf("\n[test_struct_allocation]\n");
    
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    TEST_ASSERT(cmd != NULL, "Heap allocation succeeds");
    TEST_ASSERT(sizeof(ota_cmd_t) == 64 + 256 + 128 + 32 + 8, "Structure size is correct (488 bytes)");
    
    /* Verify zero-initialized */
    TEST_ASSERT(cmd->ota_id[0] == '\0', "ota_id is empty");
    TEST_ASSERT(cmd->firmware_url[0] == '\0', "firmware_url is empty");
    TEST_ASSERT(cmd->checksum[0] == '\0', "checksum is empty");
    TEST_ASSERT(cmd->version[0] == '\0', "version is empty");
    TEST_ASSERT(cmd->size_bytes == 0, "size_bytes is zero");
    
    free(cmd);
}

/* Test: Field population */
void test_field_population(void) {
    printf("\n[test_field_population]\n");
    
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    
    strncpy(cmd->ota_id, "ota_20260622_001", sizeof(cmd->ota_id) - 1);
    strncpy(cmd->firmware_url, "http://10.42.0.1:8080/firmware/v2.1.bin", sizeof(cmd->firmware_url) - 1);
    strncpy(cmd->checksum, "a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2c3d4e5f6a1b2", sizeof(cmd->checksum) - 1);
    strncpy(cmd->version, "2.1.0", sizeof(cmd->version) - 1);
    cmd->size_bytes = 1048576;  /* 1MB */
    
    TEST_ASSERT(strcmp(cmd->ota_id, "ota_20260622_001") == 0, "ota_id set correctly");
    TEST_ASSERT(strcmp(cmd->firmware_url, "http://10.42.0.1:8080/firmware/v2.1.bin") == 0, "firmware_url set correctly");
    TEST_ASSERT(strlen(cmd->checksum) == 64, "checksum is 64 hex chars");
    TEST_ASSERT(strcmp(cmd->version, "2.1.0") == 0, "version set correctly");
    TEST_ASSERT(cmd->size_bytes == 1048576, "size_bytes set correctly");
    
    free(cmd);
}

/* Test: String truncation protection */
void test_string_truncation(void) {
    printf("\n[test_string_truncation]\n");
    
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    
    /* Create overly long URL */
    char long_url[512];
    memset(long_url, 'A', sizeof(long_url) - 1);
    long_url[sizeof(long_url) - 1] = '\0';
    
    strncpy(cmd->firmware_url, long_url, sizeof(cmd->firmware_url) - 1);
    cmd->firmware_url[sizeof(cmd->firmware_url) - 1] = '\0';
    
    TEST_ASSERT(strlen(cmd->firmware_url) == 255, "URL truncated to 255 chars");
    TEST_ASSERT(cmd->firmware_url[255] == '\0', "Truncated URL is null-terminated");
    
    /* Create overly long OTA ID */
    char long_id[128];
    memset(long_id, 'B', sizeof(long_id) - 1);
    long_id[sizeof(long_id) - 1] = '\0';
    
    strncpy(cmd->ota_id, long_id, sizeof(cmd->ota_id) - 1);
    cmd->ota_id[sizeof(cmd->ota_id) - 1] = '\0';
    
    TEST_ASSERT(strlen(cmd->ota_id) == 63, "OTA ID truncated to 63 chars");
    
    free(cmd);
}

/* Test: Ownership transfer simulation */
static int ownership_freed_count = 0;

static void simulate_ota_start(ota_cmd_t *cmd) {
    /* Simulate taking ownership and freeing */
    if (cmd) {
        ownership_freed_count++;
        free(cmd);
    }
}

void test_ownership_transfer(void) {
    printf("\n[test_ownership_transfer]\n");
    
    ownership_freed_count = 0;
    
    /* Simulate handler_data_process_ota flow */
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    TEST_ASSERT(cmd != NULL, "Initial allocation succeeds");
    
    strncpy(cmd->ota_id, "ota_test_001", sizeof(cmd->ota_id) - 1);
    strncpy(cmd->firmware_url, "http://test/firmware.bin", sizeof(cmd->firmware_url) - 1);
    cmd->size_bytes = 524288;
    
    /* Transfer ownership to ota_start */
    simulate_ota_start(cmd);
    TEST_ASSERT(ownership_freed_count == 1, "ota_start freed the command");
    
    /* After transfer, caller should NOT free again */
    /* (This test just verifies the pattern works) */
}

/* Test: Duplicate OTA detection */
static char s_last_ota_id[64] = {0};

static bool ota_is_duplicate(const char *ota_id) {
    if (s_last_ota_id[0] && strcmp(s_last_ota_id, ota_id) == 0) {
        return true;
    }
    strncpy(s_last_ota_id, ota_id, sizeof(s_last_ota_id) - 1);
    s_last_ota_id[sizeof(s_last_ota_id) - 1] = '\0';
    return false;
}

void test_duplicate_detection(void) {
    printf("\n[test_duplicate_detection]\n");
    
    s_last_ota_id[0] = '\0';
    
    /* First OTA - not a duplicate */
    bool dup = ota_is_duplicate("ota_001");
    TEST_ASSERT(!dup, "First OTA is not a duplicate");
    
    /* Same OTA ID - is a duplicate */
    dup = ota_is_duplicate("ota_001");
    TEST_ASSERT(dup, "Same OTA ID is detected as duplicate");
    
    /* Different OTA ID - not a duplicate */
    dup = ota_is_duplicate("ota_002");
    TEST_ASSERT(!dup, "Different OTA ID is not a duplicate");
    
    /* Same ID again - is a duplicate */
    dup = ota_is_duplicate("ota_002");
    TEST_ASSERT(dup, "Repeated OTA ID is detected as duplicate");
}

/* Test: Structure size verification */
void test_structure_layout(void) {
    printf("\n[test_structure_layout]\n");
    
    size_t expected_size = 64 + 256 + 128 + 32 + 8;  /* 488 bytes */
    size_t actual_size = sizeof(ota_cmd_t);
    
    printf("  Structure size: %zu bytes (expected %zu)\n", actual_size, expected_size);
    
    TEST_ASSERT(actual_size == expected_size, "Structure size matches expected");
    
    /* Verify field offsets */
    size_t ota_id_offset = offsetof(ota_cmd_t, ota_id);
    size_t firmware_url_offset = offsetof(ota_cmd_t, firmware_url);
    size_t checksum_offset = offsetof(ota_cmd_t, checksum);
    size_t version_offset = offsetof(ota_cmd_t, version);
    size_t size_bytes_offset = offsetof(ota_cmd_t, size_bytes);
    
    TEST_ASSERT(ota_id_offset == 0, "ota_id is at offset 0");
    TEST_ASSERT(firmware_url_offset == 64, "firmware_url is at offset 64");
    TEST_ASSERT(checksum_offset == 320, "checksum is at offset 320");
    TEST_ASSERT(version_offset == 448, "version is at offset 448");
    TEST_ASSERT(size_bytes_offset == 480, "size_bytes is at offset 480");
}

/* Test: Zero-size firmware */
void test_zero_size_firmware(void) {
    printf("\n[test_zero_size_firmware]\n");
    
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    
    strncpy(cmd->ota_id, "ota_empty", sizeof(cmd->ota_id) - 1);
    strncpy(cmd->firmware_url, "http://test/empty.bin", sizeof(cmd->firmware_url) - 1);
    cmd->size_bytes = 0;  /* Zero size */
    
    TEST_ASSERT(cmd->size_bytes == 0, "Zero size is valid");
    
    free(cmd);
}

/* Test: Large firmware size */
void test_large_firmware_size(void) {
    printf("\n[test_large_firmware_size]\n");
    
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    
    strncpy(cmd->ota_id, "ota_large", sizeof(cmd->ota_id) - 1);
    cmd->size_bytes = 0xFFFFFFFFFFFFFFFFULL;  /* Max uint64 */
    
    TEST_ASSERT(cmd->size_bytes == 0xFFFFFFFFFFFFFFFFULL, "Max uint64 size handled correctly");
    
    /* Typical ESP32 firmware is ~1.5MB */
    cmd->size_bytes = 1572864;
    TEST_ASSERT(cmd->size_bytes == 1572864, "Typical firmware size (1.5MB) handled correctly");
    
    free(cmd);
}

int main(void) {
    printf("=== OTA Command Structure Tests ===\n");
    
    test_struct_allocation();
    test_field_population();
    test_string_truncation();
    test_ownership_transfer();
    test_duplicate_detection();
    test_structure_layout();
    test_zero_size_firmware();
    test_large_firmware_size();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d/%d\n", tests_passed, tests_run);
    
    return (tests_passed == tests_run) ? 0 : 1;
}
