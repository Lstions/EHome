idf_component_register(SRCS "ehome_mqtt.c" "mqtt_transport_adapter.c"
                       INCLUDE_DIRS "."
                       REQUIRES mqtt transport log_stream)
