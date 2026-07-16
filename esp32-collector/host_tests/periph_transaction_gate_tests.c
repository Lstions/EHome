#include <errno.h>
#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>

#include "config_apply_transaction.h"
#include "periph_owner.h"
#include "freertos/semphr.h"

#define CHECK(expr) do { \
    if (!(expr)) { \
        fprintf(stderr, "CHECK failed at %s:%d: %s\n", __FILE__, __LINE__, #expr); \
        abort(); \
    } \
} while (0)

typedef struct {
    pthread_mutex_t mutex;
} host_mutex_t;

static host_mutex_t s_mutexes[4];
static int s_mutex_count;
static pthread_mutex_t s_test_mutex = PTHREAD_MUTEX_INITIALIZER;
static pthread_cond_t s_test_cond = PTHREAD_COND_INITIALIZER;
static bool s_command_attempted;
static bool s_command_blocked;
static bool s_command_entered;
static bool s_apply_entered;
static bool s_allow_apply;
static int s_controller_state = 1;
static int s_owner_state = 1;
static int s_snapshot_controller;
static int s_snapshot_owner;

SemaphoreHandle_t xSemaphoreCreateMutex(void)
{
    CHECK(s_mutex_count < (int)(sizeof(s_mutexes) / sizeof(s_mutexes[0])));
    host_mutex_t *mutex = &s_mutexes[s_mutex_count++];
    CHECK(pthread_mutex_init(&mutex->mutex, NULL) == 0);
    return mutex;
}

int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{
    (void)ticks;
    host_mutex_t *mutex = semaphore;
    int err = pthread_mutex_trylock(&mutex->mutex);
    if (err == EBUSY) {
        pthread_mutex_lock(&s_test_mutex);
        s_command_blocked = true;
        pthread_cond_broadcast(&s_test_cond);
        pthread_mutex_unlock(&s_test_mutex);
        err = pthread_mutex_lock(&mutex->mutex);
    }
    return err == 0;
}

int xSemaphoreGive(SemaphoreHandle_t semaphore)
{
    host_mutex_t *mutex = semaphore;
    return pthread_mutex_unlock(&mutex->mutex) == 0;
}

static esp_err_t gate_begin(void *ctx)
{
    (void)ctx;
    return periph_owner_transaction_begin();
}

static void gate_end(void *ctx)
{
    (void)ctx;
    periph_owner_transaction_end();
}

static esp_err_t snapshot(void *ctx)
{
    (void)ctx;
    s_snapshot_controller = s_controller_state;
    s_snapshot_owner = s_owner_state;
    return ESP_OK;
}

static esp_err_t prepare(void *ctx)
{
    (void)ctx;
    return ESP_OK;
}

static esp_err_t apply_dma(void *ctx, const config_manifest_t *manifest)
{
    (void)ctx;
    (void)manifest;
    pthread_mutex_lock(&s_test_mutex);
    s_apply_entered = true;
    pthread_cond_broadcast(&s_test_cond);
    while (!s_allow_apply) pthread_cond_wait(&s_test_cond, &s_test_mutex);
    pthread_mutex_unlock(&s_test_mutex);
    return ESP_OK;
}

static esp_err_t apply_peripherals(void *ctx, const config_manifest_t *manifest)
{
    (void)ctx;
    (void)manifest;
    s_controller_state = 3;
    s_owner_state = 3;
    return ESP_OK;
}

static esp_err_t no_op_apply(void *ctx, const config_manifest_t *manifest)
{
    (void)ctx;
    (void)manifest;
    return ESP_OK;
}

static bool commit(void *ctx)
{
    (void)ctx;
    return true;
}

static esp_err_t no_op(void *ctx)
{
    (void)ctx;
    return ESP_OK;
}

static const config_apply_ops_t OPS = {
    .begin_transaction = gate_begin,
    .end_transaction = gate_end,
    .snapshot = snapshot,
    .prepare = prepare,
    .apply_dma = apply_dma,
    .apply_peripherals = apply_peripherals,
    .apply_buses = no_op_apply,
    .apply_scheduler = no_op_apply,
    .apply_log_stream = no_op_apply,
    .commit_manifest = commit,
    .stop_scheduler = no_op,
    .cleanup_buses = no_op,
    .restore_dma = no_op,
    .restore_peripherals = no_op,
    .restore_log_stream = no_op,
    .enter_safe_state = no_op,
};

static void *transaction_thread(void *arg)
{
    const config_manifest_t *manifest = arg;
    config_apply_result_t result = config_apply_transaction_execute(
        &OPS, NULL, NULL, manifest);
    return (void *)(intptr_t)result;
}

static void *command_thread(void *arg)
{
    (void)arg;
    pthread_mutex_lock(&s_test_mutex);
    s_command_attempted = true;
    pthread_cond_broadcast(&s_test_cond);
    pthread_mutex_unlock(&s_test_mutex);

    CHECK(periph_owner_transaction_begin() == ESP_OK);
    pthread_mutex_lock(&s_test_mutex);
    s_command_entered = true;
    pthread_cond_broadcast(&s_test_cond);
    pthread_mutex_unlock(&s_test_mutex);
    /* This pair represents one real command's controller + owner mutation.
     * The test intentionally makes the two writes distinct so a torn aggregate
     * snapshot would be observable without the shared gate. */
    s_controller_state = 2;
    s_owner_state = 2;
    periph_owner_transaction_end();
    return NULL;
}

static void test_runtime_command_cannot_interleave_transaction_snapshot_and_apply(void)
{
    static const config_manifest_t manifest;
    pthread_t transaction;
    pthread_t command;

    CHECK(pthread_create(&transaction, NULL, transaction_thread, (void *)&manifest) == 0);
    pthread_mutex_lock(&s_test_mutex);
    while (!s_apply_entered) pthread_cond_wait(&s_test_cond, &s_test_mutex);
    pthread_mutex_unlock(&s_test_mutex);

    CHECK(pthread_create(&command, NULL, command_thread, NULL) == 0);
    pthread_mutex_lock(&s_test_mutex);
    while (!s_command_attempted || !s_command_blocked) {
        pthread_cond_wait(&s_test_cond, &s_test_mutex);
    }
    CHECK(!s_command_entered);
    CHECK(s_snapshot_controller == 1);
    CHECK(s_snapshot_owner == 1);
    CHECK(s_controller_state == 1);
    CHECK(s_owner_state == 1);
    s_allow_apply = true;
    pthread_cond_broadcast(&s_test_cond);
    pthread_mutex_unlock(&s_test_mutex);

    void *result = NULL;
    CHECK(pthread_join(transaction, &result) == 0);
    CHECK((config_apply_result_t)(intptr_t)result == CONFIG_APPLY_OK);
    CHECK(pthread_join(command, NULL) == 0);
    CHECK(s_command_entered);
    CHECK(s_controller_state == 2);
    CHECK(s_owner_state == 2);
}

int main(void)
{
    test_runtime_command_cannot_interleave_transaction_snapshot_and_apply();
    puts("periph_transaction_gate_tests: PASS");
    return 0;
}
