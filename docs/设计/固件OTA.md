# 固件OTA

> **状态**: 已实现（v2.2 交付 99.9% 可靠性闭环：HTTPS + SHA256 + A/B 分区 + mark-valid 自动回滚 + Hello 自动完成；36 故障点全闭环）
> **版本**: v3.4
> **日期**: 2026-08-22
> **关联**: [总体设计.md](总体设计.md) | [节点.md](节点.md) | [../协议/二进制帧协议.md](../协议/二进制帧协议.md)

## 1. 功能概述

节点固件远程升级：中心端上传固件 → 创建 OTA 任务 → 节点 HTTPS 下载 → SHA256 校验 → A/B 分区切换 → 重启 → 上线自动标记完成。

关键可靠性设计（v2.2 实测收敛至 100/100 成功）：

- **A/B 分区 + 30s mark-valid**：新镜像启动后 30 秒内验证（保活周期），失败自动回滚旧分区。
- **Hello 自动完成**：OTA 相关帧（progress/result）丢失时，节点重启后 Hello 上报 firmware_version，中心端比对自动标记 OTA success——掉线不丢任务结局。
- **并发 supersede**：同一节点新 OTA 任务创建时，旧任务自动置 supersede（可查询遗留任务结果后由新任务接管）。
- **生产 TLS 不可降级**：HTTPS 下载强制；TLS 校验用 Mozilla CRT bundle（公网）/自签 cert（内网，Kconfig 显式声明）。

## 2. 数据模型与状态机

OTA 任务状态（6 态 + cancelled）：

```
pending → downloading → installing → verifying → success
                          │                  │
                          └──► failed ◄──────┘
任一状态 ──cancel──► cancelled
```

- 状态字面量入库（非魔法数字）。
- 节点侧 9 步流程：收 OtaCmd → 去重/冲突判定 → HTTPS 下载（进度回报）→ SHA256 校验 → 分区写入 → esp_partition_get_sha256 比对 → 切换分区 → 重启 → 新镜像启动验证（mark-valid）→ Hello 上报。

## 3. 协议契约

- OtaCmd (0x0A)：`ota_id / url / checksum(SHA256 hex) / size / version / sequence`（sequence 用于重投去重）。
- OtaProg (0x0B)：`ota_id / status(0 下载中,1 安装中,2 成功,3 失败,4 校验中) / progress(0-100) / error_message`。
- 容错：OtaChecksum 长度非 64 → WARN + 跳过校验（不卡死）；帧丢失 → Hello 补完成。

## 4. API 摘要

| 端点 | 用途 |
|------|------|
| /firmwares | 固件仓库（上传/列表/单删/批删） |
| /firmwares/:id/download | 下载（一次性下载票据鉴权，防盗链） |
| /ota/tasks | 创建 OTA 任务 |
| /ota/tasks/:id/progress | 进度查询 |
| /ota/tasks/:id/cancel | 取消 |
| /nodes/:id/ota/history | 节点 OTA 历史 |

## 5. 前端

- 固件管理页（FirmwareManage.vue）：仓库列表、上传对话框、批量删除。
- OTA 任务进度卡片（节点列表/详情可见）+ 历史列表。
- ota_progress WS 事件驱动进度条。

## 6. 实现记录

| 关注点 | 位置 |
|--------|------|
| 后端 | internal/ota/ota.go、handler_ota.go、firmware_download_ticket.go |
| 前端 | views/firmware/FirmwareManage.vue、stores/ |
| 固件 | components/ota/（下载/校验/回滚）、A/B 分区表 partitions_*.csv |

Kconfig 三组合：生产 HTTPS + CRT bundle / 内网自签 cert / 开发降级（生产模式检测到降级必须拒绝）。

## 7. 验收标准

- [x] 上传 → 建任务 → 下载进度 → 校验 → 重启 → Hello 自动完成（实机 E2E）
- [x] 失败注入：checksum 错 → 失败 + 不切换分区
- [x] 并发 OTA → supersede 语义
- [x] 帧丢失恢复：progress 丢帧不影响终态
- [x] 下载票据鉴权（非登录访问 401/403）
- [x] 100/100 压力验证（v2.2 发布记录）

## 8. 已知限制

- 10MB 固件升级耗时目标 ≤5min（网络受限时超出，进度可靠展示）。
- 节点容量：A/B 双分区占用 flash 一半（S3/C6 各 4M/8M/16M 分区表可选）。
