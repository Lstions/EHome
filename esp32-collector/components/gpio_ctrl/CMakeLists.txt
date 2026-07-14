idf_component_register(SRCS "gpio_ctrl.c"
                       INCLUDE_DIRS "include"
                       REQUIRES driver esp_driver_gpio hw_profile)
