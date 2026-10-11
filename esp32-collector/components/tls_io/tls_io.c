/**
 * @file tls_io.c
 * @brief esp_tls 返回值归约实现（纯函数 + 计数）
 */
#include "tls_io.h"

#include <string.h>

static const char *const s_read_names[] = {
    [TLS_IO_READ_DATA]   = "DATA",
    [TLS_IO_READ_AGAIN]  = "AGAIN",
    [TLS_IO_READ_CLOSED] = "CLOSED",
    [TLS_IO_READ_ERROR]  = "ERROR",
};

const char *tls_io_read_name(tls_io_read_t r)
{
    if ((int)r < 0 || r > (int)TLS_IO_READ_ERROR) return "UNKNOWN";
    return s_read_names[r];
}

static const char *const s_write_names[] = {
    [TLS_IO_WRITE_WROTE] = "WROTE",
    [TLS_IO_WRITE_AGAIN] = "AGAIN",
    [TLS_IO_WRITE_ERROR] = "ERROR",
};

const char *tls_io_write_name(tls_io_write_t r)
{
    if ((int)r < 0 || r > (int)TLS_IO_WRITE_ERROR) return "UNKNOWN";
    return s_write_names[r];
}

/** 这个负值是否表示"暂无数据/稍后再来"（而不是错误）。 */
static bool is_soft_again(long raw)
{
    return raw == TLS_IO_WANT_READ || raw == TLS_IO_WANT_WRITE || raw == TLS_IO_TIMEOUT;
}

tls_io_read_t tls_io_reduce_read(long raw, size_t *n_out)
{
    if (n_out != NULL) *n_out = 0;

    if (raw > 0) {
        if (n_out != NULL) *n_out = (size_t)raw;
        return TLS_IO_READ_DATA;
    }
    /* ⭐ 0 = 对端关闭。这是本次实测 esp_tls_mbedtls.c 得出的：
     *    if (ret == MBEDTLS_ERR_SSL_PEER_CLOSE_NOTIFY) return 0;
     * 归成 AGAIN 会让断开被当成"还在等数据" ⇒ 永不重连。 */
    if (raw == 0) return TLS_IO_READ_CLOSED;
    if (raw == TLS_IO_PEER_CLOSE) return TLS_IO_READ_CLOSED;
    if (is_soft_again(raw)) return TLS_IO_READ_AGAIN;
    return TLS_IO_READ_ERROR;
}

tls_io_write_t tls_io_reduce_write(long raw, size_t *n_out)
{
    if (n_out != NULL) *n_out = 0;
    if (raw >= 0) {
        if (n_out != NULL) *n_out = (size_t)raw;   /* 含 0 = 一个都没写出（背压） */
        return TLS_IO_WRITE_WROTE;
    }
    if (is_soft_again(raw)) return TLS_IO_WRITE_AGAIN;
    return TLS_IO_WRITE_ERROR;
}

bool tls_io_read_ends_connection(tls_io_read_t r)
{
    return r == TLS_IO_READ_CLOSED || r == TLS_IO_READ_ERROR;
}

static tls_io_stats_t s_stats;

tls_io_read_t tls_io_note_read(long raw, size_t *n_out)
{
    tls_io_read_t r = tls_io_reduce_read(raw, n_out);
    switch (r) {
    case TLS_IO_READ_DATA:   s_stats.read_data++;   break;
    case TLS_IO_READ_AGAIN:  s_stats.read_again++;  break;
    case TLS_IO_READ_CLOSED: s_stats.read_closed++; break;
    default:                 s_stats.read_error++;  break;
    }
    return r;
}

tls_io_write_t tls_io_note_write(long raw, size_t *n_out)
{
    tls_io_write_t r = tls_io_reduce_write(raw, n_out);
    switch (r) {
    case TLS_IO_WRITE_WROTE: s_stats.write_wrote++; break;
    case TLS_IO_WRITE_AGAIN: s_stats.write_again++; break;
    default:                 s_stats.write_error++; break;
    }
    return r;
}

void tls_io_get_stats(tls_io_stats_t *out)
{
    if (out == NULL) return;
    *out = s_stats;
}

void tls_io_reset_stats(void)
{
    memset(&s_stats, 0, sizeof(s_stats));
}
