idf_component_register(SRCS "hw_profile.c" "hw_tables.c"
                       INCLUDE_DIRS "include"
                       REQUIRES frame dma_pool config_mgr esp_driver_uart)
