idf_component_register(SRCS "main.c"
                              "app_state.c"
                              "app_callbacks.c"
                              "config_apply_transaction.c"
                              "hello_handshake.c"
                       INCLUDE_DIRS "."
                       REQUIRES freertos nvs_flash
                       frame wifi_mgr transport ehome_mqtt ehome_tcp msg_handler
                       config_mgr scheduler ota rgb_led factory_reset
                       sync_manager bus_dma hw_profile dma_pool
                       bus_worker bus_manager log_stream gpio_ctrl pwm_ctrl periph_owner)

# Inject version from top-level CMakeLists.txt PROJECT_VER as a compile definition.
# This is the single source of truth — app_state.c reads EHOME_PROJECT_VER instead
# of hardcoding FIRMWARE_VERSION.
target_compile_definitions(${COMPONENT_LIB} PRIVATE EHOME_PROJECT_VER="${PROJECT_VER}")

# ConfigManifest rollback snapshots/workspaces must stay off callback task stacks.
set_source_files_properties(app_callbacks.c PROPERTIES
    COMPILE_OPTIONS "-Wframe-larger-than=1024")
