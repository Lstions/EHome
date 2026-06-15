idf_component_register(SRCS "scheduler.c"
                       INCLUDE_DIRS "."
                       REQUIRES config_mgr msg_handler bus_dma)
