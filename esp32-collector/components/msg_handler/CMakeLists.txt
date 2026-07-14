idf_component_register(SRCS "msg_handler.c" "handler_hello.c" "handler_config.c" "handler_writecmd.c" "handler_data.c" "handler_periph.c" "data_report_codec.c"
                       INCLUDE_DIRS "."
                       REQUIRES frame ehome_mqtt config_mgr ota factory_reset sync_manager hw_profile transport scheduler gpio_ctrl pwm_ctrl)
