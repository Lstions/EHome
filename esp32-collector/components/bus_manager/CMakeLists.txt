idf_component_register(
    SRCS "bus_manager.c"
    INCLUDE_DIRS "include"
    REQUIRES bus_dma bus_worker config_mgr dma_pool hw_profile
)
