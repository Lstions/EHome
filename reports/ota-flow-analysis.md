# OTA 全链路流程分析报告

> 2026-06-07 20:55 Sun 要求重新审视 OTA 设计 vs 实现

---

## 设计意图（来自 ota-99.9-analysis.md §6.4.3）

```
用户 POST /ota/tasks
  → CreateTask (DB insert, supersede 旧任务)
  → SendOtaCommand (MQTT 下发 OtaCmd)
  → ESP32 收到 → 下载 → SHA256 → 切换分区 → 重启
  → 重启后发 Hello (携带新固件版本)
  → HandleHelloOTACompletion (版本核对 → success)
```

## 当前实现（ota.go）

### 1. CreateTask (L314-348) ✅ 正确
- 创建 DB 记录，status=pending
- Supersede 旧 in-flight 任务
- 返回 task 给 caller

### 2. SendOtaCommand (L356-455) ✅ 正确  
- 编码 OtaCmd 帧 → MQTT Publish
- 注册 pending channel → 等 ESP32 回复 downloading (status=0)
- 最多重试 3 次（30s/50s/65s backoff）
- 全部失败 → 标记 task=failed
- 不阻塞 HTTP 请求（goroutine 异步等 ack）

### 3. HandleOtaProgress (L472-552) ✅ 正确
- ESP32 发 downloading (status=0) → 关闭 pending channel（ack）
- 更新 DB status + progress
- 终端状态 immutable（幂等）

### 4. HandleHelloOTACompletion (L588-680) ⚠️ 有设计问题

**Case A: 版本匹配 → success** ✅ 正确
- 设备重启后发 Hello 新版本 → 自动完成 OTA

**Case B: 版本不匹配 → needs_retry** ⚠️ 过早判断
- **问题**: 设备收到 OtaCmd 后可能还没开始下载，Hello 还是旧版本
- **修复**: 已加 2min grace period → 仅当任务创建超过 2min 才判 mismatch

**Case C: 10min 无进度 → failed** ✅ 正确
- 但 10min 对下载+校验+重启来说可能太短

---

## 固件端流程（ota.c）

### ota_start (L350-398) ✅ 正确
- 幂等检查 (ota_is_duplicate)
- 创建 FreeRTOS task → ota_task_func
- ota_task_func → ota_try_download (3x retry)

**新增: 直接 esp_http_client 方案**
- 绕过 esp_https_ota（HTTP 不兼容）
- 逐 chunk 读取 + esp_ota_write
- 实时进度上报

### 0x0D OtaCmd code 改用 0x0A** ⚠️ 
- 固件定义 MSG_OTA_CMD = 0x0A
- 后端 frame.go OtaCmd 编码需确认

---

## 端到端数据流

```
[后端]                              [MQTT]                    [ESP32]
POST /ota/tasks                     
  → CreateTask(pending)             
  → SendOtaCommand                  
     → Publish(nodes/1001/down) ───→ Broker ────────────────→ MSG_OTA_CMD handler
                                                                  → ota_is_duplicate? NO
                                                                  → ota_start()
                                                                  → ota_try_download()
                                                                     → HTTP GET firmware.bin
                                                                    ← OtaProgress(downloading,0%)
  ← OtaProgress(downloading,0%) ─── Broker ←───────────────  ↑
     → close pending channel (ACK!)                             ↓ ...下载中...
                                                                     → OtaProgress(downloading,50%)
  ← ...进度更新...                                              
                                                                     → SHA256 校验
                                                                     → esp_ota_set_boot_partition()
                                                                     → 重启
                                                                     ↓
                                                                     → WiFi + MQTT 连接
                                                                     → Hello(fw=2.2.6)
  ← Hello(fw=2.2.6) ────────────── Broker ←───────────────  ↑  
     → HandleHelloOTACompletion    (Case A: 版本匹配 success!)      
```

## 当前 fail 的真正原因

从日志看：
1. OTA created (12:31:05)
2. OtaCmd sent (12:31:05) ✅
3. ESP32 Hello (12:31:08) 还是 fw=2.2.5
4. HandleHelloOTACompletion → "Version mismatch" → needs_retry ❌

**根因: HandleHelloOTACompletion 在 OtaCmd 刚发出 3 秒就判 mismatch**

ESP32 收到 OtaCmd → 下载需要时间 → 期间发 Hello 还是旧版本 → 后端判 mismatch。

**修复**: 已加 2min grace period，待测试。

---

## 建议：拆离 "版本核对" 和 "超时检测"

当前 `HandleHelloOTACompletion` 同时做三件事:
1. 版本核对（"设备换上新版本了吗？"）
2. mismatch 检测（"设备版本不对？"）
3. 超时检测（"OTA 太久了？"）

建议改为:
```
HandleHelloOTACompletion:
  仅处理 Case A（版本匹配 → success）
  其他情况交给 timeoutScanner 处理
```

这样可以避免 Hello 触发过早的 mismatch 判决。

---

## TODO
- [ ] 2min grace period 生效测试
- [ ] ESP32 串口验证收到 OtaCmd 并开始下载
- [ ] 确认 firmware_id=6 (v2.2.6) URL 正确
- [ ] 重新部署后端
