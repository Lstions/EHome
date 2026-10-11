/**
 * @file device_op_wiring.c
 * @brief 把远程运维（0x22）需要的三个原语接到真实设备上。
 *
 * 为什么单独一个文件：这三个实现是**唯一**把"远程重启/恢复出厂"落到
 * 真实 nvs / esp_restart / 发送路径的地方。放在 main.c 里会和启动序列混在一起，
 * 而这里的每条分支都值得单独读一遍。
 *
 * ── 三条实现里各自的关键判断 ──
 *
 * 1) erase_namespace：**"命名空间不存在"不是失败**。
 *    nvs_open(NVS_READWRITE) 对从未创建过的命名空间返回 ESP_ERR_NVS_NOT_FOUND。
 *    那意味着"它本来就是空的"，即恢复出厂想要的状态**已经达到**。
 *    把它当失败会让"设备上还没写过 config"这种完全正常的情况报 ERASE_FAILED，
 *    操作员于是看到一个永远失败、其实已经成功的操作。
 *    （与 factory_reset.c 的按键路径同一口径：它也只忽略这一个错误码。）
 *
 * 2) send_frame：用 msg_handler_publish_checked —— **带校验的**发布。
 *    它在失败时会如实返回错误，而 device_op 正是靠这个返回值决定"不重启"
 *    （ACK 送不出去还重启，操作员看到的就是"点了没反应"而设备其实重启了）。
 *    不要换成不检查返回值的 msg_handler_publish()。
 *
 * 3) restart：esp_restart() 不返回。device_op 保证它是在 ACK 之后才被调用。
 */
#include <string.h>

#include "esp_log.h"
#include "esp_system.h"
/* nvs_flash.h 在 IDF 里已经 include 了 nvs.h（nvs_open/erase_all/commit 都在那），
 * 只写这一个头，宿主测试也就不必再为 nvs.h 造一份桩。 */
#include "nvs_flash.h"

#include "device_op.h"
#include "msg_handler_device_op.h"
#include "msg_handler_hooks.h"

static const char *TAG = "DEVICE_OP_IO";

/* ── 原语 1：擦除一个 NVS 命名空间 ── */
static int devop_erase_namespace(const char *ns_name)
{
    if (ns_name == NULL || ns_name[0] == '\0') return -1;

    nvs_handle_t handle;
    esp_err_t err = nvs_open(ns_name, NVS_READWRITE, &handle);

    if (err == ESP_ERR_NVS_NOT_FOUND) {
        /* 命名空间从未创建 ⇒ 已经是空的 ⇒ 目标状态已达成，算成功。
         * 这不是"宽容"，而是**正确**：恢复出厂要的是"没有配置"，
         * 而"从来没有过配置"正是没有配置。 */
        ESP_LOGI(TAG, "namespace %s does not exist — already empty, treating as erased",
                 ns_name);
        return 0;
    }
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "nvs_open(%s) failed: %s", ns_name, esp_err_to_name(err));
        return -1;
    }

    err = nvs_erase_all(handle);
    if (err == ESP_OK) {
        err = nvs_commit(handle);
    }
    nvs_close(handle);

    if (err != ESP_OK) {
        ESP_LOGE(TAG, "erase %s failed: %s", ns_name, esp_err_to_name(err));
        return -1;
    }
    ESP_LOGI(TAG, "erased namespace %s", ns_name);
    return 0;
}

/* ── 原语 2：把 ACK 同步送出 ── */
static int devop_send_frame(const uint8_t *frame, size_t len)
{
    if (frame == NULL || len == 0) return -1;

    /* 用 checked 版本：device_op 依赖"失败 ⇒ 不重启"，
     * 而 unchecked 版本会把这个信息丢掉。 */
    esp_err_t err = msg_handler_publish_checked(frame, len);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "ACK publish failed: %s — the device will NOT restart "
                      "(otherwise the operator sees nothing and assumes failure)",
                 esp_err_to_name(err));
        return -1;
    }
    ESP_LOGI(TAG, "ACK sent (%u bytes)", (unsigned)len);
    return 0;
}

/* ── 原语 3：重启 ── */
static void devop_restart(void)
{
    ESP_LOGW(TAG, "device op complete — restarting now");
    esp_restart();
}

static const device_op_hooks_t DEVOP_HOOKS = {
    .erase_namespace = devop_erase_namespace,
    .send_frame = devop_send_frame,
    .restart = devop_restart,
};

/**
 * 注入运维原语。**必须在任何传输（MQTT/TCP）开始收包之前调用** ——
 * 否则一条早到的 0x22 会命中"未注入 ⇒ 拒绝执行"分支，
 * 而操作员看到的是"设备没反应"，不是"还没准备好"。
 */
void device_op_wiring_init(void)
{
    msg_handler_set_device_op_hooks(&DEVOP_HOOKS);
    ESP_LOGI(TAG, "device op primitives injected (remote reboot / factory reset armed)");
}
