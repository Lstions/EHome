idf_component_register(
    SRCS "log_stream.c"
    INCLUDE_DIRS "include"
    REQUIRES "msg_handler" "frame" "esp_system"
)
