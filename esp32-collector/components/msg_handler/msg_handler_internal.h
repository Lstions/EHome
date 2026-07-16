/**
 * @file msg_handler_internal.h
 * @brief Internal API shared between msg_handler core and handler modules.
 *
 * NOT for external callers — only for handler_*.c files within this component.
 * Also defines field number enums for all protocol message types.
 */

#ifndef MSG_HANDLER_INTERNAL_H
#define MSG_HANDLER_INTERNAL_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "frame_codec.h"
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Publish a frame via current transport or broadcast fallback. */
esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len);
void msg_handler_publish(const uint8_t *data, size_t len);

/* ================================================================
 *  Field number enums — one per message type
 *  Usage: switch (field.field_num) { case HELLO_F_NODE_ID: ... }
 * ================================================================ */

/* Hello (0x01) */
typedef enum {
    HELLO_F_NODE_ID       = 1,
    HELLO_F_FW_VERSION    = 2,
    HELLO_F_MODEL          = 3,
    HELLO_F_CHANNEL_COUNT  = 4,
    HELLO_F_EPOCH         = 5,
    HELLO_F_HAS_MANIFEST  = 6,
    HELLO_F_LAST_MANIFEST = 7,
    HELLO_F_PROTO_VERSION = 8,
} hello_field_t;

/* HelloAck (0x12) */
typedef enum {
    HELLO_ACK_F_SERVER_TIME = 1,
    HELLO_ACK_F_FEATURES    = 2,
} hello_ack_field_t;

/* Ping (0x08) / Pong (0x09) */
typedef enum {
    PING_F_TIMESTAMP = 1,
} ping_field_t;

/* ConfigManifest (0x04) */
typedef enum {
    CFG_MFST_F_MANIFEST_ID = 1,
    /* field 2: removed in v2.4 */
    CFG_MFST_F_TEMPLATES   = 3,
    CFG_MFST_F_CHANNELS    = 4,
    CFG_MFST_F_DMA         = 5,
} config_manifest_field_t;

/* Template sub-message */
typedef enum {
    TEMPLATE_F_ID         = 1,
    TEMPLATE_F_WRITE_DATA = 2,
    TEMPLATE_F_READ_LEN   = 3,
    TEMPLATE_F_DELAY_MS   = 4,
} template_field_t;

/* Channel sub-message */
typedef enum {
    CHANNEL_F_ID             = 1,
    CHANNEL_F_HW_ID          = 2,
    CHANNEL_F_TEMPLATE_IDS   = 3,
    CHANNEL_F_INTERVAL_MS    = 4,
    CHANNEL_F_ENABLED        = 5,
    CHANNEL_F_BUS_TYPE       = 6,
    CHANNEL_F_BUS_CONFIG     = 7,
    /* field 8: reserved */
    CHANNEL_F_EDGE_DEVICES   = 9,
} channel_field_t;

/* EdgeDevice sub-message */
typedef enum {
    EDGE_DEV_F_ID       = 1,
    EDGE_DEV_F_HW_ID    = 2,
    EDGE_DEV_F_COMMANDS  = 3,
} edge_device_field_t;

/* Command sub-message */
typedef enum {
    CMD_F_TEMPLATE_ID = 1,
    CMD_F_INTERVAL_MS = 2,
    CMD_F_ENABLED     = 3,
} command_field_t;

/* DmaConfig sub-message */
typedef enum {
    DMA_CFG_F_ID      = 1,
    DMA_CFG_F_ENABLED = 2,
    DMA_CFG_F_BIND_TO = 3,
} dma_config_field_t;

/* WriteCmd (0x06) */
typedef enum {
    WRITE_CMD_F_REQUEST_ID = 1,
    WRITE_CMD_F_CHANNEL_ID = 2,
    WRITE_CMD_F_DATA       = 3,
    WRITE_CMD_F_READ_SIZE  = 4,
} write_cmd_field_t;

/* ScanReq (0x0D) */
typedef enum {
    SCAN_REQ_F_REQUEST_ID = 1,
    SCAN_REQ_F_HW_ID      = 2,
    SCAN_REQ_F_SCAN_TYPE  = 3,
    SCAN_REQ_F_START_ADDR = 4,
    SCAN_REQ_F_END_ADDR   = 5,
    SCAN_REQ_F_TIMEOUT_MS = 6,
} scan_req_field_t;

/* QueryReq (0x0E) */
typedef enum {
    QUERY_REQ_F_REQUEST_ID = 1,
    QUERY_REQ_F_QUERY_TYPE = 2,
} query_req_field_t;

/* StatusReport (0x02) */
typedef enum {
    STATUS_RPT_F_UPTIME        = 1,
    STATUS_RPT_F_STATUS        = 2,
    STATUS_RPT_F_CHANNEL_COUNT = 3,
    STATUS_RPT_F_EPOCH         = 4,
    STATUS_RPT_F_SYNC_STATE    = 5,
    STATUS_RPT_F_CONFIG_HASH   = 6,
    STATUS_RPT_F_CH_HEALTH     = 7,
} status_report_field_t;

/* DataReport (0x03) */
typedef enum {
    DATA_RPT_F_CHANNEL_ID  = 1,
    DATA_RPT_F_TIMESTAMP   = 2,
    DATA_RPT_F_SEQUENCE    = 3,
    DATA_RPT_F_RAW_DATA    = 4,
    DATA_RPT_F_ERROR_CODE  = 5,
    DATA_RPT_F_REQUEST_ID  = 6,
    DATA_RPT_F_EDGE_DEV_ID = 7,
    DATA_RPT_F_CMD_INDEX   = 8,
} data_report_field_t;

/* OtaCmd (0x0A) */
typedef enum {
    OTA_CMD_F_OTA_ID   = 1,
    OTA_CMD_F_URL      = 2,
    OTA_CMD_F_CHECKSUM = 3,
    OTA_CMD_F_SIZE     = 4,
    OTA_CMD_F_VERSION  = 5,
    OTA_CMD_F_SEQUENCE = 6,
} ota_cmd_field_t;

/* OtaProg (0x0B) */
typedef enum {
    OTA_PROG_F_OTA_ID    = 1,
    OTA_PROG_F_STATUS    = 2,
    OTA_PROG_F_PROGRESS  = 3,
    OTA_PROG_F_ERROR_MSG = 4,
} ota_prog_field_t;

/* ConfigQuery (0x10) */
typedef enum {
    CFG_QUERY_F_REQUEST_ID = 1,
} config_query_field_t;

/* QueryResources (0x1A) */
typedef enum {
    QUERY_RES_F_REQUEST_ID = 1,
} query_resources_field_t;

/* ConfigResult (0x05) */
typedef enum {
    CFG_RESULT_F_MANIFEST_ID = 1,
    CFG_RESULT_F_SUCCESS     = 2,
} config_result_field_t;

/* ConfigReport (0x11) */
typedef enum {
    CFG_REPORT_F_REQUEST_ID  = 1,
    CFG_REPORT_F_MANIFEST_ID = 2,
    CFG_REPORT_F_TMPL_COUNT  = 3,
    CFG_REPORT_F_CH_COUNT    = 4,
} config_report_field_t;

/* WriteRsp (0x07) */
typedef enum {
    WRITE_RSP_F_REQUEST_ID = 1,
    WRITE_RSP_F_SUCCESS    = 2,
    WRITE_RSP_F_ERROR_CODE = 3,
    WRITE_RSP_F_ERROR_MSG  = 4,
} write_rsp_field_t;

/* ScanRpt (0x0C) */
typedef enum {
    SCAN_RPT_F_REQUEST_ID = 1,
    SCAN_RPT_F_HW_ID      = 2,
    SCAN_RPT_F_SUCCESS    = 3,
    SCAN_RPT_F_ADDRESSES  = 4,
} scan_rpt_field_t;

/* QueryRsp (0x0F) */
typedef enum {
    QUERY_RSP_F_REQUEST_ID = 1,
    QUERY_RSP_F_SUCCESS    = 2,
    QUERY_RSP_F_ERROR_MSG  = 3,
} query_rsp_field_t;

/* PeriphCmd (0x1B) — center → node */
typedef enum {
    PERIPH_CMD_F_REQUEST_ID  = 1,
    PERIPH_CMD_F_PERIPH_TYPE = 2,
    PERIPH_CMD_F_RESOURCE_ID = 3,
    PERIPH_CMD_F_ACTION      = 4,
    PERIPH_CMD_F_VALUE       = 5,
    PERIPH_CMD_F_CONFIG      = 6,
} periph_cmd_field_t;

/* PeriphRsp (0x1C) — node → center */
typedef enum {
    PERIPH_RSP_F_REQUEST_ID  = 1,
    PERIPH_RSP_F_SUCCESS     = 2,
    PERIPH_RSP_F_VALUE       = 3,
    PERIPH_RSP_F_ERROR_CODE  = 4,
    PERIPH_RSP_F_PERIPH_TYPE = 5,
    PERIPH_RSP_F_RESOURCE_ID = 6,
    PERIPH_RSP_F_ACTION      = 7,
    PERIPH_RSP_F_RUNNING     = 8,
} periph_rsp_field_t;

/* Periph type values */
typedef enum {
    PERIPH_TYPE_GPIO = 1,
    PERIPH_TYPE_PWM  = 2,
} periph_type_t;

/* Periph error codes */
typedef enum {
    PERIPH_ERR_OK                = 0,
    PERIPH_ERR_INVALID_PIN       = 1,
    PERIPH_ERR_INVALID_ACTION    = 2,
    PERIPH_ERR_INVALID_PARAM     = 3,
    PERIPH_ERR_RESOURCE_EXHAUSTED = 4,
    PERIPH_ERR_NOT_CONFIGURED    = 5,
    PERIPH_ERR_HW_ERROR          = 6,
    PERIPH_ERR_PIN_CONFLICT      = 7,
} periph_error_t;

/* ConfigManifest GPIO sub-fields (field 11) */
typedef enum {
    GPIO_CFG_F_PIN           = 1,
    GPIO_CFG_F_DIRECTION     = 2,
    GPIO_CFG_F_INITIAL_LEVEL = 3,
} gpio_config_field_t;

/* ConfigManifest PWM sub-fields (field 12) */
typedef enum {
    PWM_CFG_F_CHANNEL    = 1,
    PWM_CFG_F_PIN        = 2,
    PWM_CFG_F_FREQUENCY  = 3,
    PWM_CFG_F_DUTY       = 4,
    PWM_CFG_F_RESOLUTION = 5,
    PWM_CFG_F_AUTO_START = 6,
} pwm_config_field_t;

#ifdef __cplusplus
}
#endif

#endif /* MSG_HANDLER_INTERNAL_H */
