/**
 * @file msg_handler_device_op.h
 * @brief 远程运维（0x22 重启 / 恢复出厂）的注入契约。
 *
 * 单独一个头文件而不是塞进 msg_handler.h 的理由：
 * msg_handler.h 为了描述整个分发器，include 了 scheduler.h / config_mgr.h /
 * esp_err.h —— 于是一份**只描述三个函数指针**的契约，却要求调用方（以及
 * 宿主测试）拉进整棵组件树。
 * 拆出来之后 handler_device_op.c 与它的宿主测试只需
 * device_op.h + frame_codec.h + 本文件，契约可以独立编译、独立验证。
 */
#ifndef MSG_HANDLER_DEVICE_OP_H
#define MSG_HANDLER_DEVICE_OP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "frame_codec.h"

#ifdef __cplusplus
extern "C" {
#endif

/**
 * 远程运维操作需要的三个原语（由 main 注入；msg_handler 不依赖 nvs/esp_restart）。
 *
 * 每一个都必须成功才继续：擦不干净就不重启（否则设备在半擦状态下起来，
 * 比不擦更难查）。
 */
typedef struct {
    /** 擦除一个 NVS 命名空间。返回 0 成功。 */
    int (*erase_namespace)(const char *ns_name);
    /**
     * 把一帧**同步**送出（写完或超时才算返回）。
     * 返回 0 成功。失败 ⇒ 不重启（见 device_op.h 的 ACK_FLUSH_FAILED）。
     */
    int (*send_frame)(const uint8_t *frame, size_t len);
    /** 重启。设备上不返回。 */
    void (*restart)(void);
} device_op_hooks_t;

/** 注入运维操作原语。传 NULL 可清除（此后 0x22 会被拒绝执行）。 */
void msg_handler_set_device_op_hooks(const device_op_hooks_t *hooks);

/** 原语是否已注入。false 时收到 0x22 只告警、不执行任何操作。 */
bool msg_handler_device_op_ready(void);

/** 处理一条 MSG_DEVICE_OP (0x22)。 */
void handler_device_op_process(frame_decoder_t *dec);

#ifdef __cplusplus
}
#endif

#endif /* MSG_HANDLER_DEVICE_OP_H */
