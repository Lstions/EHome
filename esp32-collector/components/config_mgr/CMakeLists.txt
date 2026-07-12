idf_component_register(SRCS "config_mgr.c"
                       INCLUDE_DIRS "."
                       REQUIRES frame nvs_flash dma_pool log_stream)
