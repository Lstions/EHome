idf_component_register(SRCS "msg_handler.c" "handler_hello.c" "handler_config.c" "handler_writecmd.c" "handler_channel_cmd_v2.c" "handler_data.c" "handler_periph.c" "periph_config_apply.c" "data_report_codec.c"
                       INCLUDE_DIRS "."
                       REQUIRES frame ehome_mqtt config_mgr ota sync_manager hw_profile transport scheduler bus_worker gpio_ctrl pwm_ctrl periph_owner)
