/* sync_manager_uplink_tests.c —— 2026-10-08 真机缺陷的回归护栏
 *
 * 缺陷（真机，C6 + P3 + MQTT 死地址，见设计文档 §164）：
 *   sync_manager 原先**硬编码**判 mqtt_client_is_connected_impl()，于是无 MQTT 时
 *   sync_manager_request_sync() **直接 return**（只留一条 WARN）——
 *   "周期 / 怀疑 / 无配置"三条**主动请求同步**的路径全部失效。
 *   配置之所以还能同步，是靠 device_link 自己的握手 Hello 兜住的，不是本模块的功劳。
 *   ⇒ §7.3 P4（后端关 MQTT）后 mqtt_client_is_connected_impl() 永远 false
 *     ⇒ 这三条路径**永久死掉且不报错**。
 *
 * 修法：把"有没有可用上行"做成**注入的函数指针**（P7），由 main 接到上行仲裁。
 *
 * 本用例断言三件事：
 *   ① **注入后**：MQTT 不可用但注入的判据说"上行可用" ⇒ Hello **必须发出去**；
 *   ② **注入后**：两条都不通 ⇒ 不发；
 *   ③ **未注入时**：**保守返回 false ⇒ 不发**（fail-closed）。
 *
 * ⚠ 2026-10-08（§194）：③ 的语义**已反转**。原断言是"未注入时退化为只看 MQTT
 *   （与改动前逐位一致）"；MQTT 彻底移除后，该兜底依赖的
 *   mqtt_client_is_connected_impl() **已不存在**，生产改为**保守返回 false**
 *   ⇒ 断言随之翻转。保留该用例（而非删除）的理由：它锁住
 *     "**未注入时不假装有上行**"这条 fail-closed 性质 —— 若有人为了
 *     "让同步更积极"而在未注入时返回 true，本用例立刻红。
 *
 * ⚠ 顺带：mqtt 替身（s_mqtt_connected）**已不再被生产读取**（生产对
 *   mqtt_client_* 零引用）⇒ 它现在是死替身。删除它，因为它会让读者
 *   误以为"生产还在看 MQTT 的连接状态"。
 */
#include <stdio.h>
#include <string.h>
#include <stdbool.h>

#include "sync_manager.h"

/* ---- 注入的"上行可用"判据 ---- */
static bool s_uplink_available = false;
static bool uplink_stub(void) { return s_uplink_available; }

/* ---- 观察点：Hello 有没有被请求发出 ---- */
static int s_hello_calls = 0;
static void on_send_hello(void) { s_hello_calls++; }

static int s_failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { printf("  FAIL %s\n", msg); s_failures++; } \
    else         { printf("  ok   %s\n", msg); } \
} while (0)

static void reset(void)
{
    s_uplink_available = false;
    s_hello_calls = 0;
}

/* ① 注入后：注入的判据说"上行可用" ⇒ Hello 必须发出去。
 * 它凭什么会失败：把 uplink_available() 改回忽略注入、直接 return false，
 * 本用例立刻红（hello_calls 会是 0）。 */
static void test_injected_gate_allows_sync_without_mqtt(void)
{
    reset();
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_send_hello);
    sync_manager_register_uplink_available_cb(uplink_stub);

    /* ⚠ 用 SYNC_REASON_FORCED 而不是 PERIODIC：后者受**去重窗口**抑制
     * （should_request_sync 里 (now - last_sync) > SYNC_PERIODIC_SEC），
     * 会把本用例变成"在测去重策略"而不是"在测上行可用性判定"。 */
    s_uplink_available = true;  /* 3.0 可用 */
    sync_manager_request_sync(SYNC_REASON_FORCED);

    CHECK(s_hello_calls == 1,
          "注入的判据说上行可用时，必须请求发 Hello（3.0 是唯一上行）");
}

/* ② 注入后：两条都不通 ⇒ 不发。
 * 它凭什么会失败：把失败分支的 return 去掉，会继续往下走并调用 cb。 */
static void test_injected_gate_blocks_when_no_uplink(void)
{
    reset();
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_send_hello);
    sync_manager_register_uplink_available_cb(uplink_stub);

    s_uplink_available = false;
    sync_manager_request_sync(SYNC_REASON_FORCED);

    CHECK(s_hello_calls == 0, "上行不可用时不得请求发 Hello");
}

/* ③ **未注入**时**保守返回 false**（fail-closed）—— MQTT 移除后的现行语义。
 *
 * ⚠⚠ **本用例必须最先运行**（main 里的调用顺序即判据）：
 *   sync_manager_register_uplink_available_cb() 是**单槽赋值**且**没有注销接口**
 *   ⇒ 一旦任何用例注入过，进程内就**回不到"未注入"状态**。
 *   （这不是产品缺陷：固件里 main 只注册一次；但测试必须尊重它。）
 *   我第一版把它排在注入用例**之后**，于是 s_uplink_available_cb 仍指向 stub，
 *   而 stub 读的 s_uplink_available 被 reset() 清成 false ⇒ 断言假红。
 *   **教训：单槽注入的模块，其"未注入"断言只能在最前面测。**
 *
 * ⚠ 2026-10-08（§194）：本用例的**断言已随 MQTT 移除翻转**。
 *   旧版测的是"未注入时退化为只看 MQTT（与改动前逐位一致）" ——
 *   但那个兜底依赖的 mqtt_client_is_connected_impl() 已随组件删除，
 *   生产改为**保守返回 false**。若还断言"MQTT 可用 ⇒ 发"，就得给生产
 *   塞回一个 MQTT 判据，正是本轮要删的东西。
 *   ⇒ 保留用例本身（而非删掉）的理由：它锁住"**未注入时不假装有上行**"
 *     这条 fail-closed 性质。**它凭什么会失败**：把 uplink_available() 的
 *     NULL 分支改成 return true（比如有人为了"让同步更积极"这么干），
 *     下面那句 CHECK 立刻红。 */
static void test_without_injection_fails_closed(void)
{
    reset();
    sync_manager_init();
    sync_manager_register_send_hello_cb(on_send_hello);
    /* ⚠ 刻意**不**注入 uplink cb */

    sync_manager_request_sync(SYNC_REASON_FORCED);
    CHECK(s_hello_calls == 0,
          "未注入时：保守返回 false ⇒ 不发（fail-closed；MQTT 移除后无兜底）");
}

int main(void)
{
    printf("=== sync_manager 上行可用性（2026-10-08 真机缺陷护栏）===\n");
    /* ⚠ 顺序即判据：③ 测的是"**未注入**时的退化行为"，而注入是**单槽且不可注销**
     * ⇒ 它必须**最先**跑（见该函数上方的说明）。 */
    test_without_injection_fails_closed();
    test_injected_gate_allows_sync_without_mqtt();
    test_injected_gate_blocks_when_no_uplink();
    printf("=== %s（失败 %d）===\n", s_failures == 0 ? "PASS" : "FAIL", s_failures);
    return s_failures == 0 ? 0 : 1;
}
