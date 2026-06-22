# ESP32-Collector 组件优化方案 v2.0 (Final)

**基于专家辩论评审 v1→v2 的改进:**
- config_mgr: 改用双缓冲策略（消除 TOCTOU）
- NVS helper: 简化为 `nvs_*_safe()` 函数（移除 session 抽象）
- wifi_mgr: 拆为 2 文件（合并 wifi_nvs 到 wifi_mgr）
- frame 辅助函数: 扩展为完整 field 访问器 API
- field number: 改用 enum 而非 #define
- 新增: 锁顺序规则、OTA 回滚、HTTP provisioning 安全增强
- 新增: ota_cmd_t 结构体封装

---

## Phase 1: P0 — 并发安全与崩溃修复

### 1.1 config_mgr 双缓冲并发保护

**问题:** apply_manifest() 的 clear→parse→set_applied 流程中，其他任务读到半解析状态
**方案:** 双缓冲 + 原子切换

```c
// config_mgr.c 内部
static config_manifest_t s_manifests[2];
static volatile int s_active_idx = 0;
static SemaphoreHandle_t s_mutex = NULL;

void config_mgr_init(void) {
    s_mutex = xSemaphoreCreateMutex();
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    s_initialized = true;
}

bool config_mgr_apply_manifest(const uint8_t *data, size_t len) {
    int new_idx = 1 - s_active_idx;
    config_manifest_t *target = &s_manifests[new_idx];
    
    memset(target, 0, sizeof(*target));
    if (!parse_manifest_into(data, len, target)) {
        return false;
    }
    target->applied = true;
    
    // 原子切换（持锁 < 1us）
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    s_active_idx = new_idx;
    xSemaphoreGive(s_mutex);
    
    return true;
}

const config_manifest_t *config_mgr_get_manifest(void) {
    // 无锁读取（双缓冲保证安全）
    return s_initialized ? &s_manifests[s_active_idx] : NULL;
}
```

**注意:** `get_manifest()` 返回指针仍然安全，因为旧缓冲在被覆盖前不会有人读（下次 apply 会写另一个缓冲）。但 StatusReport 等读取者如果在 apply 期间长时间遍历 channels，可能读到旧数据——这是可接受的（最终一致性）。

**新增 API:**
- `config_mgr_lock()` / `config_mgr_unlock()` — 供 app_callbacks 的 handle_config_applied 使用长锁区间（cleanup + rebuild bus）

### 1.2 frame_encode_string NULL 守卫

**方案:** 在 frame_codec.c 的 frame_encode_string 内部处理 NULL：

```c
frame_err_t frame_encode_string(frame_encoder_t *enc, uint8_t field_num, const char *str) {
    if (str == NULL) str = "";  // NULL → 空字符串
    size_t len = strlen(str);
    // ... 原有逻辑
}
```

同时在 handler 调用点保留 NULL check + WARNING 日志（双重保险）。

### 1.3 handler_data.c OtaCmd 结构体化

**方案:** 定义 `ota_cmd_t` 结构体，栈分配：

```c
// ota.h
typedef struct {
    char ota_id[64];
    char firmware_url[256];
    char checksum[128];
    char version[32];
    uint64_t size_bytes;
} ota_cmd_t;

// ota_start 改为接收结构体
void ota_start(const ota_cmd_t *cmd);

// handler_data.c
void handler_data_process_ota(frame_decoder_t *dec) {
    ota_cmd_t cmd = {0};  // 栈分配，480 字节，ESP32 任务栈足够
    // ... 解析到 cmd
    ota_start(&cmd);
}
```

**ota.c 内部:** `ota_task_args_t` 直接复用 `ota_cmd_t`（不再单独定义）。

### 1.4 handler_writecmd.c ScanRpt/QueryRsp NULL check

`msg_handler_send_scan_rpt(request_id=NULL)` 会 crash（strlen(NULL)）。
所有 send_* 函数入口加 NULL check：

```c
void msg_handler_send_scan_rpt(const char *request_id, ...) {
    if (!request_id) request_id = "";
    // ...
}
```

---

## Phase 2: P1 — 架构级重构

### 2.1 frame 辅助函数扩展

在 frame_codec.h 中新增 field 访问器 API（放在 .c 中实现，非 inline）：

```c
// frame_codec.h — 新增
/** 安全提取 string field 到固定 buffer（含 NULL 检查和截断保护） */
frame_err_t frame_field_get_string(const frame_field_t *field, char *buf, size_t buf_size);

/** 安全提取 varint field */
frame_err_t frame_field_get_varint(const frame_field_t *field, uint64_t *value);

/** 安全提取 bytes field（返回指针+长度，零拷贝） */
frame_err_t frame_field_get_bytes(const frame_field_t *field, const uint8_t **data, size_t *len);
```

实现（frame_codec.c）：
```c
frame_err_t frame_field_get_string(const frame_field_t *field, char *buf, size_t buf_size) {
    if (!field || !buf || buf_size == 0) return FRAME_ERR_INVALID_TAG;
    if (field->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    if (!field->value.bytes.ptr) { buf[0] = '\0'; return FRAME_OK; }
    size_t copy = field->value.bytes.len < buf_size - 1
                ? field->value.bytes.len : buf_size - 1;
    memcpy(buf, field->value.bytes.ptr, copy);
    buf[copy] = '\0';
    return FRAME_OK;
}
```

替换 handler_config.c, handler_writecmd.c, handler_data.c 中所有 request_id 拷贝模式。

### 2.2 Field Number 枚举化

按消息类型定义 enum，放在各 handler 的头文件中：

```c
// handler_hello.h
typedef enum {
    HELLO_F_NODE_ID       = 1,
    HELLO_F_FW_VERSION    = 2,
    HELLO_F_MODEL          = 3,
    HELLO_F_CHANNEL_COUNT  = 4,
    HELLO_F_EPOCH         = 5,
    HELLO_F_HAS_MANIFEST  = 6,
    HELLO_F_LAST_MANIFEST = 7,
    HELLO_F_PROTO_VERSION = 8,
} hello_field_t;

// msg_handler_internal.h (config 相关)
typedef enum {
    CFG_MFST_F_MANIFEST_ID = 1,
    CFG_MFST_F_TEMPLATES   = 3,
    CFG_MFST_F_CHANNELS    = 4,
    CFG_MFST_F_DMA         = 5,
} config_manifest_field_t;

// handler_writecmd.c 内部
typedef enum {
    WRITE_CMD_F_REQUEST_ID = 1,
    WRITE_CMD_F_CHANNEL_ID = 2,
    WRITE_CMD_F_DATA       = 3,
    WRITE_CMD_F_READ_SIZE  = 4,
} write_cmd_field_t;

// 类似定义 scan_req_field_t, query_req_field_t, data_report_field_t, etc.
```

替换所有 handler_*.c 和 config_mgr.c 中的数字 field。

### 2.3 NVS 辅助函数（简化版）

新增 `nvs_helper.h`（纯 inline，无 .c 文件）：

```c
// nvs_helper.h — 内联辅助函数，减少 NVS open/close 重复代码
#pragma once
#include "nvs_flash.h"
#include "esp_err.h"

static inline esp_err_t nvs_get_str_safe(const char *ns, const char *key,
                                          char *buf, size_t buf_size) {
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READONLY, &h);
    if (err != ESP_OK) { buf[0] = '\0'; return err; }
    size_t len = buf_size;
    err = nvs_get_str(h, key, buf, &len);
    nvs_close(h);
    if (err != ESP_OK) buf[0] = '\0';
    return err;
}

static inline esp_err_t nvs_set_str_safe(const char *ns, const char *key,
                                          const char *value) {
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_set_str(h, key, value);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

static inline esp_err_t nvs_get_u64_safe(const char *ns, const char *key,
                                          uint64_t *value) {
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READONLY, &h);
    if (err != ESP_OK) { *value = 0; return err; }
    err = nvs_get_u64(h, key, value);
    nvs_close(h);
    if (err != ESP_OK) *value = 0;
    return err;
}

static inline esp_err_t nvs_set_u64_safe(const char *ns, const char *key,
                                          uint64_t value) {
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    err = nvs_set_u64(h, key, value);
    if (err == ESP_OK) err = nvs_commit(h);
    nvs_close(h);
    return err;
}

static inline esp_err_t nvs_erase_keys(const char *ns, const char **keys, int count) {
    nvs_handle_t h;
    esp_err_t err = nvs_open(ns, NVS_READWRITE, &h);
    if (err != ESP_OK) return err;
    for (int i = 0; i < count; i++) nvs_erase_key(h, keys[i]);
    err = nvs_commit(h);
    nvs_close(h);
    return err;
}
```

config_mgr, wifi_mgr, ota 全部改用这些函数，减少 NVS 样板代码。

### 2.4 并发保护统一 + 锁顺序规则

**需要 mutex 的组件:**

| 组件 | 保护对象 | 锁类型 |
|------|---------|--------|
| config_mgr | s_manifests[] + s_active_idx | xSemaphoreCreateMutex (优先级继承) |
| msg_handler | s_current_transport | 已有 mutex，保留 |
| ehome_mqtt | s_ctx (新增) | xSemaphoreCreateMutex |

**不需要额外 mutex 的组件:**
- bus_dma: 已有 mutex
- dma_pool: 已有 mutex
- scheduler: 单任务调用，不需要
- sync_manager: 无状态修改
- hw_profile: 只读数据

**锁顺序规则（防止死锁）:**
```
config_mgr > dma_pool > bus_dma > msg_handler > mqtt_client
持锁期间只能获取更低优先级的锁，禁止反向获取。
持锁期间禁止调用阻塞 API（网络 I/O、flash 写入）。
```

### 2.5 wifi_mgr 拆分 + 安全加固

拆分为 2 个 .c 文件：

```
wifi_mgr/
├── wifi_mgr.c          // STA 连接 + 事件处理 + NVS 凭证
├── wifi_provisioning.c // SoftAP + HTTP 配置页面
├── wifi_mgr.h          // 公共 API
├── wifi_mgr_internal.h // 内部接口（wifi_provisioning.c 使用）
└── CMakeLists.txt
```

**wifi_mgr_internal.h:**
```c
// wifi_provisioning.c 调用的内部接口
void wifi_mgr_save_credentials_and_restart(const char *ssid, const char *password);
```

**安全加固:**
- 替换 sscanf 为手动 URL-decode + key=value 解析
- AP 密码从 Kconfig 读取（CONFIG_PROV_AP_PASSWORD，默认 "ehome-setup-2024"）
- wifi_mgr_start() 改为非阻塞（xEventGroupWaitBits 移到独立 task）
- provisioning 30 分钟超时自动关闭 SoftAP + HTTP server
- HTTP handler 中不再调用 esp_restart()，改为设置 flag + vTaskDelay + esp_restart

### 2.6 ehome_mqtt 状态封装

```c
// ehome_mqtt.c 内部
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
    SemaphoreHandle_t mutex;
} mqtt_client_ctx_t;

static mqtt_client_ctx_t s_ctx;
```

所有原来的 6 个全局 static 变量收归到 s_ctx 中。mutex 保护 state 变更和 publish 操作。

---

## Phase 3: P2 — 技术债清理

### 3.1 ota.c 路径合并 + 回滚机制

抽取公共函数：
```c
static esp_err_t ota_download_to_partition(const char *url,
                                            const esp_partition_t *partition,
                                            uint32_t *out_total_bytes);

static esp_err_t ota_verify_checksum(const char *expected_hex,
                                      const esp_partition_t *partition,
                                      uint32_t total_bytes);

static esp_err_t ota_switch_and_reboot(const esp_partition_t *partition);
```

**回滚机制:**
- app_main 中检查 `ESP_OTA_IMG_PENDING_VERIFY` 状态
- 自检通过 → `esp_ota_mark_app_valid_cancel_rollback()`
- 自检失败 → `esp_ota_mark_app_invalid_rollback_and_reboot()`

### 3.2 uart0_boot 合并到 bus_dma

- 删除 uart0_boot 组件（uart0_boot.c, uart0_boot.h, CMakeLists.txt）
- bus_dma.c 已有 UART0_START_INDEX=1 和 is_pin_reserved()
- 更新 main.c 移除 uart0_boot_init() 调用
- 更新 CMakeLists.txt 移除 uart0_boot 依赖

### 3.3 长函数拆分

- config_mgr.c: `parse_manifest()` 拆为 `parse_templates()`, `parse_channels()`, `parse_edge_devices()`, `parse_dma_config()`
- ota.c: `ota_try_download()` 拆为 `ota_download_to_partition()` + `ota_verify_checksum()`
- bus_dma.c: 拆为 `bus_dma_uart.c`, `bus_dma_spi.c`, `bus_dma_i2c.c`（保持 bus_dma.c 作为公共入口）

### 3.4 factory_reset 安全加固

- 改为只擦除特定 namespace：
  ```c
  static const char *RESET_NAMESPACES[] = {"wifi_cfg", "config", "ota"};
  ```
- 逐个 `nvs_erase_all(namespace)` 而非全局 `nvs_erase_all`

---

## Phase 4: P3 — 持续改进

### 4.1 注释语言统一为英文
### 4.2 单元测试（Unity 框架，ESP-IDF 内置）
### 4.3 文档更新

---

## 实施顺序（最小爆炸半径原则）

```
Step 1: frame_codec NULL guard + field 访问器（最安全，无副作用）
Step 2: field number 枚举化（纯重命名，无逻辑变更）
Step 3: config_mgr 双缓冲（中等风险，需要验证 StatusReport 正确性）
Step 4: handler_data.c ota_cmd_t 结构体化（低风险，接口变更）
Step 5: NVS helper 函数（低风险，纯代码简化）
Step 6: ehome_mqtt 状态封装（中等风险，需验证 MQTT 连接/断连/重连）
Step 7: wifi_mgr 拆分 + 安全（高风险，需完整 WiFi 测试）
Step 8: ota 路径合并 + 回滚（高风险，需完整 OTA 测试）
Step 9: uart0_boot 合并（低风险）
Step 10: 长函数拆分（低风险，纯重构）
Step 11: factory_reset 安全（低风险）
```

---

## 不做什么（明确排除）

- 不改协议设计（frame_codec 已是项目最好的组件）
- 不改 transport 抽象层（vtable 设计已经很好）
- 不改 scheduler 调度策略（背压+退避设计成熟）
- 不改 dma_pool 分配算法（三级策略优雅）
- 不改 sync_manager 7-reason 模型（无状态修改，不需要锁）
- 不引入 JSON（协议永远用二进制）
- 不给所有组件加 mutex（过度保护，只在有并发风险的组件加）
