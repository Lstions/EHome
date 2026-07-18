#ifndef MQTT_SUPERVISOR_NOTIFY_H
#define MQTT_SUPERVISOR_NOTIFY_H

#include <stddef.h>

/* This seam has no FreeRTOS dependency so host tests can exercise the
 * notification decision without compiling WiFi/MQTT callbacks. */
typedef void (*mqtt_supervisor_notify_fn_t)(void *task);

static inline void mqtt_supervisor_notify_if_running(void *task,
                                                      mqtt_supervisor_notify_fn_t notify)
{
    if (task != NULL) {
        notify(task);
    }
}

#endif /* MQTT_SUPERVISOR_NOTIFY_H */
