#ifndef HELLO_HOST_MSG_HANDLER_H
#define HELLO_HOST_MSG_HANDLER_H
#include <stdint.h>
void msg_handler_send_hello(const char *node_id, const char *firmware,
                            const char *model, uint8_t channel_count,
                            uint32_t handshake_nonce);
void msg_handler_send_resource_report(void);
bool msg_handler_is_hello_ack_received(void);
uint64_t msg_handler_get_server_time(void);
void msg_handler_reset_hello_ack(void);
#endif
