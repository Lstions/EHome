#ifndef HOST_TEST_RGB_LED_H
#define HOST_TEST_RGB_LED_H

typedef enum {
    LED_STATE_BOOTING = 0,
    LED_STATE_FACTORY_RESET,
    LED_STATE_RUNNING,
    LED_STATE_SERVER_OFFLINE,
    LED_STATE_WAITING_CONFIG
} led_state_t;

void rgb_led_set_state(led_state_t state);

#endif
