# Legacy 控制路径盘点

> 基线：`master@c33698ce16fe6918d81423d5fddbc598b9aeaae1`
>
> 阶段：边缘设备控制统一方案 Phase 0-A

## 结论

当前所有已知物理 TX 入口已分类。用户业务写和 raw diagnostics 在生产默认配置下均 fail-closed；仅周期采集、受控 legacy read 和设备初始化仍可使用 legacy WriteCmd。任何新增调用点必须更新本表和对应测试。

| 入口 | 分类 | Phase 0-A 状态 | 后续目标 |
|---|---|---|---|
| `POST /edge-devices/:id/operations` | mapped_action | 唯一业务控制入口；仅 `enabled_device_actions` 中、已通过动作门禁的 Action 可由 V2 dispatcher 派发 | 按 Action 风险逐项开放 |
| `POST /edge-devices/:id/execute` read | disabled | 返回 410；生产配置不存在 legacy read bridge | 已由 ChannelCmdV2 Operation API 取代 |
| `POST /edge-devices/:id/execute` write | mapped_action | 返回 410；`bridge` 配置不会启用旧 direct 实现 | Phase 1/4 映射可信 Action |
| `POST /edge-devices/:id/change-address` | mapped_action | 返回 410 | 迁移为验证后 CAS 更新的 Action |
| `POST /channels/:id/write` | raw_diagnostic | 返回 410 | Phase 1 独立 capability/reason/audit service |
| REST terminal history/write | raw_diagnostic | 返回 410 | 与 diagnostics service 统一 |
| WebSocket terminal history/write | raw_diagnostic | 专用回调拒绝；通用 WS `send` raw 写旁路已移除 | 与 diagnostics service 统一 |
| `deviceinit.Orchestrator` | internal | 保留；read 写入 1–30000ms RX timeout | 后续共享 Channel admission |
| 固件周期采集 | internal | 保留，继续使用现有 per-bus worker | 不进入用户 Action Catalog |
| `channel_id=0 + FC00` factory reset | disabled | 已从 WriteCmd handler 移除，channel 0 严格拒绝 | 未来独立 critical 控制协议 |

## 固件 fail-closed 规则

- WriteCmd field 1/2/3 必填且不可重复；未知字段、wrong wire type、dirty EOF 拒绝；
- request/channel 必须非零；TX 最大 128 字节；RX 最大 256 字节；timeout 为 1–30000ms；
- Channel 必须对应已初始化 `bus_dma_ctx_t`；非法 bus 不回退 UART0；
- UART 只接受当前构建支持的 UART0/UART1 queue；
- queue 不存在、queue full、Channel 不存在均返回明确 WriteRsp 失败；
- WriteRsp 仅表示固件 admission/TX 结果，不表示目标设备协议 ACK。

## 日志策略

- 普通后端日志不记录完整 WriteCmd TX；只记录字节数和 SHA-256 前缀；
- ConfigTemplate/execute/change-address 日志不输出 command hex；
- raw terminal 历史在 diagnostics service 完成前不可通过 REST/WS 读取。
