# multi-bus 架构修复适配记录

## 适配范围

目标分支：`codex/multi-bus-event-driven-plan`。

本次适配保留 multi-bus 的动态租约、按控制器分离队列、单一 UART RX 事件所有者和配置同步入口；未将单总线分支的轮询式 UART 实现直接移植过来。

## 已落地修复

1. 实例隔离
   - SN-3001 地址只更新目标 `EdgeDevice.HardwareID`。
   - 波特率只更新目标 `Channel.BusConfig` 的 UART 波特率字段（字节 2～5，大端序）。
   - 不再修改共享 `DeviceConfig.Connection.default_params`。

2. 命令最终态与配置副作用原子提交
   - `RecordInboxWithFinalizer` 将有效 final event、命令状态和配置副作用置于同一数据库事务。
   - 配置变更写入 `ConfigChangeOutbox`，SyncGate 在内存事件丢失或进程重启后回放 pending 事件。

3. 物理线级身份和执行门禁
   - ChannelCmdV2 wire digest 绑定实际 boot、channel、deadline、TX/RX 参数和物理步骤。
   - 当前执行引擎仅开放低风险、单步、只读动作；中高风险、写动作和 bounded sequence 均保持不可用。

4. multi-bus 固件安全边界
   - ChannelCmdV2 槽状态改为当前 boot 的 RAM 生命周期，不再对高频控制命令写 `ctrl_v2` NVS；配置管理、同步和 OTA 的 NVS 不受影响。
   - UART FIFO/buffer overflow 保留错误标记并返回失败；`read_size` 短帧在 idle 完成时返回失败，不再静默成功。
   - 配置挂起期间等待 UART pending response 会中断，当前命令以 `1007` 失败，避免配置切换过程中继续发物理命令。

5. review 后补齐的边界
   - Inbox finalizer 在 Execution/Attempt 条件更新成功后才执行；并发旧事件不会单独提交配置副作用。
   - UART 硬超时统一清理部分帧、overflow、chunk 和 idle 时间戳，避免后续被误报为 passive telemetry。
   - 共享物理 `hw_id` 的 DMA lease 只有最后一个逻辑 Channel 才能释放；注销/清理一个 Channel 不会破坏同物理总线上的其他 Channel。
   - SN-3001 Action Catalog 增加地址感知编译/校验，发送帧和 Final 响应均绑定目标 `EdgeDevice.HardwareID`，不再固定使用地址 `0x01` 或广播地址。
   - SyncGate 对 durable config outbox 的 `PROCESSED` 更新错误不再静默吞掉。

## 验证结果

- 后端：`go test ./...` 通过。
- 固件主机白盒：CTest 30/30 通过，包含新增短帧 `0x03` 和溢出 `0x02` 断言。
- ESP-IDF 固件：使用现有 ESP32-C6 构建目录完成编译、链接和镜像生成，应用分区余量约 23%。
- review 回归：后端 `go test ./...` 通过；ESP32 host CTest 30/30 通过，覆盖共享 DMA lease 和硬超时部分帧清理；ESP-IDF 固件重新编译、链接和生成镜像成功。

生产 API 验证、真实 MQTT/collector 联调和前端实际操作仍需在部署环境按测试方案执行。
