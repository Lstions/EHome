/* transport_dualstack_tests.c —— 双栈期"同一帧被发两次"的**实证**
 *
 * ## 为什么必须有这个文件
 *
 * 3.0 的目标是把上行从 MQTT 迁到 TCP，而迁移期（设计 §7.3 P0-P3）**两条链路同时活着**：
 * MQTT（服务 2.8.0 设备）与 3.0 TCP 会话。
 *
 * `msg_handler_publish_checked` 的选路是：
 *   1. `s_current_transport`（**仅在下行处理期间**被 `msg_handler_process_with_transport` 设置）
 *   2. 否则 `transport_broadcast` —— 而 broadcast **对每一个 is_connected() 为真的 transport 都发**
 *
 * ⇒ 设备**主动上行**（Hello/DataReport/DataBatch/状态上报）时 `s_current_transport` 为 NULL
 *   （那是在链路任务里发的，不是在下行处理中），于是走 broadcast。
 *   若 MQTT 与 3.0 会话**同时 connected**，同一帧会被**发两次**。
 *
 * ## 这不是"可能"，是可测的
 * 本文件用**真实的 components/transport/transport.c**（不是替身）构造那个场景并数 send 次数。
 * 数字就是证据：2 次 = 双发。
 *
 * ## 为什么它值得单独立文件
 * "同一帧发两遍"在本项目里是**静默**的：
 *   - 后端可能把重复 Hello 当重连（无害），但把重复 DataReport/DataBatch 当**两批数据**入库；
 *   - 设备侧看不到任何错误（两次 send 都返回 ESP_OK）。
 * 而这正是审计 D-01 同一族（"压平/重复"）的形态。
 *
 * ## ⚠ 一个我自己踩到的坑（写在这里省下一个人）
 * `transport_manager_init()` **不是** reset —— 它在 `s_initialized` 已为真时**直接 return**。
 * 我第一版用它"重置"注册表，于是第二个用例的 transport **根本没注册进去**，
 * 而 broadcast 仍遍历着**第一个用例遗留的、已被 free 的** transport
 * ⇒ 读已释放内存 ⇒ **SIGILL 崩溃**（退出码 132，没有任何测试输出）。
 * ⇒ 正确做法：每个用例用**独立的 transport 对象**，并在结束时 `transport_unregister`，
 *    而不是指望 init 帮你清空。
 */
#include "transport.h"

#include <stdio.h>
#include <stdlib.h>

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  ", __FILE__, __LINE__);           \
                       printf(__VA_ARGS__); printf("\n"); s_failures++; }   \
    } while (0)

typedef struct { int send_calls; bool connected; } fake_t;

static esp_err_t f_init(transport_t *t, const void *cfg) { (void)t; (void)cfg; return ESP_OK; }
static esp_err_t f_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t f_stop(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t f_send(transport_t *t, const uint8_t *d, size_t n)
{
    fake_t *f = (fake_t *)t->priv_data; (void)d; (void)n; f->send_calls++; return ESP_OK;
}
static bool f_conn(transport_t *t) { return ((fake_t *)t->priv_data)->connected; }
static void f_deinit(transport_t *t) { (void)t; }

static const transport_ops_t FAKE_OPS = {
    .init = f_init, .start = f_start, .stop = f_stop,
    .send = f_send, .is_connected = f_conn, .deinit = f_deinit,
};

static transport_t *make(transport_type_t type, fake_t *f)
{
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->ops = &FAKE_OPS; t->type = type; t->priv_data = f;
    return t;
}

/* ⭐ 核心：双栈期（MQTT + 3.0 TCP **都 connected**）广播一帧会发生什么 */
static void test_dualstack_broadcast_sends_twice(void)
{
    transport_manager_init();

    fake_t mqtt = { 0, true };    /* MQTT 已连接（服务 2.8.0 设备） */
    fake_t tcp3 = { 0, true };    /* 3.0 TCP 会话已 READY */
    transport_t *tm = make(TRANSPORT_TYPE_MQTT, &mqtt);
    transport_t *tt = make(TRANSPORT_TYPE_TCP,  &tcp3);
    CHECK(transport_register(tm) == ESP_OK, "注册 MQTT");
    CHECK(transport_register(tt) == ESP_OK, "注册 3.0 TCP");

    transport_broadcast_report_t rep;
    (void)transport_broadcast_ex((const uint8_t *)"HELLO", 5, &rep);

    /* 实证：两条都 connected ⇒ 两条都被调用 ⇒ **同一帧发了两次** */
    CHECK(mqtt.send_calls == 1, "MQTT 应被调用 1 次，实际 %d", mqtt.send_calls);
    CHECK(tcp3.send_calls == 1, "3.0 TCP 应被调用 1 次，实际 %d", tcp3.send_calls);

    /* 把结论**显式**断言出来：这就是"双栈期设备主动上行会被发两次"。
     * 本用例不是在断言"这是对的" —— 它是在**记录当前真实行为**，
     * 以便：① 任何"修好双发"的改动都会让它变红并要求更新；
     *       ② 接入 3.0 transport 的人**先看到这个数字**再决定怎么接。 */
    CHECK(mqtt.send_calls + tcp3.send_calls == 2,
          "双栈期一帧被投递 %d 次（期望 2 = 双发已被证实）",
          mqtt.send_calls + tcp3.send_calls);
    CHECK(rep.attempted == 2, "attempted 应为 2，实际 %d", rep.attempted);
    CHECK(rep.sent == 2, "sent 应为 2，实际 %d", rep.sent);

    /* 对照：只有 MQTT 连接时只发一次（迁移前行为，必须保持不变） */
    transport_manager_init();
    fake_t only_mqtt = { 0, true };
    fake_t off_tcp   = { 0, false };
    transport_t *m2 = make(TRANSPORT_TYPE_MQTT, &only_mqtt);
    transport_t *t2 = make(TRANSPORT_TYPE_TCP,  &off_tcp);
    (void)transport_register(m2);
    (void)transport_register(t2);
    (void)transport_broadcast_ex((const uint8_t *)"HELLO", 5, NULL);
    CHECK(only_mqtt.send_calls == 1, "仅 MQTT 连接时应只发 1 次，实际 %d", only_mqtt.send_calls);
    CHECK(off_tcp.send_calls == 0, "未连接的 3.0 不应被调用，实际 %d", off_tcp.send_calls);

    /* ⚠ 必须注销：下一阶段（对照）用的是**另外两个**对象，
     * 若把这两个留在注册表里，它们会在后续 broadcast 里被再次调用。 */
    (void)transport_unregister(tm);
    (void)transport_unregister(tt);
    (void)transport_unregister(m2);
    (void)transport_unregister(t2);
}

/* 反向对照：3.0 未 READY 时**不得**被投递（否则就是上一轮 is_connected 判据的意义所在） */
static void test_three_zero_not_ready_is_not_delivered(void)
{
    /* ⚠ **不要**再调 transport_manager_init()：它不是 reset（见文件头说明）。
     * 注册表此时应当是干净的（上一个用例已 unregister 自己的对象）。 */
    fake_t mqtt = { 0, true };
    fake_t tcp3 = { 0, false };   /* 链路通但未握手（WAIT_HANDSHAKE） */
    transport_t *tm = make(TRANSPORT_TYPE_MQTT, &mqtt);
    transport_t *tt = make(TRANSPORT_TYPE_TCP,  &tcp3);
    (void)transport_register(tm);
    (void)transport_register(tt);

    (void)transport_broadcast_ex((const uint8_t *)"HELLO", 5, NULL);

    CHECK(tcp3.send_calls == 0,
          "未 READY 的 3.0 会话不得被投递（投了就是静默丢弃），实际 %d", tcp3.send_calls);
    CHECK(mqtt.send_calls == 1, "MQTT 兜底应仍收到 1 次，实际 %d", mqtt.send_calls);

    (void)transport_unregister(tm);
    (void)transport_unregister(tt);
}


/* ==========================================================================
 * task-21 新增：仲裁后【恰好一条】被投递
 *
 * ## 与上一条用例的关系（两条并存，不是替换）
 *
 * test_dualstack_broadcast_sends_twice 记录的是【broadcast 的固有 fan-out 语义】：
 * 它对每个 is_connected() 为真的 transport 都发。**那个事实依然为真**，
 * 它是「为什么需要仲裁层」的证据，不该被改绿（Lead 明确要求保留）。
 *
 * 本用例记录的是【接线后的生产行为】：
 * 仲裁层让两条门互斥 ⇒ 任一时刻最多一条 is_connected 为真 ⇒ 恰好一次投递。
 *
 * ## 诚实说明：这条用例建模的是什么
 *
 * 它【不能】测真实仲裁层 —— 那要编 uplink_arbiter.c + session + MQTT，
 * 而本 target 只链 transport.c（见 CMakeLists 的注释）。
 * 它测的是：给定「门互斥」这个性质，broadcast 是否恰好投一条。
 * ⇒ 门的互斥性由 uplink_arbiter_tests.c 穷举 16 种组合证明（那边是纯函数）；
 *    本条证明「互斥 ⇒ 单发」这一步。两条合起来才是完整论证。
 *
 * 这也是本卡要求的形态：双发无法在 transport 层消除（那是 broadcast 的语义），
 * 只能由上层选路消除 —— 本条就是那个「上层选路」的可执行证据。
 * ========================================================================== */
static void test_arbiter_keeps_exactly_one_delivery(void)
{
    /* 场景：双栈稳态，但仲裁层选中 TCP ⇒ 3.0 门开、MQTT 门关。
     * 注意 MQTT 的链路本身是通的 —— 只是【门】关着。
     * 这正是 M1' 的实质：不动注册表，只动门。 */
    transport_manager_init();
    fake_t mqtt = { 0, false };   /* 门关：仲裁未选中 MQTT */
    fake_t tcp3 = { 0, true  };   /* 门开：仲裁选中 TCP 且 READY */
    transport_t *tm = make(TRANSPORT_TYPE_MQTT, &mqtt);
    transport_t *tt = make(TRANSPORT_TYPE_TCP,  &tcp3);
    CHECK(transport_register(tm) == ESP_OK, "注册 MQTT");
    CHECK(transport_register(tt) == ESP_OK, "注册 3.0 TCP");

    transport_broadcast_report_t rep;
    (void)transport_broadcast_ex((const uint8_t *)"TELEMETRY", 9, &rep);

    CHECK(tcp3.send_calls == 1, "仲裁选中 TCP ⇒ 3.0 应恰好被投 1 次，实际 %d",
          tcp3.send_calls);
    CHECK(mqtt.send_calls == 0, "仲裁未选中 MQTT ⇒ MQTT 必须 0 次，实际 %d",
          mqtt.send_calls);
    CHECK(tcp3.send_calls + mqtt.send_calls == 1,
          "仲裁后必须恰好一次投递，实际 %d 次（2 次=双发未消除，0 次=上行掉了）",
          tcp3.send_calls + mqtt.send_calls);
    CHECK(rep.attempted == 1, "attempted 应为 1，实际 %d", rep.attempted);
    CHECK(rep.sent == 1, "sent 应为 1，实际 %d", rep.sent);

    (void)transport_unregister(tm);
    (void)transport_unregister(tt);

    /* 反向：仲裁切到 MQTT（TCP 连续失败达阈值）⇒ 恰好 MQTT 一条。
     * 这一半同样重要：只测「TCP 优先」会漏掉「兜底还通不通」。 */
    fake_t mqtt2 = { 0, true  };   /* 门开：兜底态 */
    fake_t tcp2  = { 0, false };   /* 门关：3.0 未 READY（或 tsel 已切 MQTT） */
    transport_t *m2 = make(TRANSPORT_TYPE_MQTT, &mqtt2);
    transport_t *t2 = make(TRANSPORT_TYPE_TCP,  &tcp2);
    (void)transport_register(m2);
    (void)transport_register(t2);
    transport_broadcast_report_t rep2;
    (void)transport_broadcast_ex((const uint8_t *)"TELEMETRY", 9, &rep2);
    CHECK(mqtt2.send_calls == 1, "兜底态 MQTT 应恰好被投 1 次，实际 %d", mqtt2.send_calls);
    CHECK(tcp2.send_calls == 0, "兜底态 3.0 必须 0 次，实际 %d", tcp2.send_calls);
    CHECK(rep2.mqtt_attempted && !rep2.tcp_attempted,
          "兜底态应只尝试 MQTT（mqtt=%d tcp=%d）",
          (int)rep2.mqtt_attempted, (int)rep2.tcp_attempted);
    (void)transport_unregister(m2);
    (void)transport_unregister(t2);
}
int main(void)
{
    test_dualstack_broadcast_sends_twice();
    test_three_zero_not_ready_is_not_delivered();
    test_arbiter_keeps_exactly_one_delivery();

    if (s_failures) { printf("transport_dualstack_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("transport_dualstack_tests: all checks passed\n");
    return 0;
}
