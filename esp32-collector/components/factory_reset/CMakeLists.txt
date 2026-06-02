idf_component_register(SRCS "factory_reset.c"
                       INCLUDE_DIRS "include"
                       REQUIRES esp_driver_gpio nvs_flash rgb_led freertos)
