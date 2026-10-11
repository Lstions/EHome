/* device_link_downlink_tests.c —— 3.0 下行帧的**分发判定**（宿主可测）
 *
 * ## 这组用例防的是什么
 * `devlink_on_msg` 原先**只打一行日志 + 记 type**，从不调用
 * `msg_handler_process`。后果不是崩溃，而是**静默丢弃**：
 * 后端经 3.0 链路下发的 0x22（重启/恢复出厂）、配置下发等，
 * 在固件里被丢掉，操作员点"重启"设备毫无反应且**没有任何错误**。
 *
 * 补上调用之后还有第二个坑：**头里的 type 与 payload 首字节的 type**
 * 是同一语义的两处表示。不一致时若"挑一个信"，就会出现
 * 「按 header 派发、按 payload 解码」的静默错派发。
 *
 * ## 为什么断言要打到"后果"层
 * 只断言"计数器 +1"是不够的 —— 一个**永远丢弃**的实现也能让计数器好看。
 * 所以每条丢弃用例都同时断言"**确实没进分发**"，并且另有一条
 * **正常帧必须进分发**的反向对照（否则"全丢"也能骗过前面所有用例）。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "device_link_wiring.h"

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ══════════ 假分发：直接观察"到底有没有进去、进去的字节对不对" ══════════ */

#define CAP_MAX 64
static uint8_t s_cap[CAP_MAX];
static size_t  s_cap_len;
static int     s_call_count;

static void fake_dispatch(const uint8_t *data, size_t len)
{
    s_call_count++;
    s_cap_len = (len < CAP_MAX) ? len : CAP_MAX;
    if (data != NULL && s_cap_len > 0) memcpy(s_cap, data, s_cap_len);
}

static void reset_capture(void)
{
    s_call_count = 0;
    s_cap_len = 0;
    memset(s_cap, 0, sizeof(s_cap));
}

/* ══════════ ① 正常帧 ⇒ 真的进了分发，且拿到的是 payload（含首字节）══════════
 *
 * 这条同时是**反向对照**：若实现"永远丢弃"，本用例立刻红。 */
static void test_normal_frame_reaches_dispatch(void)
{
    reset_capture();
    devlink_rx_stats_t st = { 0, 0, 0 };

    /* 一条 0x22（远程运维）帧：payload 首字节 = 类型 = 0x22（2.x 约定）。 */
    const uint8_t payload[4] = { 0x22, 0x08, 0x01, 0x12 };

    devlink_rx_verdict_t v = devlink_rx_handle(0x22, sizeof(payload), payload,
                                               fake_dispatch, &st);

    CHECK(v == DEVLINK_RX_DISPATCH, "正常帧应判 DISPATCH（实际 %s）",
          devlink_rx_verdict_name(v));
    /* ⭐ 后果层：真的进了分发 */
    CHECK(s_call_count == 1, "正常帧必须**真的**进分发（实际调用 %d 次）—— "
                             "0 次说明 0x22 又被静默丢弃了", s_call_count);
    /* ⭐ 传的是 payload 原样，**不是** header。msg_handler_process 读 data[0]，
     *   若传 header，这里第一个字节就会是 ver 而不是 0x22。 */
    CHECK(s_cap_len == sizeof(payload), "分发拿到 %zu 字节，期望 %zu",
          s_cap_len, sizeof(payload));
    CHECK(s_cap[0] == 0x22, "分发的第 0 字节必须是 payload 的 0x22（不是 header 的 ver）；"
                            "实际 0x%02X —— msg_handler_process 按 data[0] 取类型",
          s_cap[0]);
    CHECK(memcmp(s_cap, payload, sizeof(payload)) == 0,
          "交给分发的字节必须与 payload 逐字节相同");
    CHECK(st.dispatched == 1 && st.dropped_empty == 0 && st.dropped_type_mismatch == 0,
          "计数应为 派发=1 空=0 不一致=0（实际 %u/%u/%u）",
          (unsigned)st.dispatched, (unsigned)st.dropped_empty,
          (unsigned)st.dropped_type_mismatch);
}

/* ══════════ ② 类型不一致 ⇒ 丢弃 + 计数 + **没进分发** ══════════ */
static void test_type_mismatch_is_dropped(void)
{
    reset_capture();
    devlink_rx_stats_t st = { 0, 0, 0 };

    /* header 说 0x22，payload 首字节说 0x20 —— 两处表示不一致。
     * 若"挑一个信"，就会出现按 0x22 派发、却按 0x20 解码的静默错派发。 */
    const uint8_t payload[4] = { 0x20, 0x01, 0x02, 0x03 };

    devlink_rx_verdict_t v = devlink_rx_handle(0x22, sizeof(payload), payload,
                                               fake_dispatch, &st);

    CHECK(v == DEVLINK_RX_DROP_TYPE_MISMATCH, "类型不一致应判 DROP_TYPE_MISMATCH（实际 %s）",
          devlink_rx_verdict_name(v));
    CHECK(st.dropped_type_mismatch == 1, "类型不一致计数应 +1（实际 %u）",
          (unsigned)st.dropped_type_mismatch);
    CHECK(st.dispatched == 0, "不一致时派发计数必须为 0");
    /* ⭐ 后果层：**没有**进分发（只看计数器是能被"永远丢弃"骗过的） */
    CHECK(s_call_count == 0, "类型不一致的帧**绝不能**进分发（实际调用 %d 次）",
          s_call_count);

    /* 反向：交换方向（payload 说 0x22、header 说 0x20）同样必须丢弃 ——
     * 校验不能只在"某一个方向"成立。 */
    reset_capture();
    const uint8_t p2[2] = { 0x22, 0x00 };
    (void)devlink_rx_handle(0x20, sizeof(p2), p2, fake_dispatch, &st);
    CHECK(s_call_count == 0, "反向不一致也必须丢弃（实际调用 %d 次）", s_call_count);
    CHECK(st.dropped_type_mismatch == 2, "累计不一致计数应为 2（实际 %u）",
          (unsigned)st.dropped_type_mismatch);
}

/* ══════════ ③ 空 payload ⇒ 不越界、不崩、丢弃 ══════════ */
static void test_empty_payload_is_safe(void)
{
    reset_capture();
    devlink_rx_stats_t st = { 0, 0, 0 };

    /* payload_len == 0：**不得读 payload[0]**。
     * 这里给一个真实指针，但长度为 0 —— 越界读在宿主上未必崩，
     * 但用 NULL 传指针可以把"读了首字节"变成立刻段错误，从而**必然**暴露。 */
    devlink_rx_verdict_t v = devlink_rx_handle(0x22, 0, NULL, fake_dispatch, &st);
    CHECK(v == DEVLINK_RX_DROP_EMPTY, "空 payload 应判 DROP_EMPTY（实际 %s）",
          devlink_rx_verdict_name(v));
    CHECK(st.dropped_empty == 1, "空帧计数应 +1（实际 %u）", (unsigned)st.dropped_empty);
    CHECK(s_call_count == 0, "空帧不得进分发");
    CHECK(st.dropped_type_mismatch == 0,
          "空帧**不是**类型不一致（别把两种原因混成一个计数）");

    /* 非 NULL 指针但 len==0：同样不得分发 */
    const uint8_t dummy = 0x22;
    (void)devlink_rx_handle(0x22, 0, &dummy, fake_dispatch, &st);
    CHECK(st.dropped_empty == 2, "len==0 且指针非空也应判空（实际 %u）",
          (unsigned)st.dropped_empty);
    CHECK(s_call_count == 0, "len==0 不得进分发");
}

/* ══════════ ④ 判定是纯函数：同样输入永远同样输出 ══════════ */
static void test_verdict_is_pure(void)
{
    const uint8_t ok[2] = { 0x22, 0x00 };
    for (int i = 0; i < 5; i++) {
        CHECK(devlink_rx_verdict(0x22, sizeof(ok), ok) == DEVLINK_RX_DISPATCH,
              "判定应稳定可重复");
    }
    /* stats == NULL / dispatch == NULL 都要能安全调用（诊断路径会这么用） */
    reset_capture();
    CHECK(devlink_rx_handle(0x22, sizeof(ok), ok, NULL, NULL) == DEVLINK_RX_DISPATCH,
          "dispatch/stats 为 NULL 时仍应返回正确判定");
    CHECK(s_call_count == 0, "dispatch 为 NULL 时不应调用任何东西");
}

int main(void)
{
    test_normal_frame_reaches_dispatch();
    test_type_mismatch_is_dropped();
    test_empty_payload_is_safe();
    test_verdict_is_pure();

    if (s_failures) { printf("device_link_downlink_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_link_downlink_tests: all checks passed\n");
    return 0;
}
