idf_component_register(
    SRCS "bus_dma.c"
    INCLUDE_DIRS "include"
    REQUIRES freertos driver esp_driver_uart esp_driver_spi esp_driver_i2c esp_driver_gpio rgb_led nvs_flash
)
