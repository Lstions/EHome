idf_component_register(SRCS "ota.c"
                       INCLUDE_DIRS "."
                       REQUIRES msg_handler app_update esp_https_ota mbedtls)
