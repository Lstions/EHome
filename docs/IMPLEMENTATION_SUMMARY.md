# ESP32 通信协议分层实现总结

## 完成的工作

### 1. 传输层协议分层架构

实现了完整的传输层抽象，支持多种通信协议（MQTT、TCP）：

```
┌─────────────────────────────────────┐
│         应用层 (msg_handler)          │
│  - ConfigQuery/Report               │
│  - DataReport                       │
│  - WriteCmd/Rsp                     │
│  - Ping/Pong                        │
│  - ResourceReport                   │
└─────────────────────────────────────┘
              ↓
┌─────────────────────────────────────┐
│      传输层抽象 (transport.h)        │
│  - transport_t 统一接口             │
│  - msg_cb / state_cb 回调           │
│  - send / init / start / stop       │
└─────────────────────────────────────┘
              ↓
    ┌───────────────────┬───────────────────┐
    ↓                   ↓                   ↓
┌─────────────┐  ┌─────────────┐  ┌─────────────┐
│   MQTT      │  │   TCP       │  │  (Future)   │
│  Transport  │  │  Transport  │  │  Transport  │
└─────────────┘  └─────────────┘  └─────────────┘
```

### 2. 传输层路由机制

实现了智能消息路由：
- **请求-响应模式**: 响应通过接收请求的同一传输通道返回
- **上下文传递**: `msg_handler_process_with_transport()` 保存当前传输上下文
- **智能发布**: `msg_handler_publish()` 优先使用当前传输，否则回退到 MQTT

### 3. 关键代码变更

#### 3.1 msg_handler.c
- 添加 `s_current_transport` 全局变量保存当前传输上下文
- 添加 `msg_handler_publish()` 统一发布函数
- 添加 `msg_handler_process_with_transport()` 带上下文的处理函数
- 所有 `msg_handler_send_*()` 函数改用 `msg_handler_publish()`

#### 3.2 main.c
- `on_transport_msg()` 回调接收 `transport_t *` 作为上下文
- TCP 传输注册时传递自身指针作为 `msg_cb_ctx`
- WiFi 连接后自动启动 TCP 服务器

#### 3.3 Kconfig 配置
- `CONFIG_DEBUG_TCP_ENABLED=n` (默认关闭)
- `CONFIG_DEBUG_TCP_PORT=8088` (TCP 端口)

### 4. 测试验证

**测试脚本**: `/tmp/test_tcp_routing.py`

**测试结果**:
```
✓ 连接成功 (10.42.0.173:8088)
✓ 发送 ConfigQuery (0x10)
✓ 收到 ConfigReport (0x11, 25 bytes)
✓ 传输路由正确 - 响应通过 TCP 返回
```

## 下一步：BMP280 SPI 通信

### 目标
实现 BMP280 传感器通过 SPI 总线读取数据：
1. 创建 BMP280 SPI 驱动组件
2. 集成到 bus_dma 框架
3. 通过 TCP 测试读取温度/气压数据
4. 通过 MQTT 上传数据到后端
5. 前端显示传感器数据
6. E2E+CDP 端到端验证

### 实施计划

#### Phase 1: BMP280 SPI 驱动
- [ ] 创建 `components/bmp280/` 组件
- [ ] 实现 SPI 初始化 (模式、频率、CS 引脚)
- [ ] 实现寄存器读取 (chip_id, calib, data)
- [ ] 实现温度和气压计算
- [ ] 集成到 bus_dma 框架

#### Phase 2: TCP 测试
- [ ] 创建测试脚本读取 BMP280 数据
- [ ] 验证 SPI 通信正确性
- [ ] 验证数据计算准确性

#### Phase 3: MQTT 集成
- [ ] 配置 MQTT 通道
- [ ] 实现数据上报
- [ ] 后端接收和存储

#### Phase 4: 前端显示
- [ ] 添加传感器数据显示
- [ ] 实时更新图表

#### Phase 5: E2E+CDP 验证
- [ ] Playwright E2E 测试
- [ ] CDP 协议模拟用户操作
- [ ] 完整流程验证

## 技术细节

### 传输层接口定义

```c
typedef struct transport {
    const transport_ops_t *ops;
    transport_type_t type;
    transport_state_t state;
    
    transport_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    transport_state_cb_t state_cb;
    void *state_cb_ctx;
    
    void *priv_data;
} transport_t;

typedef struct transport_ops {
    esp_err_t (*init)(transport_t *t, const void *cfg);
    esp_err_t (*start)(transport_t *t);
    esp_err_t (*stop)(transport_t *t);
    esp_err_t (*send)(transport_t *t, const uint8_t *data, size_t len);
    bool (*is_connected)(transport_t *t);
} transport_ops_t;
```

### 消息路由流程

```
1. TCP 客户端发送消息
   ↓
2. tcp_client_task 接收消息
   ↓
3. 调用 transport->msg_cb(data, len, transport)
   ↓
4. on_transport_msg() 接收 transport 上下文
   ↓
5. msg_handler_process_with_transport(data, len, transport)
   ↓
6. 保存 s_current_transport = transport
   ↓
7. msg_handler_process() 处理消息
   ↓
8. msg_handler_send_*() 调用 msg_handler_publish()
   ↓
9. msg_handler_publish() 检查 s_current_transport
   ↓
10. 通过 s_current_transport->ops->send() 发送响应
    ↓
11. TCP 客户端收到响应
```

## 文件清单

### 新增文件
- `components/transport/transport.h` - 传输层接口定义
- `components/transport/transport.c` - 传输管理器实现
- `components/ehome_tcp/ehome_tcp.h` - TCP 传输接口
- `components/ehome_tcp/ehome_tcp.c` - TCP 传输实现

### 修改文件
- `components/msg_handler/msg_handler.h` - 添加 transport 相关声明
- `components/msg_handler/msg_handler.c` - 实现路由机制
- `components/msg_handler/CMakeLists.txt` - 添加 transport 依赖
- `main/main.c` - 集成 TCP 传输
- `main/Kconfig.projbuild` - 添加 TCP 配置选项
- `sdkconfig.defaults` - TCP 默认关闭

### 测试脚本
- `/tmp/test_tcp_routing.py` - TCP 路由测试
- `/tmp/test_tcp_bidirectional.py` - TCP 双向通信测试

## 总结

成功实现了 ESP32 通信协议分层架构：
- ✅ 传输层抽象统一接口
- ✅ 智能消息路由机制
- ✅ TCP/MQTT 双通道支持
- ✅ TCP 默认关闭（调试用）
- ✅ 完整测试验证

为后续 BMP280 SPI 通信和端到端验证奠定了坚实基础。
