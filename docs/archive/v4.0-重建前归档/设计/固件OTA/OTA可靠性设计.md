# EHomeSystem OTA 可靠性设计 — 99.9% 成功率方案

> 版本: v1.0 | 日期: 2026-06-06 | 基于 E2E 实测 + ESP-IDF v6.0.1 官方文档

## 1. 当前故障面分析

基于 E2E 实测 (v2.2.0 → v2.2.4 共 4 轮 OTA 测试)，梳理了以下 8 类故障点：

### 1.1 ESP32 端 (ota.c)

| # | 故障点 | 严重性 | 影响 | 当前状态 |
|---|--------|--------|------|---------|
| F1 | `build_ota_http_config` timeout=10s | 🔴 高 | 1MB+ 固件耗时 8-12s，弱 WiFi 下 10s 必超时 | 已改 30s, 仍不足 |
| F2 | `esp_https_ota()` 无进度上报 | 🟡 中 | 后端卡在 downloading 阶段，无法判断死活 | 未修 |
| F3 | `esp_https_ota()` 下载后无 success 帧 | 🟡 中 | 依赖 Hello 自动完成 (3h 延迟) | 依赖已有机制 |
| F4 | HTTP 直连无重试 | 🔴 高 | 瞬时网络波动 → 永久失败，无恢复 | 未修 |
| F5 | 无静默成功帧导致后端 3 小时卡住 | 🟡 中 | task #13 downloading → 3h15m → success (Hello) | 已有兜底但延迟大 |
| F6 | 固件 URL 无效/404 → esp_https_ota 报错但无详细日志 | 🟡 中 | 运维排障困难 | 未修 |
| F7 | 同版本拒绝逻辑缺失 | 🟢 低 | 重复升级浪费带宽，但无影响 | 未修 |
| F8 | 校验后不调用 `esp_ota_set_boot_partition` | 🔴 致命 | 实际上 esp_https_ota 内部已调用，但代码不可见 (黑盒) | 需确认 |

### 1.2 后端 (ota.go)

| # | 故障点 | 严重性 | 影响 | 当前状态 |
|---|--------|--------|------|---------|
| B1 | `HandleHelloOTACompletion` 只在 Hello 时触发 | 🟡 中 | ESP32 重启后 3h 才标记 success | 间隔太长 |
| B2 | `HandleOtaProgress` 下游帧丢失 → 永久卡住 | 🔴 高 | MQTT 帧在网络不良时可能被丢弃 (QoS 0) | 未修 |
| B3 | 同设备 OTA supersede 只标记旧任务 failed | 🟢 低 | 逻辑正确但缺少通知 | 已正确 |
| B4 | 10min 超时以 `StartedAt` 计，但 started_at 只在 downloading 时设 | 🟡 中 | pending 卡住的任务永不过期 | 需修复 |

### 1.3 基础设施

| # | 故障点 | 严重性 | 影响 | 当前状态 |
|---|--------|--------|------|---------|
| I1 | MQTT QoS 0 → 帧丢失 | 🔴 高 | OtaCommand/OtaProgress 都无重发保障 | 未修 |
| I2 | HTTP 固件下载无断点续传 (Range) | 🟡 中 | 弱网下 1MB 下载失败率 ~5-10% | 未实现 |
| I3 | 网络拓扑变化 → IP 变更 → HTTP URL 失效 | 🟢 低 | 内网设备 IP 漂移后 URL 不可达 | 未处理 |

---

## 2. ESP-IDF 官方最佳实践 (已查阅)

| 实践 | 来源 | 当前状态 |
|------|------|---------|
| **App Rollback Enable** | `CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE=y` | ❌ 未启用 |
| **OTA partition anti-corruption** | otadata 双扇区写入，power-loss safe | ✅ ESP-IDF 内置 |
| **esp_ota_mark_app_valid_cancel_rollback** | 新固件启动后 30s 内调用 | ✅ 已在 main.c |
| **esp_ota_get_state_partition** 诊断 | 首启时检查 PENDING_VERIFY → 自检 | ❌ 未实现 |
| **两阶段提交** (esp_ota_begin → write → end) | begin/perform/end 是原子操作 | ❌ 改用 esp_https_ota() 单阶段 |
| **HTTP Range 断点续传** | esp_http_client 支持 Range header | ❌ 未实现 |
| **固件签名验证** | `esp_https_ota` 内置 image signature verify | ✅ 当前 SHA256 手动校验 |

---

## 3. 99.9% 可靠性方案

### 3.1 总体架构

```
                    ┌──────────────┐      ┌──────────────┐
                    │   ESP32-OTA  │      │  Backend-OTA │
                    └──────┬───────┘      └──────┬───────┘
                           │                     │
  ┌─ 可靠下发 (QoS 1) ────┼─────────────────────┤
  │  OtaCommand 持久化    │   MQTT QoS 1         │ retry 3次, 指数退避
  │  + 超时重发          │  nodes/1001/down     │
  └───────────────────────┤                     │
                           │                     │
  ┌─ 可靠下载 ────────────┼─────────────────────┤
  │  HTTP Range 断点续传  │   GET /firmwares/    │ 主动进度轮询 (可选)
  │  3次重试 + 退避      │   :filename/download │
  │  超时=60s (慢网)     │                      │
  └───────────────────────┤                     │
                           │                     │
  ┌─ 可靠校验 ────────────┤                     │
  │  SHA256 manual verify │   OtaProgress (QoS1) │ 超时兜底检测
  │  Image signature      │   0x0B frames        │ if downloading >5min
  └───────────────────────┤                     │ → 主动下发重试
                           │                     │
  ┌─ 可靠完成 ────────────┤                     │
  │  发送 success 前      │   OtaProgress        │ HandleHelloOTACompletion
  │  等 2s 确保 MQTT 发出 │   status=1,100%      │ 缩短到 2min 内
  │  THEN set_boot_part    │                      │ + Hello 自动完成
  └───────────────────────┤                     │
                           │                     │
  ┌─ 可靠启动 ────────────┤                     │
  │  ROLLBACK_ENABLE=y    │   Hello (fw=ver)     │ DB 备份节点旧版本号
  │  PENDING_VERIFY 诊断  │                      │ rollback 时需人工介入
  │  WiFi/MQTT 自检通过   │                      │
  │  THEN mark_valid      │                      │
  └───────────────────────┴──────────────────────┘
```

### 3.2 ESP32 端改造

#### 3.2.1 分级重试 + 指数退避

```c
// ota.c 新增
#define OTA_MAX_RETRIES      3
#define OTA_RETRY_BASE_MS    2000   // 2s → 4s → 8s
#define OTA_CONNECT_TIMEOUT  60000  // 60s 全流程
#define OTA_HTTP_TIMEOUT     30000  // 30s HTTP 超时

void ota_start_with_retry(const char *ota_id, const char *url,
                           const char *checksum, uint64_t size) {
    for (int attempt = 1; attempt <= OTA_MAX_RETRIES; attempt++) {
        esp_err_t err = ota_download_and_verify(ota_id, url, checksum, size);
        if (err == ESP_OK) {
            ota_complete_reboot();
            return; // never reached (reboots)
        }
        ESP_LOGW(TAG, "OTA attempt %d/%d failed: %s",
                 attempt, OTA_MAX_RETRIES, esp_err_to_name(err));
        if (attempt < OTA_MAX_RETRIES) {
            int delay_ms = OTA_RETRY_BASE_MS * (1 << (attempt - 1));
            msg_handler_send_ota_prog(ota_id, 0, 0, "retry"); // 恢复 downloading
            vTaskDelay(pdMS_TO_TICKS(delay_ms));
        }
    }
    // 全部失败 → 报告失败
    msg_handler_send_ota_prog(ota_id, 3, 0, "All retries exhausted");
    s_upgrading = false;
}
```

**成功率提升**: 重试 3 次可将瞬时网络故障的成功率从 ~90% → ~99.9% (假设单次失败率 10%)

#### 3.2.2 App Rollback (防 brick)

```kconfig
# sdkconfig.defaults 新增
CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE=y
```

```c
// main.c 新增启动自检
void app_self_test_and_commit(void) {
    const esp_partition_t *running = esp_ota_get_running_partition();
    esp_ota_img_states_t ota_state;
    if (esp_ota_get_state_partition(running, &ota_state) != ESP_OK) return;

    if (ota_state == ESP_OTA_IMG_PENDING_VERIFY) {
        ESP_LOGI(TAG, "First boot after OTA — running self-test...");

        // 1. WiFi 连接检查 (最多等 30s)
        bool wifi_ok = wait_for_wifi(30000);
        // 2. MQTT 连接检查
        bool mqtt_ok = wait_for_mqtt(15000);
        // 3. 状态上报成功
        bool hello_ok = send_hello_and_wait_ack(15000);

        if (wifi_ok && mqtt_ok && hello_ok) {
            ESP_LOGI(TAG, "Self-test PASSED, marking firmware valid");
            esp_ota_mark_app_valid_cancel_rollback();
        } else {
            ESP_LOGE(TAG, "Self-test FAILED, rolling back...");
            esp_ota_mark_app_invalid_rollback_and_reboot();
        }
    }
}
```

**防 brick 保证**: 新固件启动后自检失败 → 自动回滚到旧固件，设备永不死机。

#### 3.2.3 进度上报 + 静默修复

```c
// ota.c: esp_https_ota 无进度回调，改为 begin/perform 循环 + 手动进度
// 但 begin/perform 在 HTTP 模式下有 bug → 改用 esp_https_ota + 单独进度上报

static esp_err_t ota_download_and_verify(const char *ota_id, const char *url,
                                          const char *checksum, uint64_t size) {
    // ... 同现有 ota_start 逻辑 ...

    // 关键修复: 成功路径发 success 帧并等待
    msg_handler_send_ota_prog(ota_id, 1, 100, NULL);
    vTaskDelay(pdMS_TO_TICKS(2000));  // 确保 MQTT 帧发出

    // 设置 boot partition
    err = esp_ota_set_boot_partition(update_partition);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to set boot partition: %s", esp_err_to_name(err));
        msg_handler_send_ota_prog(ota_id, 3, 0, "Boot partition set failed");
        return err;
    }

    ESP_LOGI(TAG, "OTA success, restarting...");
    esp_restart();
    return ESP_OK; // unreachable
}
```

### 3.3 后端改造

#### 3.3.1 Pending 超时 + StartedAt 修复

```go
// ota.go: HandleHelloOTACompletion 增加 pending 超时逻辑
func (m *Manager) HandleHelloOTACompletion(...) {
    // ... 现有逻辑 ...

    // Case A: 同版本 → success ✅
    // Case B: 超时 >10min → failed ✅

    // Case C (NEW): pending 超过 10min 从未变成 downloading → failed
    if task.Status == StatusPending && task.CreatedAt != nil &&
        now.Sub(*task.CreatedAt) > 10*time.Minute {
        task.Status = StatusFailed
        task.ErrorMsg = "Timeout: OTA command not acknowledged by device"
        // ...
    }
}
```

#### 3.3.2 主动进度轮询 (P2 可选)

```go
// 后端定期检查 downloading 状态超过 5min 的任务
// → 重新下发 OtaCommand (重试)
func (m *Manager) RetryStuckDownloads() {
    cutoff := time.Now().Add(-5 * time.Minute)
    var tasks []models.OTATask
    m.db.Where("status = 'downloading' AND started_at < ?", cutoff).Find(&tasks)
    for _, t := range tasks {
        logger.Warnf("[OTA] Retrying stuck download: %s", t.OtaID)
        m.SendOtaCommand(&t)  // 重新下发
    }
}
```

#### 3.3.3 MQTT QoS 升级

```go
// mqtt/client.go: OTA 相关 topic 用 QoS 1
func (c *Client) PublishWithQoS(topic string, payload []byte, qos byte) error {
    token := c.client.Publish(topic, qos, false, payload)
    token.WaitTimeout(5 * time.Second)
    return token.Error()
}

// OTA 下发
m.mqtt.PublishWithQoS(topic, enc.Bytes(), 1)  // QoS 1: 至少一次送达
```

### 3.4 可观测性

| 指标 | 来源 | 告警阈值 |
|------|------|---------|
| `ota_total` | DB `ota_tasks` COUNT | 无 |
| `ota_success_rate` | success / total × 100 | < 99% |
| `ota_avg_duration` | completed_at - started_at | > 5min |
| `ota_retry_count` | error_msg LIKE '%retry%' | > 1/task |
| `ota_rollback_count` | ESP32 端自检失败 | > 0 |
| `ota_hello_completion_pct` | error_msg = 'Auto-completed via Hello' | > 50% |

---

## 4. 预期成功率计算

| 组件 | 当前可靠性 | 改造后 | 来源 |
|------|-----------|--------|------|
| OtaCmd 下发 (MQTT) | ~99% (QoS 0) | **99.9%** (QoS 1 + 重发) | MQTT 规范 |
| HTTP 固件下载 | ~90% (无重试) | **~99.9%** (3 次重试) | 3 重试 × 10% 失败率 |
| SHA256 校验 | 100% | 100% | 确定性 |
| 设备重启 | ~99.99% | 99.99% | 硬件可靠性 |
| 新固件启动 | ~99.9% | **~99.99%** (Rollback) | ESP-IDF 官方 |
| Hello 自动完成 | 100% (最终) | 100% | 确定性 |
| **端到端** | **~88%** | **~99.7%+** | 乘积 |

> 注: 99.7% = 每 1000 次 OTA 有 3 次需人工介入。配合 Rollback 保护不 brick 设备后，实际可用性远超 99.9%。

---

## 5. 实施计划

### Phase 1: 紧急修复 (1-2h) — 达到 ~95%

| 项 | 文件 | 工作量 |
|----|------|--------|
| S1. MQTT QoS 1 (OtaCommand + OtaProgress) | `mqtt/client.go`, `ota.go` | 30min |
| S2. HTTP 超时 10s→60s | `ota.c:build_ota_http_config` | 5min |
| S3. 成功后发 success 帧 + 等 2s | `ota.c:ota_start` | 10min |
| S4. Pending 超时兜底 | `ota.go:HandleHelloOTACompletion` | 10min |

### Phase 2: 核心可靠性 (半天) — 达到 ~99%

| 项 | 文件 | 工作量 |
|----|------|--------|
| S5. OTA 3 次重试 + 指数退避 | `ota.c` | 1h |
| S6. App Rollback Enable + 自检 | `main.c`, `sdkconfig.defaults` | 1.5h |
| S7. 后端重试下发 (stuck downloads) | `ota.go` | 45min |

### Phase 3: 最佳实践 (1 天) — 达到 ~99.9%

| 项 | 文件 | 工作量 |
|----|------|--------|
| S8. HTTP Range 断点续传 (弱网大文件) | `ota.c` | 3h |
| S9. 同版本拒绝 (前端+后端) | `handler_ota.go`, `ota.go` | 1h |
| S10. 可观测性指标 + 告警 | `ota.go`, 监控配置 | 2h |

---

## 6. 附: 故障树 (FTA)

```
OTA 失败
├── [F1] OtaCommand 未送达 ───────────────── QoS 0 丢帧 ──→ S1 (QoS 1)
│   └── 重试 3 次仍失败 ──→ B1 超时标记 failed ─→ S4
├── [F4] HTTP 下载失败 ───────────────────── 网络瞬断 ──→ S5 (重试 3 次)
│   ├── 1MB+ 慢网超时 ──→ S2 (timeout=60s)
│   └── URL 不可达 ────→ S7 (后端重发 OtaCmd)
├── [F8] 校验失败 ─────────────────────────── checksum 不匹配 ─→ 阻止烧写
├── [ROLLBACK] 新固件崩溃 ─────────────────── S6 (自检 + rollback)
│   └── 自检: WiFi/MQTT/Hello 任一失败 → rollback
└── [I2] 进度丢失 ─────────────────────────── S3 (发 success 前等 2s)
    └── 即使丢失 ──→ B1 (Hello 自动完成)
```
