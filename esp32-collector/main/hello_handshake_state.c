#include "hello_handshake_state.h"

#include <stddef.h>

#define HELLO_SM_MAX_ATTEMPTS 3U
#define HELLO_SM_TIMEOUT_TICKS 20U

void hello_sm_init(hello_sm_t *sm)
{
    *sm = (hello_sm_t){0};
}

void hello_sm_on_connected(hello_sm_t *sm, uint32_t generation)
{
    sm->generation = generation;
    sm->attempts = 0;
    sm->elapsed_ticks = 0;
    sm->running = true;
    sm->pending_send = true;
    sm->waiting_ack = false;
    sm->ack_armed = false;
    sm->ack_received = false;
}

hello_sm_action_t hello_sm_accept_generation(hello_sm_t *sm, uint32_t generation)
{
    hello_sm_on_connected(sm, generation);
    return hello_sm_next_action(sm);
}

hello_sm_action_t hello_sm_next_action(hello_sm_t *sm)
{
    if (!sm->running || !sm->pending_send) {
        return HELLO_SM_ACTION_NONE;
    }

    sm->pending_send = false;
    return HELLO_SM_ACTION_SEND_HELLO;
}

void hello_sm_on_hello_sent(hello_sm_t *sm)
{
    if (sm == NULL || !sm->running || sm->waiting_ack) {
        return;
    }
    sm->waiting_ack = true;
    sm->ack_armed = true;
    sm->ack_received = false;
    sm->elapsed_ticks = 0;
    sm->attempts++;
}

bool hello_sm_notify_ack(hello_sm_t *sm)
{
    if (sm == NULL || !sm->running || !sm->waiting_ack || !sm->ack_armed) {
        return false;
    }
    sm->ack_received = true;
    return true;
}

hello_sm_action_t hello_sm_tick(hello_sm_t *sm)
{
    if (!sm->running || !sm->waiting_ack) {
        return HELLO_SM_ACTION_NONE;
    }

    if (sm->ack_received) {
        sm->waiting_ack = false;
        sm->ack_armed = false;
        sm->ack_received = false;
        sm->running = false;
        sm->pending_send = false;
        return HELLO_SM_ACTION_COMPLETE;
    }

    sm->elapsed_ticks++;
    if (sm->elapsed_ticks < HELLO_SM_TIMEOUT_TICKS) {
        return HELLO_SM_ACTION_NONE;
    }

    sm->waiting_ack = false;
    sm->ack_armed = false;
    sm->ack_received = false;
    sm->pending_send = true;
    sm->elapsed_ticks = 0;
    if (sm->attempts >= HELLO_SM_MAX_ATTEMPTS) {
        sm->attempts = 0;
        return HELLO_SM_ACTION_FAILED;
    }
    return HELLO_SM_ACTION_SEND_HELLO;
}

bool hello_sm_is_running(const hello_sm_t *sm)
{
    return sm != NULL && sm->running;
}

bool hello_sm_is_waiting(const hello_sm_t *sm)
{
    return sm != NULL && sm->waiting_ack;
}
