#ifndef HELLO_HOST_APP_STATE_H
#define HELLO_HOST_APP_STATE_H
#include <stdbool.h>
#include <stdint.h>
#define NODE_ID_MAX_LEN 32
typedef struct {
    char node_id[NODE_ID_MAX_LEN];
    bool hello_task_running;
} app_state_t;
const char *get_firmware_version(void);
const char *get_model_name(void);
#endif
