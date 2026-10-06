/**
 * @file net_policy.h
 * @brief 网络状态 -> 动作 的【纯决策】函数（宿主可测）
 *
 * 为什么单独成模块：D-03 是一个【控制流】缺陷 ——
 *   main/app_callbacks.c 里 TCP 启动块被写在一个 break 之后，成了不可达代码，
 *   于是"TCP 从不启动"。这类缺陷在原来的结构下【无法被测试发现】：
 *   它藏在 main/ 的一个 switch 里，而 main/ 不能被宿主编译（D-24）。
 *
 * 做法：把"此刻该不该启动 TCP"变成【纯函数】，于是：
 *   - 宿主可测（本文件不依赖 IDF）；
 *   - 变异自证可做（去掉一个条件 -> 测试必红）；
 *   - app_callbacks.c 只剩"按决策执行"，控制流不再是唯一真相来源。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（实施设计约束 C2）。
 */
#ifndef EHOME_NET_POLICY_H
#define EHOME_NET_POLICY_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * 此刻是否应当【启动】TCP 上行？
 *
 * @param tcp_configured 传输对象已创建（编译期开关开启且注册成功）
 * @param tcp_connected  传输【当前】已处于 CONNECTED（已连上，无需再启）
 * @param wifi_connected WiFi 已连上（TCP 的前提）
 *
 * 语义：三个条件【同时】成立才启动 ——
 *   未配置 -> 不启动（没有对象可启）；
 *   已连接 -> 不启动（避免重复 start，原代码即有此判断，保留）；
 *   WiFi 没连 -> 不启动（连了也连不上，白费一次 connect）。
 */
bool net_policy_should_start_tcp(bool tcp_configured, bool tcp_connected, bool wifi_connected);

/**
 * 启动期断言：TCP 应当连上却迟迟没连上 -> 需要告警。
 *
 * 这不是"超时重试"（重连由传输自身/上层负责），而是【可观测性】：
 * 一个从不启动的传输，如果没有这条断言，就只能靠人肉发现（D-03 就是这样潜伏的）。
 *
 * @param tcp_configured 传输已创建
 * @param tcp_connected  当前是否已连接
 * @param elapsed_ms     自 WiFi 连上起经过的毫秒数
 * @param deadline_ms    容忍上限
 */
bool net_policy_tcp_start_overdue(bool tcp_configured, bool tcp_connected,
                                  uint32_t elapsed_ms, uint32_t deadline_ms);

/** 建议的启动期容忍上限（毫秒）。取值理由：给 DHCP + TCP+TLS 握手留足时间，
 *  同时让"从不启动"在可接受时间内暴露。 */
#define NET_POLICY_TCP_START_DEADLINE_MS 15000u

#ifdef __cplusplus
}
#endif
#endif /* EHOME_NET_POLICY_H */
