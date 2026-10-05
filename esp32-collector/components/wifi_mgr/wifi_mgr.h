/**
 * @file wifi_mgr.h
 * @brief WiFi Manager - STA mode with auto-reconnect and NVS persistence
 */

#ifndef WIFI_MGR_H
#define WIFI_MGR_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === WiFi state callback === */
typedef enum {
    WIFI_MGR_DISCONNECTED,
    WIFI_MGR_CONNECTING,
    WIFI_MGR_CONNECTED,
    WIFI_MGR_FAILED,
} wifi_mgr_state_t;

typedef void (*wifi_mgr_state_cb_t)(wifi_mgr_state_t state, void *ctx);

/* === Init / Start === */
void wifi_mgr_init(void);
void wifi_mgr_start(void);
void wifi_mgr_stop(void);

/* === State query === */
wifi_mgr_state_t wifi_mgr_get_state(void);
bool wifi_mgr_is_connected(void);

/* === Liveness check（2026-10-05）===
 *
 * 为什么需要它：本固件遇到过一次"驱动以为还连着、实际已经掉线"的静默失联。
 * 现场证据：
 *   - 设备 uptime 525s 持续增长，固件完全正常；
 *   - 但服务端 ping 100% 丢包、ARP 表里**一条表项都没有**（L2 都不在）；
 *   - 串口里**没有任何 WiFi 日志**（0 条 Reconnecting / 0 条 disconnected）——
 *     即 `WIFI_EVENT_STA_DISCONNECTED` **从未触发**。
 *
 * 所以事件驱动的重连阶梯（wifi_event_handler 里的快速/慢速重试）永远不会启动：
 * 没有事件，就没有重试。MQTT 那边只能反复 esp-tls select() timeout，极具误导性。
 *
 * 本函数用一个**主动探针**补上这个缺口。判据必须来自链路之外，因为
 * `esp_wifi_sta_get_ap_info()` 读的是**驱动自己缓存的 AP 记录** ——
 * 驱动认为还连着时它会照常成功返回陈旧数据，发现不了问题（第一版就栽在这里）。
 *
 * 因此同时看两个信号：
 *   (a) 驱动层：`esp_wifi_sta_get_ap_info()` 是否还能返回 AP 信息；
 *   (b) 应用层：调用方传入的 `app_network_ok`（main.c 传 MQTT 连接状态）。
 * 二者任一持续失败超过阈值（60s）即强制重新关联。
 *
 * @param app_network_ok 应用层网络是否真的通（例如 MQTT 已连接）。
 * @return true = 链路正常或无需处理；false = 刚刚执行了一次强制恢复。
 */
bool wifi_mgr_check_liveness(bool app_network_ok);

/* === RSSI query === */
/**
 * @brief Get current WiFi RSSI in dBm.
 * @return Negative dBm value (e.g. -55) when connected;
 *         0 when disconnected or query fails.
 */
int wifi_mgr_get_rssi_dbm(void);

/* === Credentials (NVS) === */
bool wifi_mgr_save_credentials(const char *ssid, const char *password);
bool wifi_mgr_load_credentials(char *ssid, size_t ssid_len, char *password, size_t pwd_len);
void wifi_mgr_clear_credentials(void);
bool wifi_mgr_has_credentials(void);

/* === Callbacks === */
void wifi_mgr_register_state_cb(wifi_mgr_state_cb_t cb, void *ctx);

/* === Provisioning === */
void wifi_mgr_start_provisioning(void);
void wifi_mgr_stop_provisioning(void);

#ifdef __cplusplus
}
#endif

#endif /* WIFI_MGR_H */
