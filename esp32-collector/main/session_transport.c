/**
 * @file session_transport.c
 * @brief `session` → `transport_ops_t` 适配。理由与语义见 session_transport.h。
 *
 * 结构：**纯判定**（宿主可编、可测）+ **IDF 胶水**（注册/选路）。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "session_transport.h"

/* ══════════════════════ 纯判定（宿主与固件都编）══════════════════════ */

bool session_transport_ready(session_state_t st)
{
    /* 只有握手完成才算"可承载普通上行"。
     * 为什么 WAIT_HANDSHAKE 不算：那时链路虽通，但应用层未就绪（配置/能力未知），
     * 投给它等于**静默丢弃** —— 而 transport_broadcast 只在 is_connected() 为真时发送，
     * 所以这里回答错，症状是"帧消失且无错误"。
     * ⚠ 这不影响 Hello：Hello 由链路任务直接 session_send，不经过本判据（见头文件）。 */
    return st == SESSION_READY;
}

bool session_transport_connected(session_state_t st, bool gate_open)
{
    /* 唯一一处组合判据（P4）。顺序有意如此：先判**语义**（能不能发），
     * 再判**策略**（该不该走 TCP）。反过来会在未握手时走进策略分支，
     * 让"静默丢弃"看起来像"策略没选中"。 */
    return session_transport_ready(st) && gate_open;
}

const char *stx_send_class_name(stx_send_class_t c)
{
    switch (c) {
    case STX_SEND_DONE:         return "DONE";
    case STX_SEND_RETRY:        return "RETRY";
    case STX_SEND_NOT_READY:    return "NOT_READY";
    case STX_SEND_TOO_BIG:      return "TOO_BIG";
    case STX_SEND_STREAM_DIRTY: return "STREAM_DIRTY";
    default:                    return "UNKNOWN";
    }
}

stx_send_class_t stx_classify_send(link_result_t r, size_t progress, size_t len)
{
    /* ⚠ 顺序很重要：**先判"流是否已被污染"**，再判 link_result_t。
     *
     * 为什么：一旦有字节写出去（progress > 0）而本次没写完整帧，TCP 流里就有半帧，
     * 对端重组器会一直等剩余字节。此时**重试整帧是错的**（会写出重复前缀 ⇒ 静默损坏）。
     * 所以"部分写出 + 未完成"必须归为 STREAM_DIRTY（要求重建链路），
     * 而**不能**被 link_result_t 的取值掩盖成 RETRY。
     *
     * 这一条覆盖了 D-30 的另一半：link 层保证"不重发整帧"，
     * 但**只有本层知道"这次到底写出去多少"**，所以判定必须在这里做。 */
    if (progress > 0 && progress < len) {
        return STX_SEND_STREAM_DIRTY;
    }

    switch (r) {
    case LINK_SENT_FULL:
        /* 契约：progress == len。若不符，说明 link 层违约 —— 按污染处理（保守）。 */
        return (progress == len) ? STX_SEND_DONE : STX_SEND_STREAM_DIRTY;

    case LINK_SENT_PARTIAL:
        /* 写了一部分但本次返回 —— 对**单次调用**而言仍是"未完成"。
         * progress>0 时上面已归 STREAM_DIRTY；progress==0 的 PARTIAL 是矛盾的，
         * 保守归为可重试（调用方会再次调用并续写）。 */
        return (progress == 0) ? STX_SEND_RETRY : STX_SEND_STREAM_DIRTY;

    case LINK_BACKPRESSURE:
        /* 一字节都没写出（link.h 的契约）⇒ 整帧稍后重试是**安全**的。 */
        return (progress == 0) ? STX_SEND_RETRY : STX_SEND_STREAM_DIRTY;

    case LINK_NOT_READY:
    case LINK_FATAL:
        return STX_SEND_NOT_READY;

    case LINK_PAYLOAD_TOO_BIG:
        return STX_SEND_TOO_BIG;

    default:
        return STX_SEND_NOT_READY;   /* 未知取值：保守当作"不可用" */
    }
}

int stx_to_esp_err(stx_send_class_t c)
{
    switch (c) {
    case STX_SEND_DONE:         return 0;                    /* ESP_OK */
    case STX_SEND_RETRY:        return 0x107;                /* ESP_ERR_TIMEOUT */
    case STX_SEND_NOT_READY:    return 0x103;                /* ESP_ERR_INVALID_STATE */
    case STX_SEND_TOO_BIG:      return 0x104;                /* ESP_ERR_INVALID_SIZE */
    case STX_SEND_STREAM_DIRTY: return 0x101;                /* ESP_FAIL */
    default:                    return 0x101;                /* ESP_FAIL */
    }
}

/* ══════════════════════ IDF 胶水 ══════════════════════ */
#ifndef SESSION_TRANSPORT_HOST_TEST

#include "esp_err.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "SESS_TX";

typedef struct {
    session_t *sess;
} sess_tx_priv_t;

/* 与 link.h 给的循环模式一致：PARTIAL 续写、BACKPRESSURE 整帧重试。
 * 这里把它包成"一次调用完成整帧"的语义，因为 transport_ops.send 的契约是
 * "发一帧"，没有 progress 出参。 */
static esp_err_t sess_tx_send(transport_t *t, const uint8_t *data, size_t len)
{
    if (t == NULL || t->priv_data == NULL || data == NULL || len == 0) {
        return (esp_err_t)stx_to_esp_err(STX_SEND_NOT_READY);
    }
    sess_tx_priv_t *p = (sess_tx_priv_t *)t->priv_data;

    size_t progress = 0;
    /* 有界重试：背压时让出 CPU 再试，避免在 transport 上下文里空转。 */
    for (int attempt = 0; attempt < 64; attempt++) {
        link_result_t r = session_send(p->sess, data, len, &progress);
        stx_send_class_t c = stx_classify_send(r, progress, len);

        if (c == STX_SEND_DONE) return ESP_OK;

        if (c == STX_SEND_RETRY) {
            vTaskDelay(1);
            continue;                 /* progress 未动 ⇒ 重发整帧安全 */
        }

        /* 其余（含 STREAM_DIRTY）**如实回报**，绝不压成 ESP_OK。
         * STREAM_DIRTY 尤其重要：流已被污染，调用方需重建链路。 */
        ESP_LOGW(TAG, "send 未完成: class=%s r=%d progress=%u/%u",
                 stx_send_class_name(c), (int)r, (unsigned)progress, (unsigned)len);
        return (esp_err_t)stx_to_esp_err(c);
    }
    ESP_LOGE(TAG, "send 重试上限用尽（背压持续）: progress=%u/%u", (unsigned)progress, (unsigned)len);
    return (esp_err_t)stx_to_esp_err(STX_SEND_RETRY);
}

/* task-21：注入式仲裁闸。未注入 ⇒ NULL ⇒ 只看 READY（= 本卡之前的行为）。 */
static session_transport_gate_fn s_gate = NULL;

void session_transport_set_gate(session_transport_gate_fn gate)
{
    s_gate = gate;
}

static bool sess_tx_is_connected(transport_t *t)
{
    if (t == NULL || t->priv_data == NULL) return false;
    sess_tx_priv_t *p = (sess_tx_priv_t *)t->priv_data;

    /* 两层含义**都要**，缺一不可：
     *   1. session_transport_ready —— **语义**："这一帧能不能投给它"（未握手=静默丢弃）；
     *   2. 仲裁闸                  —— **策略**："现在该不该走 TCP"（否则双栈稳态双发）。
     *
     * ⚠ 复用的是同一个 session_transport_ready()，**没有第二份定义**（P4）。
     * ⚠ 闸为 NULL 时退化为只判 1 ⇒ 与 task-21 之前逐位相同。 */
    bool gate_open = (s_gate == NULL) ? true : s_gate();
    return session_transport_connected(session_state(p->sess), gate_open);
}

static const transport_ops_t s_sess_tx_ops = {
    .init         = NULL,          /* 有意为 NULL：会话由 main 创建/拥有，本层不重复初始化 */
    .start        = NULL,          /* 同上：链路任务由 device_link_wiring 驱动 */
    .stop         = NULL,          /* 有意为 NULL：本层不拥有会话生命周期（见头文件） */
    .send         = sess_tx_send,
    .is_connected = sess_tx_is_connected,
    .deinit       = NULL,          /* 幂等：无运行时资源需清理 */
};

transport_t *session_transport_create(session_t *s)
{
    if (s == NULL) return NULL;

    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    if (t == NULL) { ESP_LOGE(TAG, "transport_t 分配失败"); return NULL; }
    sess_tx_priv_t *p = (sess_tx_priv_t *)calloc(1, sizeof(*p));
    if (p == NULL) { free(t); ESP_LOGE(TAG, "priv 分配失败"); return NULL; }

    p->sess = s;
    t->ops = &s_sess_tx_ops;
    t->type = TRANSPORT_TYPE_TCP;   /* 复用既有枚举：3.0 链路就是 TCP+mTLS */
    /* ⚠ 常量名是 TRANSPORT_DISCONNECTED（不是 TRANSPORT_STATE_DISCONNECTED）。
     * 这个错**只在 IDF 构建里暴露**：整个 IDF 胶水段在 `#ifndef SESSION_TRANSPORT_HOST_TEST` 内，
     * 宿主构建根本不编它 ⇒ 宿主 102/102 全绿，而 IDF `error: undeclared`。
     * （2026-10-07 实测：本文件首次被编进固件时才暴露。） */
    t->state = TRANSPORT_DISCONNECTED;
    t->priv_data = p;

    /* 注册**不要求**已 READY：未 READY 时 is_connected 返回 false，
     * broadcast 自然跳过它，MQTT 兜底照常工作（见头文件的"为什么本轮不接 transport_sel"）。 */
    if (transport_register(t) != ESP_OK) {
        ESP_LOGE(TAG, "transport_register 失败");
        free(p); free(t);
        return NULL;
    }
    ESP_LOGI(TAG, "3.0 会话已注册为 transport（上行可在 READY 后走 TCP）");
    return t;
}

void session_transport_destroy(transport_t *t)
{
    if (t == NULL) return;
    (void)transport_unregister(t);
    free(t->priv_data);
    free(t);
}

#endif /* !SESSION_TRANSPORT_HOST_TEST */
