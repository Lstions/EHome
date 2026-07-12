idf_component_register(
    SRCS "log_stream.c" "log_stream_codec.c"
    INCLUDE_DIRS "include"
    REQUIRES "frame" "esp_system" "esp_timer"
)
