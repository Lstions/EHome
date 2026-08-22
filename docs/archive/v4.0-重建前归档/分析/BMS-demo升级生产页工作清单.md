# BMS Demo 页 → 生产页升级工作清单（深入分析）

> 触发：分析 `frontend-shared/src/dev/BmsDemoPage.vue`（designs/bms.png 像素级原型）要升级为真实生产页面所需的工作。
> 分析方法：逐功能对照 demo 源码 ↔ 生产代码（BmsDetailPage.vue + 共享组件 + 后端 handler 契约），标注真实度与工作缺口。
> 结论先行：这不是"对接 API"能了事的——demo 的半数功能（全局搜索/通知中心/远程连接/平台级统计卡/设备操作"一键执行"）在真实系统里是**不同的产品形态**（有后端契约或完全不存在），需要产品决策裁剪或改造；另一半（指标/趋势/电芯/MOS/保护/数据流/指令频率）生产页已有对应组件，核心工作是**保留生产实现、回填 demo 缺失的真实数据链路与交互**。

---

## 0. 现状盘点（demo 与生产各自拥有什么）

| 维度 | BmsDemoPage.vue (dev) | BmsDetailPage.vue (生产) |
|---|---|---|
| 定位 | designs/bms.png 像素复刻，position:fixed 覆盖 MainLayout | MainLayout children，真实路由 `/edge-device/:id` |
| 数据 | 100% 静态 mock（写死常量 + setInterval 假刷新） | useDeviceData（API fetch + WS 实时订阅 + 序列号守卫） |
| 指标卡 | 6 张平台级卡（在线设备/离线设备/告警/平均温度/电压/SOC） | 4 张设备级卡（SOC/剩余容量/总电压/电流，MetricStatCard） |
| 图表 | 裸 echarts 自绘正弦波趋势图 | HistoryChartSection（共享 LineChart）+ BmsCellVoltageHistoryChart |
| 温度/MOS/保护 | 静态假数据 + 假开关 | BmsProtectionGrid（bitmask 解码）+ BmsMosStatus（只读） |
| 实时数据流 | 自制 table + interval 刷新 | RealtimeDataList（明文/hex/错误码/清空） |
| 指令频率 | 自制折叠 + 5 条写死 | CommandFrequencySection→CommandList（真实 API） |
| 受控操作 | 4 项静态 confirm 假闭环 | DeviceControlPanel（catalog/确认/重验证/历史/人工处置） |
| 设备操作 | 重启/远程连接/导出/查日志/离线 → 假 toast | DeviceHeader 编辑/删除/同步HA/刷新；DeviceControlPanel 真实 catalog |
| 全局功能 | 侧边栏导航/全局搜索/通知中心/远程连接 | MainLayout 全局导航；**通知中心 UI 已存在**（MainLayout.vue:138-174 铃铛+未读 badge+面板+全部已读+点击跳转，调用 getNotifications/getUnreadCount/markAsRead/markAllAsRead，自仓库重建 2026-06-02 即存在）；全局搜索/远程连接无生产形态 |
| 响应式 | 1200/768/480 三档自制 | 生产共享 useResponsive + 移动端组件 |

共享组件（生产页已在用，demo 若要升级应整体复用而非重写）：
- `DeviceHeader.vue`（253 行：编辑/删除/同步HA/刷新 + 移动端溢出下拉）
- `DeviceInfoCard.vue`（127 行：基本信息双形态）
- `HistoryChartSection.vue`（361 行：历史数据图）
- `CommandFrequencySection.vue` → `CommandList.vue`（指令频率，真实 API `GET/PUT /edge-devices/:id/commands`）
- `DeviceControlPanel.vue`（64 行：受控操作 + 操作历史时间线，真实 API）
- `BmsCellVoltageChart.vue` / `BmsCellVoltageHistoryChart.vue` / `BmsProtectionGrid.vue` / `BmsMosStatus.vue`
- `RealtimeDataList.vue`、`MetricStatCard.vue`、`StatusItemGrid.vue`
- `useDeviceData.ts`（218 行：WS 订阅 + 竞态守卫 + 服务端时间戳）

后端相关契约（已核验，非 stub）：
- `GET /edge-devices/:id/actions`（操作 catalog，commandexec.Catalog）
- `POST /edge-devices/:id/operations`（创建操作，含 Idempotency-Key/ConfirmationToken/Reason，202/409/403 recent_auth_required/429）
- `POST /edge-devices/:id/actions/:action_id/confirm`（确认）
- `GET /device-operations/:execution_id`、`/cancel`、`/resolve`（人工处置）
- `GET /edge-devices/:id/commands`（指令模板含 interval）、`PUT`（写 interval）
- `GET /edge-devices/:id/latest-data`（最新数据）
- `GET /devices/:id/history`、`/devices/:id/sensor-data`（历史数据，**实际注册在 `/devices/:id/...` 前缀**，非 `/edge-devices/:id/...`）
- `GET /unified-data/historical-batch`（生产组件 useHistoryData 实际走的批量历史端点，含 fallback `/devices/:id/history`）
- `events.DeviceOperationUpdate`/`DataUpdate`/`ChannelData`/`EdgeDeviceStatus`（WS）

---

## 1. 升级总策略（先决策再动手）

**建议骨架：不做"把 BmsDemoPage 改造成生产页"，而是"以 BmsDetailPage 为壳，按 demo 补充缺失的产品能力 + 统一视觉"**，理由：
1. 生产页已有 90% 的数据链路（WS/API/竞态/组件代际守卫），demo 的一切数据都是假的，改造 demo 等于重写全部数据层；
2. demo 的覆盖式布局（自绘 sidebar/topbar）必须弃用——生产 chrome 由 MainLayout 提供（既有约定）；
3. 视觉统一：demo 用的是老版渐变圆底统计卡、老卡片间距，生产已统一 MetricStatCard/theme token；应按生产设计系统收敛 demo 视觉。

需要产品拍板的决策点（详见 §5）：
- A. demo 的"重启设备/远程连接/导出/查日志/设为离线"是否保留？真实系统里它们对应 DeviceControlPanel 的 catalog（多数操作 disabled）+ DeviceHeader 编辑/删除/同步HA。**建议**：保留"重启设备"（走 DeviceControlPanel catalog 中 Risk=low 的读操作；bms_restart 是 reset 语义、当前 disabled，见 §2.7），其余收敛到生产已有操作。
- B. 通知中心是否做全局 UI？**已实现**（MainLayout 铃铛+面板，见 §0）——无需决策，本期只确认入口可达。
- C. "全局搜索 Ctrl+K"真实系统无此功能。建议裁剪（不做），或作为独立产品功能规划。
- D. 平台级统计卡（在线设备数等）与设备详情页语义冲突。建议删除，保留设备级 4 卡。

---

## 2. 数据层：把 mock 换成真实契约（逐项）

复用 `useDeviceData` 已实现的部分，需补齐/确认：

### 2.1 设备基本信息（demo 的 device-card → 生产 DeviceInfoCard）
| demo 字段 | 生产数据源 | 状态 |
|---|---|---|
| 设备名称/类型 | `edgeDeviceStore.fetchDetail(id)` → `device.name` / `device_type` | 已有 ✅ |
| 在线状态 | `device.status` + WS `edge_device_status` | 已有 ✅ |
| 固件版本 | `device.config_version`（非 v1.4.8 硬编码） | demo 改 ✅ |
| 节点路径 | `device.node_id` + `GET /nodes/:id`（节点名称） | 需补 ✅→ |
| 运行时长 | 无直接字段；可用 `last_data_at` 或节点 `uptime_seconds`（node_status 事件）派生 | ⚠️ 需数据源决策 |
| IP/MAC/设备时间 | **后端无此字段**（EdgeDevice 模型无 IP/MAC）。要么裁剪，要么后端加字段（不建议） | ✂️ 建议删除 |

**动作**：demo 的 device-photo/名称行/节点/固件/时长改用 DeviceInfoCard + DeviceHeader；"节点"链接跳 `/node/:node_id/overview`。

### 2.2 统计卡（6 张 → 4 张设备级）
替换为生产已有的 4 张 MetricStatCard 卡片数据绑定（SOC/剩余容量/总电压/电流，来自 `latestData.rsoc/remaining_capacity/total_voltage/current`）。
demo 的"在线设备/离线设备/告警/平均温度"是平台大盘指标，不属于设备详情。**删除**。

### 2.3 运行趋势（demo 自绘 chart → HistoryChartSection）
- 生产 `HistoryChartSection` 已是共享图表（含时间范围/指标选择、服务端时间戳、LineChart 主题/tooltip 双列）。
- demo 的 5 个时间范围（1h/6h/12h/24h/7d）与 4 个指标（温度/电压/电流/SOC）需与 HistoryChartSection 的能力对齐。**实测缺口**：生产 `useTimeRange` 只支持 1h/24h/7d/custom（**缺 6h/12h**）——若需对齐 demo 的 5 档，须扩展 useTimeRange 加 6h/12h 档（共享 composable，改动影响所有用它的页面，需回归）；指标覆盖需确认 HistoryChartSection 是否支持 SOC/温度/电压/电流 4 类。不覆盖则扩展组件而非自绘。
- 电芯电压历史 → `BmsCellVoltageHistoryChart`（生产已有，含服务端时间）。

### 2.4 温度/MOS/保护（demo → 生产组件直换）
| demo | 生产 | 动作 |
|---|---|---|
| 3 探头静态 → 生产 tempProbes（temp_1..8 / temperature_1..8，三档色） | 已有 ✅ | demo 换组件 |
| MOS 开关（假） → BmsMosStatus 只读（fet_status bitmask 解码） | 已有 ✅ | 换组件，删除开关（真实控制走 DeviceControlPanel catalog） |
| 保护 8 项写死 → BmsProtectionGrid（protection_status 16 位 bitmask 解码 12 项） | 已有 ✅ | 换组件 |

### 2.5 实时数据流（demo table → RealtimeDataList）
生产已有（明文/16进制、实时/历史标签、错误码标签、清空按钮、autoScroll）。demo 的"暂停/恢复"按钮是假交互——生产无暂停语义（数据是推流），**裁剪**或保留为"停流本地显示"的本地开关（不建议引入）。

### 2.6 指令频率（demo 自制 → CommandFrequencySection）
生产已有（真实 API + 0=禁用语义 + 加载失败重试 + 输入校验）。demo 5 条写死的 read_basic/read_cell/read_hw/read_combined/read_prot 恰与后端 jiabaida ControlActions（read_basic_info / read_cell_voltage / read_hardware_version / read_comprehensive / read_protection_count）**一一对应但 id/描述不同** —— 以真实 catalog 为准，demo 文案可作 UI 文案参考。

### 2.7 受控操作（demo 4 项假 → DeviceControlPanel 真实）
- 生产 DeviceControlPanel 已实现：catalog 加载、available/unavailable 过滤（含 Schema 客户端支持检查）、风险级确认（medium+ 走 confirm 弹窗）、Idempotency-Key、10 分钟最近身份验证（recent_auth_required → 重验证弹窗）、操作历史时间线、UNKNOWN 人工处置（resolve）。
- **关键差异**：后端 jiabaida `ControlActions()` 里 5 个读操作 `Enabled: false`（注释"remain disabled until a real BMS supplies…"），`bms_restart` 也是 `Enabled:false`，`set_mos_policy` compiler 存在但 catalog 不可用。**即：BMS 当前没有任何可用操作**。升级后受控操作区在产品上会显示"当前没有可执行的受控操作"（空态）。必须接受这一点，不能造假按钮。
- **归因修正（2026-08-14）**：控制开关机制已改为「实现即默认启用」——`EnabledDeviceActions` 环境变量白名单已删除，`DeviceControlV2Enabled` 默认 true，具备完整执行链（verifier + 对账语义）的操作默认可用（SN-3001 的 `set_rain_sensitivity`/`set_device_address`/`set_baud_rate` 已默认启用）。BMS 仍不可用是因为 jiabaida 驱动的操作**尚未声明对账语义**（`Verification` 为空 + 无 verifier 接线），fail-closed 正确。**BMS 启用路径**：给 jiabaida 的 `bms_restart`/`set_mos_policy` 补 `Verification` 声明 + verifier（bounded_sequence 编译器/verifier 已存在，commit 27c1ccf4），即可默认启用，无需环境变量、无需白名单。
- demo 的"恢复默认配置/升级固件/清除告警"真实后端无对应 action（升级固件走 OTA 模块，独立入口）→ **裁剪/改跳 OTA 页**。

### 2.8 操作历史（demo table → DeviceControlPanel timeline）
生产已有（真实 operation history + 状态/结果/人工处置）。demo 的 addHistory 本地数组**删除**。

---

## 3. 设备操作按钮（demo"重启/远程/导出/日志/离线"真实形态）

这些 demo 按钮在真实系统里没有"设备详情页直连"的产品形态：

| demo 按钮 | 真实形态 | 处置建议 |
|---|---|---|
| 重启设备 | DeviceControlPanel catalog 的 bms_restart（disabled：jiabaida 未声明对账语义） | 保留按钮但接 DeviceControlPanel；不可用时显示空态（补 Verifier 后默认可用） |
| 远程连接 | 后端有 `GET /channels/:channel_id/terminal` + `/write` + WS `terminal_ack`（通道终端） | 若要保留：转为"通道终端"入口（需设备所属通道），属独立功能 |
| 导出数据 | 后端无"导出"端点；数据可经 `/edge-devices/:id/history` + 前端 CSV | 裁剪或做前端导出 |
| 查看日志 | 后端 `GET /nodes/:id/logstream` 或 `handler_logstream.go`（LogPanel 已用于 NodeOverview） | 可接 LogPanel（若设备挂节点） |
| 设为离线/在线 | 无此 API（在线状态由设备上报决定） | **裁剪**（不合理产品语义） |

---

## 4. 全局功能（demo-only，产品决策）

| demo 功能 | 后端契约 | 建议 |
|---|---|---|
| 自绘侧边栏导航 | MainLayout 已有真实导航 | 删除 demo sidebar，回归 MainLayout |
| 全局搜索 Ctrl+K | 无后端；`GET /unified-data/…` 只查数据不查设备/节点名 | **裁剪**（或独立产品功能） |
| 通知中心 | 后端有 `/notifications` / `unread-count` / `read` / `read-all` + WS `notification` 事件，**生产前端 UI 已存在**（MainLayout 铃铛+面板+全部已读+点击跳转，见 §0） | **无需另立全局任务**——已实现；本页升级只需确认入口可达即可 |
| 面包屑/返回 | 生产 PageHeader + router.back() | 换生产组件 |
| 远程连接抽屉 | 通道终端 + 权限 | 视产品决策 |

---

## 5. 必须的产品决策清单（实现前拍板）

1. **demo 的"完整 demo chrome"是否保留**：建议弃用（生产用 MainLayout）。
2. **样式方向**：生产统一 MetricStatCard/theme token/dark 适配，demo 老渐变统计卡是否全量替换（建议替换，符合既有规范）。
3. **BMS 无可用操作的产品形态**：受控操作区空态是正确语义（jiabaida 尚未声明对账语义 → fail-closed）。区别于旧选项"后端补 EnabledDeviceActions"：**白名单机制已删除（2026-08-14）**，启用路径 = 给 jiabaida 补 Verifier + Verification 声明，实现即默认启用（参照 SN-3001）。
4. **通知中心/全局搜索**：通知中心 UI **已实现**（MainLayout 铃铛，见 §0）——无需决策，本期只确认入口可达；全局搜索无后端契约，**裁剪**（或独立产品功能）。
5. **是否会部署到多设备类型**：demo 的 BMS 专属卡片（电芯/MOS）直接进通用设备详情 or 保持 BMS 专属（生产 BmsDetailPage 已是 BMS 专属，无冲突）。

---

## 6. 实施任务拆解（按依赖排序，含验证门禁）

### Phase 0：技术准备（0.5d）
- [ ] 读 `docs/设计/边缘设备/详细设计.md` 确认 EdgeDevice 数据字典（已核验 models.go）
- [ ] 确认 HistoryChartSection 支持的指标/时间范围配置（demo 的 5 范围 4 指标是否覆盖；**已知缺口：useTimeRange 缺 6h/12h**）
- [x] 确认通知中心后端契约形状（**已确认**：UI 已存在 MainLayout.vue:138-174，无需另立任务）

### Phase 1：生产页对齐 demo 缺口（2-3d）
- [ ] BmsDetailPage 补"节点名称/节点跳转"（device.node_id → /node/:node_id/overview）
- [ ] BmsDetailPage 补"固件版本"展示（device.config_version）
- [ ] 与 demo 视觉对齐的细节（卡片间距/标题字号/图标风格——以设计稿为准但用生产 token）
- [ ] （产品同意才做）通道终端入口 / LogPanel 入口 / 前端数据导出
- [ ] 全量 vitest + typecheck + CDP 验收（桌面 1440 + 移动 375）

### Phase 2：demo 页收敛（0.5d）
- [ ] BmsDemoPage 顶部加"DEV DEMO"显著水印/门禁（防误当生产页）
- [ ] 删除或禁用 demo 中"导出/离线/全局搜索"等假功能按钮，改为明确的"demo 占位"样式
- [ ] 文档标注 demo ≡ 设计稿预览，不承载真数据

### Phase 3（独立任务，可选）：
- [x] 全局通知中心 UI（**已完成**：MainLayout.vue:138-174，2026-08-14 核验）
- [ ] BMS 受控操作启用（给 jiabaida 的 bms_restart/set_mos_policy 补 Verifier + Verification 声明 → bounded_sequence 默认启用；无需白名单/环境变量。注意：其中 set_mos_policy/bms_restart 需真机验证读回对账）
- [ ] （可选）useTimeRange 扩展 6h/12h 档（若需对齐 demo 5 档时间范围；共享 composable 需回归所有消费页）

---

## 7. 风险与注意

1. **不要造假数据填表**：BMS catalog 无可用操作（jiabaida 未声明对账语义）→ 空态是正确语义（参照 §2.7）；测试也不得 mock 出后端不存在的响应（规则见 demo→生产升级模式 §7）。注意 SN-3001 系列已默认启用，勿再按"全不可用"写测试。
2. **写操作门控**：任何新增写路径都必须离线门控 + 组件代际守卫（route.params.id 变化时序列号自增），参照 NodeOverview 审查清单 §8.1。
3. **WS payload 以后端为准**：订阅事件时逐字段回源核验（如 `node_status` 无 connection_quality，延迟在 `ping_result`；`edge_device_status` 的 payload 同理）。
4. **序列号 vs 数字主键**：设备关联节点跳转用 `device.node_id`（物理序列号）拼 URL，后端 `findNodeByID` 双形态兼容，前端须确认。
5. **测试背书陷阱**：新加的 spec mock 值必须与后端真实响应形状一致（如 `actions` 空数组、`commands` 返回真实字段）。
6. **双页互链**：生产有 NodeDetail（旧版 `/node/:id`）与 NodeOverview（新版 `/node/:id/overview`）；BMS 详情跳节点应明确目标页，避免用户迷失。

---

## 8. 最终结论

- **工作量**：核心（Phase 1）约 2-3 人日；不含全局搜索等独立产品功能（通知中心已实现，不占工作量）。
- **产出物**：以 BmsDetailPage 为壳 + 生产共享组件 + 真实 API/WS；demo 仅保留为设计稿预览（加水印）。
- **必须产品拍板**：§5 的 5 项（尤其 BMS 无可用操作的空态、是否扩展 6h/12h 时间范围）。
- **不做什么**：不把 demo 的假交互搬到生产；不做后端不存在的字段（IP/MAC）；不做"设为离线"等反产品语义按钮。
