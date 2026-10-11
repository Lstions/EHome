/**
 * @file dispatch.h
 * @brief 消息分发表 —— 数据驱动，替代散落的 switch/if 链（设计文档 1.3；原则 P3/P4）
 *
 * 现状的问题：各消息由 msg_handler/ 的 8 个 handler_*.c 各自认领
 * （合计 3,521 行），"有哪些类型"这个知识没有单一来源（D-04）。
 * 这里把路由变成【数据】：新增消息类型 = 加一行，不改控制流。
 *
 * 关键语义：未知类型【必须】可见地失败（DISP_UNKNOWN_TYPE），
 * 不允许静默忽略 —— 静默忽略会让"设备没反应"变成最难查的问题。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_DISPATCH_H
#define EHOME_DISPATCH_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    DISP_OK = 0,
    DISP_UNKNOWN_TYPE,   /* 表里没有这个类型 —— 不静默（P3） */
    DISP_HANDLER_ERR,    /* 处理函数自己报错 */
    DISP_BAD_ARG
} disp_result_t;

/** 处理函数：payload 已经【定界完毕】（由 wire 负责），不含长度前缀。 */
typedef disp_result_t (*disp_handler_fn)(void *ctx, uint8_t type,
                                         const uint8_t *payload, size_t len);

typedef struct {
    uint8_t         type;
    disp_handler_fn fn;
    const char     *name;   /* 诊断/日志用；必须非空 */
} disp_route_t;

/**
 * 查表并调用。确定性行为：
 *   - type 无对应项 -> DISP_UNKNOWN_TYPE（不调用任何 handler）；
 *   - fn == NULL 的项视为"声明了但未实现" -> DISP_UNKNOWN_TYPE
 *     （【不】静默跳过，也不蹦空指针）；
 *   - 命中则返回 handler 的结果（不压平）。
 */
disp_result_t dispatch_frame(const disp_route_t *table, size_t n,
                             uint8_t type, const uint8_t *payload, size_t len,
                             void *ctx);

/** 校验表自身（重名/空名/NULL fn 计数）——给门禁用。 */
typedef struct {
    size_t routes;
    size_t null_fn;       /* 声明未实现 */
    size_t null_name;
    size_t duplicate_type;
} disp_table_audit_t;

void disp_audit_table(const disp_route_t *table, size_t n, disp_table_audit_t *out);

const char *disp_result_name(disp_result_t r);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_DISPATCH_H */
