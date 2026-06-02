idf_component_register(SRCS "bus.c"
                       INCLUDE_DIRS "."
                       REQUIRES driver esp_driver_i2c esp_driver_uart)
