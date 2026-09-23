idf_component_register(
    SRCS "bus_dma.c"
    INCLUDE_DIRS "include"
    # esp_driver_usb_serial_jtag: the C6 internal USB endpoint can serve as a
    # data bus (BUS_TYPE_USB) in addition to being the console.
    REQUIRES freertos driver esp_driver_uart esp_driver_spi esp_driver_i2c esp_driver_gpio esp_driver_usb_serial_jtag rgb_led nvs_flash hw_profile
)
