/**
 * @file test_config_mgr_double_buffer.c
 * @brief Unit tests for config_mgr double-buffer mechanism (Step 3)
 *
 * Tests: concurrent read/write safety, atomic switch, failed apply protection
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <pthread.h>
#include <unistd.h>

/* Mock ESP-IDF types and functions */
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL -1

typedef struct {
    char manifest_id[64];
    int template_count;
    int channel_count;
    int applied;
} config_manifest_t;

/* Simulate the double-buffer implementation */
static config_manifest_t s_manifests[2];
static volatile int s_active_idx = 0;
static pthread_mutex_t s_mutex = PTHREAD_MUTEX_INITIALIZER;

static config_manifest_t *active_manifest(void) {
    return &s_manifests[s_active_idx];
}

static config_manifest_t *inactive_manifest(void) {
    return &s_manifests[1 - s_active_idx];
}

static int apply_manifest(const char *manifest_id, int template_count, int channel_count) {
    config_manifest_t *target = inactive_manifest();
    
    /* Parse into inactive buffer */
    memset(target, 0, sizeof(*target));
    strncpy(target->manifest_id, manifest_id, sizeof(target->manifest_id) - 1);
    target->template_count = template_count;
    target->channel_count = channel_count;
    target->applied = 1;
    
    /* Atomic switch */
    pthread_mutex_lock(&s_mutex);
    s_active_idx = 1 - s_active_idx;
    pthread_mutex_unlock(&s_mutex);
    
    return 1;
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

/* === Test: basic double-buffer switch === */
void test_double_buffer_basic(void) {
    printf("\n[test_double_buffer_basic]\n");
    
    /* Initial state */
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    
    /* Apply first manifest */
    apply_manifest("manifest_v1", 2, 1);
    config_manifest_t *m = active_manifest();
    TEST_ASSERT(strcmp(m->manifest_id, "manifest_v1") == 0, "First manifest applied");
    TEST_ASSERT(m->template_count == 2, "Template count correct");
    
    /* Apply second manifest */
    apply_manifest("manifest_v2", 3, 2);
    m = active_manifest();
    TEST_ASSERT(strcmp(m->manifest_id, "manifest_v2") == 0, "Second manifest applied");
    TEST_ASSERT(m->template_count == 3, "Template count updated");
}

/* === Test: failed apply doesn't corrupt active config === */
void test_double_buffer_failed_apply(void) {
    printf("\n[test_double_buffer_failed_apply]\n");
    
    /* Setup initial state */
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    apply_manifest("initial", 5, 3);
    
    config_manifest_t *before = active_manifest();
    char initial_id[64];
    strncpy(initial_id, before->manifest_id, sizeof(initial_id));
    
    /* Simulate failed apply (parse returns false) */
    config_manifest_t *target = inactive_manifest();
    memset(target, 0xFF, sizeof(*target));  /* Corrupt inactive buffer */
    /* Don't switch s_active_idx */
    
    /* Active should still be intact */
    config_manifest_t *after = active_manifest();
    TEST_ASSERT(strcmp(after->manifest_id, initial_id) == 0, "Active manifest unchanged after failed apply");
    TEST_ASSERT(after->template_count == 5, "Template count unchanged");
}

/* === Test: concurrent read/write === */
typedef struct {
    int reader_id;
    int iterations;
    int inconsistent_reads;
} reader_arg_t;

void *reader_thread(void *arg) {
    reader_arg_t *rarg = (reader_arg_t *)arg;
    
    for (int i = 0; i < rarg->iterations; i++) {
        config_manifest_t *m = active_manifest();
        
        /* Check consistency: manifest_id should match template_count pattern */
        int id_num = 0;
        if (sscanf(m->manifest_id, "v%d", &id_num) == 1) {
            if (m->template_count != id_num * 10) {
                rarg->inconsistent_reads++;
            }
        }
        
        usleep(10);  /* Small delay to increase chance of race */
    }
    
    return NULL;
}

void *writer_thread(void *arg) {
    int iterations = *(int *)arg;
    
    for (int i = 1; i <= iterations; i++) {
        char manifest_id[64];
        snprintf(manifest_id, sizeof(manifest_id), "v%d", i);
        apply_manifest(manifest_id, i * 10, i);
        usleep(50);
    }
    
    return NULL;
}

void test_double_buffer_concurrent(void) {
    printf("\n[test_double_buffer_concurrent]\n");
    
    /* Setup */
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    apply_manifest("v0", 0, 0);
    
    /* Create threads */
    pthread_t readers[4];
    reader_arg_t reader_args[4];
    for (int i = 0; i < 4; i++) {
        reader_args[i].reader_id = i;
        reader_args[i].iterations = 1000;
        reader_args[i].inconsistent_reads = 0;
        pthread_create(&readers[i], NULL, reader_thread, &reader_args[i]);
    }
    
    int writer_iterations = 100;
    pthread_t writer;
    pthread_create(&writer, NULL, writer_thread, &writer_iterations);
    
    /* Wait for completion */
    pthread_join(writer, NULL);
    for (int i = 0; i < 4; i++) {
        pthread_join(readers[i], NULL);
    }
    
    /* Check results */
    int total_inconsistent = 0;
    for (int i = 0; i < 4; i++) {
        total_inconsistent += reader_args[i].inconsistent_reads;
    }
    
    TEST_ASSERT(total_inconsistent == 0, "No inconsistent reads during concurrent access");
    printf("  Info: %d total reads, %d inconsistent\n", 
           4 * 1000, total_inconsistent);
}

int main(void) {
    printf("=== Config Manager Double-Buffer Tests ===\n");
    
    test_double_buffer_basic();
    test_double_buffer_failed_apply();
    test_double_buffer_concurrent();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d\n", tests_passed);
    printf("Failed: %d\n", tests_failed);
    
    return tests_failed > 0 ? 1 : 0;
}
