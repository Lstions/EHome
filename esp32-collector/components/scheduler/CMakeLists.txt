idf_component_register(SRCS "scheduler.c"
                       INCLUDE_DIRS "."
                       REQUIRES config_mgr bus_dma log_stream)
