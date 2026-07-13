idf_component_register(
    SRCS "log_stream.c" "log_capture.c" "log_capture_esp.c" "log_stream_codec.c"
    INCLUDE_DIRS "include"
    REQUIRES "frame" "esp_system" "esp_timer" "log"
)

# IDF v6 exposes esp_log(config, tag, format, ...) before UART formatting. Add
# the wrapper to the project link options directly: a component archive's
# INTERFACE library item is not guaranteed to reach the final application link.
idf_build_set_property(LINK_OPTIONS "-Wl,--wrap=esp_log" APPEND)
