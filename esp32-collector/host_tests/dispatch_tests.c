/* dispatch_tests.c —— 分发表（原则 P3/P4） */
#include "dispatch.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

typedef struct { int calls; uint8_t last_type; size_t last_len; } ctx_t;

static disp_result_t h_ok(void *ctx, uint8_t type, const uint8_t *p, size_t n)
{
    ctx_t *c = (ctx_t *)ctx;
    c->calls++; c->last_type = type; c->last_len = n;
    (void)p;
    return DISP_OK;
}
static disp_result_t h_err(void *ctx, uint8_t type, const uint8_t *p, size_t n)
{
    ctx_t *c = (ctx_t *)ctx; c->calls++;
    (void)type; (void)p; (void)n;
    return DISP_HANDLER_ERR;
}

static const disp_route_t TABLE[] = {
    { 0x01, h_ok,  "Hello" },
    { 0x20, h_ok,  "DataBatch" },
    { 0x30, h_err, "AlwaysErr" },
    { 0x40, NULL,  "DeclaredButUnimplemented" },
};

/* 1) 命中并调用 */
static void test_hit(void)
{
    ctx_t c = {0};
    CHECK(dispatch_frame(TABLE, 4, 0x20, (const uint8_t *)"xy", 2, &c) == DISP_OK);
    CHECK(c.calls == 1);
    CHECK(c.last_type == 0x20);
    CHECK(c.last_len == 2);
}

/* 2) 【P3 核心】未知类型必须可见地失败，不能静默 */
static void test_unknown_is_visible(void)
{
    ctx_t c = {0};
    CHECK(dispatch_frame(TABLE, 4, 0x99, NULL, 0, &c) == DISP_UNKNOWN_TYPE);
    CHECK(c.calls == 0);   /* 不能调用任何 handler */
}

/* 3) 声明了但未实现（fn==NULL）也要可见失败，且不能蹦空指针 */
static void test_declared_but_unimplemented(void)
{
    ctx_t c = {0};
    CHECK(dispatch_frame(TABLE, 4, 0x40, NULL, 0, &c) == DISP_UNKNOWN_TYPE);
    CHECK(c.calls == 0);
}

/* 4) handler 的错误被【原样透传】（不压平） */
static void test_handler_error_propagates(void)
{
    ctx_t c = {0};
    CHECK(dispatch_frame(TABLE, 4, 0x30, NULL, 0, &c) == DISP_HANDLER_ERR);
    CHECK(c.calls == 1);
}

/* 5) 空表/参数错 */
static void test_empty_and_bad_args(void)
{
    ctx_t c = {0};
    CHECK(dispatch_frame(NULL, 0, 0x01, NULL, 0, &c) == DISP_BAD_ARG);
    CHECK(dispatch_frame(TABLE, 0, 0x01, NULL, 0, &c) == DISP_UNKNOWN_TYPE);
}

/* 6) 表自审：重名/NULL 计数（给门禁用） */
static void test_table_audit(void)
{
    disp_table_audit_t a;
    disp_audit_table(TABLE, 4, &a);
    CHECK(a.routes == 4);
    CHECK(a.null_fn == 1);
    CHECK(a.null_name == 0);
    CHECK(a.duplicate_type == 0);

    static const disp_route_t DUP[] = {
        { 0x01, h_ok, "A" }, { 0x01, h_ok, "B" }, { 0x02, h_ok, NULL },
    };
    disp_audit_table(DUP, 3, &a);
    CHECK(a.duplicate_type == 1);
    CHECK(a.null_name == 1);
}

/* 7) 结果名唯一非空 */
static void test_names(void)
{
    for (int i = 0; i <= DISP_BAD_ARG; i++) {
        const char *n = disp_result_name((disp_result_t)i);
        CHECK(n != NULL && n[0] && strcmp(n, "UNKNOWN") != 0);
    }
    CHECK(strcmp(disp_result_name((disp_result_t)77), "UNKNOWN") == 0);
}

int main(void)
{
    test_hit();
    test_unknown_is_visible();
    test_declared_but_unimplemented();
    test_handler_error_propagates();
    test_empty_and_bad_args();
    test_table_audit();
    test_names();
    if (s_failures) { printf("dispatch_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("dispatch_tests: all checks passed\n");
    return 0;
}
