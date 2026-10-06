/**
 * @file dispatch.c
 * @brief 分发表实现（线性查找：表很小，可读性优先；P4 优先于微优化）
 */
#include "dispatch.h"

static const char *const s_names[] = {
    [DISP_OK] = "OK",
    [DISP_UNKNOWN_TYPE] = "UNKNOWN_TYPE",
    [DISP_HANDLER_ERR] = "HANDLER_ERR",
    [DISP_BAD_ARG] = "BAD_ARG",
};

const char *disp_result_name(disp_result_t r)
{
    if ((int)r < 0 || r > (int)DISP_BAD_ARG) return "UNKNOWN";
    return s_names[r];
}

disp_result_t dispatch_frame(const disp_route_t *table, size_t n,
                             uint8_t type, const uint8_t *payload, size_t len,
                             void *ctx)
{
    if (table == NULL) return DISP_BAD_ARG;
    for (size_t i = 0; i < n; i++) {
        if (table[i].type != type) continue;
        /* 声明了但没有实现 —— 必须可见地失败，不静默跳过 */
        if (table[i].fn == NULL) return DISP_UNKNOWN_TYPE;
        return table[i].fn(ctx, type, payload, len);
    }
    return DISP_UNKNOWN_TYPE;   /* 不静默：调用方能看到"没人处理" */
}

void disp_audit_table(const disp_route_t *table, size_t n, disp_table_audit_t *out)
{
    if (out == NULL) return;
    out->routes = n;
    out->null_fn = 0;
    out->null_name = 0;
    out->duplicate_type = 0;
    if (table == NULL) return;
    for (size_t i = 0; i < n; i++) {
        if (table[i].fn == NULL) out->null_fn++;
        if (table[i].name == NULL || table[i].name[0] == '\0') out->null_name++;
        for (size_t j = i + 1; j < n; j++) {
            if (table[i].type == table[j].type) out->duplicate_type++;
        }
    }
}
