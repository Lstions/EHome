idf_component_register(
    SRCS "uart0_boot.c"
    INCLUDE_DIRS "include"
    REQUIRES driver esp_driver_gpio rgb_led nvs_flash
)
