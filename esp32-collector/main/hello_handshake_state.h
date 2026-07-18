#ifndef HELLO_HANDSHAKE_STATE_H
#define HELLO_HANDSHAKE_STATE_H

#include <stdbool.h>
#include <stdint.h>

/* Pure generation/action state used by the FreeRTOS worker and host tests. */
typedef enum {
    HELLO_SM_ACTION_NONE = 0,
    HELLO_SM_ACTION_SEND_HELLO,
    HELLO_SM_ACTION_COMPLETE,
    HELLO_SM_ACTION_FAILED,
} hello_sm_action_t;

typedef struct {
    uint32_t generation;
    uint32_t attempts;
    uint32_t elapsed_ticks;
    bool running;
    bool pending_send;
    bool waiting_ack;
    bool ack_armed;
    bool ack_received;
} hello_sm_t;

void hello_sm_init(hello_sm_t *sm);
void hello_sm_on_connected(hello_sm_t *sm, uint32_t generation);
hello_sm_action_t hello_sm_accept_generation(hello_sm_t *sm, uint32_t generation);
hello_sm_action_t hello_sm_next_action(hello_sm_t *sm);
void hello_sm_on_hello_sent(hello_sm_t *sm);
bool hello_sm_notify_ack(hello_sm_t *sm);
hello_sm_action_t hello_sm_tick(hello_sm_t *sm);
bool hello_sm_is_running(const hello_sm_t *sm);
bool hello_sm_is_waiting(const hello_sm_t *sm);

#endif
