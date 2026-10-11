/**
 * @file device_op.h
 * @brief 设备运维操作：重启 / 恢复出厂（保留连通性）—— 前端可远程触发
 *
 * ## 需求（2026-10-06 用户提出）
 *   "要能前端操作节点设备恢复出厂（不重置 wifi 连接信息）、重启"
 *
 * ## 设计要点
 *
 * ### 1) 两档 NVS 分区，远程只能碰第二档
 *   **A 档 · 连通性与身份（远程操作【绝不】擦）**
 *     - wifi_cfg        SSID/密码（wifi_mgr.c:22）
 *     - 客户端证书/私钥  3.0 mTLS 用（设计 §4.3；目前尚未落地）
 *   为什么：这两个一旦被远程擦掉，设备**再也连不回来** —— 操作员在远端
 *   把设备变成砖，只能到现场重新配网/重签证书。物理按键路径可以擦 A 档
 *   （人就在现场），远程路径不可以。
 *
 *   **B 档 · 运行配置（远程"恢复出厂"擦这一档）**
 *     - config          config_epoch / manifest_id（config_mgr.c:19）
 *   擦掉后设备重启 → Hello 带空 manifest_id → 后端下发完整配置 → 重新配置。
 *   这正是"恢复出厂"期望的效果，且**设备仍能连上**。
 *
 * ### 2) ota 命名空间【不在】远程路径内（有意与按键路径不同）
 *   现有按键路径擦 {wifi_cfg, config, ota}（factory_reset.c:15）。
 *   远程路径只擦 {config}，**不擦 ota**：ota 存的是固件/回滚状态，不是"配置"。
 *   擦它可能触发非预期的回滚，而用户要的"恢复出厂"指的是配置。
 *   ⇒ 若确认要远程清 OTA 状态，请显式加第二个操作码，不要偷偷塞进这一档。
 *
 * ### 3) ACK 必须在重启【之前】送出
 *   重启会断开连接。若先重启，前端永远不知道结果 —— 只能在重连后靠"设备
 *   好像重启过"去猜。所以本模块把顺序**结构固定**为：
 *      (擦除) → 刷新 ACK → 重启
 *   顺序写在一个函数里，调用方无法搞反（host_tests/device_op_tests.c 用
 *   调用顺序断言把这条锁住）。
 *
 * ### 4) 擦除失败则不重启
 *   设备留在运行状态，ACK 带失败码，操作员可重试。
 *   对比："失败了但还是重启" ⇒ 操作员以为成功、设备却处于半配置状态。
 *
 * ### 5) 与按键路径的关系（不改动既有安全 rails）
 *   现有按键恢复出厂只响应**上电后 10s 窗口**，这是 2026-10-04 现场事故
 *   （PWM0 配到 GPIO0，duty 3% ⇒ 引脚被拉低被当成"按住 BOOT" ⇒ 每 8.8s
 *   擦一次 NVS 并重启）之后的防线。**本模块不碰那条路径**：
 *   远程命令是"已认证 + 显式 + 不重复"的事件，与"引脚电平被误读"不是一类，
 *   因此远程路径不受该窗口限制。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_DEVICE_OP_H
#define EHOME_DEVICE_OP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === 3.0 消息类型（运维类别）===
 * 这两个号的**权威定义在 components/frame/frame_codec.h 的消息表**里
 * （check_message_types.py 按那张表扫两端一致性）。
 * 本头文件刻意【不依赖 IDF、也不 include frame_codec】——它要能在宿主编译，
 * 因此这里保留一份数值，并由 handler_device_op.c 用 _Static_assert 钉住
 * 两边相等：谁只改一边，固件就编不过，数值不会悄悄漂移。
 * 相邻占用：0x20 DATA_BATCH / 0x21 MEM_REPORT。 */
#ifndef MSG_DEVICE_OP
#define MSG_DEVICE_OP      0x22u   /* 下行：请设备执行一个运维操作 */
#endif
#ifndef MSG_DEVICE_OP_ACK
#define MSG_DEVICE_OP_ACK  0x23u   /* 上行：执行结果（重启前送出） */
#endif
/* #ifndef 守卫的两个用途：
 *   1) 单独 include 本头（宿主测试）时仍有这两个号 —— 保持"不依赖 IDF"；
 *   2) 同时 include frame_codec.h 时**不重复定义**（权威表说了算）。 */

/* === 操作码（走线路，必须【只增不改】）=== */
typedef enum {
    DEVICE_OP_REBOOT = 1,                   /* 仅重启 */
    DEVICE_OP_FACTORY_RESET_KEEP_CONN = 2,  /* 恢复出厂但保留连通性与身份 */
} device_op_t;

/** 执行结果（进 ACK，前端据此显示成败）。 */
typedef enum {
    DEVOP_OK = 0,
    DEVOP_ERR_UNKNOWN_OP = 1,      /* 操作码不认识（版本不匹配） */
    DEVOP_ERR_BUSY = 2,            /* 已有操作在执行中 */
    DEVOP_ERR_ERASE_FAILED = 3,    /* 擦除失败：设备**未**重启，可重试 */
    DEVOP_ERR_BAD_ARG = 4,
    DEVOP_ERR_ACK_FLUSH_FAILED = 5,/* ACK 没能送出：**不重启**（否则前端永远不知道）*/
} device_op_result_t;

const char *device_op_result_name(device_op_result_t r);
const char *device_op_name(device_op_t op);

/**
 * 注入的操作原语。
 * 设备上接 nvs / esp_restart；宿主测试注入可控假实现
 * （这样"擦哪些命名空间""顺序对不对"都能在本机断言）。
 */
typedef struct {
    /** 擦除一个命名空间。返回 0 成功，非 0 失败。 */
    int (*erase_namespace)(void *ctx, const char *ns_name);

    /**
     * 把【带 `result` 的】ACK 编码并送出（阻塞到写完或超时）。
     *
     * ⚠ 这个签名是 2026-10-06 改的，原签名是
     * `flush_ack(ctx, const uint8_t *ack, size_t len)` —— **那个签名有个真实缺陷**：
     *
     * ACK 里必须带结果码，而**结果码要等 execute 跑完才知道**。
     * 调用方在调用前能编出来的 ACK 只能是"OK"（它没有别的信息）。
     * 于是"擦除失败"这条路径会把**一个写着 OK 的 ACK**发出去，
     * 服务端据此告诉操作员"恢复出厂成功" ——
     * **而设备根本没擦、没重启，还在跑旧配置**。
     * 这是设备侧版本的"假成功"，与后端那个 stale-ACK 是同一类问题。
     *
     * 现在由模块告诉调用方"该报什么结果"，调用方负责编码
     * （request_id 由调用方在收到请求时捕获，编解码仍归 msgcodec）。
     *
     * 返回 0 成功，非 0 失败。**失败则不重启**（见 DEVOP_ERR_ACK_FLUSH_FAILED）。
     */
    int (*flush_ack)(void *ctx, device_op_result_t result);

    /** 重启。正常实现【不返回】；宿主测试的假实现直接返回。 */
    void (*restart)(void *ctx);
} device_op_io_t;

/**
 * 执行一个运维操作。顺序固定为：(擦除) → 刷新 ACK → 重启。
 *
 * @param restarted_out  可选；true 表示重启原语已被调用。
 * @return 结果码。**调用方必须把它写进 ACK** —— 通过 flush_ack 的 result 参数传下去，
 *         不要自己另算一份（两份结果必然漂移）。
 *
 * 确定性行为（由 host_tests/device_op_tests.c 逐条锁定）：
 *   - 未知操作码        -> UNKNOWN_OP，**不擦除、不刷新、不重启**；
 *   - 已有操作进行中    -> BUSY，同上；
 *   - 擦除失败          -> ERASE_FAILED，**刷新 ACK 报告 ERASE_FAILED，但不重启**；
 *   - ACK 刷新失败      -> ACK_FLUSH_FAILED，**不重启**；
 *   - 全部成功          -> OK，且重启前 ACK 已送出。
 */
device_op_result_t device_op_execute(const device_op_io_t *io, void *io_ctx,
                                     device_op_t op,
                                     bool *restarted_out);

/**
 * 远程"恢复出厂"要擦的命名空间清单（**单一事实来源**，P4）。
 * 测试与文档都读这里，不各自维护一份副本。
 *
 * 明确【不包含】wifi_cfg（连通性）与 ota（固件状态）。
 */
const char *const *device_op_factory_namespaces(size_t *count_out);

/** 是否处于"操作已发起"状态（用于拒绝重复触发）。 */
bool device_op_in_progress(void);

/** 测试/诊断用：复位内部状态（设备上不需要调用）。 */
void device_op_reset_state(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_DEVICE_OP_H */
