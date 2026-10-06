idf_component_register(
    SRCS "tls_esp.c"
    INCLUDE_DIRS "include"
    REQUIRES link_tcp tls_io tls_guard tls_link_adapt esp-tls mbedtls
)
