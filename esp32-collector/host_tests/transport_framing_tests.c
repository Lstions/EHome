/* transport_framing_tests.c —— D-09 定帧契约（宿主可测）
 *
 * 这些断言的存在理由：D-09 是"契约只存在于实现者脑子里"造成的 ——
 * ehome_tcp.c 把一次 recv() 的字节块当【完整消息】回调，
 * 而 transport.h 从未声明"一次回调 = 一条消息"这个前提。
 * 把交付语义变成可查询的数据后，它就有了可断言的真相。 */
#include "transport.h"
#include "net_policy.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) MQTT 自带分帧 -> 每次回调是一条完整消息 */
static void test_mqtt_delivers_messages(void)
{
    CHECK(transport_framing_of(TRANSPORT_TYPE_MQTT) == TRANSPORT_DELIVERS_MESSAGES);
}

/* 2) 【核心】TCP 是字节流 -> 每次回调是任意字节块，消费者必须自行定界。
 *    若这条被改成 MESSAGES，就等于把 D-09 的错误假设写进了契约。 */
static void test_tcp_delivers_stream(void)
{
    CHECK(transport_framing_of(TRANSPORT_TYPE_TCP) == TRANSPORT_DELIVERS_STREAM);
}

/* 3) 【P3 精神】未知类型不猜成"完整消息"——
 *    乐观默认正是 D-09 的成因。 */
static void test_unknown_does_not_default_to_messages(void)
{
    CHECK(transport_framing_of((transport_type_t)99) == TRANSPORT_FRAMING_UNKNOWN);
    CHECK(transport_framing_of((transport_type_t)-1) == TRANSPORT_FRAMING_UNKNOWN);
    /* 明确断言"不是 MESSAGES"—— 这是本条的重点 */
    CHECK(transport_framing_of((transport_type_t)99) != TRANSPORT_DELIVERS_MESSAGES);
}

/* 4) 所有【已知】类型都必须有明确表态（不能是 UNKNOWN） */
static void test_all_known_types_are_declared(void)
{
    const transport_type_t known[] = { TRANSPORT_TYPE_MQTT, TRANSPORT_TYPE_TCP };
    for (size_t i = 0; i < sizeof(known) / sizeof(known[0]); i++) {
        transport_framing_t f = transport_framing_of(known[i]);
        CHECK(f == TRANSPORT_DELIVERS_MESSAGES || f == TRANSPORT_DELIVERS_STREAM);
    }
}

/* 5) 名字唯一且非空（避免名字在多处各写一遍 —— P4） */
static void test_names(void)
{
    CHECK(strcmp(transport_framing_name(TRANSPORT_DELIVERS_MESSAGES), "MESSAGES") == 0);
    CHECK(strcmp(transport_framing_name(TRANSPORT_DELIVERS_STREAM), "STREAM") == 0);
    CHECK(strcmp(transport_framing_name(TRANSPORT_FRAMING_UNKNOWN), "UNKNOWN") == 0);
    CHECK(strcmp(transport_framing_name((transport_framing_t)42), "UNKNOWN") == 0);
}

/* 6) 两种语义必须【互不相同】—— 否则契约没有区分力 */
static void test_semantics_are_distinct(void)
{
    CHECK(transport_framing_of(TRANSPORT_TYPE_MQTT) !=
          transport_framing_of(TRANSPORT_TYPE_TCP));
}

/* === D-21：操作表自审 ===
 * 让"哪个钩子有、哪个没有"变成可断言的数据，而不是靠人读源码。 */
static esp_err_t fake_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t fake_stop(transport_t *t)  { (void)t; return ESP_OK; }
static esp_err_t fake_send(transport_t *t, const uint8_t *d, size_t n)
{ (void)t; (void)d; (void)n; return ESP_OK; }
static bool fake_conn(transport_t *t) { (void)t; return true; }

static void test_ops_audit_complete_table(void)
{
    const transport_ops_t full = {
        .start = fake_start, .stop = fake_stop,
        .send = fake_send, .is_connected = fake_conn,
    };
    transport_ops_audit_t a;
    transport_audit_ops(&full, &a);
    CHECK(a.required_missing == 0);
    CHECK(a.has_start && a.has_stop && a.has_send && a.has_is_connected);
    CHECK(a.has_init == false && a.has_deinit == false);
    CHECK(a.optional_missing == 2);   /* 可选缺席要看得见，不是无视 */
}

static void test_ops_audit_flags_missing_required(void)
{
    /* 缺 send -> 必需缺失必须被数出来 */
    const transport_ops_t no_send = {
        .start = fake_start, .stop = fake_stop, .is_connected = fake_conn,
    };
    transport_ops_audit_t a;
    transport_audit_ops(&no_send, &a);
    CHECK(a.required_missing == 1);
    CHECK(a.has_send == false);

    /* NULL 整表：不崩，且必需全缺 */
    transport_audit_ops(NULL, &a);
    CHECK(a.required_missing == 4);
    CHECK(a.optional_missing == 2);
}

/* === D-10：写结果分类 —— 只有 COMPLETE 算成功 === */
static void test_write_classification(void)
{
    /* 全部写出 = 唯一成功 */
    CHECK(net_policy_classify_write(100, 100, false) == WRITE_COMPLETE);
    CHECK(net_policy_write_is_success(WRITE_COMPLETE) == true);

    /* 【核心】部分写【不是】成功 —— 旧代码用 written > 0 当成功，这就是 D-10 */
    CHECK(net_policy_classify_write(100, 40, false) == WRITE_PARTIAL);
    CHECK(net_policy_write_is_success(WRITE_PARTIAL) == false);
    CHECK(net_policy_classify_write(100, 1, false) == WRITE_PARTIAL);
    CHECK(net_policy_write_is_success(WRITE_PARTIAL) == false);

    /* 一个字节都没写出 */
    CHECK(net_policy_classify_write(100, 0, false) == WRITE_NOTHING);
    CHECK(net_policy_write_is_success(WRITE_NOTHING) == false);

    /* 硬错误优先于"写了多少"：部分写后 EPIPE 应是 ERROR（重试无意义） */
    CHECK(net_policy_classify_write(100, 40, true) == WRITE_ERROR);
    CHECK(net_policy_classify_write(100, 100, true) == WRITE_ERROR);
    CHECK(net_policy_write_is_success(WRITE_ERROR) == false);

    /* 零长度请求：视为完整（没有要写的东西） */
    CHECK(net_policy_classify_write(0, 0, false) == WRITE_COMPLETE);
}

static void test_write_names(void)
{
    CHECK(strcmp(net_policy_write_outcome_name(WRITE_COMPLETE), "COMPLETE") == 0);
    CHECK(strcmp(net_policy_write_outcome_name(WRITE_PARTIAL), "PARTIAL") == 0);
    CHECK(strcmp(net_policy_write_outcome_name(WRITE_NOTHING), "NOTHING") == 0);
    CHECK(strcmp(net_policy_write_outcome_name(WRITE_ERROR), "ERROR") == 0);
    CHECK(strcmp(net_policy_write_outcome_name((write_outcome_t)9), "UNKNOWN") == 0);
}

int main(void)
{
    test_mqtt_delivers_messages();
    test_tcp_delivers_stream();
    test_unknown_does_not_default_to_messages();
    test_all_known_types_are_declared();
    test_names();
    test_semantics_are_distinct();
    test_ops_audit_complete_table();
    test_ops_audit_flags_missing_required();
    test_write_classification();
    test_write_names();
    if (s_failures) { printf("transport_framing_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("transport_framing_tests: all checks passed\n");
    return 0;
}
