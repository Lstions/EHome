idf_component_register(
    SRCS "uart_dma.c"
    INCLUDE_DIRS "include"
    REQUIRES freertos esp_driver_uart
)
