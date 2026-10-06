/**
 * @file tls_link_adapt.c
 * @brief 契约翻译实现（纯函数）
 */
#include "tls_link_adapt.h"

int tls_link_adapt_read_result(tls_io_read_t r, size_t n)
{
    switch (r) {
    case TLS_IO_READ_DATA:
        /* 归约已保证 DATA 时 n > 0；若为 0 说明上游违约，
         * **不**伪装成"暂无数据"（那正是本层要防的混淆）。 */
        if (n == 0) return LINK_TCP_IO_ERROR;
        /* 溢出保护：link 契约的返回是 int */
        if (n > 0x7FFFFFFFu) return LINK_TCP_IO_ERROR;
        return (int)n;
    case TLS_IO_READ_AGAIN:
        return LINK_TCP_IO_AGAIN;      /* 0：正常，继续读 */
    case TLS_IO_READ_CLOSED:
        return LINK_TCP_IO_CLOSED;     /* -1：**对端关闭，非故障** */
    case TLS_IO_READ_ERROR:
    default:
        return LINK_TCP_IO_ERROR;      /* -2：硬错误 */
    }
}

int tls_link_adapt_write_result(tls_io_write_t r, size_t n)
{
    switch (r) {
    case TLS_IO_WRITE_WROTE:
        /* 写出 0 字节是**合法**的（背压）：必须返回 0，不能返回负值 ——
         * 负值会被 link 层当成"链路故障"，导致无谓重建。 */
        if (n > 0x7FFFFFFFu) return -2;
        return (int)n;
    case TLS_IO_WRITE_AGAIN:
        return 0;                      /* 暂时写不动 ⇒ 背压（可重试） */
    case TLS_IO_WRITE_ERROR:
    default:
        return -2;                     /* 硬错误 */
    }
}

bool tls_link_read_ends_connection(int link_read_ret)
{
    return link_read_ret == LINK_TCP_IO_CLOSED || link_read_ret == LINK_TCP_IO_ERROR;
}
