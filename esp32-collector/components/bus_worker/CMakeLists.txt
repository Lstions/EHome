idf_component_register(
    SRCS "bus_worker.c"
    INCLUDE_DIRS "include"
    REQUIRES bus_dma scheduler esp_timer esp_hw_support log_stream
)
