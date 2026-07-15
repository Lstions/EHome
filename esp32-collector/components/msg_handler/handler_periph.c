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
    uint8_t  pin;
} periph_rsp_item_t;

/* === PeriphCmd item (sent to periph_cmd_queue for PWM async execution) === */
typedef struct {
    uint32_t request_id;
    uint8_t  periph_type;
    uint8_t  pin;
    uint8_t  action;
    uint32_t value;
    uint8_t  config[16];
    size_t   config_len;
} periph_cmd_item_t;

/* === Queues === */
static QueueHandle_t s_periph_cmd_queue = NULL;  /* PWM commands → periph_worker */
static QueueHandle_t s_periph_rsp_queue = NULL;  /* Results → periph_rsp_task */
static TaskHandle_t  s_periph_worker_task = NULL;
static TaskHandle_t  s_periph_rsp_task = NULL;
static bool s_initialized = false;

/* === Forward declarations === */
static void periph_worker_task(void *pv);
static void periph_rsp_task_func(void *pv);
static void send_periph_rsp(uint32_t request_id, bool success, uint32_t value,
                             uint8_t error_code, uint8_t periph_type, uint8_t pin);
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
void handler_periph_init(void)
{
    if (s_initialized) return;

    s_periph_cmd_queue = xQueueCreate(PERIPH_CMD_QUEUE_DEPTH, sizeof(periph_cmd_item_t));
    s_periph_rsp_queue = xQueueCreate(PERIPH_RSP_QUEUE_DEPTH, sizeof(periph_rsp_item_t));

    if (!s_periph_cmd_queue || !s_periph_rsp_queue) {
        ESP_LOGE(TAG, "Failed to create periph queues");
        return;
    }

    xTaskCreate(periph_worker_task, "periph_worker", 4096, NULL, 4, &s_periph_worker_task);
    xTaskCreate(periph_rsp_task_func, "periph_rsp", 4096, NULL, 4, &s_periph_rsp_task);

    s_initialized = true;
    ESP_LOGI(TAG, "Periph handler initialized (cmd_queue=%p, rsp_queue=%p)",
             s_periph_cmd_queue, s_periph_rsp_queue);
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
    uint8_t  pin = 0;
    uint8_t  action = 0;
    uint32_t value = 0;
    const uint8_t *config_data = NULL;
    size_t   config_len = 0;

    frame_field_t field;
    frame_err_t err;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case PERIPH_CMD_F_REQUEST_ID:
            request_id = (uint32_t)field.value.varint;
            break;
        case PERIPH_CMD_F_PERIPH_TYPE:
            periph_type = (uint8_t)field.value.varint;
            break;
        case PERIPH_CMD_F_PIN:
            pin = (uint8_t)field.value.varint;
            break;
        case PERIPH_CMD_F_ACTION:
            action = (uint8_t)field.value.varint;
            break;
        case PERIPH_CMD_F_VALUE:
            value = (uint32_t)field.value.varint;
            break;
        case PERIPH_CMD_F_CONFIG:
            if (field.wire_type == WIRE_LENGTH_DELIMITED) {
                config_data = field.value.bytes.ptr;
                config_len = field.value.bytes.len;
            }
            break;
        }
    }

    ESP_LOGI(TAG, "PeriphCmd: req=%lu, type=%d, pin=%d, action=%d, value=%lu, cfg_len=%zu",
             (unsigned long)request_id, periph_type, pin, action,
             (unsigned long)value, config_len);

    switch (periph_type) {
    case PERIPH_TYPE_GPIO:
        /* GPIO ops are μs-level — execute directly in callback */
        process_gpio_cmd(request_id, pin, action, value, config_data, config_len);
        break;

    case PERIPH_TYPE_PWM:
        /* PWM START/STOP may block — forward to worker task */
        {
            periph_cmd_item_t item = {0};
            item.request_id = request_id;
            item.periph_type = periph_type;
            item.pin = pin;
            item.action = action;
            item.value = value;
            if (config_data && config_len > 0 && config_len <= sizeof(item.config)) {
                memcpy(item.config, config_data, config_len);
                item.config_len = config_len;
            }
            if (xQueueSend(s_periph_cmd_queue, &item, pdMS_TO_TICKS(100)) != pdTRUE) {
                ESP_LOGW(TAG, "periph_cmd_queue full, rejecting PWM cmd");
                send_periph_rsp(request_id, false, 0, PERIPH_ERR_HW_ERROR,
                                PERIPH_TYPE_PWM, pin);
            }
        }
        break;

    default:
        ESP_LOGW(TAG, "Unknown periph_type=%d", periph_type);
        send_periph_rsp(request_id, false, 0, PERIPH_ERR_INVALID_ACTION,
                        periph_type, pin);
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

    switch (action) {
    case GPIO_ACTION_SET_LOW:
        ret = gpio_ctrl_set(pin, 0);
        break;
    case GPIO_ACTION_SET_HIGH:
        ret = gpio_ctrl_set(pin, 1);
        break;
    case GPIO_ACTION_READ: {
        int level = gpio_ctrl_read(pin);
        if (level < 0) {
            error_code = PERIPH_ERR_NOT_CONFIGURED;
            send_periph_rsp(request_id, false, 0, error_code,
                            PERIPH_TYPE_GPIO, pin);
            return;
        }
        rsp_value = (uint32_t)level;
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_GPIO, pin);
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
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_GPIO, pin);
        return;
    }

    /* Map esp_err_t to periph error code */
    if (ret != ESP_OK) {
        if (ret == ESP_ERR_INVALID_ARG) error_code = PERIPH_ERR_INVALID_PIN;
        else if (ret == ESP_ERR_INVALID_STATE) error_code = PERIPH_ERR_NOT_CONFIGURED;
        else error_code = PERIPH_ERR_HW_ERROR;
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_GPIO, pin);
    } else {
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_GPIO, pin);
    }
}

/* ========================================================================
 *  PWM command processing (executed in periph_worker task)
 * ======================================================================== */

static void process_pwm_cmd_impl(periph_cmd_item_t *item)
{
    uint32_t request_id = item->request_id;
    uint8_t  pin = item->pin;
    uint8_t  action = item->action;
    uint32_t value = item->value;
    const uint8_t *config = item->config;
    size_t   config_len = item->config_len;

    esp_err_t ret = ESP_OK;
    uint32_t rsp_value = 0;
    uint8_t error_code = PERIPH_ERR_OK;

    switch (action) {
    case PWM_ACTION_SET_DUTY:
        if (value > PWM_DUTY_MAX) {
            error_code = PERIPH_ERR_INVALID_PARAM;
            send_periph_rsp(request_id, false, 0, error_code,
                            PERIPH_TYPE_PWM, pin);
            return;
        }
        ret = pwm_ctrl_set_duty(pin, (uint16_t)value);
        break;

    case PWM_ACTION_SET_FREQ: {
        /* config = [resolution:1B] */
        uint8_t resolution = 0;
        if (config && config_len >= 1) resolution = config[0];
        ret = pwm_ctrl_set_freq(pin, value, resolution);
        break;
    }

    case PWM_ACTION_START: {
        /* config = [freq:4B LE, duty:2B LE, resolution:1B] */
        uint32_t freq = 1000;
        uint16_t duty = 0;
        uint8_t resolution = PWM_RES_DEFAULT;
        if (config && config_len >= 7) {
            freq = (uint32_t)config[0] | ((uint32_t)config[1] << 8) |
                   ((uint32_t)config[2] << 16) | ((uint32_t)config[3] << 24);
            duty = (uint16_t)config[4] | ((uint16_t)config[5] << 8);
            resolution = config[6];
        } else if (config && config_len >= 6) {
            freq = (uint32_t)config[0] | ((uint32_t)config[1] << 8) |
                   ((uint32_t)config[2] << 16) | ((uint32_t)config[3] << 24);
            duty = (uint16_t)config[4] | ((uint16_t)config[5] << 8);
        }
        /* If value field has freq, use it as fallback */
        if (freq == 0 && value > 0) freq = value;
        ret = pwm_ctrl_start(pin, freq, duty, resolution);
        break;
    }

    case PWM_ACTION_STOP:
        ret = pwm_ctrl_stop(pin);
        break;

    case PWM_ACTION_READ: {
        uint16_t duty = pwm_ctrl_get_duty(pin);
        rsp_value = (uint32_t)duty;
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_PWM, pin);
        return;
    }

    case PWM_ACTION_SET_RESOLUTION:
        ret = pwm_ctrl_set_resolution(pin, (uint8_t)value);
        break;

    default:
        error_code = PERIPH_ERR_INVALID_ACTION;
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_PWM, pin);
        return;
    }

    /* Map esp_err_t to periph error code */
    if (ret != ESP_OK) {
        if (ret == ESP_ERR_INVALID_ARG) error_code = PERIPH_ERR_INVALID_PARAM;
        else if (ret == ESP_ERR_NOT_FOUND) error_code = PERIPH_ERR_RESOURCE_EXHAUSTED;
        else if (ret == ESP_ERR_INVALID_STATE) error_code = PERIPH_ERR_NOT_CONFIGURED;
        else error_code = PERIPH_ERR_HW_ERROR;
        send_periph_rsp(request_id, false, 0, error_code,
                        PERIPH_TYPE_PWM, pin);
    } else {
        send_periph_rsp(request_id, true, rsp_value, PERIPH_ERR_OK,
                        PERIPH_TYPE_PWM, pin);
    }
}

/* ========================================================================
 *  PeriphRsp queue + task
 * ======================================================================== */

/**
 * @brief Enqueue PeriphRsp for async sending (never call publish from callback).
 */
static void send_periph_rsp(uint32_t request_id, bool success, uint32_t value,
                             uint8_t error_code, uint8_t periph_type, uint8_t pin)
{
    periph_rsp_item_t item = {
        .request_id  = request_id,
        .success     = success,
        .value       = value,
        .error_code  = error_code,
        .periph_type = periph_type,
        .pin         = pin,
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
            if (item.value > 0) {
                frame_encode_varint(&enc, PERIPH_RSP_F_VALUE, item.value);
            }
            if (item.error_code != PERIPH_ERR_OK) {
                frame_encode_varint(&enc, PERIPH_RSP_F_ERROR_CODE, item.error_code);
            }
            /* Optional periph_type and pin for async event scenarios */
            frame_encode_varint(&enc, PERIPH_RSP_F_PERIPH_TYPE, item.periph_type);
            frame_encode_varint(&enc, PERIPH_RSP_F_PIN, item.pin);

            ESP_LOGI(TAG, "PeriphRsp: req=%lu, success=%d, value=%lu, err=%d, type=%d, pin=%d",
                     (unsigned long)item.request_id, item.success,
                     (unsigned long)item.value, item.error_code,
                     item.periph_type, item.pin);

            /* Publish — this is safe because we're NOT in MQTT callback context */
            msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
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

void handler_periph_apply_configs(const config_manifest_t *cfg)
{
    if (!cfg) return;

    /* Apply GPIO configs */
    if (cfg->gpio_config_count > 0) {
        gpio_config_entry_t entries[MAX_GPIO_CONFIGS];
        int count = 0;
        for (int i = 0; i < cfg->gpio_config_count && count < MAX_GPIO_CONFIGS; i++) {
            entries[count].pin = cfg->gpio_configs[i].pin;
            entries[count].direction = cfg->gpio_configs[i].direction;
            entries[count].initial_level = cfg->gpio_configs[i].initial_level;
            count++;
        }
        esp_err_t ret = gpio_ctrl_init(entries, count);
        if (ret != ESP_OK) {
            ESP_LOGW(TAG, "GPIO init returned %s (partial failure possible)",
                     esp_err_to_name(ret));
        }
    }

    /* Apply PWM configs */
    if (cfg->pwm_config_count > 0) {
        pwm_config_entry_t entries[MAX_PWM_CONFIGS];
        int count = 0;
        for (int i = 0; i < cfg->pwm_config_count && count < MAX_PWM_CONFIGS; i++) {
            entries[count].pin = cfg->pwm_configs[i].pin;
            entries[count].frequency = cfg->pwm_configs[i].frequency;
            entries[count].duty = cfg->pwm_configs[i].duty;
            entries[count].resolution = cfg->pwm_configs[i].resolution;
            entries[count].auto_start = cfg->pwm_configs[i].auto_start;
            count++;
        }
        esp_err_t ret = pwm_ctrl_init(entries, count);
        if (ret != ESP_OK) {
            ESP_LOGW(TAG, "PWM init returned %s (partial failure possible)",
                     esp_err_to_name(ret));
        }
    }
}
