/**
 * @file test_wifi_provisioning_security.c
 * @brief Unit tests for WiFi provisioning security (Step 7)
 *
 * Tests: URL decode injection protection, input validation, buffer overflow
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <ctype.h>

/* Mock ESP-IDF types */
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL -1
#define ESP_ERR_INVALID_ARG -2
#define ESP_ERR_NO_MEM -3

/* Copy of url_decode from wifi_provisioning.c */
static int url_decode(char *dst, size_t dst_len, const char *src, size_t src_len) {
    if (!dst || !src || dst_len == 0) return -1;
    
    size_t i = 0, j = 0;
    while (i < src_len && j < dst_len - 1) {
        if (src[i] == '%') {
            if (i + 2 >= src_len) return -1;  /* Incomplete percent encoding */
            char hex[3] = {src[i + 1], src[i + 2], '\0'};
            char *endptr;
            long val = strtol(hex, &endptr, 16);
            if (endptr != hex + 2) return -1;
            dst[j++] = (char)val;
            i += 3;
        } else if (src[i] == '+') {
            dst[j++] = ' ';
            i++;
        } else {
            dst[j++] = src[i++];
        }
    }
    dst[j] = '\0';
    return (int)j;
}

/* Copy of parse_form_data from wifi_provisioning.c (simplified) */
static int parse_form_data(const char *form_data, char *ssid, size_t ssid_len,
                          char *password, size_t password_len) {
    if (!form_data || !ssid || !password) return -1;
    
    const char *ssid_start = strstr(form_data, "ssid=");
    if (!ssid_start) return -1;
    ssid_start += 5;
    
    const char *ssid_end = strchr(ssid_start, '&');
    if (!ssid_end) ssid_end = ssid_start + strlen(ssid_start);
    
    size_t ssid_raw_len = ssid_end - ssid_start;
    if (ssid_raw_len >= ssid_len * 3) return -1;  /* URL encoding can triple size */
    
    if (url_decode(ssid, ssid_len, ssid_start, ssid_raw_len) < 0) return -1;
    
    const char *pwd_start = strstr(form_data, "password=");
    if (pwd_start) {
        pwd_start += 9;
        const char *pwd_end = strchr(pwd_start, '&');
        if (!pwd_end) pwd_end = pwd_start + strlen(pwd_start);
        
        size_t pwd_raw_len = pwd_end - pwd_start;
        if (pwd_raw_len >= password_len * 3) return -1;
        
        if (url_decode(password, password_len, pwd_start, pwd_raw_len) < 0) return -1;
    } else {
        password[0] = '\0';
    }
    
    return 0;
}

static int tests_passed = 0;
static int tests_failed = 0;

#define TEST_ASSERT(cond, msg) do { \
    if (cond) { \
        printf("  PASS: %s\n", msg); \
        tests_passed++; \
    } else { \
        printf("  FAIL: %s (line %d)\n", msg, __LINE__); \
        tests_failed++; \
    } \
} while(0)

/* === Test: normal URL decode === */
void test_url_decode_normal(void) {
    printf("\n[test_url_decode_normal]\n");
    
    char out[64];
    int len;
    
    len = url_decode(out, sizeof(out), "Hello%20World", 13);
    TEST_ASSERT(len >= 0 && strcmp(out, "Hello World") == 0, "Decode %20 to space");
    
    len = url_decode(out, sizeof(out), "test+value", 10);
    TEST_ASSERT(len >= 0 && strcmp(out, "test value") == 0, "Decode + to space");
    
    len = url_decode(out, sizeof(out), "100%25", 6);
    TEST_ASSERT(len >= 0 && strcmp(out, "100%") == 0, "Decode %25 to %");
}

/* === Test: URL decode injection attacks === */
void test_url_decode_injection(void) {
    printf("\n[test_url_decode_injection]\n");
    
    char out[64];
    int len;
    
    /* Incomplete percent encoding */
    len = url_decode(out, sizeof(out), "test%", 5);
    TEST_ASSERT(len < 0, "Reject incomplete percent encoding");
    
    /* Invalid hex digits */
    len = url_decode(out, sizeof(out), "test%GG", 7);
    TEST_ASSERT(len < 0, "Reject invalid hex digits");
    
    /* Single digit hex */
    len = url_decode(out, sizeof(out), "test%2", 6);
    TEST_ASSERT(len < 0, "Reject single digit hex");
}

/* === Test: buffer overflow protection === */
void test_url_decode_overflow(void) {
    printf("\n[test_url_decode_overflow]\n");
    
    char out[8];
    int len;
    
    /* String longer than buffer */
    len = url_decode(out, sizeof(out), "very_long_string", 16);
    TEST_ASSERT(len >= 0, "Should truncate long string");
    TEST_ASSERT(strlen(out) <= 7, "Should fit in buffer");
    TEST_ASSERT(out[7] == '\0' || strlen(out) < 8, "Should be NUL-terminated");
}

/* === Test: form data parsing === */
void test_parse_form_data_normal(void) {
    printf("\n[test_parse_form_data_normal]\n");
    
    char ssid[32], password[64];
    int ret;
    
    /* Normal form data */
    ret = parse_form_data("ssid=MyNetwork&password=secret123", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret == 0, "Parse normal form data");
    TEST_ASSERT(strcmp(ssid, "MyNetwork") == 0, "SSID parsed correctly");
    TEST_ASSERT(strcmp(password, "secret123") == 0, "Password parsed correctly");
    
    /* URL-encoded SSID */
    ret = parse_form_data("ssid=My%20Network&password=pass", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret == 0, "Parse URL-encoded SSID");
    TEST_ASSERT(strcmp(ssid, "My Network") == 0, "SSID decoded correctly");
    
    /* No password (open network) */
    ret = parse_form_data("ssid=OpenNetwork", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret == 0, "Parse form without password");
    TEST_ASSERT(strcmp(ssid, "OpenNetwork") == 0, "SSID parsed");
    TEST_ASSERT(strlen(password) == 0, "Password empty for open network");
}

/* === Test: form data injection attacks === */
void test_parse_form_data_injection(void) {
    printf("\n[test_parse_form_data_injection]\n");
    
    char ssid[32], password[64];
    int ret;
    
    /* Missing ssid parameter */
    ret = parse_form_data("password=secret", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret < 0, "Reject missing ssid");
    
    /* Extremely long SSID */
    char long_form[512];
    snprintf(long_form, sizeof(long_form), "ssid=");
    for (int i = 5; i < 500; i++) long_form[i] = 'A';
    long_form[500] = '&';
    long_form[501] = '\0';
    strcat(long_form, "password=test");
    
    ret = parse_form_data(long_form, ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret < 0, "Reject extremely long SSID");
    
    /* NULL parameters */
    ret = parse_form_data(NULL, ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret < 0, "Reject NULL form_data");
    
    ret = parse_form_data("ssid=test", NULL, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret < 0, "Reject NULL ssid buffer");
}

/* === Test: special characters === */
void test_parse_form_data_special(void) {
    printf("\n[test_parse_form_data_special]\n");
    
    char ssid[32], password[64];
    int ret;
    
    /* SSID with special characters */
    ret = parse_form_data("ssid=My%26Network&password=pass%3Dword", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret == 0, "Parse special characters");
    TEST_ASSERT(strcmp(ssid, "My&Network") == 0, "Ampersand decoded");
    TEST_ASSERT(strcmp(password, "pass=word") == 0, "Equals sign decoded");
    
    /* Chinese characters (UTF-8) */
    ret = parse_form_data("ssid=%E4%B8%AD%E6%96%87&password=test", ssid, sizeof(ssid), password, sizeof(password));
    TEST_ASSERT(ret == 0, "Parse UTF-8 encoded SSID");
}

int main(void) {
    printf("=== WiFi Provisioning Security Tests ===\n");
    
    test_url_decode_normal();
    test_url_decode_injection();
    test_url_decode_overflow();
    test_parse_form_data_normal();
    test_parse_form_data_injection();
    test_parse_form_data_special();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d\n", tests_passed);
    printf("Failed: %d\n", tests_failed);
    
    return tests_failed > 0 ? 1 : 0;
}
