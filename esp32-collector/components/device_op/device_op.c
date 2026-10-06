/**
 * @file device_op.c
 * @brief 运维操作实现 —— 顺序结构固定：(擦除) → 刷新 ACK → 重启
 */
#include "device_op.h"

static const char *const s_result_names[] = {
    [DEVOP_OK]                    = "OK",
    [DEVOP_ERR_UNKNOWN_OP]        = "UNKNOWN_OP",
    [DEVOP_ERR_BUSY]              = "BUSY",
    [DEVOP_ERR_ERASE_FAILED]      = "ERASE_FAILED",
    [DEVOP_ERR_BAD_ARG]           = "BAD_ARG",
    [DEVOP_ERR_ACK_FLUSH_FAILED]  = "ACK_FLUSH_FAILED",
};

const char *device_op_result_name(device_op_result_t r)
{
    if ((int)r < 0 || r > (int)DEVOP_ERR_ACK_FLUSH_FAILED) return "UNKNOWN";
    return s_result_names[r];
}

const char *device_op_name(device_op_t op)
{
    switch (op) {
    case DEVICE_OP_REBOOT:                  return "REBOOT";
    case DEVICE_OP_FACTORY_RESET_KEEP_CONN: return "FACTORY_RESET_KEEP_CONN";
    default:                                return "UNKNOWN";
    }
}

/**
 * 远程"恢复出厂"擦除清单 —— **单一事实来源**。
 *
 * 只有 config：擦掉它迫使设备重启后重新 Hello + 拉取完整配置。
 *
 * 刻意【不在】这里的两项（改动前请读 device_op.h 的理由）：
 *   - wifi_cfg : 擦掉就再也连不回来（远程变砖）
 *   - ota      : 固件/回滚状态，不是"配置"；擦它可能触发非预期回滚
 */
static const char *const s_factory_ns[] = {
    "config",
};
#define FACTORY_NS_COUNT (sizeof(s_factory_ns) / sizeof(s_factory_ns[0]))

/* 运行期一次性状态。设备上一旦置位就不再清零（操作以重启收尾）。 */
static bool s_in_progress = false;

bool device_op_in_progress(void) { return s_in_progress; }

void device_op_reset_state(void) { s_in_progress = false; }

const char *const *device_op_factory_namespaces(size_t *count_out)
{
    if (count_out != NULL) *count_out = FACTORY_NS_COUNT;
    return s_factory_ns;
}

device_op_result_t device_op_execute(const device_op_io_t *io, void *io_ctx,
                                     device_op_t op,
                                     const uint8_t *ack, size_t ack_len,
                                     bool *restarted_out)
{
    if (restarted_out != NULL) *restarted_out = false;
    if (io == NULL || ack == NULL || ack_len == 0) return DEVOP_ERR_BAD_ARG;

    /* 操作码不认识：什么都不做（版本不匹配时不要把设备弄成半执行状态） */
    if (op != DEVICE_OP_REBOOT && op != DEVICE_OP_FACTORY_RESET_KEEP_CONN) {
        return DEVOP_ERR_UNKNOWN_OP;
    }

    /* 单飞：已有操作在进行中（例如前端重复点击、或重置尚未重启完又来一条） */
    if (s_in_progress) return DEVOP_ERR_BUSY;
    s_in_progress = true;

    /* ---- 步骤 1：恢复出厂先擦 B 档（仅 config）----
     * 重启操作不擦任何东西。 */
    if (op == DEVICE_OP_FACTORY_RESET_KEEP_CONN) {
        for (size_t i = 0; i < FACTORY_NS_COUNT; i++) {
            /* 任何一项擦不掉都算失败：不继续、不重启，把结果如实报给前端。
             * 注意：此处 **不** 因为某项失败就跳过 ACK —— 失败也要让前端知道。 */
            if (io->erase_namespace == NULL ||
                io->erase_namespace(io_ctx, s_factory_ns[i]) != 0) {
                if (io->flush_ack != NULL) {
                    (void)io->flush_ack(io_ctx, ack, ack_len);
                }
                /* 未重启 ⇒ 必须解除单飞，否则操作员"重试"会拿到 BUSY、
                 * 只能靠重启设备才能再试（与"可重试"的设计意图矛盾）。
                 * 这条是 host 测试抓出来的：我原来的写法把状态留着了。 */
                s_in_progress = false;
                return DEVOP_ERR_ERASE_FAILED;
            }
        }
    }

    /* ---- 步骤 2：把 ACK 送出去（必须在重启之前）----
     * 顺序不能反：先重启就断链，前端永远拿不到结果。 */
    if (io->flush_ack == NULL || io->flush_ack(io_ctx, ack, ack_len) != 0) {
        /* ACK 送不出去也【不重启】—— 否则操作员看到的是"点了没反应"，
         * 而设备其实重启了。宁可保持现状让操作员重试。
         * 同理解除单飞，让重试可行。 */
        s_in_progress = false;
        return DEVOP_ERR_ACK_FLUSH_FAILED;
    }

    /* ---- 步骤 3：重启 ---- */
    if (io->restart == NULL) return DEVOP_ERR_ACK_FLUSH_FAILED;
    if (restarted_out != NULL) *restarted_out = true;
    io->restart(io_ctx);   /* 设备上不返回 */
    return DEVOP_OK;
}
