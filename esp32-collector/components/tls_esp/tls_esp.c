/**
 * @file tls_esp.c
 * @brief 真 esp_tls 的薄适配实现
 *
 * 本层刻意保持"薄"：所有判断都委托给可宿主测试的纯函数层。
 * 这里只负责调 esp_tls 并搬运结果。
 */
#include "tls_esp.h"

#include <stdlib.h>
#include <string.h>

#include "esp_log.h"
#include "esp_tls.h"
#include "esp_tls_errors.h"

#include "tls_guard.h"
#include "tls_io.h"
#include "tls_link_adapt.h"

static const char *TAG = "tls_esp";

struct tls_esp_conn {
    esp_tls_t *tls;
    tls_esp_config_t cfg;   /* 复制；certs 指针由调用方保证生命周期 */
};

static tls_esp_stats_t s_stats;

void tls_esp_get_stats(tls_esp_stats_t *out)
{
    if (out == NULL) return;
    *out = s_stats;
}

void tls_esp_reset_stats(void)
{
    memset(&s_stats, 0, sizeof(s_stats));
}

tls_esp_config_t *tls_esp_config_new(const tls_esp_config_t *cfg)
{
    if (cfg == NULL || cfg->host == NULL) return NULL;
    tls_esp_config_t *c = (tls_esp_config_t *)calloc(1, sizeof(*c));
    if (c == NULL) return NULL;
    *c = *cfg;      /* 浅拷贝：证书缓冲与 now_epoch 由调用方保证生命周期 */
    return c;
}

void tls_esp_config_free(tls_esp_config_t *cfg)
{
    free(cfg);
}

/** 本地配置是否齐备（缺证书 -> 重试无用）。 */
static bool certs_complete(const tls_esp_certs_t *c)
{
    return c->ca_pem != NULL && c->ca_len > 0 &&
           c->cert_pem != NULL && c->cert_len > 0 &&
           c->key_pem != NULL && c->key_len > 0;
}

/**
 * ⭐ 把 tls_guard 的动作映射成 link_tcp 要的 hard_fatal。
 *
 * 这张表是本文件最关键的三行 —— 尤其第二行：
 *   SYNC_TIME_FIRST -> hard_fatal = **false**
 * 若这里写成 true，就是 K11：设备把"没校时"当成"证书永久失效"而永远放弃。
 */
static bool action_is_hard_fatal(tls_action_t a)
{
    switch (a) {
    case TLS_ACTION_FATAL:           return true;   /* 证书真不对 / 配置缺失 */
    case TLS_ACTION_SYNC_TIME_FIRST: return false;  /* ⭐ 可自愈：先校时再重试 */
    case TLS_ACTION_RETRY_BACKOFF:   return false;  /* 网络类：退避重试 */
    case TLS_ACTION_PROCEED:         return false;
    default:                         return false;  /* 认不出就当可重试（可逆优先）*/
    }
}

void *tls_esp_connect(void *io_ctx, bool *hard_fatal)
{
    if (hard_fatal != NULL) *hard_fatal = false;

    tls_esp_config_t *cfg = (tls_esp_config_t *)io_ctx;
    if (cfg == NULL) {
        s_stats.connects_failed++;
        s_stats.connects_hard_fatal++;
        if (hard_fatal != NULL) *hard_fatal = true;
        return NULL;
    }

    /* 1) 本地配置缺失：重试不会长出证书 => 硬失败。
     *    先用 tls_guard 的 CONFIG 分类走一遍，保证"分级规则只有一个来源"。 */
    if (!certs_complete(&cfg->certs)) {
        tls_failure_t f = TLS_FAIL_CONFIG;
        tls_action_t a = tls_guard_note(true, f);
        s_stats.connects_failed++;
        s_stats.connects_hard_fatal++;
        ESP_LOGE(TAG, "证书/密钥配置缺失 -> %s（重试无用）", tls_action_name(a));
        if (hard_fatal != NULL) *hard_fatal = action_is_hard_fatal(a);
        return NULL;
    }

    /* 2) 建 TLS 上下文 */
    esp_tls_t *tls = esp_tls_init();
    if (tls == NULL) {
        s_stats.connects_failed++;
        s_stats.connects_soft++;
        ESP_LOGE(TAG, "esp_tls_init 失败（内存？）");
        return NULL;   /* 内存类 -> 可重试 */
    }

    esp_tls_cfg_t ecfg = { 0 };
    ecfg.cacert_buf      = cfg->certs.ca_pem;
    ecfg.cacert_bytes    = (unsigned)cfg->certs.ca_len;
    ecfg.clientcert_buf  = cfg->certs.cert_pem;
    ecfg.clientcert_bytes = (unsigned)cfg->certs.cert_len;
    ecfg.clientkey_buf   = cfg->certs.key_pem;
    ecfg.clientkey_bytes = (unsigned)cfg->certs.key_len;
    ecfg.timeout_ms      = cfg->timeout_ms;

    /* ⚠ esp_tls_cfg_t 在 IDF 6.1 **没有** error_handle 字段（那是旧版写法）。
     * 正确途径是从 tls 上下文取：esp_tls_get_error_handle(tls, &h)。
     * 必须在 connect **之前**取 —— 该调用只依赖 tls 上下文，先取同样有效。 */
    esp_tls_error_handle_t err_handle = NULL;
    if (esp_tls_get_error_handle(tls, &err_handle) != ESP_OK) {
        err_handle = NULL;   /* 取不到就退化为 UNCLASSIFIED（可重试），不猜 */
    }

    /* 3) 连接（同步）。返回 1 成功，-1 失败（含超时）。 */
    int ret = esp_tls_conn_new_sync(cfg->host, (int)strlen(cfg->host),
                                    (int)cfg->port, &ecfg, tls);
    if (ret != 1) {
        /* 4) 取失败信息并归约，再按 (时间可信, 失败类别) 分类（§52/§54）。
         *
         * ⚠ 顺序陷阱（读 IDF 源码后确认）：
         *   - esp_tls_get_and_clear_last_error() 会 **memset 整个 handle**
         *     （esp_tls.c:815），所以必须**最后**调它；
         *   - 它一次就给出 (last_error, esp_tls_error_code, esp_tls_flags)，
         *     这三样已足够做四分类 ⇒ 无需再按槽位去取 type。
         *   - 公开结构里**没有 type 字段**（只有 last_error/code/flags）。
         *
         * 取不到 err_handle 时退化为 UNKNOWN（按可重试处理，见 tls_guard 注释）。*/
        int esp_tls_code = 0, esp_tls_flags = 0;
        unsigned last_error = 0;
        if (err_handle != NULL) {
            last_error = (unsigned)esp_tls_get_and_clear_last_error(
                             err_handle, &esp_tls_code, &esp_tls_flags);
        }

        tls_failure_t f = tls_guard_reduce_esp_error(last_error, esp_tls_code,
                                                    esp_tls_flags);

        /* ⭐ 时间可信性必须在**失败之后**参与判定（不做前置阻断）*/
        uint64_t now = (cfg->now_epoch != NULL) ? cfg->now_epoch() : 0;
        bool time_trusted = tls_guard_time_is_trusted(now);

        tls_action_t action = tls_guard_note(time_trusted, f);
        bool hard = action_is_hard_fatal(action);

        s_stats.connects_failed++;
        if (hard) s_stats.connects_hard_fatal++; else s_stats.connects_soft++;
        if (!time_trusted && tls_failure_is_time_related(f)) {
            s_stats.cert_fail_while_time_untrusted++;
        }

        ESP_LOGW(TAG, "连接失败: last_error=0x%X code=%d cert_flags=0x%X -> %s；"
                      "now=%llu time_trusted=%d -> %s (hard_fatal=%d)",
                 last_error, esp_tls_code, esp_tls_flags, tls_failure_name(f),
                 (unsigned long long)now, (int)time_trusted,
                 tls_action_name(action), (int)hard);

        esp_tls_conn_destroy(tls);
        if (hard_fatal != NULL) *hard_fatal = hard;
        return NULL;
    }

    tls_esp_conn_t *conn = (tls_esp_conn_t *)calloc(1, sizeof(*conn));
    if (conn == NULL) {
        esp_tls_conn_destroy(tls);
        s_stats.connects_failed++;
        s_stats.connects_soft++;
        return NULL;
    }
    conn->tls = tls;
    conn->cfg = *cfg;
    s_stats.connects_ok++;
    ESP_LOGI(TAG, "mTLS 连接建立 -> %s:%u", cfg->host, (unsigned)cfg->port);
    return conn;
}

int tls_esp_read(void *handle, uint8_t *buf, size_t cap)
{
    tls_esp_conn_t *c = (tls_esp_conn_t *)handle;
    if (c == NULL || c->tls == NULL || buf == NULL || cap == 0) return LINK_TCP_IO_ERROR;

    long raw = (long)esp_tls_conn_read(c->tls, buf, cap);

    /* 裸值 -> 四态（§56）-> link 的 int 约定（§58）。两跳都不在这里判断。 */
    size_t n = 0;
    tls_io_read_t r = tls_io_note_read(raw, &n);
    return tls_link_adapt_read_result(r, n);
}

int tls_esp_write(void *handle, const uint8_t *data, size_t len)
{
    tls_esp_conn_t *c = (tls_esp_conn_t *)handle;
    if (c == NULL || c->tls == NULL || data == NULL || len == 0) return -2;

    long raw = (long)esp_tls_conn_write(c->tls, data, len);

    size_t n = 0;
    tls_io_write_t r = tls_io_note_write(raw, &n);
    return tls_link_adapt_write_result(r, n);
}

void tls_esp_close(void *handle)
{
    tls_esp_conn_t *c = (tls_esp_conn_t *)handle;
    if (c == NULL) return;
    if (c->tls != NULL) {
        esp_tls_conn_destroy(c->tls);   /* 同时关闭底层 socket */
        c->tls = NULL;
    }
    free(c);
}

static const link_tcp_io_t s_tls_io = {
    .connect = tls_esp_connect,
    .write   = tls_esp_write,
    .read    = tls_esp_read,
    .close   = tls_esp_close,
};

const link_tcp_io_t *tls_esp_io(void)
{
    return &s_tls_io;
}
