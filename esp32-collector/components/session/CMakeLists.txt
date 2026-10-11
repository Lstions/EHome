idf_component_register(
    SRCS "session.c"
    INCLUDE_DIRS "include"
    REQUIRES link link_tcp rx_pump link_rx_adapt
)
