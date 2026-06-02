# WiFi Provisioning Component

idf_component_register(SRCS "wifi_provisioning.c"
                       INCLUDE_DIRS "."
                       REQUIRES nvs_flash esp_wifi)
