idf_component_register(SRCS "sync_manager.c"
                       INCLUDE_DIRS "."
                       REQUIRES config_mgr ehome_mqtt nvs_flash)
