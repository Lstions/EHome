# 专家辩论评审 - 第二轮（挑战 v2）

## 嵌入式架构师：v2 方案挑战

### 1.1 config_mgr 双缓冲 — 有隐患

**问题:** v2 说"get_manifest() 无锁读取"——但 `s_active_idx` 是 int，ESP32 双核上读 int 不是原子的（理论上可能读到撕裂值）。

**修正:** 
- `s_active_idx` 必须用 `volatile int` + `__atomic_load_n(&s_active_idx, __ATOMIC_ACQUIRE)` 
- 或者更简单：ESP32 的 int 读写在单字边界上实际是原子的（Xtensa 架构保证），但为安全起见用 `_Atomic int`

**问题2:** 双缓冲意味着内存翻倍。config_manifest_t 大小：
- 16 templates × (4+64+4+4+4) = 16×80 = 1280 字节
- 8 channels × (4+4+32+4+4+1+1+128+4 + 5×(4+4+3×12)) ≈ 8×280 = 2240 字节  
- 8 dma_configs × (4+1+16) = 168 字节
- 总计约 3.7KB × 2 = 7.4KB

ESP32-S3 有 512KB SRAM，7.4KB 完全可接受。**同意双缓冲。**

### 1.3 ota_cmd_t 栈分配 — 需要验证

handler_data_process_ota 在 MQTT 回调中执行。MQTT task 栈是 4096 字节（见 ehome_mqtt.c），480 字节的 ota_cmd_t 加上函数调用栈帧约 600 字节，占 15%。可行但偏紧。

**修正:** 改为 heap 分配更安全：
```c
void handler_data_process_ota(frame_decoder_t *dec) {
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    if (!cmd) { ESP_LOGE(TAG, "No memory for OTA cmd"); return; }
    // ... 解析
    ota_start(cmd);  // ota_start 内部负责 free
}
```

### 2.4 锁顺序规则 — 缺少实际约束机制

文档化的锁顺序规则依赖开发者自觉遵守，容易违反。

**建议:** 不需要额外的工具约束（嵌入式项目不值得），但在 code review checklist 中加入锁顺序检查项。**可接受。**

## 软件设计专家：v2 方案挑战

### 2.1 frame_field_get_string 放在 .c 而非 .h

v2 说"放在 .c 中实现，非 inline"。但这个函数非常短（5行），inline 更合适。

**修正:** 放在 frame_codec.h 中作为 static inline，与现有 frame_encode_bool 等 inline 辅助函数一致。

### 2.2 field number enum — 命名前缀冲突

v2 用 `HELLO_F_*`, `CFG_MFST_F_*` 等前缀。但 `CFG_MFST_F_TEMPLATES = 3` 和 `CFG_MFST_F_CHANNELS = 4` 的 field number 不连续（没有 field 2），enum 允许不连续但容易让新开发者困惑。

**修正:** 在 enum 定义中加注释说明跳过的 field number：
```c
typedef enum {
    CFG_MFST_F_MANIFEST_ID = 1,
    // field 2: reserved (was config_hash, removed in v2.4)
    CFG_MFST_F_TEMPLATES   = 3,
    CFG_MFST_F_CHANNELS    = 4,
    CFG_MFST_F_DMA         = 5,
} config_manifest_field_t;
```

### 2.3 NVS helper — static inline 在 .h 中的问题

如果 nvs_helper.h 被多个 .c 文件 include，每个 .c 文件都会生成一份函数副本。但 ESP-IDF 的编译器会优化（-Os），且这些函数很短（~20 字节代码），影响可忽略。

**但有一个实际问题:** `nvs_erase_keys` 接受 `const char **keys`，在调用点构造数组不够方便。

**修正:** 改为可变参数或保持当前设计（调用点构造数组即可）。**保持当前设计。**

## 安全专家：v2 方案挑战

### 1.2 NULL 守卫 — 掩盖 bug

`frame_encode_string` 将 NULL 转为空字符串会掩盖上游 bug（传 NULL 说明逻辑有误）。

**修正:** 分两层处理：
- `frame_encode_string`: NULL → 编码空字符串 + ESP_LOGW（记录但不崩溃）
- handler 层: 在调用前检查 NULL 并记录具体的上下文（哪个 handler、哪个字段）

这样既不崩溃，又能追踪 bug。**v2 已有此设计，同意。**

### 2.5 wifi_mgr provisioning 安全 — 不够

v2 说"AP 密码从 Kconfig 读取"，但 Kconfig 编译时固定，所有设备密码相同。

**修正:** 
- AP SSID 加 MAC 后缀：`EHome-Setup-AABBCC`（已部分实现）
- AP 密码编译时固定可接受（这是 provisioning，不是生产安全）
- 但必须在 provisioning 完成后立即关闭 SoftAP（30分钟超时 + 连接成功后立即关闭）
- 在 provisioning 页面的 HTML 中加 meta refresh 显示连接状态

**v2 已有 30 分钟超时，补充连接成功后立即关闭。**

### OTA 回滚 — v2 遗漏了自检内容

v2 说"自检通过 → mark_valid"，但没定义自检内容。

**补充自检项:**
1. WiFi 连接成功（IP 获取）
2. MQTT 连接成功（broker 可达）
3. 第一个 StatusReport 发送成功

全部通过后才 mark_valid。任何一个失败 3 次 → mark_invalid + rollback。

---

## 最终决议

v2 方案整体合理，以下是需要修正的 4 个点（已纳入 v2.1 Final）：

| # | 修正项 | 原因 |
|---|--------|------|
| 1 | s_active_idx 用 `_Atomic int` 或 `volatile` | ESP32 双核安全 |
| 2 | ota_cmd_t 改为 heap 分配 | MQTT task 栈偏紧 |
| 3 | frame_field_get_* 改为 static inline 放在 .h | 与现有 API 风格一致 |
| 4 | OTA 自检内容明确定义 | v2 遗漏 |
