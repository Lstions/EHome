idf_component_register(SRCS "wifi_mgr.c" "wifi_provisioning.c"
                       INCLUDE_DIRS "."
                       REQUIRES nvs_flash esp_wifi esp_event esp_http_server)
