# 固件OTA

> **状态**: 已实现（v2.2 交付 99.9% 可靠性闭环：HTTPS + SHA256 + A/B 分区 + mark-valid 自动回滚 + Hello 自动完成；36 故障点全闭环）
> **版本**: v3.7
> **日期**: 2026-11-18
> **回滚能力边界（v3.7）**: 应用侧“确认”逻辑已就位，且判据**全部本地可达成**；自动回滚本身由
> `CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE` 编进 **bootloader.bin**，只有整片烧录 / 现场重烧过的
> 设备才具备。**未重烧设备没有自动回滚**（见 §8）。
> **关联**: [总体设计.md](总体设计.md) | [节点.md](节点.md) | [../协议/二进制帧协议.md](../协议/二进制帧协议.md)

## 1. 功能概述

节点固件远程升级：中心端上传固件 → 创建 OTA 任务 → 节点 HTTPS 下载 → SHA256 校验 → A/B 分区切换 → 重启 → 上线自动标记完成。

关键可靠性设计（v2.2 实测收敛至 100/100 成功）：

- **A/B 分区 + 30s 本地自检 mark-valid**：新镜像首次启动处于 `PENDING_VERIFY`；应用在
  `uptime ≥ 30s` 且**不在安全模式**且**本地关键初始化**完成（不含网络）后才标 `VALID`（判据**全部本地可达成**，
  不含联网 / Hello / StatusReport）。确认由独立任务 `ota_confirm_task` 驱动、在 `wifi_mgr_start()` 之前创建，
  **确认时刻不得被网络阻塞**（2026-11-19 实机修复：原实现被同步 WiFi 连接超时推迟到 31.9s）。
  未确认就复位 ⇒ 引导器下次启动置 `ABORTED` 并退回旧分区（需 bootloader 已开启回滚，见 §8）。
- **Hello 自动完成**：OTA 相关帧（progress/result）丢失时，节点重启后 Hello 上报 firmware_version，中心端比对自动标记 OTA success——掉线不丢任务结局。
- **并发 supersede**：同一节点新 OTA 任务创建时，旧任务自动置 failed（`error_msg="Superseded by new attempt"`，ota.go:364-377；可查询遗留任务结果后由新任务接管）。
- **生产 TLS 不可降级**：HTTPS 下载强制；TLS 校验用 Mozilla CRT bundle（公网）/自签 cert（内网，Kconfig 显式声明）。

## 2. 数据模型与状态机

OTA 任务状态字面量**共 8 个**（backend/internal/ota/ota.go:24-33，入库非魔法数字）：

| 状态字面量 | 含义 | 终态 |
|-----------|------|------|
| `pending` | 任务已创建，等待设备确认/开始下载 | 否 |
| `downloading` | 设备正在 HTTPS 下载固件 | 否 |
| `verifying` | 校验阶段（**服务端扩展态**，见下） | 否 |
| `installing` | 写入/切换分区阶段 | 否 |
| `success` | 升级成功（OtaProg 上报或 Hello 自动补完成） | 是 |
| `failed` | 失败（**含用户取消**，见 §2.1） | 是 |
| `timeout` | 超时（timeoutScanner 写入，ota.go:193-228，条件更新在 221-223） | 是 |
| `needs_retry` | 需要重试（已定义，当前无写入点，见下） | 否¹ |

服务端视角状态机：

```
pending ──► downloading ──► installing ──► success
   │            │              │
   │            │              └──► failed / timeout
   │            └──► verifying ──► installing
   ──► failed / timeout

任一非终态 ─cancel（仅 pending/downloading）──► failed
                                         error_msg = "cancelled by user (during download)"
```

- 状态推进由 OtaProg 的 wire code 映射驱动（wire 0→downloading、1→installing、2→success、3→failed、4→verifying；常量 ota.go:41-47，映射 ota.go:598-622），服务端**不强制线性顺序**：未知 wire code 保持当前状态（fail-open，ota.go:618-622）。
- `verifying` 由服务端扩展 wire code `4` 驱动；**当前 ESP32 固件不发 4**——其 OTA_STAGE_VERIFYING / OTA_STAGE_APPLYING 都映射到 `status=1`，故线上任务通常不经 `verifying` 直接进入 `installing`。
- ¹ **`needs_retry` 是已定义但当前无写入点**的字面量，且**不在后端 `terminalStates` 集合内**（该集合仅含 `success` / `failed` / `timeout`，ota.go:49-54）。它只在自动回滚的**失败计数集合**（ota.go:256、339，与 failed/timeout 一起计入）以及前端的不成功终态集合（OTAForm.vue:157，用于停止轮询）中被引用；前端映射表也已为它保留文案与问题态颜色。因无写入点，该状态当前**不可达**，此处按前向兼容记录。
- 超时扫描覆盖 `pending` / `downloading` / `installing`（ota.go:81-85），超阈值后置 `timeout` 并写 `completed_at`。

### 2.1 取消语义：不新增终态，复用 failed

**`cancelled` 不是状态字面量**——后端 8 个状态里没有它，数据库也不会出现该值，任何按状态过滤的查询都取不到它。用户取消**复用 `failed`**，靠 `error_msg` 区分：

| 项 | 值 |
|---|---|
| 任务状态 | `failed` |
| `error_msg` | `cancelled by user (during download)` |
| WS 事件 | `ota_progress`：`ota_id` / `status=failed` / `progress` / `reason="cancelled"` |

**取消允许窗口：仅 `pending` / `downloading`。**

- 窗口由 `cancellableStates = []string{StatusPending, StatusDownloading}` 定义（ota.go:61），判定用 `slices.Contains`（ota.go:733-735）。
- `verifying` / `installing` 阶段：服务端**拒绝**取消，返回 **HTTP 409 Conflict**（错误链含 `ErrTaskNotCancellable`，ota.go:63-65 的 sentinel；API 层 `errors.Is` → 409 在 handler_ota.go:111-118）。
- 终态（`success` / `failed` / `timeout`）：返回 400（`"task %s is already in terminal state %s"`，ota.go:729-731；terminalStates 见 ota.go:49-54）。
- 任务不存在：返回 400（`"task not found: %w"`，ota.go:726-728）。
- 并发竞态：取消走**条件更新**（`WHERE id=? AND status=<读取到的状态>`，ota.go:739-745），`RowsAffected != 1` 即判定状态已变并返回 `ErrTaskNotCancellable`（ota.go:749-753），绝不"先读后写整行 Save"。
- 取消成功时一并清理 ack 等待通道：`pendingMu` 内先查 `ok` 再 `delete` + `close(ch)`（ota.go:758-766），否则该等待协程要空等到 `ackTimeout(30s)×ackMaxRetries(3)`（ota.go:119-120）才退出。

**实现与测试出处（2026-09-19 收尾复核实测）**：实现 ota.go:724-778；API 路由与 409 映射 handler_ota.go:104-120。
单测：`ota_test.go` 的 `TestCancelTask`(257)、`_AlreadyTerminal`(306)、`_NotFound`(328)、`_RejectedDuringInstalling`(365)、`_RejectedDuringVerifying`(394)、`_DownloadingAllowed`(423)、`_ConditionalUpdateRejectsStaleState`(460)、`_ClearsPendingAckChannel`(525)；
API 层：`handler_ota_vendor_user_test.go` 的 `TestOTA_CancelTask_InvalidID`(129,400)、`_InstallingReturns409`(144)、`_VerifyingReturns409`(177)、`_PendingSucceeds`(199,200)。

**为什么这样切分**：进入写分区阶段（verifying/installing）后，服务端单方面把任务置为终态，只会制造"账上说已取消、设备照样刷入新固件"的**账实不符**——服务端再也无法与设备对账。下载阶段取消**不触碰引导分区**，可以安全地在服务端结账；而写分区阶段没有设备侧中止能力（见 §3.1），所以**不谎称已取消**。

> 历史沿革：`cancelled` 状态 + "用户主动取消"源自归档设计 [../archive/v4.0-重建前归档/实现/固件OTA.md](../archive/v4.0-重建前归档/实现/固件OTA.md) 的 P3 计划项（**未勾选、未实现**）。本文件此前把它写成既成事实，前端两处 OTA 映射表也照抄留下了死的 `'cancelled'` 条目，本次一并收回；归档目录本身是历史快照，不再改写。

## 3. 协议契约

- OtaCmd (0x0A)：`ota_id / url / checksum(SHA256 hex) / size / version / sequence`（sequence 用于重投去重）。
- OtaProg (0x0B)：`ota_id / status(0 下载中,1 安装中,2 成功,3 失败,4 校验中) / progress(0-100) / error_message`。
- 容错：OtaChecksum 长度非 64 → WARN + 跳过校验（不卡死）；帧丢失 → Hello 补完成。

### 3.1 取消没有下行帧（**已知缺口**）

当前**不存在** OTA 取消下行帧：esp32-collector/components/frame/frame_codec.h:38-71 的 MSG 表中 OTA 相关只有 `MSG_OTA_CMD=0x0A`（SVR→ESP）与 `MSG_OTA_PROG=0x0B`（ESP→SVR），**没有取消/中止消息号**（实测该文件无 `CANCEL`/`ABORT` 任何匹配）。

因此现状是：

- 服务端"取消"**只是服务端语义**：置 DB 终态 + 清理 ack 等待通道 + 广播 `ota_progress` WS 事件（events.go:21）给前端，**全程不下发任何下行帧**——`CancelTask` 不调用 `SendOtaCommand`，MQTT 侧无对应 topic 消息。
- 设备侧**未实现中止**：components/ota/ota.c 全仓**无 `esp_ota_abort()` 调用**；且 `s_upgrading` 期间收到新的 `OtaCmd` 会被 `ota_classify_cmd`（ota.c:203-224）判为 `OTA_CMD_BUSY`，由 handler_data.c:84-88 **静默丢弃**（仅打 WARN 日志，无回执）。
- 结果：在 `pending`/`downloading` 阶段取消后，**设备仍会继续完成当前下载**，只是服务端不再跟踪该任务。

⇒ 这是**已知缺口**，不是既有能力：不得据此宣称"取消已下发给设备"或"设备会停止升级"。

## 4. API 摘要

| 端点 | 用途 |
|------|------|
| /firmwares | 固件仓库（上传/列表/单删/批删） |
| /firmwares/:id/download | 下载（一次性下载票据鉴权，防盗链） |
| /ota/tasks | 创建 OTA 任务 |
| /ota/tasks/:id/progress | 进度查询 |
| /ota/tasks/:id/cancel | 取消：仅 `pending`/`downloading` 可取消，复用 `failed` + `error_msg`；`verifying`/`installing` 返回 409；终态返回 400（见 §2.1） |
| /nodes/:id/ota/history | 节点 OTA 历史 |

## 5. 前端

- 固件管理页（FirmwareManage.vue）：仓库列表、上传对话框、批量删除。
- OTA 任务进度卡片（节点列表/详情可见）+ 历史列表。
- ota_progress WS 事件驱动进度条。
- 取消按钮**本就只在 `pending` / `downloading` 行渲染**（NodeDetail.vue:385-386、NodeOverview.vue:589），与 §2.1 的服务端允许窗口一致——这是既有事实，未扩大。
- OTA 状态文案/颜色映射表覆盖后端 8 态：NodeDetail.vue `OTA_STATUS_TYPES`(779-788) / `OTA_STATUS_TEXTS`(790-799)；NodeOverview.vue `otaStatusText`(1036) / `otaTagClass`(1043-1050，`timeout`→`bus-tag-red`、`needs_retry`→`bus-tag-orange`，样式定义 1907-1908)；OTAForm.vue `OTA_STATUS_TEXT`(135-146) / `OTA_TERMINAL_FAILURE`(157)。三处均无 `cancelled` 条目。

## 6. 实现记录

| 关注点 | 位置 |
|--------|------|
| 后端 | internal/ota/ota.go（状态机/取消闸门）、internal/api/handler_ota.go（取消路由 + 409 映射）、firmware_download_ticket.go |
| 前端 | views/firmware/FirmwareManage.vue、views/node/NodeDetail.vue、views/node/NodeOverview.vue、stores/ |
| 固件 | components/ota/（下载/校验/回滚）、components/frame/frame_codec.h（MSG 表：**无取消消息**）、A/B 分区表 partitions_*.csv |

Kconfig 三组合：生产 HTTPS + CRT bundle / 内网自签 cert / 开发降级（生产模式检测到降级必须拒绝）。

## 7. 验收标准

- [x] 上传 → 建任务 → 下载进度 → 校验 → 重启 → Hello 自动完成（实机 E2E）
- [x] 确认 → otadata `VALID` → 复位不回退（2026-11-19 真机全链路：2.8.0 → 2.9.0，证据见 [沙箱验证报告](../验证/固件与平台更新机制-沙箱验证-2026-11-18.md) §16）
- [x] 失败注入：checksum 错 → 失败 + 不切换分区
- [x] 并发 OTA → supersede 语义
- [x] 帧丢失恢复：progress 丢帧不影响终态
- [x] 下载票据鉴权（非登录访问 401/403）
- [x] 100/100 压力验证（v2.2 发布记录）
- [x] 取消语义闭环（2026-09-19 收尾复核）：仅 `pending`/`downloading` 可取消；`verifying`/`installing` 返回 409；终态返回 400；并发竞态由条件更新拒绝；取消时清理 ack 等待通道。证据：实现 ota.go:724-778、handler_ota.go:104-120；测试 `TestCancelTask_RejectedDuringInstalling/Verifying`、`_ConditionalUpdateRejectsStaleState`、`_ClearsPendingAckChannel`（ota_test.go:365/394/460/525）与 `TestOTA_CancelTask_InstallingReturns409/VerifyingReturns409`（handler_ota_vendor_user_test.go:144/177）；`go test -race ./internal/ota/... ./internal/api/...` 两包均 ok
- [ ] 设备侧取消/中止（**未实现**）：无取消下行帧，设备不能中止进行中的下载（见 §3.1）
- [x] 回滚语义**实机**验证（2026-11-19，ESP32-C6 8MB / c6-n8）：FW-RB-01 坏镜像崩溃后引导器置 ABORTED 并回退；02 清 NVS 无 AP 仍确认（30.541s，早于 WiFi 连接超时 0.326s）；03 安全模式不确认、复位后回退；04 主动 invalid 立即回退。证据见 [沙箱验证报告](../验证/固件与平台更新机制-沙箱验证-2026-11-18.md) §13。
  未覆盖：`ESP_ERR_OTA_ROLLBACK_FAILED` 降级分支（需另一分区无可用镜像）。

## 8. 已知限制

- 10MB 固件升级耗时目标 ≤5min（网络受限时超出，进度可靠展示）。
- 节点容量：A/B 双分区占用 flash 一半（S3/C6 各 4M/8M/16M 分区表可选）。
- **取消不通知设备**（已知缺口，见 §3.1，**task-1 落地后依然成立**）：服务端取消只结束服务端跟踪，不下发任何下行帧；设备侧无取消帧（frame_codec.h:38-71）、全仓无 `esp_ota_abort()`（实测仅归档文档提及），`s_upgrading` 期间新命令被 `ota_classify_cmd`(ota.c:203-224) 判 BUSY 后由 handler_data.c:84-88 静默丢弃 ⇒ 设备仍会完成当前下载。
- `needs_retry` 状态字面量已定义但当前无写入点（见 §2），前端映射已前向兼容。
- **自动回滚依赖 bootloader 版本（M3b 未完成）**：`CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE`
  编进 `bootloader.bin`，而 OTA 只写应用分区 ⇒ 只通过 OTA 升级到本版本（应用侧判据已就位）、
  bootloader 仍是旧固件的设备**不具备**自动回滚，须现场重烧（方案 D-1 / M3b）。
- **回滚触发条件对已确认镜像是“无回滚”**：镜像已被标 `VALID` 后，后续复位不会再退回旧分区；
  此时“能跑但确定不可用”只能靠主动 invalid（且要求另一分区有可用镜像，否则退化为普通复位）。
