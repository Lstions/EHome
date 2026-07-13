# ESP32 系统日志远程查看设计（Log V2）

## 1. 目标与边界

EHomeSystem 通过既有 MQTT 上行链路远程查看 ESP32 采集器系统日志，并保持原 UART 日志后端不变。固件只负责采集、限流、编码和发送，不感知 WebSocket、数据库持久化或其他后端消费者。

固件配置只有：

| 字段 | 类型 | 默认值 | 含义 |
|---|---|---:|---|
| `log_stream.enabled` | bool | `false` | 启停远端采集与发送任务 |
| `log_stream.level` | uint8 | `2` | 远端阈值：0=ERROR，1=WARN，2=INFO，3=DEBUG，4=VERBOSE |

持久化开关属于后端，不进入 Config Manifest，也不进入 ESP32 上行帧。

## 2. 端到端架构

```text
ESP-IDF Log V2 ESP_LOGx
        │
        │ final ELF: -Wl,--wrap=esp_log
        ▼
__wrap_esp_log ───────────────► esp_log_va ─► 原 UART/IDF backend
        │（仅非 constrained、非 binary、已 attach 且未 suppression）
        ▼
静态有界 log_capture ring（满时覆盖最旧；竞争时不阻塞并计数）
        ▼
log_tx_task（最多 4 条/批，200 ms 周期）
        ▼
MsgLogStream 0x1D / MQTT QoS 0
        ▼
后端 MQTT handler ─► LogEventBus ─┬─► WebSocket `node_log`
                                  └─► DB consumer（按节点持久化开关）
```

`log_stream_emit()` 保留为远端专用结构化诊断入口，与原生日志共用同一 ring。远端阈值不会调用 `esp_log_level_set("*", ...)`，不会改变 UART 的运行时过滤策略。

## 3. 原生日志捕获

### 3.1 Log V2 与最终链接门禁

捕获依赖 ESP-IDF v6 Log V2 的 `esp_log_config_t + tag + format + args` 调用形态：

```ini
CONFIG_LOG_VERSION_2=y
CONFIG_LOG_DEFAULT_LEVEL_INFO=y
CONFIG_LOG_MAXIMUM_LEVEL_VERBOSE=y
```

默认控制台仍为 INFO；maximum=VERBOSE 只保证 D/V 调用没有在编译期被裁掉，远端是否采集由 `log_stream.level` 决定。

`log_stream/CMakeLists.txt` 通过 IDF project link options 把 `-Wl,--wrap=esp_log` 传播到最终应用 ELF。生产构建门禁同时检查：

1. 生成的 C6/S3 `sdkconfig` 为 Log V2、默认 INFO、最大 VERBOSE；
2. 最终 link command/build graph 含 `--wrap=esp_log`；
3. 最终 ELF 含 `__wrap_esp_log`、`esp_log_va`、attach/detach 符号。

### 3.2 UART 保真与格式化

wrapper 对可采集日志复制 `va_list`，只把格式化后的正文、tag、level 和 `esp_timer_get_time()` 单调微秒 uptime 写入 ring；原 `va_list` 仍且仅调用一次 `esp_log_va()`，因此 IDF 原过滤、格式化、锁和 UART backend 保持不变。递归保护覆盖捕获和原 backend 转发全过程，backend 内部嵌套日志只转发、不二次采集。

### 3.3 IRAM/runtime constrained 安全边界

wrapper 保持 `IRAM_ATTR`。入口在任何 reader 原子、capture 指针或 TLS recursion guard 访问之前处理受限上下文：

- `config.opts.constrained_env` 为真时直接把原始 `va_list` 转发给 `esp_log_va()`，不调用 `esp_log_util_is_constrained()`；
- 否则先调用 `esp_log_util_is_constrained()`；若为真，同样直接转发；
- 两条受限分支均不做远端格式化、ring 操作或 capture/TLS 访问；
- binary log 不进入文本 capture；
- 所有路径仍且仅调用一次 `esp_log_va()`，保持原 UART/IDF backend 与 `va_list` 语义。

C6/S3 最终 ELF 的反汇编必须作为顺序证据：运行期 helper call 位于 reader atomic 和 capture load 之前；配置受限分支则在 helper call 前直接跳到原 backend。这里不对编译器生成的函数栈帧作 IRAM 安全承诺，也不把普通路径中的 `vsnprintf`、TLS 或普通 RAM ring 宣称为 ISR/cache-disabled 可用。

## 4. 有界并发与生命周期

### 4.1 Ring

ring、TX batch 和编码 buffer 均为静态固定容量；push 非阻塞：锁竞争时丢弃当前条目并增加 contention 计数，ring 满时覆盖最旧条目并增加 dropped-oldest 计数。TX 每次最多 drain 4 条。

### 4.2 Wrapper attach/detach

wrapper 使用原子 capture 指针和原子 reader 计数。detach 的协议是：

1. 原子发布 `NULL`，阻止新 reader 获取 capture；
2. 有界等待已加载旧指针的 reader 退出；
3. 等待期间 `taskYIELD()` + `vTaskDelay(1)`，避免单核 C6 上忙等饿死 reader；
4. ISR 调用 detach 直接拒绝；
5. reader 未静默时不重置静态 ring。

Host 回归测试直接用 pthread/C11 barrier 编译并执行生产 `log_capture_esp.c`：reader 在真实 capture 格式化点暂停，detach 并发等待，reader 完成后才允许 quiescence；另有真实 ring push/drain 基础竞争与守恒检查。测试不以 sleep 作为同步，也不 stub 掉 pointer+reader handshake。

### 4.3 Start/stop/set-level

`log_stream` 使用 `STOPPED → STARTING → RUNNING → STOPPING → STOPPED` 原子状态机：

- 重复 start 只更新当前 level，不创建第二个 task；
- S3 上新 task 先等待一次性 notification gate，创建者发布 handle 和 RUNNING 后才释放；
- stop 先进入 STOPPING、detach wrapper、等待显式 capture user，然后通知 TX task 协作退出；
- stop timeout 不强删 task，worker 完成 publish 后自行清 handle、发退出事件并完成 STOPPED；
- STOPPING 期间拒绝 restart，避免重置仍被使用的静态 storage；
- publish callback、task handle、state 和显式 capture user 都使用原子同步。

Host 生命周期测试覆盖 task 创建失败、重复 start、stop timeout、晚到 worker 完成、STOPPING 阻止重启，以及 set-level 持有 capture 与 stop 的 barrier 交错。

## 5. MQTT QoS 与反馈环抑制

MsgLogStream 使用 QoS 0；普通非日志帧继续使用 QoS 1。QoS 选择本身不是反馈环证明，因为 esp-mqtt 可在 `publish()` 返回、同步 suppression 恢复后异步投递 `MQTT_EVENT_PUBLISHED`。

因此 MQTT event handler 按事件分类：

- `CONNECTED`：保留连接与订阅诊断；
- `DISCONNECTED`：保留 WARN；
- `ERROR`：保留 ERROR；
- `DATA`：保留 DEBUG 并分发消息；
- `PUBLISHED`：明确静默，不生成远端可捕获日志；
- 其他无诊断价值的事件：不做“全事件 INFO”记录。

TX task 对 publish 调用栈仍使用 capture suppression，屏蔽同步 MQTT 内部日志；事件分类消除 suppression 恢复后的 `log frame → async PUBLISHED log → log frame` 回声。Host 测试明确模拟“publish 返回并恢复 suppression 后再投递 PUBLISHED”，断言不会新增 capture，同时验证普通 QoS 1 行为及关键 CONNECTED/DISCONNECTED/ERROR 诊断仍保留。

## 6. 帧协议

MsgLogStream 的消息类型是 `0x1D`，字段采用现有 frame codec 的 TLV/嵌套结构，而不是固定字节布局：

```text
outer message type: 0x1D
field 1: count (varint)
field 2: sequence (varint, uint16 回绕)
field 3: repeated embedded entry
  entry field 1: level (varint)
  entry field 2: uptime_us (varint)
  entry field 3: tag (string)
  entry field 4: message (string)
```

固件 uptime 是启动后的单调时间，不是绝对墙钟时间。后端接收时记录 server time 供历史查询；协议中无 persist flag。

## 7. 资源模型

### 7.1 常驻与启用增量

当前实现为静态有界 storage，因而 `enabled=false` **不是零 RAM**：ring、TX batch、编码 buffer、capture/control/event-group storage 常驻 `.bss`。关闭时保证：

- 无 `log_tx_task`；
- 无 MQTT 日志帧流量；
- wrapper 未 attach，不产生远端 capture；
- 无日志流动态分配；
- 只有静态 RAM 下限仍常驻。

固件自有资源门禁 `LOG_STREAM_OWNED_RAM_BYTES <= 4096` 统计 ring、TX buffers、配置的 1536 B task stack 和源码可见 control objects。按当前 host ABI，entry storage 与 TX buffer/stack 小计约 3392 B，加 control objects 后约 3.5 KB；这是**静态常驻/配置栈的下限口径**，不是完整运行时增量。

该门禁明确不包含动态分配的 FreeRTOS TCB、allocator metadata、每任务 TLS，也不把 host `sizeof` 当作 C6/S3 实机 heap 结果。完整资源结论必须在目标板记录启停前后 free heap 和 task stack high-water；在没有这些数据前，只能声明“已通过源码已知资源的 4 KB 上限门禁”。

### 7.2 未宣称的实测

本设计不声称已完成 30 分钟长稳，也不编造 C6/S3 heap、TCB/TLS 或 high-water 数值。当前已知 C6 证据是 wrapper IRAM/runtime-constrained 实机验证；并发 detach/push/drain 证据来自确定性 host pthread 测试，而不是实机多任务压力测试。

## 8. 验证门禁

每次固件修改至少执行：

1. 全新 `/tmp` 目录 CMake build + CTest：codec/ring、wrapper 行为、真实 wrapper/detach 并发、生命周期交错、MQTT 异步 PUBLISHED 回归；
2. C6 `c6-n8` 与 S3 `s3-n8` 全新完整构建；
3. 两个目标的生成 sdkconfig、最终 link option 和 ELF wrapper 符号检查；
4. `git diff --check`；
5. 搜索 `MQTT_EVENT_PUBLISHED` 和旧的 `"MQTT event: %d"`，确保 PUBLISHED 无日志路径；
6. 实机发布前补做 UART 对照、目标板并发压力、heap/stack high-water 与规定时长长稳。

Host 测试可以证明状态机和 C11 原子握手在 host 调度下的行为，但不能替代 C6 单核/S3 双核 FreeRTOS 的最终并发与资源实测。
