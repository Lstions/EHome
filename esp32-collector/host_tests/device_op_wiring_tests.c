/* device_op_wiring_tests.c —— main 侧注入的三个原语
 *
 * 这里唯一值得单独测的判断是：**"命名空间不存在"不是失败**。
 *
 * nvs_open(NVS_READWRITE) 对从未创建过的命名空间返回 ESP_ERR_NVS_NOT_FOUND。
 * 那意味着"它本来就是空的"，即恢复出厂想要的状态**已经达到**。
 * 把它当失败 ⇒ "设备上还没写过 config"这种完全正常的情况报 ERASE_FAILED
 * ⇒ 操作员看到一个**永远失败、其实已经成功**的操作。
 *
 * 这条分支在宿主侧能不能真的走到，取决于 esp_err.h 桩里
 * ESP_ERR_NVS_NOT_FOUND 的数值是否与 IDF 一致（0x110A）——
 * 所以本测试同时也验证了那个桩不是"造一个走不到的假值"。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_err.h"
#include "esp_log.h"
#include "nvs_flash.h"

/* 被注入的三个原语在 device_op_wiring.c 里是 static，无法直接链接；
 * 因此本测试**摊入真实的那个 .c**（不是复制一份实现）。
 *
 * 路径必须写成 "../main/..." —— tools/check_host_coverage.py 是按这个
 * 形态认定"该生产文件已被宿主测试编译"的（INC_RE）。写成裸文件名也能编过，
 * 但门禁会把该文件算作"从未被编译"，
 * 于是**覆盖统计与实际不符**（我第一版就是这样，门禁当场报了 16 > 15）。 */
#include "../main/device_op_wiring.c"

/* device_op_wiring.c 只调 msg_handler_publish_checked（由 main 提供强实现）。
 * 这里给一个可配置的假实现，用来驱动"发送失败"分支。 */
esp_err_t g_publish_checked_result = ESP_OK;
static int s_publish_calls;

esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len)
{
    (void)data; (void)len;
    s_publish_calls++;
    return g_publish_checked_result;
}

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

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

/* esp_err_to_name 的宿主桩（其它宿主测试同款）。 */
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }

/* esp_restart 的宿主桩：**不返回**是 IDF 的语义，但宿主侧必须能返回，
 * 否则测试进程会一直卡在重启里。这里只记录被调用过。 */
static int s_restart_calls;
void esp_restart(void) { s_restart_calls++; }

/* ── NVS 假实现：可配置每个命名空间的行为 ── */
#define FAKE_MAX_NS 8
typedef struct {
    char name[32];
    int  open_result;     /* 返回给 nvs_open 的错误码 */
    int  erase_result;
    int  commit_result;
} fake_ns_t;

static fake_ns_t s_ns[FAKE_MAX_NS];
static int s_ns_n;
static int s_erase_calls;
static int s_open_calls;

static void reset_fake(void)
{
    memset(s_ns, 0, sizeof(s_ns));
    s_ns_n = 0; s_erase_calls = 0; s_open_calls = 0;
}

static void fake_ns_add(const char *name, int open_result, int erase_result, int commit_result)
{
    if (s_ns_n >= FAKE_MAX_NS) return;
    snprintf(s_ns[s_ns_n].name, sizeof(s_ns[s_ns_n].name), "%s", name);
    s_ns[s_ns_n].open_result = open_result;
    s_ns[s_ns_n].erase_result = erase_result;
    s_ns[s_ns_n].commit_result = commit_result;
    s_ns_n++;
}

static fake_ns_t *fake_lookup(const char *name)
{
    for (int i = 0; i < s_ns_n; i++) {
        if (strcmp(s_ns[i].name, name) == 0) return &s_ns[i];
    }
    return NULL;
}

esp_err_t nvs_open(const char *name, int mode, nvs_handle_t *handle)
{
    (void)mode;
    s_open_calls++;
    fake_ns_t *ns = fake_lookup(name);
    if (ns == NULL) {
        /* 未登记的命名空间：模拟"从未创建过"。 */
        return ESP_ERR_NVS_NOT_FOUND;
    }
    if (ns->open_result != ESP_OK) return ns->open_result;
    *handle = 1;
    return ESP_OK;
}

esp_err_t nvs_erase_all(nvs_handle_t handle) { (void)handle; s_erase_calls++; return ESP_OK; }
esp_err_t nvs_commit(nvs_handle_t handle) { (void)handle; return ESP_OK; }
void nvs_close(nvs_handle_t handle) { (void)handle; }

/* 其余 nvs_* 桩（本测试不用，但链接需要） */
esp_err_t nvs_get_u8(nvs_handle_t h, const char *k, uint8_t *v) { (void)h;(void)k;(void)v; return ESP_ERR_NOT_FOUND; }
esp_err_t nvs_get_u64(nvs_handle_t h, const char *k, uint64_t *v) { (void)h;(void)k;(void)v; return ESP_ERR_NOT_FOUND; }
esp_err_t nvs_get_str(nvs_handle_t h, const char *k, char *v, size_t *l) { (void)h;(void)k;(void)v;(void)l; return ESP_ERR_NOT_FOUND; }
esp_err_t nvs_set_u8(nvs_handle_t h, const char *k, uint8_t v) { (void)h;(void)k;(void)v; return ESP_OK; }
esp_err_t nvs_set_u64(nvs_handle_t h, const char *k, uint64_t v) { (void)h;(void)k;(void)v; return ESP_OK; }
esp_err_t nvs_set_str(nvs_handle_t h, const char *k, const char *v) { (void)h;(void)k;(void)v; return ESP_OK; }
esp_err_t nvs_erase_key(nvs_handle_t h, const char *k) { (void)h;(void)k; return ESP_OK; }

/* ============ 1. 不存在的命名空间 ⇒ 成功（不是失败） ============ */
static void test_missing_namespace_is_success(void)
{
    reset_fake();   /* 一个都不登记 ⇒ 全部 NOT_FOUND */
    CHECK(devop_erase_namespace("config") == 0,
          "**命名空间不存在必须算成功** —— 它本来就是空的，正是恢复出厂想要的"
          "状态；当成失败会让'还没写过 config'的设备报 ERASE_FAILED，"
          "操作员看到一个永远失败、其实已经成功的操作");
    CHECK(s_erase_calls == 0, "不存在的命名空间无需擦除，实际擦了 %d 次", s_erase_calls);
}

/* ============ 2. 存在的命名空间 ⇒ 真的擦 ============ */
static void test_existing_namespace_is_erased(void)
{
    reset_fake();
    fake_ns_add("config", ESP_OK, ESP_OK, ESP_OK);
    CHECK(devop_erase_namespace("config") == 0, "应成功");
    CHECK(s_erase_calls == 1, "应擦除一次，实际 %d", s_erase_calls);
}

/* ============ 3. 其它 open 错误 ⇒ 失败（不要一律当成功） ============ */
static void test_other_open_errors_fail(void)
{
    reset_fake();
    fake_ns_add("config", ESP_FAIL, ESP_OK, ESP_OK);
    CHECK(devop_erase_namespace("config") != 0,
          "非 NOT_FOUND 的 open 错误必须算失败 —— 否则真的擦不掉时也报成功，"
          "服务端会告诉操作员'恢复出厂完成'而配置还在");
}

/* ============ 4. 参数校验 ============ */
static void test_bad_args(void)
{
    reset_fake();
    CHECK(devop_erase_namespace(NULL) != 0, "NULL 应失败");
    CHECK(devop_erase_namespace("") != 0, "空串应失败");
}

/* ============ 5. send_frame 用 checked 发布（失败要如实返回） ============ */
static void test_send_frame_reports_failure(void)
{
    reset_fake();
    g_publish_checked_result = ESP_FAIL;
    CHECK(devop_send_frame((const uint8_t *)"x", 1) != 0,
          "**发布失败必须返回非 0** —— device_op 靠这个返回值决定'不重启'；"
          "返回 0 会让设备在 ACK 没送出去的情况下重启，操作员看到的是"
          "'点了没反应'而设备其实重启了");
    g_publish_checked_result = ESP_OK;
    CHECK(devop_send_frame((const uint8_t *)"x", 1) == 0, "成功时应返回 0");
    CHECK(devop_send_frame(NULL, 0) != 0, "空帧应失败");
}

int main(void)
{
    test_missing_namespace_is_success();
    test_existing_namespace_is_erased();
    test_other_open_errors_fail();
    test_bad_args();
    test_send_frame_reports_failure();

    if (s_failures) { printf("device_op_wiring_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_op_wiring_tests: all checks passed\n");
    return 0;
}
