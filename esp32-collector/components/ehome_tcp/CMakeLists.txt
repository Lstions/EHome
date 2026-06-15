idf_component_register(
    SRCS "ehome_tcp.c"
    INCLUDE_DIRS "include"
    REQUIRES transport lwip freertos
)
