/* ⭐ 方案 D（2026-10-10）：`channel_cmd_v2_borrow_plan()` 的 host-test 替身。
 *
 * 生产实现（handler_channel_cmd_v2.c）从 msg_handler 的 control 槽位里借出
 * plan；host_tests 不链接该文件，因此这里提供一个**行为等价的替身**：
 * 用测试自己的一块 plan buffer 承载，并模拟槽位状态校验。
 *
 * 与生产实现的对应关系（保持这些语义一致，否则测试会变成假绿）：
 *   · 槽位状态不是 QUEUED/COMPLETING => 返回 false（模拟"已复用"）
 *   · plan_len == 0                   => 返回 false（本来就没有 plan）
 *   · 否则交回指针/长度/步数
 *
 * 测试通过 host_test_plan_* 辅助函数装载 plan —— 这取代了旧测试直接写
 * cmd->plan_data 的做法（队列元素已不再内联 plan buffer）。
 */
#include <string.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stddef.h>   /* size_t */

#define HOST_TEST_PLAN_MAX 512
#define HOST_TEST_SLOT_COUNT 5

typedef struct {
    uint8_t  data[HOST_TEST_PLAN_MAX];
    size_t   len;
    uint8_t  steps;
    bool     queued;   /* 模拟槽位是否处于 QUEUED/COMPLETING */
} host_test_slot_t;

static host_test_slot_t s_host_slots[HOST_TEST_SLOT_COUNT];

/* --- 测试用辅助接口（仅在 host 测试中可见）--- */
void host_test_plan_reset(void)
{
    memset(s_host_slots, 0, sizeof(s_host_slots));
    for (int i = 0; i < HOST_TEST_SLOT_COUNT; i++) s_host_slots[i].queued = true;
}

void host_test_plan_clear(uint8_t slot)
{
    if (slot >= HOST_TEST_SLOT_COUNT) return;
    memset(&s_host_slots[slot], 0, sizeof(s_host_slots[slot]));
    s_host_slots[slot].queued = true;
}

/* 追加一个 step（复刻 handler_channel_cmd_v2.c 的 2 B 小端长度前缀格式）*/
void host_test_plan_append(uint8_t slot, const uint8_t *bytes, size_t n)
{
    if (slot >= HOST_TEST_SLOT_COUNT || !bytes) return;
    host_test_slot_t *s = &s_host_slots[slot];
    if (s->len + 2 + n > HOST_TEST_PLAN_MAX) return;
    s->data[s->len]     = (uint8_t)(n & 0xffU);
    s->data[s->len + 1] = (uint8_t)(n >> 8);
    memcpy(s->data + s->len + 2, bytes, n);
    s->len += 2 + n;
    s->steps++;
}

/* 直接设置原始 plan 字节（用于构造畸形 plan 的负例）*/
void host_test_plan_set_raw(uint8_t slot, const uint8_t *bytes, size_t n, uint8_t steps)
{
    if (slot >= HOST_TEST_SLOT_COUNT || !bytes || n > HOST_TEST_PLAN_MAX) return;
    host_test_slot_t *s = &s_host_slots[slot];
    memcpy(s->data, bytes, n);
    s->len = n;
    s->steps = steps;
}

/* 模拟「槽位已被复用」——用于验证访问器会拒绝而非交出脏数据 */
void host_test_plan_set_released(uint8_t slot)
{
    if (slot >= HOST_TEST_SLOT_COUNT) return;
    s_host_slots[slot].queued = false;
}

/* --- 被测代码调用的访问器（与生产实现同签名、同语义）--- */
bool channel_cmd_v2_borrow_plan(uint8_t slot, const uint8_t **out_plan,
                                size_t *out_len, uint8_t *out_steps)
{
    if (!out_plan || !out_len || !out_steps) return false;
    *out_plan = NULL; *out_len = 0; *out_steps = 0;
    if (slot >= HOST_TEST_SLOT_COUNT) return false;
    host_test_slot_t *s = &s_host_slots[slot];
    if (!s->queued) return false;        /* 不变量被破坏：拒绝交出 plan */
    if (s->len == 0) return false;       /* 本来就没有 plan */
    *out_plan  = s->data;
    *out_len   = s->len;
    *out_steps = s->steps;
    return true;
}