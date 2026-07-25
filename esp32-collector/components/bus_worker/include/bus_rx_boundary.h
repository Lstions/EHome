#ifndef BUS_RX_BOUNDARY_H
#define BUS_RX_BOUNDARY_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define BUS_RX_FIXED_BLOCK_SIZE 512U

/* Return the next complete automatic boundary, or zero when more bytes are
 * required.  This is deliberately protocol-neutral and exposes no user
 * selectable receive mode. */
static inline size_t bus_rx_boundary_length(size_t buffered, bool pending,
                                             bool channel_cmd_v2,
                                             uint32_t read_size)
{
    if (buffered == 0 || (pending && channel_cmd_v2 && read_size == 0)) return 0;
    size_t target = read_size > 0 ? (size_t)read_size : BUS_RX_FIXED_BLOCK_SIZE;
    return buffered >= target ? target : 0;
}

#endif
