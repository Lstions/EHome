# DMA资源管理设计

> **状态**: 已实现（v2.4/v2.5 交付：ResourceReport field 8 DMA 资源上报 + ConfigManifest field 5 DMA 配置 + dma_pool 三级分配；字段拼接已随 v3.0 补齐 field 8 解码分支）
> **版本**: v3.4
> **日期**: 2026-08-22
> **关联**: [总体设计.md](总体设计.md) | [多总线事件驱动架构.md](多总线事件驱动架构.md) | [../协议/二进制帧协议.md](../协议/二进制帧协议.md) | [通道.md](通道.md)

## 1. 背景

ESP32-C6 GDMA 有 6 通道，UART/SPI/I2C 三类总线争用。历史"每总线类型一个 DMA"的静态绑定在多总线并行下有资源冲突，且中心端无法感知节点 DMA 现状。方案：DMA 资源走统一"上报-配置"协议，由中心端按节点实报做分配决策。

## 2. 资源上报（ResourceReport field 8）

```
DmaChannel 子消息（repeated）:
  field 1 (varint): dma_id          — DMA 通道 ID（平台唯一）
  field 2 (string): name            — "GDMA_CH0"
  field 3 (varint): dma_type        — 0=GDMA, 1=EDMA, 2=DMA2D
  field 4 (varint): capabilities    — bit0=TX, bit1=RX, bit2=burst
  field 5 (varint): max_burst       — 最大突发长度
  field 6 (varint): state           — 0=free, 1=allocated, 2=disabled
  field 7 (string): bound_to        — 绑定目标（"UART1"/""）
  field 8 (varint): compatible_bus  — bit0=UART, bit1=I2C, bit2=SPI
```

## 3. 配置下发（ConfigManifest field 5 + channel field 8）

```
ConfigManifest field 5: dma_channel_configs（repeated）
  field 1 dma_id / field 2 enabled / field 3 bind_to（"UART1"）

ChannelConfig field 8: dma_enabled（该通道是否偏好 DMA）
```

**编码/解码对称**：后端起 field 8 必须与固件 parse_channel_fields 的 case 8 对齐（2026-07 曾修复 field 8 缺失 case 8 的 fail-open 缺陷，已闭环，见 §6 已知缺陷记录）。

## 4. bus_config 与 hw_id

- bus_config hex 格式：UART = [tx_pin, rx_pin, baud×4(BE)]（+ 可选 flags 字节 offset6 bit0=DMA）；SPI/I2C 各有布局。
- hw_id 规范（v2.0）：派生自硬件资源表（hw_tables），格式 `derive_hw_id`（bus_manager.c 算法为权威）。
- bus_config 不足字节 → uart_init 返回 ESP_ERR_INVALID_SIZE → ConfigManifest 整个事务拒绝。

## 5. 固件 dma_pool

- 三级分配：register（静态注册）→ allocate（动态申请）→ release（归还）。
- 引用计数：共享总线管理（多通道同总线共享 DMA）；ref_count 归零才真正释放。
- 背压：DMA 队列满时退避 + 指标上报。
- S3/C6 差异：通道数与能力由 hw_tables 硬件表区分（模板方法模式）。

## 6. 已知缺陷与修复记录

| 缺陷 | 修复 |
|------|------|
| ConfigManifest channel field 8（DMA 偏好）编码后固件 parse 无 case 8 → dma_enabled=false 被静默忽略且 bus_config 缺 flags 时默认 fail-open 启用 DMA | 修复链路 4 文件：config_mgr.h 增字段（默认 true 向后兼容）、parse_channel_fields 加 case 8、reg_bus_channel 接受显式参数、bus_config_get_dma_enabled 收敛 |
| 空 BusConfig → channelRoutePins hex 解码失败报错 | 无引脚路由跳过冲突校验（guard BusConfig != ""） |
| DMA 绑定表与总线表不同步（find_ctx 成功但 bus_ch 索引过期） | ch_idx 越界守卫（execute_uart_batch 等入口校验） |

## 7. 后端 API

| 端点 | 用途 |
|------|------|
| /nodes/:id/dma-channels | 节点 DMA 通道列表（只读视图） |
| /nodes/:id/dma-config | DMA 配置读写 |

前端：NodeOverview 总线配置 TAB 内嵌 DMA 绑定（el-switch 行）+ DMA 只读资源 TAB。

## 8. 验收标准

- [x] ResourceReport DMA 上报字段编码/解码 round-trip
- [x] ConfigManifest DMA 配置应用（ref_count 行为）
- [x] DMA 绑定对总线数据通道生效（实机压测 8/8）
- [x] 空 bus_config 场景不崩溃
- [x] field 8 编解码对称（修复后回归测试）
