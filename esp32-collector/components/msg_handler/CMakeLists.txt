idf_component_register(SRCS "msg_handler.c"
                       INCLUDE_DIRS "."
                       REQUIRES frame ehome_mqtt config_mgr ota scheduler factory_reset sync_manager hw_profile)
