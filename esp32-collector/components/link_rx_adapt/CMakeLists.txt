idf_component_register(
    SRCS "link_rx_adapt.c"
    INCLUDE_DIRS "include"
    REQUIRES link_tcp rx_pump
)
