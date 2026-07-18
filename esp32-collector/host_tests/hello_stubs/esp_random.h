#ifndef HELLO_HOST_ESP_RANDOM_H
#define HELLO_HOST_ESP_RANDOM_H

#include <stdint.h>

/* Deterministic stand-in for the ESP-IDF hardware RNG in host tests. */
static inline uint32_t esp_random(void)
{
    static uint32_t sequence = 0x13579BDFU;
    sequence += 0x1020304U;
    if (sequence == 0) sequence = 1;
    return sequence;
}

#endif
