idf_component_register(
    SRCS "bus_worker.c"
    INCLUDE_DIRS "include"
    REQUIRES bus_dma cmd_queue scheduler
)
