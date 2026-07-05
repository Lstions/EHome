# ESP32-Collector 组件优化方案 v2.1 (Final)

**两轮专家辩论后的最终方案。v2→v2.1 修正:**
1. `s_active_idx` 用 `volatile int`（ESP32 双核原子读写保证）
2. `ota_cmd_t` 改为 heap 分配（MQTT task 栈偏紧）
3. `frame_field_get_*` 改为 static inline 放在 .h（与现有 API 风格一致）
4. OTA 自检内容明确定义

---

## Phase 1: P0 — 并发安全与崩溃修复 (Step 1-4)

### Step 1: frame_codec NULL guard + field 访问器

**1a. frame_encode_string NULL guard**
```c
// frame_codec.c — 修改现有函数
frame_err_t frame_encode_string(frame_encoder_t *enc, uint8_t field_num, const char *str) {
    if (str == NULL) str = "";  // NULL → 空字符串，不崩溃
    size_t len = strlen(str);
    // ... 原有逻辑不变
}
```

**1b. field 访问器（static inline 放在 frame_codec.h）**
```c
// frame_codec.h — 新增（与 frame_encode_bool 等 inline 风格一致）
static inline frame_err_t frame_field_get_string(const frame_field_t *f, char *buf, size_t sz) {
    if (!f || !buf || sz == 0) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    if (!f->value.bytes.ptr) { buf[0] = '\0'; return FRAME_OK; }
    size_t n = f->value.bytes.len < sz - 1 ? f->value.bytes.len : sz - 1;
    memcpy(buf, f->value.bytes.ptr, n);
    buf[n] = '\0';
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_varint(const frame_field_t *f, uint64_t *v) {
    if (!f || !v) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
    *v = f->value.varint;
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_bytes(const frame_field_t *f,
                                                  const uint8_t **data, size_t *len) {
    if (!f || !data || !len) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    *data = f->value.bytes.ptr;
    *len = f->value.bytes.len;
    return FRAME_OK;
}
```

**影响文件:** frame_codec.h, frame_codec.c, handler_config.c, handler_writecmd.c, handler_data.c, config_mgr.c

---

### Step 2: Field Number 枚举化

每个消息类型定义 enum，放在对应头文件中：

```c
// handler_hello.h — 新增
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

typedef enum {
    HELLO_ACK_F_SERVER_TIME = 1,
    HELLO_ACK_F_FEATURES    = 2,
} hello_ack_field_t;

typedef enum {
    PING_F_TIMESTAMP = 1,
} ping_field_t;

// msg_handler_internal.h — 新增（config 相关）
typedef enum {
    CFG_MFST_F_MANIFEST_ID = 1,
    // field 2: reserved (removed in v2.4)
    CFG_MFST_F_TEMPLATES   = 3,
    CFG_MFST_F_CHANNELS    = 4,
    CFG_MFST_F_DMA         = 5,
} config_manifest_field_t;

typedef enum {
    TEMPLATE_F_ID         = 1,
    TEMPLATE_F_WRITE_DATA = 2,
    TEMPLATE_F_READ_LEN   = 3,
    TEMPLATE_F_DELAY_MS   = 4,
} template_field_t;

typedef enum {
    CHANNEL_F_ID             = 1,
    CHANNEL_F_HW_ID          = 2,
    CHANNEL_F_TEMPLATE_IDS   = 3,
    CHANNEL_F_INTERVAL_MS    = 4,
    CHANNEL_F_ENABLED        = 5,
    CHANNEL_F_BUS_TYPE       = 6,
    CHANNEL_F_BUS_CONFIG     = 7,
    CHANNEL_F_EDGE_DEVICES   = 9,
} channel_field_t;

typedef enum {
    EDGE_DEV_F_ID        = 1,
    EDGE_DEV_F_HW_ID     = 2,
    EDGE_DEV_F_COMMANDS   = 3,
} edge_device_field_t;

typedef enum {
    CMD_F_TEMPLATE_ID  = 1,
    CMD_F_INTERVAL_MS  = 2,
    CMD_F_ENABLED      = 3,
} command_field_t;

typedef enum {
    DMA_CFG_F_ID      = 1,
    DMA_CFG_F_ENABLED = 2,
    DMA_CFG_F_BIND_TO = 3,
} dma_config_field_t;

// handler_writecmd.c 内部（static，不暴露）
typedef enum {
    WRITE_CMD_F_REQUEST_ID = 1,
    WRITE_CMD_F_CHANNEL_ID = 2,
    WRITE_CMD_F_DATA       = 3,
    WRITE_CMD_F_READ_SIZE  = 4,
} write_cmd_field_t;

typedef enum {
    SCAN_REQ_F_REQUEST_ID  = 1,
    SCAN_REQ_F_HW_ID       = 2,
    SCAN_REQ_F_SCAN_TYPE   = 3,
    SCAN_REQ_F_START_ADDR  = 4,
    SCAN_REQ_F_END_ADDR    = 5,
    SCAN_REQ_F_TIMEOUT_MS  = 6,
} scan_req_field_t;

typedef enum {
    QUERY_REQ_F_REQUEST_ID = 1,
    QUERY_REQ_F_QUERY_TYPE = 2,
} query_req_field_t;

// handler_data.c 内部
typedef enum {
    STATUS_RPT_F_UPTIME        = 1,
    STATUS_RPT_F_STATUS        = 2,
    STATUS_RPT_F_CHANNEL_COUNT = 3,
    STATUS_RPT_F_EPOCH         = 4,
    STATUS_RPT_F_SYNC_STATE    = 5,
    STATUS_RPT_F_CONFIG_HASH   = 6,
    STATUS_RPT_F_CH_HEALTH     = 7,
} status_report_field_t;

typedef enum {
    DATA_RPT_F_CHANNEL_ID    = 1,
    DATA_RPT_F_TIMESTAMP     = 2,
    DATA_RPT_F_SEQUENCE      = 3,
    DATA_RPT_F_RAW_DATA      = 4,
    DATA_RPT_F_ERROR_CODE    = 5,
    DATA_RPT_F_REQUEST_ID    = 6,
    DATA_RPT_F_EDGE_DEV_ID   = 7,
    DATA_RPT_F_CMD_INDEX     = 8,
} data_report_field_t;

typedef enum {
    OTA_CMD_F_OTA_ID       = 1,
    OTA_CMD_F_URL          = 2,
    OTA_CMD_F_CHECKSUM     = 3,
    OTA_CMD_F_SIZE         = 4,
    OTA_CMD_F_VERSION      = 5,
} ota_cmd_field_t;

typedef enum {
    OTA_PROG_F_OTA_ID      = 1,
    OTA_PROG_F_STATUS      = 2,
    OTA_PROG_F_PROGRESS    = 3,
    OTA_PROG_F_ERROR_MSG   = 4,
} ota_prog_field_t;

// handler_config.c 内部
typedef enum {
    CFG_QUERY_F_REQUEST_ID = 1,
} config_query_field_t;

typedef enum {
    QUERY_RES_F_REQUEST_ID = 1,
} query_resources_field_t;

typedef enum {
    CFG_RESULT_F_MANIFEST_ID = 1,
    CFG_RESULT_F_SUCCESS     = 2,
} config_result_field_t;

typedef enum {
    CFG_REPORT_F_REQUEST_ID   = 1,
    CFG_REPORT_F_MANIFEST_ID  = 2,
    CFG_REPORT_F_TMPL_COUNT   = 3,
    CFG_REPORT_F_CH_COUNT     = 4,
} config_report_field_t;
```

**影响文件:** handler_hello.h, msg_handler_internal.h, handler_writecmd.c, handler_data.c, handler_config.c, handler_hello.c, config_mgr.c

---

### Step 3: config_mgr 双缓冲并发保护

```c
// config_mgr.c — 核心变更
static config_manifest_t s_manifests[2];
static volatile int s_active_idx = 0;  // volatile: ESP32 Xtensa 单字原子读写
static SemaphoreHandle_t s_mutex = NULL;
static bool s_initialized = false;

void config_mgr_init(void) {
    if (s_initialized) return;
    s_mutex = xSemaphoreCreateMutex();
    memset(s_manifests, 0, sizeof(s_manifests));
    s_active_idx = 0;
    s_initialized = true;
}

bool config_mgr_apply_manifest(const uint8_t *data, size_t len) {
    if (!data || len < 1) return false;
    
    int new_idx = 1 - s_active_idx;
    config_manifest_t *target = &s_manifests[new_idx];
    
    memset(target, 0, sizeof(*target));
    if (!parse_manifest_into(data, len, target)) {
        return false;
    }
    target->applied = true;
    
    // 原子切换（持锁 < 1us，仅切换索引）
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    s_active_idx = new_idx;
    xSemaphoreGive(s_mutex);
    
    return true;
}

const config_manifest_t *config_mgr_get_manifest(void) {
    return s_initialized ? &s_manifests[s_active_idx] : NULL;
}

// 长锁区间（供 app_callbacks handle_config_applied 使用）
void config_mgr_lock(void) {
    xSemaphoreTake(s_mutex, portMAX_DELAY);
}
void config_mgr_unlock(void) {
    xSemaphoreGive(s_mutex);
}
```

**注意:** `parse_manifest_into` 是 `parse_manifest` 的重命名+签名变更（接受 target 参数），逻辑不变。

**影响文件:** config_mgr.c, config_mgr.h, app_callbacks.c（加 lock/unlock 包裹）

---

### Step 4: handler_data.c OtaCmd 结构体化 + heap 分配

```c
// ota.h — 新增
typedef struct {
    char ota_id[64];
    char firmware_url[256];
    char checksum[128];
    char version[32];
    uint64_t size_bytes;
} ota_cmd_t;

void ota_start(const ota_cmd_t *cmd);  // 签名变更

// handler_data.c — 修改
void handler_data_process_ota(frame_decoder_t *dec) {
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));  // heap 分配
    if (!cmd) { ESP_LOGE(TAG, "No memory for OTA cmd"); return; }
    
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case OTA_CMD_F_OTA_ID:   frame_field_get_string(&field, cmd->ota_id, sizeof(cmd->ota_id)); break;
        case OTA_CMD_F_URL:      frame_field_get_string(&field, cmd->firmware_url, sizeof(cmd->firmware_url)); break;
        case OTA_CMD_F_CHECKSUM: frame_field_get_string(&field, cmd->checksum, sizeof(cmd->checksum)); break;
        case OTA_CMD_F_SIZE:     frame_field_get_varint(&field, &cmd->size_bytes); break;
        case OTA_CMD_F_VERSION:  frame_field_get_string(&field, cmd->version, sizeof(cmd->version)); break;
        }
    }
    
    if (ota_is_duplicate(cmd->ota_id)) {
        ESP_LOGW(TAG, "OTA duplicate: %s", cmd->ota_id);
        free(cmd);
        return;
    }
    ota_start(cmd);  // ota_start 负责 free
}

// ota.c — 修改 ota_start 签名
void ota_start(const ota_cmd_t *cmd) {
    if (!cmd || s_upgrading) { free((void*)cmd); return; }
    s_upgrading = true;
    
    // cmd 直接传递给 OTA task（task 负责 free）
    xTaskCreate(ota_task_func, "ota_task", 16384, (void*)cmd, 5, NULL);
}
```

**影响文件:** ota.h, ota.c, handler_data.c

---

## Phase 2: P1 — 架构级重构 (Step 5-8)

### Step 5: NVS 辅助函数

新增 `components/nvs_helper/nvs_helper.h`（纯 inline header-only，无 .c）：

```c
#pragma once
#include "nvs_flash.h"

static inline esp_err_t nvs_get_str_safe(const char *ns, const char *key, char *buf, size_t sz) {
    nvs_handle_t h; esp_err_t e = nvs_open(ns, NVS_READONLY, &h);
    if (e != ESP_OK) { buf[0] = '\0'; return e; }
    size_t len = sz; e = nvs_get_str(h, key, buf, &len); nvs_close(h);
    if (e != ESP_OK) buf[0] = '\0'; return e;
}

static inline esp_err_t nvs_set_str_safe(const char *ns, const char *key, const char *val) {
    nvs_handle_t h; esp_err_t e = nvs_open(ns, NVS_READWRITE, &h);
    if (e != ESP_OK) return e;
    e = nvs_set_str(h, key, val); if (e == ESP_OK) e = nvs_commit(h); nvs_close(h); return e;
}

static inline esp_err_t nvs_get_u64_safe(const char *ns, const char *key, uint64_t *val) {
    nvs_handle_t h; esp_err_t e = nvs_open(ns, NVS_READONLY, &h);
    if (e != ESP_OK) { *val = 0; return e; }
    e = nvs_get_u64(h, key, val); nvs_close(h);
    if (e != ESP_OK) *val = 0; return e;
}

static inline esp_err_t nvs_set_u64_safe(const char *ns, const char *key, uint64_t val) {
    nvs_handle_t h; esp_err_t e = nvs_open(ns, NVS_READWRITE, &h);
    if (e != ESP_OK) return e;
    e = nvs_set_u64(h, key, val); if (e == ESP_OK) e = nvs_commit(h); nvs_close(h); return e;
}

static inline esp_err_t nvs_erase_keys(const char *ns, const char **keys, int count) {
    nvs_handle_t h; esp_err_t e = nvs_open(ns, NVS_READWRITE, &h);
    if (e != ESP_OK) return e;
    for (int i = 0; i < count; i++) nvs_erase_key(h, keys[i]);
    e = nvs_commit(h); nvs_close(h); return e;
}
```

**影响文件:** 新增 nvs_helper/nvs_helper.h + CMakeLists.txt，修改 config_mgr.c, wifi_mgr.c, ota.c, factory_reset.c

---

### Step 6: ehome_mqtt 状态封装

```c
// ehome_mqtt.c — 将 6 个 global static 收归到结构体
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

// set_state 内部加锁
static void set_state(mqtt_client_state_t state) {
    xSemaphoreTake(s_ctx.mutex, portMAX_DELAY);
    if (s_ctx.state != state) {
        s_ctx.state = state;
        mqtt_state_cb_t cb = s_ctx.state_cb;
        void *ctx = s_ctx.state_cb_ctx;
        xSemaphoreGive(s_ctx.mutex);
        if (cb) cb(state, ctx);
    } else {
        xSemaphoreGive(s_ctx.mutex);
    }
}
```

**影响文件:** ehome_mqtt.c, ehome_mqtt.h

---

### Step 7: wifi_mgr 拆分 + 安全加固

```
wifi_mgr/
├── wifi_mgr.c            // STA 连接 + 事件 + NVS（使用 nvs_helper）
├── wifi_provisioning.c   // SoftAP + HTTP 配置页面
├── wifi_mgr.h            // 公共 API
├── wifi_provisioning.h   // provisioning 内部 API
├── CMakeLists.txt
└── Kconfig
```

**wifi_provisioning.c 安全修复:**
- 替换 sscanf 为手动 URL-decode
- AP 密码从 Kconfig 读取
- 30 分钟超时 + 连接成功后立即关闭
- esp_restart 改为 flag + delay

**wifi_mgr.c 非阻塞修复:**
- `wifi_mgr_start()` 中 `xEventGroupWaitBits` 移到独立 task

**影响文件:** wifi_mgr.c (拆分), 新增 wifi_provisioning.c, wifi_provisioning.h

---

### Step 8: OTA 路径合并 + 回滚机制

```c
// ota.c — 重构
static esp_err_t ota_download_to_partition(const char *url,
                                            const esp_partition_t *part,
                                            uint32_t *out_bytes) {
    bool is_https = (strncmp(url, "https://", 8) == 0);
    if (is_https) return ota_download_https(url, part, out_bytes);
    else          return ota_download_http(url, part, out_bytes);
}

static esp_err_t ota_verify_checksum(const char *expected_hex,
                                      const esp_partition_t *part,
                                      uint32_t total_bytes) { /* ... */ }

static esp_err_t ota_switch_and_reboot(const esp_partition_t *part) { /* ... */ }
```

**OTA 自检（app_main 中）:**
```c
// 自检项：
// 1. WiFi 连接成功（IP 获取）
// 2. MQTT 连接成功（broker 可达）
// 3. 第一个 StatusReport 发送成功
// 全部通过 → esp_ota_mark_app_valid_cancel_rollback()
// 任一失败 3 次 → esp_ota_mark_app_invalid_rollback_and_reboot()
```

**影响文件:** ota.c, main.c (或 app_main.c)

---

## Phase 3: P2 — 技术债清理 (Step 9-11)

### Step 9: uart0_boot 合并到 bus_dma
- 删除 uart0_boot 组件
- bus_dma.c 已有 UART0_START_INDEX=1 和 is_pin_reserved()
- 更新 main.c 移除 uart0_boot_init() 调用
- 更新顶层 CMakeLists.txt

### Step 10: 长函数拆分
- config_mgr.c: `parse_manifest_into()` 拆为 `parse_templates()`, `parse_channels()`, `parse_edge_devices()`, `parse_dma_config()`
- ota.c: 已在 Step 8 完成
- bus_dma.c: 拆为 `bus_dma_uart.c`, `bus_dma_spi.c`, `bus_dma_i2c.c`

### Step 11: factory_reset 安全加固
```c
void factory_reset_trigger(void) {
    // 只擦除特定 namespace
    const char *ns_list[] = {"wifi_cfg", "config", "ota"};
    for (int i = 0; i < 3; i++) {
        nvs_handle_t h;
        if (nvs_open(ns_list[i], NVS_READWRITE, &h) == ESP_OK) {
            nvs_erase_all(h);
            nvs_commit(h);
            nvs_close(h);
        }
    }
    // LED 闪烁指示 + 重启
    // ...
    esp_restart();
}
```

---

## 实施顺序（最小爆炸半径）

```
Step  1: frame_codec NULL guard + field 访问器     [安全] 无副作用
Step  2: field number 枚举化                       [安全] 纯重命名
Step  3: config_mgr 双缓冲                         [中等] 需验证 StatusReport
Step  4: handler_data.c ota_cmd_t 结构体化          [低] 接口变更
Step  5: NVS helper 函数                           [低] 纯代码简化
Step  6: ehome_mqtt 状态封装                       [中等] 需验证 MQTT 全链路
Step  7: wifi_mgr 拆分 + 安全                      [高] 需完整 WiFi 测试
Step  8: ota 路径合并 + 回滚                       [高] 需完整 OTA 测试
Step  9: uart0_boot 合并                           [低] 简单删除
Step 10: 长函数拆分                                [低] 纯重构
Step 11: factory_reset 安全                        [低] 简单修改
```

---

## 不做什么（明确排除）

- 不改协议设计（frame_codec 已是项目最好的组件）
- 不改 transport 抽象层（vtable 设计已经很好）
- 不改 scheduler 调度策略（背压+退避设计成熟）
- 不改 dma_pool 分配算法（三级策略优雅）
- 不改 sync_manager 7-reason 模型
- 不引入 JSON（协议永远用二进制）
- 不给所有组件加 mutex（只在有并发风险的组件加）

---

## 影响分析 & 测试用例

### 各 Step 影响的功能模块

| Step | 改动组件 | 直接影响的功能 | 需回归测试的场景 |
|------|---------|---------------|----------------|
| 1 | frame_codec | 所有消息编解码 | Hello/StatusReport/DataReport/ConfigResult/WriteRsp/Pong/OtaProg/ScanRpt/QueryRsp/ConfigReport/ResourceReport 的编解码 |
| 2 | 所有 handler + config_mgr | 所有消息 field 解析 | 同上（纯重命名，逻辑不变） |
| 3 | config_mgr | ConfigManifest 应用、StatusReport 读取、ConfigReport 响应 | ① 服务端推送 ConfigManifest → ESP32 解析并回复 ConfigResult ② StatusReport 中 epoch/channel_count 正确 ③ ConfigQuery → ConfigReport 响应正确 |
| 4 | handler_data + ota | OTA 命令解析、OTA 下载 | ① 服务端发送 OtaCmd → ESP32 解析参数 ② ota_start 接收正确参数 ③ OTA 下载+校验+重启 |
| 5 | config_mgr + wifi_mgr + ota + factory_reset | 所有 NVS 读写 | ① epoch 保存/读取 ② manifest_id 保存/读取 ③ WiFi 凭证保存/读取 ④ factory_reset 清除 |
| 6 | ehome_mqtt | MQTT 连接/断连/重连/发布/订阅 | ① MQTT 连接 → 状态变更 ② 发布消息 ③ 接收消息 ④ 断连重连 |
| 7 | wifi_mgr + wifi_provisioning | WiFi 连接、AP 配网、HTTP 服务器 | ① WiFi STA 连接 ② AP 配网页面 ③ 表单提交 → 保存凭证 → 重启 ④ 30分钟超时 |
| 8 | ota + main | OTA 全链路、回滚 | ① HTTP OTA 下载 ② HTTPS OTA 下载 ③ SHA256 校验 ④ 回滚机制 |
| 9 | bus_dma + main | UART0 初始化 | ① UART0 不被用于数据通道 ② boot 模式正常 |
| 10 | config_mgr + ota + bus_dma | 内部重构 | 全部回归（纯拆分，逻辑不变） |
| 11 | factory_reset | NVS 擦除 | ① factory_reset 只擦指定 namespace ② WiFi 凭证被清除 ③ config epoch 被清除 |

### E2E 测试用例

#### E2E-1: 完整启动流程（每次改动后必跑）
```
前置: 设备已烧录固件，服务端运行
步骤:
  1. 设备上电
  2. 观察串口日志: WiFi 连接 → MQTT 连接 → Hello 发送
  3. 服务端验证: 收到 Hello（node_id, fw_version, model, channel_count, epoch, proto_ver=2.1）
  4. 服务端回复 HelloAck（server_time, features）
  5. 观察串口: "HelloAck: server_time=... features=..."
  6. 等待 5 秒，服务端收到 StatusReport（uptime, status="online", channel_count, epoch, sync_state）
预期: 全部通过，无 crash，无异常日志
```

#### E2E-2: ConfigManifest 推送与应用
```
前置: E2E-1 通过，设备在线
步骤:
  1. 服务端发送 ConfigManifest（manifest_id="test_001", 2 templates, 1 channel）
  2. 观察串口: "Applied manifest: test_001, templates=2, channels=1"
  3. 服务端收到 ConfigResult（manifest_id="test_001", success=true）
  4. 等待 5 秒，StatusReport 中 channel_count=1, epoch 更新
  5. 服务端发送 ConfigQuery → 收到 ConfigReport（manifest_id="test_001", tmpl=2, ch=1）
预期: manifest 正确应用，所有响应字段正确
```

#### E2E-3: WriteCommand 执行
```
前置: E2E-2 通过，channel 已配置（UART1, 9600 baud）
步骤:
  1. 服务端发送 WriteCmd（request_id=100, channel_id=1, data=[0x01,0x03,0x00,0x00,0x00,0x01], read_size=5）
  2. 观察串口: "WriteCmd: req=100, ch=1, len=7"
  3. bus_worker 执行 UART 写入
  4. 服务端收到 WriteRsp（request_id=100, success=true）或 DataReport
预期: WriteCmd 正确路由到对应 channel，响应正确
```

#### E2E-4: OTA 全链路
```
前置: E2E-1 通过，设备在线，OTA 固件已部署到 HTTP 服务器
步骤:
  1. 服务端发送 OtaCmd（ota_id="ota_001", url="http://10.42.0.1:8080/firmware.bin", checksum=SHA256, size=N, version="2.2"）
  2. 观察串口: "Starting OTA: ota_001 from http://..."
  3. 服务端收到 OtaProg（ota_id="ota_001", status=0, progress=0%）
  4. 等待下载完成，收到 OtaProg（progress=100%）
  5. 观察串口: "SHA256 checksum verified OK" → "OTA success, restarting..."
  6. 设备重启，连接 WiFi → MQTT → 发送 Hello（fw_version="2.2"）
  7. 自检通过 → mark_valid
预期: OTA 下载、校验、重启、回连全流程成功
```

#### E2E-5: Factory Reset
```
前置: E2E-2 通过，设备有 manifest 配置
步骤:
  1. 服务端发送 WriteCmd（channel_id=0, data=[0xFC, 0x00]）
  2. 服务端收到 WriteRsp（success=true）
  3. 观察串口: "Factory reset command received"
  4. 设备重启
  5. 重启后 Hello 中 epoch=0, has_manifest=0, last_manifest=NULL
预期: NVS 被清除，设备回到初始状态
```

#### E2E-6: WiFi 配网（Provisioning）
```
前置: 设备无 WiFi 凭证（factory reset 后）
步骤:
  1. 设备上电，进入 provisioning 模式
  2. 观察串口: "=== Provisioning Mode ===" + "Connect to AP: EHome-Setup"
  3. PC 连接 "EHome-Setup" AP
  4. 浏览器访问 http://192.168.4.1 → 看到配网页面
  5. 输入 SSID="ABC" Password="..." → 提交
  6. 观察串口: "Provisioning: SSID=ABC" → "WiFi credentials saved"
  7. 设备重启，连接 WiFi → MQTT → Hello
预期: 配网成功，设备连接到指定 WiFi
```

#### E2E-7: MQTT 断连重连
```
前置: E2E-1 通过，设备在线
步骤:
  1. 杀掉 EMQX broker
  2. 观察串口: "MQTT disconnected" → 状态变为 DISCONNECTED
  3. 等待 5 秒，观察自动重连尝试
  4. 重启 EMQX broker
  5. 观察串口: "MQTT connected to broker" → 自动 subscribe down-topic
  6. 服务端收到新的 Hello（sync_manager 触发重连后 sync）
预期: 断连检测 → 自动重连 → 重新订阅 → 恢复通信
```

#### E2E-8: Ping/Pong 心跳
```
前置: E2E-1 通过，设备在线
步骤:
  1. 服务端发送 Ping（timestamp_us=12345678）
  2. 服务端收到 Pong（timestamp_us=12345678）
预期: Pong 中 timestamp 与 Ping 一致
```

#### E2E-9: ScanReq（I2C 扫描）
```
前置: E2E-2 通过，有 I2C channel 配置
步骤:
  1. 服务端发送 ScanReq（request_id="scan_001", hardware_id=0x68, scan_type=1）
  2. 观察串口: "ScanReq I2C: req=scan_001 hw=104"
  3. 服务端收到 ScanRpt（request_id="scan_001", success=true, addresses=[0x68, ...]）
预期: I2C 扫描执行，结果正确返回
```

#### E2E-10: ResourceReport
```
前置: E2E-2 通过，设备在线
步骤:
  1. 服务端发送 QueryResources（request_id="res_001"）
  2. 服务端收到 ResourceReport（包含 hw_capabilities + dma_state + channel_info）
预期: ResourceReport 包含完整的硬件能力和 DMA 状态
```

### 单元测试用例（host 端可运行，不需硬件）

#### UT-1: frame_codec（扩展现有 frame_codec_test.c）
```c
// 新增测试：
test_encode_string_null()       — NULL 输入编码为空字符串
test_field_get_string_normal()  — 正常提取
test_field_get_string_truncate()— 截断保护
test_field_get_string_null_ptr()— NULL ptr 安全
test_field_get_varint_normal()  — 正常提取
test_field_get_varint_wrong_wire() — wire type 不匹配
test_encode_decode_all_msg_types() — 所有 12 种消息类型的编解码往返
```

#### UT-2: config_mgr 双缓冲
```c
test_double_buffer_basic()      — apply → get 正确
test_double_buffer_switch()     — 两次 apply，第二次覆盖第一次
test_double_buffer_failed_apply()— 失败的 apply 不破坏现有配置
test_double_buffer_concurrent() — 两个 FreeRTOS 任务并发读写（需 ESP32 运行）
```

#### UT-3: NVS helper
```c
test_nvs_str_roundtrip()        — set → get 往返
test_nvs_u64_roundtrip()        — set → get 往返
test_nvs_not_found()            — 读取不存在的 key
test_nvs_erase_keys()           — 批量擦除
```

### 每个 Step 的验证 checklist

```
Step 1 验证:
  □ frame_codec_test.c 全部通过（含新增用例）
  □ E2E-1（启动流程）通过
  □ E2E-2（ConfigManifest）通过
  □ E2E-3（WriteCommand）通过
  □ E2E-8（Ping/Pong）通过

Step 2 验证:
  □ 编译无 warning（-Werror 开启）
  □ E2E-1 ~ E2E-10 全部通过（field 枚举化不影响运行时行为）

Step 3 验证:
  □ config_mgr 单元测试通过
  □ E2E-2（ConfigManifest）通过
  □ E2E-1 中 StatusReport 字段正确
  □ 并发压测：快速连续发送 10 个 ConfigManifest，无 crash

Step 4 验证:
  □ E2E-4（OTA 全链路）通过
  □ 编译无 warning

Step 5 验证:
  □ NVS helper 单元测试通过
  □ E2E-1（启动流程）通过 — epoch/manifest_id 读写正确
  □ E2E-5（Factory Reset）通过 — NVS 清除正确
  □ E2E-6（WiFi 配网）通过 — 凭证保存正确

Step 6 验证:
  □ E2E-7（MQTT 断连重连）通过
  □ E2E-1（启动流程）通过

Step 7 验证:
  □ E2E-6（WiFi 配网）通过
  □ E2E-1（启动流程）通过
  □ provisioning 30 分钟超时测试
  □ URL-decode 特殊字符测试（%20, +, &, =）

Step 8 验证:
  □ E2E-4（OTA 全链路）通过 — HTTP 和 HTTPS 两种路径
  □ OTA 回滚测试（烧录坏固件 → 自动回滚）
  □ SHA256 校验失败测试

Step 9 验证:
  □ E2E-1（启动流程）通过
  □ UART0 不被数据通道使用

Step 10 验证:
  □ E2E-1 ~ E2E-10 全部通过（纯重构，功能不变）

Step 11 验证:
  □ E2E-5（Factory Reset）通过
  □ NVS 中非目标 namespace 未被擦除
```
