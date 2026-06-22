/**
 * @file test_factory_reset_whitelist.c
 * @brief Unit tests for factory_reset namespace whitelist (Step 11)
 *
 * Tests: Only whitelisted namespaces are erased, others are preserved
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <assert.h>

/* Mock NVS types */
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL -1
#define ESP_ERR_NVS_NOT_FOUND -2

/* Whitelist from factory_reset.c */
static const char *NVS_NAMESPACE_WHITELIST[] = {
    "wifi_mgr",
    "config_mgr",
    "ota"
};

/* Simulated NVS storage */
#define MAX_NAMESPACES 10
#define MAX_KEYS_PER_NS 5

typedef struct {
    char key[32];
    char value[64];
} nvs_key_t;

typedef struct {
    char name[32];
    nvs_key_t keys[MAX_KEYS_PER_NS];
    int key_count;
    bool exists;
} nvs_namespace_t;

static nvs_namespace_t mock_nvs[MAX_NAMESPACES];
static int mock_nvs_count = 0;

/* Mock NVS functions */
static void mock_nvs_init(void) {
    memset(mock_nvs, 0, sizeof(mock_nvs));
    mock_nvs_count = 0;
}

static void mock_nvs_create_namespace(const char *name) {
    if (mock_nvs_count >= MAX_NAMESPACES) return;
    strncpy(mock_nvs[mock_nvs_count].name, name, 31);
    mock_nvs[mock_nvs_count].exists = true;
    mock_nvs[mock_nvs_count].key_count = 0;
    mock_nvs_count++;
}

static void mock_nvs_set_key(const char *ns_name, const char *key, const char *value) {
    for (int i = 0; i < mock_nvs_count; i++) {
        if (strcmp(mock_nvs[i].name, ns_name) == 0 && mock_nvs[i].exists) {
            if (mock_nvs[i].key_count < MAX_KEYS_PER_NS) {
                strncpy(mock_nvs[i].keys[mock_nvs[i].key_count].key, key, 31);
                strncpy(mock_nvs[i].keys[mock_nvs[i].key_count].value, value, 63);
                mock_nvs[i].key_count++;
            }
            return;
        }
    }
}

static bool mock_nvs_namespace_exists(const char *name) {
    for (int i = 0; i < mock_nvs_count; i++) {
        if (strcmp(mock_nvs[i].name, name) == 0 && mock_nvs[i].exists) {
            return true;
        }
    }
    return false;
}

static bool mock_nvs_namespace_empty(const char *name) {
    for (int i = 0; i < mock_nvs_count; i++) {
        if (strcmp(mock_nvs[i].name, name) == 0 && mock_nvs[i].exists) {
            return mock_nvs[i].key_count == 0;
        }
    }
    return true;
}

/* Simulate factory_reset_trigger logic */
static void simulate_factory_reset(void) {
    for (int i = 0; i < mock_nvs_count; i++) {
        if (!mock_nvs[i].exists) continue;
        
        bool in_whitelist = false;
        for (int j = 0; j < 3; j++) {
            if (strcmp(mock_nvs[i].name, NVS_NAMESPACE_WHITELIST[j]) == 0) {
                in_whitelist = true;
                break;
            }
        }
        
        if (in_whitelist) {
            /* Erase namespace */
            mock_nvs[i].key_count = 0;
        }
        /* Non-whitelisted namespaces are preserved */
    }
}

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

/* Test: Whitelisted namespaces are erased */
void test_whitelisted_namespaces_erased(void) {
    printf("\n[test_whitelisted_namespaces_erased]\n");
    
    mock_nvs_init();
    
    /* Create whitelisted namespaces with data */
    mock_nvs_create_namespace("wifi_mgr");
    mock_nvs_set_key("wifi_mgr", "ssid", "MyNetwork");
    mock_nvs_set_key("wifi_mgr", "password", "secret123");
    
    mock_nvs_create_namespace("config_mgr");
    mock_nvs_set_key("config_mgr", "device_id", "dev_001");
    
    mock_nvs_create_namespace("ota");
    mock_nvs_set_key("ota", "last_version", "1.2.3");
    
    /* Trigger factory reset */
    simulate_factory_reset();
    
    /* Verify whitelisted namespaces are erased */
    TEST_ASSERT(mock_nvs_namespace_empty("wifi_mgr"), "wifi_mgr namespace erased");
    TEST_ASSERT(mock_nvs_namespace_empty("config_mgr"), "config_mgr namespace erased");
    TEST_ASSERT(mock_nvs_namespace_empty("ota"), "ota namespace erased");
}

/* Test: Non-whitelisted namespaces are preserved */
void test_non_whitelisted_namespaces_preserved(void) {
    printf("\n[test_non_whitelisted_namespaces_preserved]\n");
    
    mock_nvs_init();
    
    /* Create non-whitelisted namespaces with data */
    mock_nvs_create_namespace("user_prefs");
    mock_nvs_set_key("user_prefs", "theme", "dark");
    mock_nvs_set_key("user_prefs", "language", "zh-CN");
    
    mock_nvs_create_namespace("app_data");
    mock_nvs_set_key("app_data", "cache", "cached_value");
    
    mock_nvs_create_namespace("logs");
    mock_nvs_set_key("logs", "last_log", "log_entry");
    
    /* Trigger factory reset */
    simulate_factory_reset();
    
    /* Verify non-whitelisted namespaces are preserved */
    TEST_ASSERT(!mock_nvs_namespace_empty("user_prefs"), "user_prefs namespace preserved");
    TEST_ASSERT(!mock_nvs_namespace_empty("app_data"), "app_data namespace preserved");
    TEST_ASSERT(!mock_nvs_namespace_empty("logs"), "logs namespace preserved");
}

/* Test: Mixed namespaces - only whitelisted erased */
void test_mixed_namespaces(void) {
    printf("\n[test_mixed_namespaces]\n");
    
    mock_nvs_init();
    
    /* Create mix of whitelisted and non-whitelisted */
    mock_nvs_create_namespace("wifi_mgr");
    mock_nvs_set_key("wifi_mgr", "ssid", "TestNetwork");
    
    mock_nvs_create_namespace("user_data");
    mock_nvs_set_key("user_data", "profile", "user_profile");
    
    mock_nvs_create_namespace("config_mgr");
    mock_nvs_set_key("config_mgr", "setting", "value");
    
    mock_nvs_create_namespace("custom_app");
    mock_nvs_set_key("custom_app", "data", "important");
    
    mock_nvs_create_namespace("ota");
    mock_nvs_set_key("ota", "version", "2.0.0");
    
    /* Trigger factory reset */
    simulate_factory_reset();
    
    /* Verify correct behavior */
    TEST_ASSERT(mock_nvs_namespace_empty("wifi_mgr"), "wifi_mgr erased (whitelisted)");
    TEST_ASSERT(mock_nvs_namespace_empty("config_mgr"), "config_mgr erased (whitelisted)");
    TEST_ASSERT(mock_nvs_namespace_empty("ota"), "ota erased (whitelisted)");
    TEST_ASSERT(!mock_nvs_namespace_empty("user_data"), "user_data preserved (not whitelisted)");
    TEST_ASSERT(!mock_nvs_namespace_empty("custom_app"), "custom_app preserved (not whitelisted)");
}

/* Test: Empty whitelisted namespaces don't cause errors */
void test_empty_whitelisted_namespaces(void) {
    printf("\n[test_empty_whitelisted_namespaces]\n");
    
    mock_nvs_init();
    
    /* Create whitelisted namespaces but leave them empty */
    mock_nvs_create_namespace("wifi_mgr");
    mock_nvs_create_namespace("config_mgr");
    mock_nvs_create_namespace("ota");
    
    /* Add some non-whitelisted data */
    mock_nvs_create_namespace("other");
    mock_nvs_set_key("other", "key", "value");
    
    /* Trigger factory reset - should not crash */
    simulate_factory_reset();
    
    /* Verify no errors */
    TEST_ASSERT(mock_nvs_namespace_empty("wifi_mgr"), "wifi_mgr still empty");
    TEST_ASSERT(mock_nvs_namespace_empty("config_mgr"), "config_mgr still empty");
    TEST_ASSERT(mock_nvs_namespace_empty("ota"), "ota still empty");
    TEST_ASSERT(!mock_nvs_namespace_empty("other"), "other namespace preserved");
}

/* Test: Whitelist is exactly 3 entries */
void test_whitelist_size(void) {
    printf("\n[test_whitelist_size]\n");
    
    int count = sizeof(NVS_NAMESPACE_WHITELIST) / sizeof(NVS_NAMESPACE_WHITELIST[0]);
    TEST_ASSERT(count == 3, "Whitelist contains exactly 3 namespaces");
    TEST_ASSERT(strcmp(NVS_NAMESPACE_WHITELIST[0], "wifi_mgr") == 0, "First entry is wifi_mgr");
    TEST_ASSERT(strcmp(NVS_NAMESPACE_WHITELIST[1], "config_mgr") == 0, "Second entry is config_mgr");
    TEST_ASSERT(strcmp(NVS_NAMESPACE_WHITELIST[2], "ota") == 0, "Third entry is ota");
}

int main(void) {
    printf("=== Factory Reset Namespace Whitelist Tests ===\n");
    
    test_whitelisted_namespaces_erased();
    test_non_whitelisted_namespaces_preserved();
    test_mixed_namespaces();
    test_empty_whitelisted_namespaces();
    test_whitelist_size();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d/%d\n", tests_passed, tests_run);
    
    return (tests_passed == tests_run) ? 0 : 1;
}
