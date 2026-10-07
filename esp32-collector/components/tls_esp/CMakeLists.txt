idf_component_register(
    SRCS "tls_esp.c"
    INCLUDE_DIRS "include"
    # heap_diag：task-34 的**分配归因**工具（诊断镜像专用，未开宏时空实现）。
    # 放在 components 而非 main：tls_esp 是组件，**不能**依赖 main ⇒ 否则循环依赖。
    REQUIRES link_tcp tls_io tls_guard tls_link_adapt esp-tls mbedtls heap_diag
)
