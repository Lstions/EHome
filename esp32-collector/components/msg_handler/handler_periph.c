/**
 * @file handler_periph.c
 * @brief Peripheral Command (PeriphCmd 0x1B) handler — GPIO + PWM
 *
 * Threading model:
 *   GPIO: executed directly in MQTT callback (μs-level ops)
 *   PWM:  forwarded to periph_worker task via periph_cmd_queue
 *   PeriphRsp: NOT sent directly in MQTT callback (deadlock risk).
 *              Results go to periph_rsp_queue → periph_rsp_task → publish.
 *
 * The periph_rsp_task (created in main.c) drains periph_rsp_queue and calls
 * msg_handler_publish() which routes through transport/MQTT safely.
 */

#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "gpio_ctrl.h"
#include "pwm_ctrl.h"
#include "periph_owner.h"
#include "periph_config_apply.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include <string.h>

#define TAG "PERIPH_H"

/* === Queue depths === */
#define PERIPH_CMD_QUEUE_DEPTH   8
#define PERIPH_RSP_QUEUE_DEPTH   8

/* === PeriphRsp item (sent to periph_rsp_queue) === */
typedef struct {
    uint32_t request_id;
    bool     success;
    uint32_t value;
    uint8_t  error_code;
    uint8_t  periph_type;
    uint8_t  resource_id;
    uint8_t  action;
    bool     running;
} periph_rsp_item_t;

/* === PeriphCmd item (sent to periph_cmd_queue for PWM async execution) === */
typedef struct {
    uint32_t request_id;
    uint8_t  periph_type;
    uint8_t  resource_id;
    uint8_t  action;
    uint32_t value;
    uint8_t  config[16];
    size_t   config_len;
} periph_cmd_item_t;

/* === Queues === */
static QueueHandle_t s_periph_cmd_queue = NULL;  /* PWM commands → periph_worker */
static QueueHandle_t s_periph_rsp_queue = NULL;  /* Results → periph_rsp_task */
static SemaphoreHandle_t s_dedup_mutex = NULL;
static TaskHandle_t  s_periph_worker_task = NULL;
static TaskHandle_t  s_periph_rsp_task = NULL;
static bool s_initialized = false;
typedef struct {
    uint32_t request_id;
    uint8_t periph_type, resource_id, action;
    uint32_t command_value;
    uint8_t config[16];
    uint8_t config_len;
    bool completed;
    bool success, running;
    uint32_t value;
    uint8_t error_code;
} dedup_entry_t;
static dedup_entry_t s_dedup[64];
static uint8_t s_dedup_next;


/* === Forward declarations === */
static void periph_worker_task(void *pv);
static void periph_rsp_task_func(void *pv);
static void send_periph_rsp(uint32_t request_id, bool success, uint32_t value,
                             uint8_t error_code, uint8_t periph_type,
                             uint8_t resource_id, uint8_t action);
static void process_gpio_cmd(uint32_t request_id, uint8_t pin, uint8_t action,
                              uint32_t value, const uint8_t *config, size_t config_len);
static void process_pwm_cmd_impl(periph_cmd_item_t *item);

/* ========================================================================
 *  Public API — called by msg_handler.c dispatch
 * ======================================================================== */

/**
 * @brief Initialize periph handler queues and tasks.
 * Called from main.c during init.
 */
esp_err_t handler_periph_init(void)
{
    if (s_initialized) return ESP_OK;

    /* Create the shared transaction gate before MQTT/config and worker tasks can
     * race on first use. */
    if (periph_owner_transaction_begin() != ESP_OK) {
        ESP_LOGE(TAG, "Failed to initialize peripheral transaction gate");
        return ESP_FAIL;
    }
    periph_owner_transaction_end();

    s_periph_cmd_queue = xQueueCreate(PERIPH_CMD_QUEUE_DEPTH, sizeof(periph_cmd_item_t));
    s_periph_rsp_queue = xQueueCreate(PERIPH_RSP_QUEUE_DEPTH, sizeof(periph_rsp_item_t));
	s_dedup_mutex = xSemaphoreCreateMutex();

    if (!s_periph_cmd_queue || !s_periph_rsp_queue || !s_dedup_mutex) {
        ESP_LOGE(TAG, "Failed to create periph queues");
		if (s_periph_cmd_queue) { vQueueDelete(s_periph_cmd_queue); s_periph_cmd_queue = NULL; }
		if (s_periph_rsp_queue) { vQueueDelete(s_periph_rsp_queue); s_periph_rsp_queue = NULL; }
		if (s_dedup_mutex) { vSemaphoreDelete(s_dedup_mutex); s_dedup_mutex = NULL; }
        return ESP_ERR_NO_MEM;
    }

    if (xTaskCreate(periph_worker_task, "periph_worker", 4096, NULL, 4,
                    &s_periph_worker_task) != pdPASS) {
        ESP_LOGE(TAG, "Failed to create periph worker task");
		vQueueDelete(s_periph_cmd_queue); s_periph_cmd_queue = NULL;
		vQueueDelete(s_periph_rsp_queue); s_periph_rsp_queue = NULL;
		vSemaphoreDelete(s_dedup_mutex); s_dedup_mutex = NULL;
        return ESP_ERR_NO_MEM;
    }
    if (xTaskCreate(periph_rsp_task_func, "periph_rsp", 4096, NULL, 4,
                    &s_periph_rsp_task) != pdPASS) {
        ESP_LOGE(TAG, "Failed to create periph response task");
        vTaskDelete(s_periph_worker_task);
        s_periph_worker_task = NULL;
		vQueueDelete(s_periph_cmd_queue); s_periph_cmd_queue = NULL;
		vQueueDelete(s_periph_rsp_queue); s_periph_rsp_queue = NULL;
		vSemaphoreDelete(s_dedup_mutex); s_dedup_mutex = NULL;
        return ESP_ERR_NO_MEM;
    }

    s_initialized = true;
    ESP_LOGI(TAG, "Periph handler initialized (cmd_queue=%p, rsp_queue=%p)",
             s_periph_cmd_queue, s_periph_rsp_queue);
    return ESP_OK;
}

/**
 * @brief Process incoming PeriphCmd (0x1B).
 * Called from msg_handler_process() in MQTT callback context.
 *
 * GPIO: executed directly (μs-level, no blocking).
 * PWM:  forwarded to periph_worker via s_periph_cmd_queue.
 */
void handler_periph_process(frame_decoder_t *dec)
{
    uint32_t request_id = 0;
    uint8_t  periph_type = 0;
    uint8_t  resource_id = 0;
    uint8_t  action = 0;
    uint32_t value = 0;
    const uint8_t *config_data = NULL;
    size_t   config_len = 0;
    bool have_request_id = false, have_type = false, have_resource = false, have_action = false;
	uint32_t seen_fields = 0;

    frame_field_t field;
    frame_err_t err;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
		if (field.field_num < 1 || field.field_num > 6 || (seen_fields & (1U << field.field_num))) {
			ESP_LOGW(TAG, "Rejecting duplicate/unknown PeriphCmd field %u", field.field_num);
			return;
		}
		seen_fields |= 1U << field.field_num;
        switch (field.field_num) {
        case PERIPH_CMD_F_REQUEST_ID:
            if (field.wire_type != WIRE_VARINT) return;
			if (field.value.varint == 0 || field.value.varint > UINT32_MAX) return;
            request_id = (uint32_t)field.value.varint;
            have_request_id = true;
            break;
        case PERIPH_CMD_F_PERIPH_TYPE:
            if (field.wire_type != WIRE_VARINT) return;
			if (field.value.varint > UINT8_MAX) return;
            periph_type = (uint8_t)field.value.varint;
            have_type = true;
            break;
        case PERIPH_CMD_F_RESOURCE_ID:
            if (field.wire_type != WIRE_VARINT) return;
			if (field.value.varint > UINT8_MAX) return;
            resource_id = (uint8_t)field.value.varint;
            have_resource = true;
            break;
        case PERIPH_CMD_F_ACTION:
            if (field.wire_type != WIRE_VARINT) return;
			if (field.value.varint > UINT8_MAX) return;
            action = (uint8_t)field.value.varint;
            have_action = true;
            break;
        case PERIPH_CMD_F_VALUE:
            if (field.wire_type != WIRE_VARINT) return;
			if (field.value.varint > UINT32_MAX) return;
            value = (uint32_t)field.value.varint;
            break;
        case PERIPH_CMD_F_CONFIG:
			if (field.wire_type != WIRE_LENGTH_DELIMITED) return;
			if (field.value.bytes.len > 16) return;
			config_data = field.value.bytes.ptr;
			config_len = field.value.bytes.len;
            break;
        }
    }

    if (err != FRAME_DONE) {
        ESP_LOGW(TAG, "Rejecting malformed PeriphCmd: decode error %d", err);
        return;
    }
    if (!have_request_id || !have_type || !have_resource || !have_action) {
        ESP_LOGW(TAG, "Rejecting PeriphCmd missing required fields");
        return;
    }
    xSemaphoreTake(s_dedup_mutex, portMAX_DELAY);

    for (size_t i = 0; i < sizeof(s_dedup) / sizeof(s_dedup[0]); i++) {
        if (s_dedup[i].request_id != request_id) continue;
        if (s_dedup[i].periph_type != periph_type || s_dedup[i].resource_id != resource_id ||
            s_dedup[i].action != action || s_dedup[i].command_value != value ||
            s_dedup[i].config_len != config_len ||
            (config_len > 0 && memcmp(s_dedup[i].config, config_data, config_len) != 0)) {
            xSemaphoreGive(s_dedup_mutex);
            ESP_LOGW(TAG, "Rejecting PeriphCmd request_id collision=%lu", (unsigned long)request_id);
            return;
        }
        if (s_dedup[i].completed) {
            periph_rsp_item_t cached = {
                .request_id = request_id, .success = s_dedup[i].success,
                .value = s_dedup[i].value, .error_code = s_dedup[i].error_code,
                .periph_type = periph_type, .resource_id = resource_id,
                .action = action, .running = s_dedup[i].running,
            };
            xSemaphoreGive(s_dedup_mutex);
            if (xQueueSend(s_periph_rsp_queue, &cached, pdMS_TO_TICKS(100)) != pdTRUE) {
                ESP_LOGW(TAG, "Cached PeriphRsp replay queue full, request_id=%lu", (unsigned long)request_id);
            }
        } else {
            xSemaphoreGive(s_dedup_mutex);
        }
        ESP_LOGW(TAG, "Ignoring duplicate PeriphCmd request_id=%lu", (unsigned long)request_id);
        return;
    }
    size_t slot = s_dedup_next++ % (sizeof(s_dedup) / sizeof(s_dedup[0]));
    if (s_dedup[slot].request_id != 0 && !s_dedup[slot].completed) {
        xSemaphoreGive(s_dedup_mutex);
        ESP_LOGW(TAG, "PeriphCmd dedup cache full with in-flight request");
        return;
    }
    s_dedup[slot] =
        (dedup_entry_t){.request_id = request_id, .periph_type = periph_type,
                        .resource_id = resource_id, .action = action,
                        .command_value = value, .config_len = config_len};
    if (config_len > 0) memcpy(s_dedup[slot].config, config_data, config_len);
    xSemaphoreGive(s_dedup_mutex);

    ESP_LOGI(TAG, "PeriphCmd: req=%lu, type=%d, resource_id=%d, action=%d, value=%lu, cfg_len=%zu",
             (unsigned long)request_id, periph_type, resource_id, action,
             (unsigned long)value, config_len);

    switch (periph_type) {
    case PERIPH_TYPE_GPIO:
        /* GPIO ops are μs-level — execute directly in callback */
        process_gpio_cmd(request_id, resource_id, action, value, config_data, config_len);
        break;

    case PERIPH_TYPE_PWM:
        /* PWM START/STOP may block — forward to worker task */
        {
            periph_cmd_item_t item = {0};
            item.request_id = request_id;
            item.periph_type = periph_type;
            item.resource_id = resource_id;
            item.action = action;
            item.value = value;
            if (config_data && config_len > 0) {
                memcpy(item.config, config_data, config_len);
                item.config_len = config_len;
            }
            if (xQueueSend(s_periph_cmd_queue, &item, pdMS_TO_TICKS(100)) != pdTRUE) {
                ESP_LOGW(TAG, "periph_cmd_queue full, rejecting PWM cmd");
                send_periph_rsp(request_id, false, 0, PERIPH_ERR_HW_ERROR,
                                PERIPH_TYPE_PWM, resource_id, action);
            }
        }
        break;

    default:
        ESP_LOGW(TAG, "Unknown periph_type=%d", periph_type);
        send_periph_rsp(request_id, false, 0, PERIPH_ERR_INVALID_ACTION,
                        periph_type, resource_id, action);
        break;
    }
}

/* ========================================================================
 *  GPIO command processing (direct in callback)
 * ======================================================================== */

static void process_gpio_cmd(uint32_t request_id, uint8_t pin, uint8_t action,
                              uint32_t value, const uint8_t *config, size_t config_len)
{
    esp_err_t ret = ESP_OK;
    uint32_t rsp_value = 0;
    uint8_t error_code = PERIPH_ERR_OK;

    ret = periph_owner_transaction_begin();
    if (ret != ESP_OK) {
        send_periph_rsp(request_id, false, 0, PERIPH_ERR_HW_ERROR,
                        PERIPH_TYPE_GPIO, pin, action);
        return;
    }

    switch (action) {
    case GPIO_ACTION_SET_LOW:
        ret = gpio_ctrl_set(pin, 0);
		if (ret == ESP_OK) rsp_value = 0;
        break;
    case GPIO_ACTION_SET_HIGH:
        ret = gpio_ctrl_set(pin, 1);
		if (ret == ESP_OK) rsp_value = 1;
        break;
    case GPIO_ACTION_READ: {
        int level = gpio_ctrl_read(pin);
        if (level < 0) {
            error_code = PERIPH_ERR_NOT_CONFIGURED;
            periph_owner_transaction_end();
            send_periph_rsp(request_id, false, 0, error_code,
                            PERIPH_TYPE_GPIO, pin, action);
            return;
        }
        rsp_value = (uint32_t)level;
        periph_owner_transaction_end();
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_GPIO, pin, action);
        return;
    }
    case GPIO_ACTION_CONFIG: {
        /* config = [direction:1B, initial_level:1B] */
        uint8_t direction = GPIO_DIR_INPUT;
        uint8_t initial_level = 0;
        if (config && config_len >= 1) direction = config[0];
        if (config && config_len >= 2) initial_level = config[1];
        ret = gpio_ctrl_config(pin, direction, initial_level);
        break;
    }
    case GPIO_ACTION_DECONFIG:
        ret = gpio_ctrl_deconfig(pin);
        break;
    case GPIO_ACTION_TOGGLE: {
        ret = gpio_ctrl_toggle(pin);
        if (ret == ESP_OK) {
            /* Read new level for response */
            int level = gpio_ctrl_read(pin);
            if (level >= 0) rsp_value = (uint32_t)level;
        }
        break;
    }
    default:
        error_code = PERIPH_ERR_INVALID_ACTION;
        periph_owner_transaction_end();
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_GPIO, pin, action);
        return;
    }

    /* The response queue is not peripheral state. Release before enqueueing so
     * manifest transactions are never delayed by response backpressure. */
    periph_owner_transaction_end();

    /* Map esp_err_t to periph error code */
    if (ret != ESP_OK) {
        if (ret == ESP_ERR_INVALID_ARG) error_code = PERIPH_ERR_INVALID_PIN;
        else if (ret == ESP_ERR_PIN_CONFLICT) error_code = PERIPH_ERR_PIN_CONFLICT;
        else if (ret == ESP_ERR_INVALID_STATE) error_code = PERIPH_ERR_NOT_CONFIGURED;
        else error_code = PERIPH_ERR_HW_ERROR;
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_GPIO, pin, action);
    } else {
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_GPIO, pin, action);
    }
}

/* ========================================================================
 *  PWM command processing (executed in periph_worker task)
 * ======================================================================== */

static void process_pwm_cmd_impl(periph_cmd_item_t *item)
{
    uint32_t request_id = item->request_id;
    uint8_t  channel = item->resource_id;
    uint8_t  action = item->action;
    uint32_t value = item->value;
    const uint8_t *config = item->config;
    size_t   config_len = item->config_len;

    esp_err_t ret = ESP_OK;
    uint32_t rsp_value = 0;
    uint8_t error_code = PERIPH_ERR_OK;

    ret = periph_owner_transaction_begin();
    if (ret != ESP_OK) {
        send_periph_rsp(request_id, false, 0, PERIPH_ERR_HW_ERROR,
                        PERIPH_TYPE_PWM, channel, action);
        return;
    }

    switch (action) {
    case PWM_ACTION_SET_DUTY:
        if (value > PWM_DUTY_MAX) {
            error_code = PERIPH_ERR_INVALID_PARAM;
            periph_owner_transaction_end();
            send_periph_rsp(request_id, false, 0, error_code,
                            PERIPH_TYPE_PWM, channel, action);
            return;
        }
        ret = pwm_ctrl_set_duty(channel, (uint16_t)value);
		if (ret == ESP_OK) rsp_value = value;
        break;

    case PWM_ACTION_SET_FREQ: {
        /* config = [resolution:1B] */
        uint8_t resolution = 0;
        if (config && config_len >= 1) resolution = config[0];
        ret = pwm_ctrl_set_freq(channel, value, resolution);
        break;
    }

    case PWM_ACTION_START: {
        /* config = [pin:1B, freq:4B LE, duty:2B LE, resolution:1B] */
        if (!config || config_len < 8) {
            periph_owner_transaction_end();
            send_periph_rsp(request_id, false, 0, PERIPH_ERR_INVALID_PARAM,
                            PERIPH_TYPE_PWM, channel, action);
            return;
        }
        uint8_t pin = config[0];
        uint32_t freq = (uint32_t)config[1] | ((uint32_t)config[2] << 8) |
                        ((uint32_t)config[3] << 16) | ((uint32_t)config[4] << 24);
        uint16_t duty = (uint16_t)config[5] | ((uint16_t)config[6] << 8);
        uint8_t resolution = config[7];
        if (freq == 0 && value > 0) freq = value;
        ret = pwm_ctrl_start(channel, pin, freq, duty, resolution);
        break;
    }

    case PWM_ACTION_STOP:
        ret = pwm_ctrl_stop(channel);
        break;

    case PWM_ACTION_READ:
        {
            uint16_t duty;
            ret = pwm_ctrl_get_duty(channel, &duty);
            if (ret == ESP_OK) rsp_value = duty;
            break;
        }

    case PWM_ACTION_SET_RESOLUTION:
        ret = pwm_ctrl_set_resolution(channel, (uint8_t)value);
        break;

    default:
        periph_owner_transaction_end();
        send_periph_rsp(request_id, false, 0, PERIPH_ERR_INVALID_ACTION,
                        PERIPH_TYPE_PWM, channel, action);
        return;
    }

    periph_owner_transaction_end();
    if (ret != ESP_OK) {
        if (ret == ESP_ERR_INVALID_ARG) error_code = PERIPH_ERR_INVALID_PARAM;
        else if (ret == ESP_ERR_NOT_FOUND) error_code = PERIPH_ERR_RESOURCE_EXHAUSTED;
        else if (ret == ESP_ERR_PIN_CONFLICT) error_code = PERIPH_ERR_PIN_CONFLICT;
        else if (ret == ESP_ERR_INVALID_STATE) error_code = PERIPH_ERR_NOT_CONFIGURED;
        else error_code = PERIPH_ERR_HW_ERROR;
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_PWM, channel, action);
    } else {
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_PWM, channel, action);
    }
}

/* ========================================================================
 *  PeriphRsp queue + task
 * ======================================================================== */

/**
 * @brief Enqueue PeriphRsp for async sending (never call publish from callback).
 */
static void send_periph_rsp(uint32_t request_id, bool success, uint32_t value,
                             uint8_t error_code, uint8_t periph_type,
                             uint8_t resource_id, uint8_t action)
{
    bool running = periph_type == PERIPH_TYPE_PWM && pwm_ctrl_is_running(resource_id);
    xSemaphoreTake(s_dedup_mutex, portMAX_DELAY);
    for (size_t i = 0; i < sizeof(s_dedup) / sizeof(s_dedup[0]); i++) {
        if (s_dedup[i].request_id == request_id && s_dedup[i].periph_type == periph_type &&
            s_dedup[i].resource_id == resource_id && s_dedup[i].action == action) {
            s_dedup[i].completed = true;
            s_dedup[i].success = success;
            s_dedup[i].value = value;
            s_dedup[i].error_code = error_code;
            s_dedup[i].running = running;
            break;
        }
    }
    xSemaphoreGive(s_dedup_mutex);
    periph_rsp_item_t item = {
        .request_id  = request_id,
        .success     = success,
        .value       = value,
        .error_code  = error_code,
        .periph_type = periph_type,
        .resource_id = resource_id,
        .action      = action,
        .running     = running,
    };

    if (s_periph_rsp_queue == NULL) {
        ESP_LOGE(TAG, "periph_rsp_queue not initialized, dropping rsp");
        return;
    }

    /* Non-blocking send — if queue is full, log and drop */
    if (xQueueSend(s_periph_rsp_queue, &item, 0) != pdTRUE) {
        ESP_LOGW(TAG, "periph_rsp_queue full, dropping rsp (req=%lu)",
                 (unsigned long)request_id);
    }
}

/**
 * @brief PeriphRsp task — drains rsp queue and publishes via transport.
 * This is the ONLY place that calls msg_handler_publish() for PeriphRsp,
 * ensuring we never publish from MQTT callback context (deadlock prevention).
 */
static void periph_rsp_task_func(void *pv)
{
    periph_rsp_item_t item;
    ESP_LOGI(TAG, "periph_rsp_task started");

    while (1) {
        if (xQueueReceive(s_periph_rsp_queue, &item, portMAX_DELAY) == pdTRUE) {
            /* Encode PeriphRsp frame */
            uint8_t buf[128];
            frame_encoder_t enc;
            frame_encoder_init(&enc, buf, sizeof(buf), MSG_PERIPH_RSP);
            frame_encode_varint(&enc, PERIPH_RSP_F_REQUEST_ID, item.request_id);
            frame_encode_varint(&enc, PERIPH_RSP_F_SUCCESS, item.success ? 1 : 0);
            frame_encode_varint(&enc, PERIPH_RSP_F_VALUE, item.value);
            if (item.error_code != PERIPH_ERR_OK) {
                frame_encode_varint(&enc, PERIPH_RSP_F_ERROR_CODE, item.error_code);
            }
            /* Optional periph_type and resource identity for async events. */
            frame_encode_varint(&enc, PERIPH_RSP_F_PERIPH_TYPE, item.periph_type);
            frame_encode_varint(&enc, PERIPH_RSP_F_RESOURCE_ID, item.resource_id);
            frame_encode_varint(&enc, PERIPH_RSP_F_ACTION, item.action);
            if (item.periph_type == PERIPH_TYPE_PWM) {
                frame_encode_varint(&enc, PERIPH_RSP_F_RUNNING, item.running ? 1 : 0);
            }

            ESP_LOGI(TAG, "PeriphRsp: req=%lu, success=%d, value=%lu, err=%d, type=%d, resource_id=%d",
                     (unsigned long)item.request_id, item.success,
                     (unsigned long)item.value, item.error_code,
                     item.periph_type, item.resource_id);

            /* Publish failure retains the cached terminal response. A QoS1
             * redelivery of the command re-enqueues this exact response. */
            if (msg_handler_publish_checked(frame_encoder_data(&enc), frame_encoder_size(&enc)) != ESP_OK) {
                ESP_LOGW(TAG, "PeriphRsp publish failed; awaiting duplicate request for replay");
            }
        }
    }
}

/* ========================================================================
 *  Periph worker task — drains PWM cmd queue and executes
 * ======================================================================== */

static void periph_worker_task(void *pv)
{
    periph_cmd_item_t item;
    ESP_LOGI(TAG, "periph_worker_task started");

    while (1) {
        if (xQueueReceive(s_periph_cmd_queue, &item, portMAX_DELAY) == pdTRUE) {
            process_pwm_cmd_impl(&item);
        }
    }
}

/* ========================================================================
 *  Apply GPIO/PWM configs from ConfigManifest
 *  Called from app_callbacks after manifest parse
 * ======================================================================== */

esp_err_t handler_periph_apply_configs(const config_manifest_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;
    esp_err_t err = periph_config_apply(cfg);
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "Peripheral manifest apply failed: %s", esp_err_to_name(err));
    }
    return err;
}

esp_err_t handler_periph_apply_configs_locked(const config_manifest_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;
    esp_err_t err = periph_config_apply_locked(cfg);
    if (err != ESP_OK) {
        ESP_LOGW(TAG, "Peripheral manifest apply failed: %s", esp_err_to_name(err));
    }
    return err;
}