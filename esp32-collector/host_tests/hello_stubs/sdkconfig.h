#ifndef HELLO_HOST_SDKCONFIG_H
#define HELLO_HOST_SDKCONFIG_H

/* Host tests compile hello_handshake_runtime.c and hello_handshake.c, whose
 * PSRAM-only internal-RAM assertions are behind
 * `#if CONFIG_SPIRAM && __XTENSA__`. __XTENSA__ is never defined here, and
 * CONFIG_SPIRAM is deliberately 0 to mirror the host's internal-RAM layout. */
#define CONFIG_SPIRAM 0

#endif
