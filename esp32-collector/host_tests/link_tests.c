/* link_tests.c —— 上行链路语义（设计文档 1.2；原则 P1/P2/P3）
 *
 * 这些用例的存在理由：D-01 的病根是【接口返回值不足以表达调用方决策】，
 * 于是调用方写出"广播发过一次、再回退重发一次"的代码（L-02）。
 * 下面的断言把"决策所需的信息"和"不该发生的事"都钉死。 */
#include "link.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;

#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond);          \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ---- 计数驱动：用调用次数证明"该调的调了、不该调的没调" ---- */
typedef struct {
    int      open_calls;
    int      close_calls;
    int      send_calls;
    uint32_t mtu;
    bool     ready;
    link_result_t next_send;   /* 驱动要返回的结果 */
} fake_t;

static link_result_t fake_open(void *ctx)
{
    fake_t *f = (fake_t *)ctx;
    f->open_calls++;
    return LINK_SENT;
}
static void fake_close(void *ctx)
{
    fake_t *f = (fake_t *)ctx;
    f->close_calls++;
}
static link_result_t fake_send(void *ctx, const uint8_t *data, size_t len)
{
    fake_t *f = (fake_t *)ctx;
    (void)data; (void)len;
    f->send_calls++;
    return f->next_send;
}
static uint32_t fake_mtu(void *ctx) { return ((fake_t *)ctx)->mtu; }
static bool fake_is_ready(void *ctx) { return ((fake_t *)ctx)->ready; }

static const link_driver_t FAKE_DRV = {
    .open = fake_open, .close = fake_close, .send = fake_send,
    .mtu = fake_mtu, .is_ready = fake_is_ready, .name = "fake",
};

static void init_fake(fake_t *f, uint32_t mtu, bool ready)
{
    memset(f, 0, sizeof(*f));
    f->mtu = mtu;
    f->ready = ready;
    f->next_send = LINK_SENT;
}

/* 1) 【P2 核心】超 MTU 必须在【发出前】被拒绝，且【绝不调用】驱动 send。
 *    用 send_calls 计数证明 —— 这是"发出去了=成功了"这类假象的根治点。 */
static void test_too_big_is_rejected_before_driver(void)
{
    fake_t f; init_fake(&f, 100, true);
    link_t *l = link_create(&FAKE_DRV, &f);
    CHECK(l != NULL);

    uint8_t buf[101] = {0};
    link_result_t r = link_send(l, buf, 101);   /* 101 > mtu 100 */
    CHECK(r == LINK_PAYLOAD_TOO_BIG);
    CHECK(f.send_calls == 0);                   /* 关键：驱动【没被调用】 */

    /* 恰好等于 MTU 是允许的（边界） */
    r = link_send(l, buf, 100);
    CHECK(r == LINK_SENT);
    CHECK(f.send_calls == 1);

    link_destroy(l);
}

/* 2) 【P1 核心】未就绪必须由【驱动的结果】表达，而不是 link 层预检。
 *
 * 这条曾经断言的是反面（预检 + send_calls==0）—— 那正是设计文档 §1.2
 * 判为 TOCTOU 的"先查再发"，与 P1"结果即决策依据"矛盾。
 * 现在断言：驱动**被调用**，且它返回的 NOT_READY 被原样透传。 */
static void test_not_ready_comes_from_driver_result(void)
{
    fake_t f; init_fake(&f, 100, false);
    f.next_send = LINK_NOT_READY;      /* 驱动自己说"没就绪" */
    link_t *l = link_create(&FAKE_DRV, &f);
    uint8_t buf[4] = {1, 2, 3, 4};

    link_result_t r = link_send(l, buf, 4);
    CHECK(r == LINK_NOT_READY);
    CHECK(f.send_calls == 1);          /* ← 驱动被调用了：结果来自它 */

    /* 反面：驱动已就绪（is_ready 为真）但 send 返回 NOT_READY（竞态下真实存在）
     * ⇒ 结果仍必须是 NOT_READY。预检实现会把它误判成"可以发"，
     * 这正是 TOCTOU 的危害：查到的状态不能代表发送时的状态。 */
    fake_t g; init_fake(&g, 100, true);
    g.next_send = LINK_NOT_READY;
    link_t *l2 = link_create(&FAKE_DRV, &g);
    CHECK(link_send(l2, buf, 4) == LINK_NOT_READY);
    CHECK(g.send_calls == 1);

    link_destroy(l);
    link_destroy(l2);
}

/* 3) 【D-01 病根】驱动的结果必须被【原样透传，不许压平】。
 *    旧实现把 NOT_CONNECTED 与 FAILED 一起压成 ESP_FAIL，
 *    调用方因此无法区分"该退避"与"该报错"。 */
static void test_driver_result_is_not_flattened(void)
{
    const link_result_t cases[] = {
        LINK_SENT, LINK_BACKPRESSURE, LINK_NOT_READY, LINK_PAYLOAD_TOO_BIG, LINK_FATAL
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        fake_t f; init_fake(&f, 1000, true);
        f.next_send = cases[i];
        link_t *l = link_create(&FAKE_DRV, &f);
        uint8_t buf[8] = {0};
        link_result_t r = link_send(l, buf, 8);
        CHECK(r == cases[i]);
        link_destroy(l);
    }
}

/* 4) 【P3】每条路径都有计数器，且互不串台 */
static void test_each_path_is_counted(void)
{
    fake_t f; init_fake(&f, 10, true);
    link_t *l = link_create(&FAKE_DRV, &f);
    uint8_t big[11] = {0};
    uint8_t ok[4] = {0};

    f.ready = false;
    f.next_send = LINK_NOT_READY;       /* 由驱动结果表达未就绪 */
    (void)link_send(l, ok, 4);          /* NOT_READY */
    f.ready = true;
    (void)link_send(l, big, 11);        /* TOO_BIG */
    f.next_send = LINK_BACKPRESSURE;
    (void)link_send(l, ok, 4);          /* BACKPRESSURE */
    f.next_send = LINK_SENT;
    (void)link_send(l, ok, 4);          /* SENT */

    link_stats_t st;
    link_get_stats(l, &st);
    CHECK(st.tx_not_ready == 1);
    CHECK(st.tx_too_big == 1);
    CHECK(st.tx_backpressure == 1);
    CHECK(st.tx_sent == 1);
    CHECK(st.tx_fatal == 0);
    CHECK(st.tx_driver_error == 0);
    link_destroy(l);
}

/* 5) 参数错 -> FATAL（不是背压：调用方不该退避重试） */
static void test_bad_args_are_fatal_not_backpressure(void)
{
    fake_t f; init_fake(&f, 100, true);
    link_t *l = link_create(&FAKE_DRV, &f);
    uint8_t buf[4] = {0};

    CHECK(link_send(NULL, buf, 4) == LINK_FATAL);
    CHECK(link_send(l, NULL, 4) == LINK_FATAL);
    CHECK(link_send(l, buf, 0) == LINK_FATAL);
    CHECK(f.send_calls == 0);

    link_stats_t st; link_get_stats(l, &st);
    /* 只有【有对象】的调用能被计数：link_send(NULL,...) 没有 link_t 可写。
     * 这是接口的固有限制，不是缺陷 —— 3 次 FATAL 里只有 2 次可观测。
     * 之所以把它写成断言，是为了让这个限制【显式】而不是等人踩坑。 */
    CHECK(st.tx_fatal == 2);
    link_destroy(l);
}

/* 6) mtu()==0 视为"不可用"—— 不能因为 0 就放行任意长度 */
static void test_zero_mtu_rejects_everything(void)
{
    fake_t f; init_fake(&f, 0, true);
    link_t *l = link_create(&FAKE_DRV, &f);
    uint8_t buf[1] = {0};
    CHECK(link_send(l, buf, 1) == LINK_PAYLOAD_TOO_BIG);
    CHECK(f.send_calls == 0);
    link_destroy(l);
}

/* 7) 【D-06 的教训】必需函数缺失 -> 构造失败，而不是运行期静默降级 */
static void test_incomplete_driver_is_rejected(void)
{
    link_driver_t incomplete = FAKE_DRV;
    incomplete.send = NULL;
    CHECK(link_create(&incomplete, NULL) == NULL);
    incomplete = FAKE_DRV;
    incomplete.mtu = NULL;
    CHECK(link_create(&incomplete, NULL) == NULL);
    CHECK(link_create(NULL, NULL) == NULL);
}

/* 8) 结果名唯一且非空（避免结果名在多个文件里各写一遍 —— P4） */
static void test_result_names(void)
{
    for (int i = 0; i < LINK_RESULT_COUNT; i++) {
        const char *n = link_result_name((link_result_t)i);
        CHECK(n != NULL && n[0] != '\0');
        CHECK(strcmp(n, "UNKNOWN") != 0);
    }
    CHECK(strcmp(link_result_name((link_result_t)99), "UNKNOWN") == 0);
    /* 名字互不相同 */
    for (int i = 0; i < LINK_RESULT_COUNT; i++) {
        for (int j = i + 1; j < LINK_RESULT_COUNT; j++) {
            CHECK(strcmp(link_result_name((link_result_t)i),
                         link_result_name((link_result_t)j)) != 0);
        }
    }
}

/* 9) 生命周期：close 只在 opened 后被调用一次 */
static void test_lifecycle(void)
{
    fake_t f; init_fake(&f, 100, true);
    link_t *l = link_create(&FAKE_DRV, &f);
    CHECK(f.open_calls == 0);
    CHECK(link_open(l) == LINK_SENT);
    CHECK(f.open_calls == 1);

    link_stats_t st; link_get_stats(l, &st);
    CHECK(st.ready == true);
    CHECK(st.mtu == 100);

    link_destroy(l);
    CHECK(f.close_calls == 1);

    /* 【P1/P4】生命周期开关必须来自 open 的事实，而不是诊断快照。
     *
     * 构造出 opened 与 stats.ready 【不一致】的情形：
     *   open 成功（opened=true）后，一次 send 把 stats.ready 刷成 false
     *   （驱动此刻未就绪）—— 若 destroy 用 stats.ready 决定 close，
     *   就会【漏掉一次 close】。观测量被当控制量用，正是这条要防的。
     * （M41 变异即如此，本断言是唯一能抓到它的地方。） */
    fake_t g; init_fake(&g, 10, true);
    link_t *l2 = link_create(&FAKE_DRV, &g);
    CHECK(link_open(l2) == LINK_SENT);
    g.ready = false;                   /* 发送后快照会变 false */
    g.next_send = LINK_NOT_READY;
    (void)link_send(l2, (const uint8_t *)"x", 1);

    link_stats_t snap;
    link_get_stats(l2, &snap);
    CHECK(snap.ready == false);        /* 快照确实已经是 false */
    CHECK(g.close_calls == 0);         /* 还没销毁 */

    link_destroy(l2);
    CHECK(g.close_calls == 1);         /* ← 仍然必须 close 一次（用 opened 判断）*/
}

int main(void)
{
    test_too_big_is_rejected_before_driver();
    test_not_ready_comes_from_driver_result();
    test_driver_result_is_not_flattened();
    test_each_path_is_counted();
    test_bad_args_are_fatal_not_backpressure();
    test_zero_mtu_rejects_everything();
    test_incomplete_driver_is_rejected();
    test_result_names();
    test_lifecycle();

    if (s_failures != 0) {
        printf("link_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("link_tests: all checks passed\n");
    return 0;
}
