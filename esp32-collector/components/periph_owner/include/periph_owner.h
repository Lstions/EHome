#ifndef PERIPH_OWNER_H
#define PERIPH_OWNER_H

#include <stdbool.h>
#include "esp_err.h"
#include "config_mgr.h"

#ifndef ESP_ERR_PIN_CONFLICT
#define ESP_ERR_PIN_CONFLICT 0x7101
#endif

typedef enum {
    PERIPH_PIN_OWNER_GPIO = 1,
    PERIPH_PIN_OWNER_PWM = 2,
} periph_pin_owner_t;

#define PERIPH_OWNER_PIN_COUNT 64
typedef struct {
    uint8_t owner[PERIPH_OWNER_PIN_COUNT];
    int8_t owner_id[PERIPH_OWNER_PIN_COUNT];
    bool bus_pins[PERIPH_OWNER_PIN_COUNT];
} periph_owner_snapshot_t;

/* Serialize a whole peripheral/config transaction against runtime commands.
 * Acquire this gate before any controller/owner operation and release it only
 * after controller state and the owner map are mutually consistent. The gate
 * is intentionally separate from controller and owner-map mutexes; callers
 * must acquire it first to keep one lock order and avoid recursive deadlocks. */
esp_err_t periph_owner_transaction_begin(void);
void periph_owner_transaction_end(void);

esp_err_t periph_owner_claim(int pin, periph_pin_owner_t owner, int owner_id);
void periph_owner_release(int pin, periph_pin_owner_t owner, int owner_id);
bool periph_owner_is_claimed_by(int pin, periph_pin_owner_t owner, int owner_id);
bool periph_owner_manifest_bus_uses_pin(const config_manifest_t *manifest, int pin);
esp_err_t periph_owner_validate_manifest(const config_manifest_t *manifest);
esp_err_t periph_owner_set_bus_manifest(const config_manifest_t *manifest);
esp_err_t periph_owner_replace_manifest(const config_manifest_t *manifest);
esp_err_t periph_owner_snapshot(periph_owner_snapshot_t *snapshot);
esp_err_t periph_owner_restore(const periph_owner_snapshot_t *snapshot);

#endif