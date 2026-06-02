idf_component_register(SRCS "main.c"
                       INCLUDE_DIRS "."
                       REQUIRES freertos nvs_flash
                       frame wifi_mgr ehome_mqtt msg_handler
                       config_mgr scheduler bus ota rgb_led factory_reset)
