# ESP32日志流设计

> **状态**: 已实现（v2.5 交付 Log V2：esp_log wrap 捕获 + 有界 ring + QoS0 反馈环抑制 + 0x1D LogStream 帧；IDF v5 适用，⚠️ IDF v6.0 vprintf hook 在 C6 上 crash —— 后端已验证，升级前必读 §6）
> **版本**: v2.5
> **日期**: 2026-08-22
> **关联**: [ESP32固件架构.md](ESP32固件架构.md) | [总体设计.md](总体设计.md) | [../协议/二进制帧协议.md](../协议/二进制帧协议.md)

## 1. 目标与边界

节点在无人值守现场，串口日志不可得。目标是远程查看 ESP32 系统日志：捕获 → 有界缓冲 → MQTT 上传 → 中心端持久化与实时查看。

边界：

- **日志只用于诊断**：QoS0 发送，不能作为可靠审计数据。
- **有界资源**：IRAM/runtime 受约束路径不捕获；ring 满则丢日志（fail-open）。
- UART 保真：UART/USB 日志口与无线通道职责分离。

## 2. 端到端架构

```
ESP32 日志源（ESP_LOGx）
  → esp_log wrap 捕获（确定非 IRAM 路径）
  → 有界 ring buffer（线程安全，多写单读）
  → 批量编码为 LogStream (0x1D)
  → MQTT up（QoS0）
  → 中心端 logstream 服务:
      ├─ DB 持久化（历史查询，有界保留）
      ├─ WS 实时推送（LogPanel 实时查看器）
      └─ 配置下发：日志级别/开关（ConfigManifest field 10）
```

## 3. 原生日志捕获（Log V2）

- wrap 捕获链：`esp_log_set_vprintf` → 过滤（级别阈值）→ ring 写入。
- Level 门禁：捕获级别由 ConfigManifest field 10（log_stream: enabled/level）动态控制。
- IRAM/runtime constrained 安全边界：ISR 与 IRAM 路径日志不进入 ring（防阻塞中断）。
- 打包策略：批量聚合（长度上限 + 定时 flush 双触发）。

## 4. 有界并发与生命周期

- ring：固定容量，满时丢弃并计数（回声抑制用于避免"日志产生日志"无限循环：捕获路径自身不打日志）。
- wrapper attach/detach：运行时开关（配置下发即时生效）。
- start/stop/set-level：三操作幂等。
- 任务隔离：日志打包/发送在日志任务内，不与总线 worker 争 CPU。

## 5. 协议契约（LogStream 0x1D）

```
field 1 (string): level   — 日志级别
field 2 (string): tag     — 组件 tag
field 3 (string): message — 日志内容
（批量传输：单帧可携带多条日志，长度受限分包）
```

ConfigManifest field 10: `log_stream { enabled, level }` —— 中心端控制开关与采集级别。

**QoS 语义**：LogStream 以 QoS0 走 up topic；丢帧不重传（诊断数据可丢，业务数据不可混为一谈）。

## 6. 已知限制

- **IDF v6.0 不兼容**：vprintf hook 在 C6 上 crash（后端验证）；当前固件目标 IDF v5.x。升级 IDF 大版本前必须重新设计捕获入口。
- 高并发日志刷屏会占 MQTT 带宽（level 阈值 + 打包批量缓解）。
- 持久化有界保留（历史查询窗口有限）。

## 7. 中心端实现

| 关注点 | 位置 |
|--------|------|
| 后端 | internal/logstream/（总线分发：DB 持久化 + WS 实时双消费者） |
| API | handler_logstream.go（/nodes/:id/logs、log-config、log-persist） |
| 前端 | views/node/LogPanel.vue（实时查看器 + 持久化开关 + 历史查询） |

## 8. 验收标准

- [x] 节点日志 → 中心端实时可见（WS）
- [x] 级别过滤与动态开关
- [x] ring 满丢弃不 crash
- [x] 回声抑制（捕获路径自身日志不放大）
- [x] 持久化开关 + 历史查询
- [x] ESP32 测框 host tests（ring 边界、batcher）
