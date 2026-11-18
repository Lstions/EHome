#ifndef HELLO_HOST_SYNC_MANAGER_H
#define HELLO_HOST_SYNC_MANAGER_H
/* 只声明本组 target 真正调用的那几个入口（生产头是超集，这里是刻意缩减的
 * 子集）。⚠ 改生产 API 时要同步这里，否则 handler_hello.c / hello_handshake.c
 * 会以 implicit declaration 在宿主目标里编译失败。 */
void sync_manager_on_hello_ack_accepted(void);
void sync_manager_on_round_failed(void);
void sync_manager_on_downlink_received(uint8_t msg_type);
#endif
