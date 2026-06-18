/**
 * @file msg_handler_internal.h
 * @brief Internal API shared between msg_handler core and handler modules.
 *
 * NOT for external callers — only for handler_*.c files within this component.
 */

#ifndef MSG_HANDLER_INTERNAL_H
#define MSG_HANDLER_INTERNAL_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "frame_codec.h"

#ifdef __cplusplus
extern "C" {
#endif

/** Publish a frame via current transport or broadcast fallback. */
void msg_handler_publish(const uint8_t *data, size_t len);

#ifdef __cplusplus
}
#endif

#endif /* MSG_HANDLER_INTERNAL_H */
