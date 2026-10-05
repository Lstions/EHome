#ifndef HELLO_HOST_ESP_ATTR_H
#define HELLO_HOST_ESP_ATTR_H

/* Host tests do not model memory regions; the DRAM pin only matters on the
 * Xtensa targets where PSRAM exists. */
#define DRAM_ATTR
#define IRAM_ATTR

#endif
