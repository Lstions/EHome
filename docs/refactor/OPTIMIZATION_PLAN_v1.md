# ESP32-Collector 组件优化方案 v1.0 (Draft)

**目标:** 系统性解决代码审查中发现的设计/编码/安全问题，从架构层面根治而非修补

---

## 一、P0 — 并发安全与崩溃修复

### 1.1 config_mgr 并发保护
**问题:** apply_manifest() 无 mutex，多线程调用导致 TOCTOU 竞态（manifest parse clear+rebuild 与 StatusReport 读取并发）
**方案:**
- 添加 `static SemaphoreHandle_t s_mutex` 到 config_mgr.c
- `config_mgr_apply_manifest()` 入口 take，出口 give
- `config_mgr_get_manifest()` 返回前拷贝到 caller 提供的 buffer（避免返回内部指针）
- 新增 `config_mgr_lock_manifest()` / `config_mgr_unlock_manifest()` 供 app_callbacks 使用长锁区间

### 1.2 handler_writecmd.c NULL crash
**问题:** error_msg 可能为 NULL，直接传给 frame_encode_string 触发 strlen(NULL)
**方案:**
- 在 frame_encode_string 内部加 NULL 守卫（编码为空字符串）
- 所有 handler 的 send_* 函数对 string 参数加 NULL check

### 1.3 handler_data.c static 缓冲区并发不安全
**问题:** OtaCmd 解析使用 static char ota_id[64] 等，多 transport 同时收到 OtaCmd 时数据被覆盖
**方案:**
- 改为函数局部变量（栈分配），ota_id[64]+firmware_url[256]+checksum[128]+version[32] 总计约 480 字节，栈空间足够
- 或使用 heap 分配 + free

---

## 二、P1 — 架构级重构

### 2.1 全局并发保护策略统一
**问题:** 各组件并发保护不统一（部分有 mutex，部分裸奔）
**方案:** 引入 "组件级 mutex 规范"
- 定义规则：每个有 mutable state 的组件必须有 static SemaphoreHandle_t
- 公共 API 入口统一加 mutex take/give
- getter 函数：返回拷贝而非内部指针（value semantics）
- 受影响的组件：config_mgr, sync_manager, scheduler, ehome_mqtt

### 2.2 公共帧解码辅助函数
**问题:** request_id 拷贝逻辑在 3 个 handler 中重复；string field 提取每次都手写
**方案:** 在 frame_codec.h 中新增辅助函数
```c
// 安全提取 string field 到固定 buffer
static inline frame_err_t frame_decode_string_to_buf(
    const frame_field_t *field, char *out, size_t out_size)
{
    if (field->wire_type != WIRE_LENGTH_DELIMITED || !field->value.bytes.ptr)
        return FRAME_ERR_INVALID_TAG;
    size_t copy = field->value.bytes.len < out_size - 1
                ? field->value.bytes.len : out_size - 1;
    memcpy(out, field->value.bytes.ptr, copy);
    out[copy] = '\0';
    return FRAME_OK;
}
```
- 替换 handler_config.c, handler_writecmd.c, handler_data.c 中所有重复的 request_id 拷贝代码
- 同时解决 NULL ptr 问题（内部检查 field->value.bytes.ptr）

### 2.3 Field Number 常量化
**问题:** 帧字段用魔数（field 1, 2, 3...），协议变更时容易遗漏
**方案:** 为每种消息类型定义 field number 枚举
```c
// frame_codec.h 或单独 frame_fields.h
/* Hello message fields */
#define HELLO_F_NODE_ID         1
#define HELLO_F_FW_VERSION      2
#define HELLO_F_MODEL            3
#define HELLO_F_CHANNEL_COUNT    4
#define HELLO_F_EPOCH           5
#define HELLO_F_HAS_MANIFEST    6
#define HELLO_F_LAST_MANIFEST   7
#define HELLO_F_PROTO_VERSION   8

/* ConfigManifest fields */
#define CFG_MFST_F_MANIFEST_ID  1
#define CFG_MFST_F_TEMPLATES    2
#define CFG_MFST_F_CHANNELS     3
#define CFG_MFST_F_EPOCH        4
#define CFG_MFST_F_DMA          5
// ... 其他消息类型
```
- 替换所有 handler_*.c 和 config_mgr.c 中的数字 field 为命名常量

### 2.4 wifi_mgr 安全加固 + 职责拆分
**问题:** sscanf 注入、硬编码密码、职责过重、阻塞式等待
**方案:** 拆分为 3 个子模块（同一组件内 3 个 .c 文件）
- `wifi_connection.c` — STA 连接管理、事件处理、自动重连
- `wifi_provisioning.c` — SoftAP + HTTP 配置页面
- `wifi_nvs.c` — NVS 凭证读写

安全修复:
- 替换 sscanf 为手动 URL-decode + key=value 解析
- provisioning 密码从 Kconfig 读取（CONFIG_PROV_AP_PASSWORD），不再硬编码
- `wifi_mgr_start()` 改为非阻塞（event group wait 移到独立 task）
- provisioning 30 分钟超时自动关闭 SoftAP + HTTP server
- HTTP handler 中不再调用 esp_restart()，改为设置 flag 让主循环处理

### 2.5 NVS 操作统一
**问题:** 每次操作都 open/close，代码重复且效率低
**方案:** 新增 `nvs_helper.h/.c` 组件
```c
// nvs_helper.h
typedef struct nvs_session nvs_session_t;

nvs_session_t *nvs_session_open(const char *ns, bool readonly);
void nvs_session_close(nvs_session_t *s);
esp_err_t nvs_session_get_str(nvs_session_t *s, const char *key, char *buf, size_t *len);
esp_err_t nvs_session_set_str(nvs_session_t *s, const char *key, const char *val);
esp_err_t nvs_session_get_u64(nvs_session_t *s, const char *key, uint64_t *val);
esp_err_t nvs_session_set_u64(nvs_session_t *s, const char *key, uint64_t val);
esp_err_t nvs_session_commit(nvs_session_t *s);
esp_err_t nvs_session_erase_key(nvs_session_t *s, const char *key);
```
- config_mgr, wifi_mgr, ota 全部改用 nvs_helper
- 内部复用 handle，批量操作只 commit 一次

---

## 三、P2 — 技术债清理

### 3.1 ota.c HTTP/HTTPS 路径合并
**问题:** 两条路径约 80 行重复代码
**方案:** 抽取 `ota_download_firmware(url, partition, &total_bytes)` 公共函数
- 内部根据 URL scheme 选择 esp_http_client 或 esp_https_ota
- 统一返回实际下载字节数（修复 HTTPS 路径用分区大小的 bug）
- `ota_try_download` 简化为：download → verify → return

### 3.2 uart0_boot 与 bus_dma 合并
**问题:** UART0 跳过逻辑在两处重复
**方案:** 删除 uart0_boot 组件，将其功能合并到 bus_dma.c
- bus_dma.c 已有 UART0_START_INDEX 和 is_pin_reserved()
- uart0_boot 的 boot 模式检测逻辑移入 bus_dma 的初始化流程

### 3.3 长函数拆分
- config_mgr.c: `config_mgr_apply_manifest()` 拆为 `parse_templates()`, `parse_channels()`, `parse_edge_devices()`, `parse_commands()`, `parse_dma_config()`
- ota.c: `ota_try_download()` 拆为 `ota_download_firmware()`, `ota_verify_checksum()`, `ota_switch_partition()`
- bus_dma.c: 各总线 init/transact/deinit 已是独立函数，但文件仍 891 行。考虑拆为 `bus_dma_uart.c`, `bus_dma_spi.c`, `bus_dma_i2c.c`

### 3.4 mqtt 全局状态封装
**问题:** 6 个 global static 变量
**方案:** 封装为 `mqtt_client_ctx_t` 结构体
```c
typedef struct {
    esp_mqtt_client_handle_t client;
    mqtt_client_state_t state;
    mqtt_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    mqtt_state_cb_t state_cb;
    void *state_cb_ctx;
    char node_id[32];
    char up_topic[64];
    char down_topic[64];
    SemaphoreHandle_t mutex;  // 新增
} mqtt_client_ctx_t;

static mqtt_client_ctx_t s_ctx;
```

---

## 四、P3 — 持续改进

### 4.1 注释语言统一
- 全部改为英文注释（项目已有大量英文注释，保持一致）

### 4.2 单元测试补充
- 优先覆盖: config_mgr manifest 解析、scheduler 调度逻辑、dma_pool 分配策略
- 使用 Unity 测试框架（ESP-IDF 内置）

### 4.3 factory_reset 安全加固
- 改为只擦除特定 namespace（wifi_cfg, config, ota），不再 nvs_erase_all
- 添加触发条件文档（按钮 GPIO + 5 秒长按）

---

## 五、实施顺序

```
Phase 1 (P0): 并发安全 + crash 修复
  1.1 config_mgr mutex
  1.2 frame_encode_string NULL guard
  1.3 handler_data static → stack

Phase 2 (P1): 架构重构
  2.2 frame 辅助函数 (先做，后续重构依赖)
  2.3 field number 常量化 (先做)
  2.1 各组件 mutex 统一
  2.5 NVS helper
  2.4 wifi_mgr 拆分+安全
  3.4 mqtt 状态封装

Phase 3 (P2): 技术债
  3.1 ota 路径合并
  3.2 uart0_boot 合并
  3.3 长函数拆分

Phase 4 (P3): 持续改进
  4.1 注释统一
  4.2 单元测试
  4.3 factory_reset 安全
```

---

## 六、不做什么（明确排除）

- 不改协议设计（frame_codec 已是项目最好的组件）
- 不改 transport 抽象层（vtable 设计已经很好）
- 不改 scheduler 调度策略（背压+退避设计成熟）
- 不改 dma_pool 分配算法（三级策略优雅）
- 不改 sync_manager 7-reason 模型（只加 mutex）
- 不引入 JSON（协议永远用二进制）
