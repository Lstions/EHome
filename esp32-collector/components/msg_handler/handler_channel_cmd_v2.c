#include "msg_handler_internal.h"
#include "esp_log.h"
#include <string.h>

/* Host decoder tests link this handler without the MQTT transport component;
 * production provides the strong checked publisher. */
__attribute__((weak)) esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len)
{
    msg_handler_publish(data, len);
    return ESP_OK;
}

#define TAG "CH_CMD_V2"
#define V2_PROTOCOL 1U
#define V2_MAX_RX 256U
#define V2_MAX_TIMEOUT 30000U
#define V2_ERR_UNSUPPORTED 1001U
#define V2_ERR_MALFORMED 1002U
#define V2_ERR_COLLISION 1003U
#define V2_ERR_BUSY 1004U
#define V2_ERR_FINAL_OVERFLOW 1005U
#define V2_ERR_EXPIRED 1006U

typedef enum {
    V2_SLOT_FREE = 0,
    V2_SLOT_QUEUED = 1,
    V2_SLOT_FINAL = 2,
    /* A completion owns the slot while publishing its result.  This prevents
     * a replay from observing a partially written final response. */
    V2_SLOT_COMPLETING = 3,
    /* A reservation is initializing command fields.  It is deliberately not
     * considered a command identity until the QUEUED release-store. */
    V2_SLOT_RESERVED = 4,
} v2_slot_state_t;
typedef struct {
    volatile uint32_t state;
    channel_cmd_v2_t cmd;
    bool final_success;
    uint32_t final_error;
    uint8_t final_raw[V2_MAX_RX];
    size_t final_raw_len;
    uint64_t completed_order;
} v2_control_slot_t;

/* The application bridge must enqueue the small slot index on an existing
 * per-bus worker queue. It must return false without physical TX if admission
 * fails. */
__attribute__((weak)) const char *channel_cmd_v2_current_boot_id(void) { return NULL; }
__attribute__((weak)) uint64_t channel_cmd_v2_current_time_ms(void) { return 0; }
__attribute__((weak)) bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot) { (void)cmd; (void)slot; return false; }
static uint32_t s_event_sequence;
static uint64_t s_completed_sequence;
static uint32_t s_reservation_lock;
static v2_control_slot_t s_slots[CHANNEL_CMD_V2_SLOT_COUNT];
static uint32_t s_accepted_count;
static uint32_t s_rejected_count;
static uint32_t s_completed_count;
static uint32_t s_replayed_count;

static uint32_t next_event_sequence(void)
{
    for (;;) {
        uint32_t current = __atomic_load_n(&s_event_sequence, __ATOMIC_RELAXED);
        uint32_t next = current + 1U;
        if (next == 0) next = 1U;
        if (__atomic_compare_exchange_n(&s_event_sequence, &current, next, false,
                                        __ATOMIC_RELAXED, __ATOMIC_RELAXED)) return next;
    }
}

static bool send_response(uint8_t type, const channel_cmd_v2_t *cmd, bool success,
                          uint32_t code, const uint8_t *raw, size_t raw_len, bool replayed)
{
    uint8_t buf[384]; frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), type);
    if (cmd) {
        if (frame_encode_bytes(&enc, 1, cmd->command_id, 16) != FRAME_OK ||
            frame_encode_bytes(&enc, 2, cmd->payload_digest, 16) != FRAME_OK ||
            frame_encode_varint(&enc, 3, cmd->attempt) != FRAME_OK ||
            frame_encode_string(&enc, 4, cmd->boot_id) != FRAME_OK) return false;
    }
    if (frame_encode_varint(&enc, 5, next_event_sequence()) != FRAME_OK ||
        frame_encode_varint(&enc, 6, success ? 1 : 0) != FRAME_OK ||
        frame_encode_varint(&enc, 7, code) != FRAME_OK) return false;
    if (type == MSG_CHANNEL_CMD_V2_FINAL) {
        if (raw && raw_len && frame_encode_bytes(&enc, 8, raw, raw_len) != FRAME_OK) return false;
        if (frame_encode_varint(&enc, 9, replayed ? 1 : 0) != FRAME_OK) return false;
    }
    return msg_handler_publish_checked(frame_encoder_data(&enc), frame_encoder_size(&enc)) == ESP_OK;
}

static void send_ack(const channel_cmd_v2_t *cmd, bool accepted, uint32_t code)
{
	if (accepted) __atomic_add_fetch(&s_accepted_count, 1, __ATOMIC_RELAXED);
	else __atomic_add_fetch(&s_rejected_count, 1, __ATOMIC_RELAXED);
    if (!send_response(MSG_CHANNEL_CMD_V2_ACK, cmd, accepted, code, NULL, 0, false)) {
        ESP_LOGE(TAG, "Failed to publish ChannelCmdV2 ACK");
    }
}

void handler_channel_cmd_v2_get_metrics(channel_cmd_v2_metrics_t *metrics)
{
	if (!metrics) return;
	metrics->accepted = __atomic_load_n(&s_accepted_count, __ATOMIC_RELAXED);
	metrics->rejected = __atomic_load_n(&s_rejected_count, __ATOMIC_RELAXED);
	metrics->completed = __atomic_load_n(&s_completed_count, __ATOMIC_RELAXED);
	metrics->replayed = __atomic_load_n(&s_replayed_count, __ATOMIC_RELAXED);
}

static bool same_identity(const channel_cmd_v2_t *a, const channel_cmd_v2_t *b)
{
    return a->attempt == b->attempt && strcmp(a->boot_id, b->boot_id) == 0 &&
           memcmp(a->command_id, b->command_id, sizeof(a->command_id)) == 0 &&
           memcmp(a->payload_digest, b->payload_digest, sizeof(a->payload_digest)) == 0 &&
           a->edge_device_id == b->edge_device_id && a->channel_id == b->channel_id &&
           a->deadline_unix_ms == b->deadline_unix_ms && a->tx_len == b->tx_len &&
           memcmp(a->tx_data, b->tx_data, a->tx_len) == 0 &&
           a->read_size == b->read_size && a->rx_timeout_ms == b->rx_timeout_ms &&
           a->post_tx_delay_ms == b->post_tx_delay_ms;
}

static bool same_command_id(const channel_cmd_v2_t *a, const channel_cmd_v2_t *b)
{
    return memcmp(a->command_id, b->command_id, sizeof(a->command_id)) == 0;
}

static void prepare_slot(v2_control_slot_t *slot, const channel_cmd_v2_t *cmd)
{
    slot->cmd = *cmd;
    slot->final_success = false;
    slot->final_error = 0;
    slot->final_raw_len = 0;
    slot->completed_order = 0;
}

static int reserve_slot(const channel_cmd_v2_t *cmd, bool *replayed)
{
    while (__atomic_exchange_n(&s_reservation_lock, 1U, __ATOMIC_ACQUIRE) != 0U) { }
    for (;;) {
        *replayed = false;
        int free_slot = -1;
        int oldest_final_slot = -1;
        uint64_t oldest_final_order = UINT64_MAX;
        for (int i = 0; i < CHANNEL_CMD_V2_SLOT_COUNT; i++) {
            uint32_t state = __atomic_load_n(&s_slots[i].state, __ATOMIC_ACQUIRE);
            if (state == V2_SLOT_FREE) {
                if (free_slot < 0) free_slot = i;
                continue;
            }
            if (state == V2_SLOT_RESERVED) continue;
            if (same_identity(&s_slots[i].cmd, cmd)) {
                *replayed = true;
                __atomic_store_n(&s_reservation_lock, 0U, __ATOMIC_RELEASE);
                return i;
            }
            if (same_command_id(&s_slots[i].cmd, cmd)) {
                __atomic_store_n(&s_reservation_lock, 0U, __ATOMIC_RELEASE);
                return -2;
            }
            if (state == V2_SLOT_FINAL && s_slots[i].completed_order < oldest_final_order) {
                oldest_final_order = s_slots[i].completed_order;
                oldest_final_slot = i;
            }
        }
        if (free_slot >= 0) {
            uint32_t expected = V2_SLOT_FREE;
            if (!__atomic_compare_exchange_n(&s_slots[free_slot].state, &expected, V2_SLOT_RESERVED,
                                             false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) continue;
            prepare_slot(&s_slots[free_slot], cmd);
            __atomic_store_n(&s_slots[free_slot].state, V2_SLOT_QUEUED, __ATOMIC_RELEASE);
            __atomic_store_n(&s_reservation_lock, 0U, __ATOMIC_RELEASE);
            return free_slot;
        }
        if (oldest_final_slot >= 0) {
            uint32_t expected = V2_SLOT_FINAL;
            if (!__atomic_compare_exchange_n(&s_slots[oldest_final_slot].state, &expected, V2_SLOT_RESERVED,
                                             false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) continue;
            prepare_slot(&s_slots[oldest_final_slot], cmd);
            __atomic_store_n(&s_slots[oldest_final_slot].state, V2_SLOT_QUEUED, __ATOMIC_RELEASE);
            __atomic_store_n(&s_reservation_lock, 0U, __ATOMIC_RELEASE);
            return oldest_final_slot;
        }
        __atomic_store_n(&s_reservation_lock, 0U, __ATOMIC_RELEASE);
        return -1;
    }
}

void handler_channel_cmd_v2_complete(uint8_t slot, bool success, uint32_t error_code,
                                     const uint8_t *raw_response, size_t raw_len)
{
    if (slot >= CHANNEL_CMD_V2_SLOT_COUNT) return;
    if (raw_len > V2_MAX_RX) {
        success = false;
        error_code = V2_ERR_FINAL_OVERFLOW;
        raw_response = NULL;
        raw_len = 0;
    }
    if (raw_len > 0 && raw_response == NULL) {
        success = false;
        error_code = V2_ERR_MALFORMED;
        raw_len = 0;
    }
    v2_control_slot_t *entry = &s_slots[slot];
    uint32_t expected = V2_SLOT_QUEUED;
    if (!__atomic_compare_exchange_n(&entry->state, &expected, V2_SLOT_COMPLETING,
                                     false, __ATOMIC_ACQ_REL, __ATOMIC_ACQUIRE)) return;
    entry->final_success = success;
    entry->final_error = error_code;
    entry->final_raw_len = raw_len;
    if (raw_len && raw_response) memcpy(entry->final_raw, raw_response, raw_len);
    entry->completed_order = __atomic_add_fetch(&s_completed_sequence, 1, __ATOMIC_RELAXED);
    __atomic_store_n(&entry->state, V2_SLOT_FINAL, __ATOMIC_RELEASE);
	__atomic_add_fetch(&s_completed_count, 1, __ATOMIC_RELAXED);
    if (!send_response(MSG_CHANNEL_CMD_V2_FINAL, &entry->cmd, success, entry->final_error,
                       entry->final_raw, entry->final_raw_len, false)) {
        ESP_LOGE(TAG, "Failed to publish ChannelCmdV2 Final");
    }
}

void handler_channel_cmd_v2_process(frame_decoder_t *dec)
{
    channel_cmd_v2_t cmd = {0}; uint32_t seen = 0; frame_field_t f; frame_err_t err;
    while ((err = frame_decoder_next(dec, &f)) == FRAME_OK) {
        if (f.field_num == 0 || f.field_num > 14 || (seen & (1U << f.field_num))) goto malformed;
        seen |= 1U << f.field_num;
        if (f.field_num == 1 || f.field_num == 2 || f.field_num == 4 || f.field_num == 8) {
            if (f.wire_type != WIRE_LENGTH_DELIMITED) goto malformed;
            const uint8_t *p=f.value.bytes.ptr; size_t n=f.value.bytes.len;
            if (f.field_num == 1) { if(n!=16)goto malformed; memcpy(cmd.command_id,p,n); }
            else if (f.field_num == 2) { if(n!=16)goto malformed; memcpy(cmd.payload_digest,p,n); }
            else if (f.field_num == 4) { if(n==0||n>32)goto malformed; memcpy(cmd.boot_id,p,n);cmd.boot_id[n]='\0'; }
            else { if(n==0||n>sizeof(cmd.tx_data))goto malformed; memcpy(cmd.tx_data,p,n);cmd.tx_len=n; }
            continue;
        }
        if (f.wire_type != WIRE_VARINT) goto malformed;
        uint64_t v=f.value.varint;
        if (f.field_num != 7 && v > UINT32_MAX) goto malformed;
        switch (f.field_num) {
        case 3:cmd.attempt=(uint32_t)v;break; case 5:cmd.edge_device_id=(uint32_t)v;break; case 6:cmd.channel_id=(uint32_t)v;break;
        case 7:cmd.deadline_unix_ms=v;break; case 9:cmd.read_size=(uint32_t)v;break; case 10:cmd.rx_timeout_ms=(uint32_t)v;break;
        case 11:cmd.post_tx_delay_ms=(uint32_t)v;break; case 12:if(v!=0)goto malformed;break; case 13:if(v!=0)goto malformed;break;
        case 14:if(v!=V2_PROTOCOL)goto malformed;break; default:goto malformed;
        }
    }
    if (err != FRAME_DONE) goto malformed;
    const uint32_t required=(1U<<1)|(1U<<2)|(1U<<3)|(1U<<4)|(1U<<5)|(1U<<6)|(1U<<7)|(1U<<8)|(1U<<9)|(1U<<10)|(1U<<11)|(1U<<12)|(1U<<13)|(1U<<14);
    if ((seen&required)!=required || !cmd.attempt || !cmd.edge_device_id || !cmd.channel_id || !cmd.deadline_unix_ms || cmd.read_size>V2_MAX_RX || !cmd.rx_timeout_ms || cmd.rx_timeout_ms>V2_MAX_TIMEOUT || cmd.post_tx_delay_ms>V2_MAX_TIMEOUT) goto malformed;
    const char *boot=channel_cmd_v2_current_boot_id();
    if (!boot || strcmp(boot,cmd.boot_id)) { send_ack(&cmd,false,V2_ERR_UNSUPPORTED);return; }
    uint64_t now_ms = channel_cmd_v2_current_time_ms();
    if (now_ms == 0 || cmd.deadline_unix_ms <= now_ms) {
        ESP_LOGW(TAG, "Rejecting expired V2 command: deadline=%llu now=%llu",
                 (unsigned long long)cmd.deadline_unix_ms,
                 (unsigned long long)now_ms);
        send_ack(&cmd, false, V2_ERR_EXPIRED);
        return;
    }
    bool replayed = false;
    int slot = reserve_slot(&cmd, &replayed);
    if (slot == -2) { send_ack(&cmd, false, V2_ERR_COLLISION); return; }
    if (slot < 0) { send_ack(&cmd, false, V2_ERR_BUSY); return; }
    if (replayed) {
        v2_control_slot_t *entry = &s_slots[slot];
		__atomic_add_fetch(&s_replayed_count, 1, __ATOMIC_RELAXED);
        send_ack(&cmd, true, 0);
        if (__atomic_load_n(&entry->state, __ATOMIC_ACQUIRE) == V2_SLOT_FINAL) {
            if (!send_response(MSG_CHANNEL_CMD_V2_FINAL, &entry->cmd, entry->final_success,
                               entry->final_error, entry->final_raw, entry->final_raw_len, true)) {
                ESP_LOGE(TAG, "Failed to publish replayed ChannelCmdV2 Final");
            }
        }
        return;
    }
    ESP_LOGI(TAG, "V2 admitted ch=%lu edge=%lu tx=%u read=%lu timeout=%lu delay=%lu slot=%d",
             (unsigned long)cmd.channel_id, (unsigned long)cmd.edge_device_id,
             (unsigned)cmd.tx_len, (unsigned long)cmd.read_size,
             (unsigned long)cmd.rx_timeout_ms, (unsigned long)cmd.post_tx_delay_ms, slot);
    if (!on_channel_cmd_v2_received(&cmd, (uint8_t)slot)) {
        __atomic_store_n(&s_slots[slot].state, V2_SLOT_FREE, __ATOMIC_RELEASE);
        send_ack(&cmd,false,V2_ERR_UNSUPPORTED); return;
    }
    send_ack(&cmd,true,0); return;
malformed:
    ESP_LOGW(TAG,"Rejecting malformed ChannelCmdV2"); send_ack(NULL,false,V2_ERR_MALFORMED);
}
