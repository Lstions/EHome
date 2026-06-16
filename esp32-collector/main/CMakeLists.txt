idf_component_register(SRCS "main.c"
                              "app_state.c"
                              "app_callbacks.c"
                              "bus_manager.c"
                              "bus_worker.c"
                              "hello_handshake.c"
                       INCLUDE_DIRS "."
                       REQUIRES freertos nvs_flash
                       frame wifi_mgr transport ehome_mqtt ehome_tcp msg_handler
                       config_mgr scheduler ota rgb_led factory_reset
                       sync_manager bus_dma hw_profile)
