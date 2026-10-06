idf_component_register(
    SRCS "tls_link_adapt.c"
    INCLUDE_DIRS "include"
    REQUIRES link_tcp tls_io
)
