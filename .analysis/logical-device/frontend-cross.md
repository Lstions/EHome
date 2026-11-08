# T3 前端信息架构与跨域耦合审计 —— 「逻辑设备」

> **审计对象**：`/logical-device` 页（`frontend-shared/src/views/logical-device/LogicalDeviceList.vue`，1070 行）+ `api/logicalDevice.ts`（156 行）+ 全部跨域引用点
> **代码基线**：本地 HEAD `c76744d3`（2026-10-23 20:53:43 +0800）；生产 `/health` 报 commit `c76744d3c8ddfac8387902fc6fca98d78ba5dbfd`（**同一提交**，故生产即本审计基线）
> **生产库采样时刻**：2026-10-23T12:54–13:22 UTC（北京时间 20:54–21:22）
> **本机库采样时刻**：2026-10-23T12:47–12:52 UTC
> **口径声明**：所有"行数"均为 `count(*)`；生产 SQL 全部只读（SELECT/EXPLAIN），未做任何 DDL/写入/重启；延迟数字为 `curl -w %{time_total}`（客户端总耗时，含网络），非服务端指标。
> **证据等级**：**T** = 实测（真跑 psql/curl/prometheus）；**C** = 代码（文件:行）；**D** = 文档。

---

## 0. 结论先行（供 Lead 快速裁决）

| # | 结论 | 等级 |
|---|---|---|
| 1 | 页面在**承诺上**只做三件事：看列表 / 合并 / 改名称+保留期；**不能创建、不能删除、不能看测点数据、无详情页** | C |
| 2 | 「合并」与「改保留期」这两个动作在整个前端**只有这一个页面能触达**（全仓唯一调用点，分母可枚举），因此"删页面"≠"少个视图" | C |
| 3 | 但这三项能力对用户的实际可达价值被**生产 42–110 秒**的接口延迟实质抵消：默认 page_size=20 实测 **110.6s**，是前端特例超时（60s）的 **1.84 倍** ⇒ 页面在默认参数下**必然超时** | T |
| 4 | 42 秒**不是数据量必然成本**，而是 `Scope.Cond()` 的 `OR` 分支让 `idx_unified_logical_ts` 索引失效 ⇒ 扫描器逐行 Filter 掉 **2936 万行**才找到 1 行。同一查询去掉 OR 分支耗时 **10.1–16.7 ms**（差约 **2500–4200 倍**） | T |
| 5 | 前端 60s 特例**确属"用前端兜住后端设计问题"**，且**已被实测证伪为不够用**（60s < 110s）。且它同时在本页之外还有**第二处同款补丁**（数据源页下拉） | T + C |
| 6 | 16 个逻辑设备中 **13 个在 `/edge-device` 页不可达**（无存活实例，只剩软删实例），只能从这个页面看到 ⇒ "只在边缘设备页补齐"覆盖不了 | T |
| 7 | 告警页选「逻辑设备」时，设备下拉实际加载的是**边缘设备列表**（`edgeDeviceApi.getList()`）⇒ 类型与候选源不一致，用户会选到错误 ID 空间 | C（**待效果层验证**，见 §9） |
| 8 | **副标题承诺的"统一视图"：后端确有按 logical_device 取数的端点，但覆盖仅 2/16、只给最新值、且本页一次都没调用** | T + C（见 §1.3） |

---

## 1. 页面到底向用户承诺了什么，与后端真实能力是否一致

### 1.1 页面自述（承诺面）

| 承诺点 | 原文 | 锚点 |
|---|---|---|
| 页面标题 | `逻辑设备` | `LogicalDeviceList.vue:6` |
| 页面副标题 | `聚合边缘设备数据，实现统一视图和数据保留策略管理` | `LogicalDeviceList.vue:6` |
| 空态说明（同一句再述） | `逻辑设备用于聚合边缘设备数据，实现统一视图和数据保留策略管理。可前往边缘设备页面添加设备后再试。` | `LogicalDeviceList.vue:142-144` |
| 表头承诺 | 名称 / 类型 / **实例数（含已删）** / 数据量估算 / 最后数据时间 / 保留天数 / 操作 | `LogicalDeviceList.vue:79-116` |
| 页级主操作 | **无**（作者显式裁决："无页级主操作时不开 #extra —— 合并/刷新是列表操作"） | `LogicalDeviceList.vue:3-4` |

**判读**：副标题承诺两件事 —— ①"统一视图" ②"数据保留策略管理"。

### 1.2 后端真实能力 vs 页面承诺

| 能力 | 后端是否有 | 前端是否暴露 | 证据 |
|---|---|---|---|
| 查询列表（分页/搜索/类型筛选） | ✅ `GET /logical-devices` | ✅ | C: `handler_logical_device.go:40-100`；`logicalDevice.ts:92-121` |
| **创建** | ❌ **无 POST 创建端点** | ❌ | C: 路由注册全集只有 5 条，见 `handler_logical_device.go:17-25`；副作用创建见 §1.3 末段 |
| **删除** | ❌ **无 DELETE 端点** | ❌ | C: 同上；删除能力挂在**边缘设备**上（`handler_edge_device.go:1088`） |
| 改名称 | ✅ `PUT /:id` | ✅ 编辑弹窗 | C: `handler_logical_device.go:181-226`；`:298-300` |
| 改保留期 | ✅ `PUT /:id` | ✅ 编辑弹窗（含调小二次确认） | C: `handler_logical_device.go:208-214`；`LogicalDeviceList.vue:662-679` |
| 合并（多源→1） | ✅ `POST /merge` + `/merge/preview` | ✅ 完整向导 | C: `handler_logical_device.go:105-158` |
| 合并进度 | ✅ `GET /merge-jobs/:id` | ✅ 轮询 | C: `handler_logical_device.go:161-177` |
| **看测点数据/历史曲线** | ⚠️ **部分有，且分裂成两套寻址**：① `/unified-data/historical*` 按**边缘设备** `device_pk` 寻址；② `/data-sources/overview?logical_device_id=` **确按逻辑设备寻址**（见 §1.3） | ❌ **本页无任何数据查看入口** | C: `handler_data.go:270-358`（`device_pk`）；`handler_source_overview.go:154`（`logical_device_id`）；本页无相关 import |
| **详情页** | — | ❌ **无 `/logical-device/:id` 路由** | C: `router/index.ts:78-82` 只有列表路由；`ls views/logical-device/` 仅 1 个 .vue |

**结论（C）**：页面副标题承诺的"统一视图"在本页**没有落地** —— 用户在本页看不到任何一个测点数值，只能看到 `row_estimate`（行数估算）与 `last_data_at`（最后一刻时间）。所谓"统一视图"在 UI 上只剩两列数字摘要。"数据保留策略管理"倒是真实（改 retention_days）。**但请注意：后端并非没有按逻辑设备看数据的能力**（§1.3），只是这个能力**没有接到本页**，也**没有覆盖到多数逻辑设备**。

> 反证线索：`MainLayout.vue:807` 里预置了面包屑标签 `'logical-device': '逻辑设备详情'`，但**没有任何路由会产生这个面包屑**（`/logical-device/:id` 不存在）。这是"曾计划有详情页、最终没实现"的残留证据。

### 1.3 【Lead 指定问题】"聚合边缘设备数据，实现统一视图"在后端有无对应能力？

> Lead 问：这个页面对用户承诺的『聚合边缘设备数据，实现统一视图』（PageHeader 副标题）在后端是否有对应能力（**有没有按 logical_device 看数据的入口**）？

**答：有，但"有"和"能用"是两件事。分三层回答，每层都给可复跑命令。**

#### 层 1 —— 后端确实存在"按逻辑设备看数据"的入口（与我最初的判断相反，此处更正）

`GET /api/v1/data-sources/overview?logical_device_id=<id>` **接受逻辑设备维度**：

- 代码：C: `handler_source_overview.go:154-156` —— `if ld := c.Query("logical_device_id"); ld != "" { q = q.Where("device_id = ?", ld) }`（`data_sources.device_id` 的语义 = `LogicalDevice.ID`，D: `docs/设计/数据源主备与故障转移.md:86`）
- 响应形状：每行 = `{logical_device_id, logical_device_name, category, label_zh, value_type, unit, latest_value, updated_at, age_sec, status}`（C: `handler_source_overview.go:41-56`）—— **这确实是"统一视图"的雏形：逻辑设备 + 数据类别 + 实时值 + 新鲜度。**
- **实测可用且很快**（T，2026-10-23T13:26Z）：

```bash
curl -s -H "Authorization: Bearer $JWT" \
  "http://$PROD_HOST:$PROD_WEB_PORT/api/v1/data-sources/overview?logical_device_id=14"
=> http=200  t=0.0148 s（14.8 毫秒）
=> {"logical_device_id":14,"logical_device_name":"bms","category":"cell_voltage_1",
    "label_zh":"第 1 节电芯电压","unit":"V","latest_value":3.314,"age_sec":0,"status":"ok"}
```

**注意这个反差**：同样由逻辑设备 14 供数，`/data-sources/overview` 用 **14.8 ms** 返回实时读数，而 `/logical-devices` 仅为了拿到"最后数据时间"一列就要 **110 秒**。**同一个系统里，同一个设备的两种读法差三个数量级。**

#### 层 2 —— 但这个入口的覆盖面和语义都不足以兑现"统一视图"

| 缺口 | 实测 | 锚点 |
|---|---|---|
| **覆盖面**：只覆盖 2/16 个逻辑设备 | `data_sources` 表 71 行，涉及 `distinct device_id` = **2**；而 `logical_devices` = **16** ⇒ **14/16 的逻辑设备在此端点下无任何字段** | T: 生产 SQL，2026-10-23T13:27Z |
| 它是 **data_sources 配置台账的投影**，不是数据直读 | 表被 `Where("status <> disabled")` 过滤；没有 `data_sources` 行的设备**永远不会出现**（哪怕 `unified_data` 里有百万行） | C: `handler_source_overview.go:148-150` |
| 只给 **latest_value**（最新一点），无历史 | 端点无时间窗参数 | C: `handler_source_overview.go:140-165` |
| **历史曲线走的是另一套寻址（边缘设备）** | `/unified-data/historical{,-batch}` 只认 `device_pk`（= EdgeDevice.ID） | C: `handler_data.go:270-271`、`:357-358`；前端 `unifiedData.ts:54` |

**这是本次审计最关键的一处"能力分裂"（C）**：
同一件"看数据"的事，**实时值按逻辑设备寻址，历史曲线按边缘设备寻址**。而 16 个逻辑设备里 **13 个没有任何存活边缘设备**（T: §6.2 的 SQL）。⇒ **对这 13 个身份，"统一视图"在物理上无法完成**：实时值端点没数据（不在 data_sources），历史端点没有可用的 `device_pk`（实例已软删）。

#### 层 3 —— 该能力本页完全没有接，而且也没接到任何能被用户发现的地方

| 事实 | 锚点 |
|---|---|
| `logical-device` 页面**没有任何** `data-sources/overview` 的调用（无 import、无请求） | C: `grep -n 'overview' LogicalDeviceList.vue` → 0 hit |
| 唯一实现该端点的页面是**数据源页的「来源总览」Tab** | C: `views/data-source/OverviewTab.vue`；`api/sourceOverview.ts` |
| 而该 Tab 在导航上叫「数据源管理」，**与"看逻辑设备数据"没有语义关联**；切到该 Tab 需要用 `?tab=` 且不按逻辑设备预筛 | C: `menuModel.ts:56`（"数据源管理"）；`DataSourcePage.vue` Tab 结构 |

#### 对 Lead 问题的直接结论

> **后端"有"按 logical_device 看数据的入口（`/data-sources/overview?logical_device_id=`，14.8ms，实测 200），但：**
> 1. **覆盖 2/16** —— 14 个逻辑设备在该入口下**完全无数据**（因为它们不在 `data_sources` 台账里）；
> 2. **只给最新值、不给历史**，历史必须换到"边缘设备"寻址，而那 13 个身份没有存活实例；
> 3. **`/logical-device` 页面一次都没调用它**，用户在这个"统一视图"页面上看不到任何读数。
>
> ⇒ **副标题承诺的"统一视图"，后端只有一块碎片（实时值、2/16 覆盖、无历史），且这块碎片被放在了错误的页面上。** 这不是"需求不存在"，而是**能力已存在但没组装**：把 `data-sources/overview?logical_device_id=` 接进本页，成本极低（14.8ms vs 110s），是本报告给出的**最高性价比修复**（详见 §6.3 的建议方向）。

---

## 2. 导航与发现性 + 全仓引用点清单（含"裸 ID"展示）

### 2.1 分母声明（可复核）

```bash
# 分母：frontend-shared/src 下所有非测试源码中命中逻辑设备的文件
cd frontend-shared/src && grep -rln 'logical' --include=*.vue --include=*.ts . | grep -v __tests__
```
命中 **17 个文件**（2026-10-23 21:00 采样）。其中真正**面向用户渲染**的引用点见下表。

### 2.2 导航入口与面包屑

| 项 | 事实 | 锚点 |
|---|---|---|
| 侧栏菜单 | `{ path: '/logical-device', title: '逻辑设备' }`，挂在分组「设备与通道」 | C: `menuModel.ts:43`、`:92` |
| 路由 | `meta: { title: '逻辑设备', icon: 'Share' }` | C: `router/index.ts:78-82` |
| 面包屑一级 | `'logical-device': '逻辑设备'` | C: `MainLayout.vue:791` |
| 面包屑二级 | `'logical-device': '逻辑设备详情'` —— **死代码**（无详情路由） | C: `MainLayout.vue:807` |

### 2.3 通知深链（跨域强耦合点）

| 通知 source | 点击后跳转 | 锚点 | 问题 |
|---|---|---|---|
| `merge_failed` | `/logical-device`（**丢失 merge_job id**） | C: `MainLayout.vue:908-910` | 通知体说"请在逻辑设备管理页查看并重试合并"（C: `migrate.go:390-395`），但**页面既不能按 job 定位、也没有重试入口**（全仓无 retry 调用点，见 §6.3） |
| `retention_expiring` | `/logical-device?retention=<id>` 打开编辑弹窗 | C: `MainLayout.vue:912-915`；页内处理 `LogicalDeviceList.vue:698-711` | **只在目标恰好位于当前页时生效**：`items.value.find(i => i.id === id)`（`:705`）只在**第 1 页 20 条**里找；目标若不在当前页/被筛选掉 ⇒ **静默无反应** |

**深链静默失效是可证伪的**：`handleRetentionDeepLink` 在 `find` 未命中时**没有任何 else 分支**（`LogicalDeviceList.vue:705-706`），也不提示、不翻页、不加筛选。生产现有 16 条恰好 < 20，所以当前**不会**触发；一旦超过 20 条即失效。这是"当前数据规模掩盖的缺陷"。

### 2.4 其它业务域引用点（**含裸 ID 展示**）

| # | 位置 | 用户看到什么 | 裸 ID | 锚点 |
|---|---|---|---|---|
| 1 | 数据源页表格 | 列名「逻辑设备」，值 = `device_id` 数字 | ✅ **裸数字** | C: `DataSourceList.vue:110-112` |
| 2 | 数据源页表单 | 「逻辑设备 ID」数字输入框 | ✅ **裸数字** | C: `DataSourceList.vue:244-251` |
| 3 | 数据源页详情抽屉 | 「逻辑设备」= `detailSource.device_id` | ✅ **裸数字** | C: `DataSourceList.vue:301` |
| 4 | 数据源页筛选 | 下拉（有名称，失败时降级为 `逻辑设备 #N`） | 部分 | C: `DataSourceList.vue:52-63` |
| 5 | 数据来源·字段目录 | 「逻辑设备」= 名称 + `#id` | 名称+ID 并存 | C: `OverviewTab.vue:43-47` |
| 6 | 告警规则列表 | `逻辑设备 #<target_id>` | ✅ **裸数字** | C: `AlertRules.vue:474` |
| 7 | 告警规则表单 | 目标设备下拉 = **边缘设备列表** | — | C: `AlertRules.vue:308-311` + `:777` |
| 8 | 删除设备弹窗 | 「逻辑设备信息」区（名称/实例数/数据量/保留天） | 名称 | C: `DeviceDeleteDialog.vue:36-51` |
| 9 | 批量删除弹窗 | 「其中 N 台属于逻辑设备（数据默认保留）」 | 只计数 | C: `DeviceBatchDeleteDialog.vue:13-15` |
| 10 | 边缘设备创建向导 | 步骤 0「历史数据继承」候选卡片（名称+#id） | 名称+ID | C: `EdgeDeviceList.vue:322-340`；`LogicalDeviceCandidateSelect.vue:55-70` |
| 11 | 合并冲突弹窗 | 「逻辑设备「name」（#id）」+ 存活实例跳转 | 名称+ID | C: `LogicalDeviceList.vue:257-264` |
| 12 | 合并进度弹窗 | `源 #N → 目标 #N` | ✅ **纯裸 ID** | C: `LogicalDeviceList.vue:277` |

**小结（C）**：上表共 **12 处**用户可见引用点，其中 **5 处直接向用户展示无名称的裸 ID**（#1 数据源表格、#2 数据源表单、#3 数据源详情、#6 告警列表、#12 合并进度）。这 5 处的共同点：**用户必须自己知道逻辑设备的数字主键**才能完成这些操作。

> **闭环断裂（C）**：`/logical-device` 的表格**没有 "ID" 列**（C: `LogicalDeviceList.vue:79-116`，共 7 列：名称/类型/实例数/数据量/最后数据时间/保留天数/操作，无主键列）。也就是说：用户在"唯一的管理页"上**看不到主键**，却被数据源页（要求**输入**主键）、告警页（展示 `#N`）、合并进度（展示 `#N`）**要求使用主键**。这是可复核的信息架构断点 —— 除非用户去查数据库，否则无法把"数据源页里的 14"对应回本页的某一行。

---

## 3. 同一物理事实的多套口径

### 3.1 三套"状态/时间"口径并列

| 口径 | 数据来源 | 用户在哪看到 | 语义 | 锚点 |
|---|---|---|---|---|
| 边缘设备状态 | `edge_devices.status` | 边缘设备页「状态」列 | 在线/等待数据/初始化中/警告/故障/已禁用/离线 | C: `EdgeDeviceList.vue:168-177` + 映射 `:1430-1443` |
| 逻辑设备最后数据时间 | `MAX(timestamp)` over scope | 逻辑设备页「最后数据时间」列 | 该逻辑身份**名下所有实例（含已删）**的最后一行数据时刻 | C: `handler_logical_device.go:91-96`；`preview.go:156-172` |
| 来源组健康 | `source_group_health` 事件表 | 数据源页「健康记录」时间线 | 主备切换事件 | C: `api/sourceGroup.ts:120-128`；`DataSourceList.vue:320-331` |

**这三套口径都在回答"这台设备现在好不好"，但答案可以互相矛盾**（C）：一台边缘设备可以 `status=offline`（当前没在传数），同时其逻辑设备 `last_data_at` 显示 3 分钟前（因为它名下**另一台已软删的实例**曾传过数，而 scope 包含软删实例 —— `scope.go:71-77` 显式 `Unscoped()`）。用户没有任何提示能理解"为什么离线设备写着刚有数据"。

### 3.2 「实例数（含已删）」语义 —— 生产实测确实反直觉

**分母**：生产 16 个逻辑设备（`count(*) from logical_devices`，2026-10-23T12:55Z）。

| 观测 | 数字 | 命令 |
|---|---|---|
| 实例数**含已删**（页面所显示） | **6 台显示 `0`；10 台显示 `1`**（合计 16） | `CountInstances` 用 `db.Unscoped()`（C: `identity.go:237-246`） |
| **实际存活实例数** | **只有 3 台真有存活实例**；显示 `1` 的 10 台里 **7 台的唯一实例已软删**（`alive=0`） | 见下方 SQL |

```sql
-- 生产只读，2026-10-23T12:56Z
select l.id, l.name, count(e.id) inst_total,
       count(e.id) filter (where e.deleted_at is null) alive
from logical_devices l left join edge_devices e on e.logical_device_id=l.id
group by 1,2 order by 1;
=> 1..6: 0|0  7..13: 1|0  14,15,16: 1|1
```

**这意味着**：页面上 **10 台设备显示"实例数 1"，其中 7 台实际一个存活实例都没有**（准确的：id 7/8/9/10/11/12/13 → `inst_total=1, alive=0`；仅 id 14/15/16 → `alive=1`）。用户看到"1 个实例"会以为设备在跑；实际是 0。表头虽写了"（含已删）"，但这个括注**无法让用户从"1"推出"0 个在跑"**。

> 这正是 v3.3 的语义选择（保留历史身份），但在 UI 上**没有区分"1 个已删" vs "1 个存活"** —— 一个 `instance_count` 数字承担了两种事实。

---

## 4. 重复/含混概念：用户是否被迫理解内部模型

### 4.1 六个概念在 UI 上的实际出现

| 概念 | 用户可见名称 | 用户在哪碰到它 | 是否解释过 |
|---|---|---|---|
| 逻辑设备 | 「逻辑设备」 | 菜单、本页、数据源页「逻辑设备 ID」、告警「逻辑设备 #N」 | 仅本页副标题一句（`:6`） |
| 边缘设备 | 「边缘设备」 | 菜单、边缘设备页 | 较清楚 |
| 设备配置 | 「配置模板」 | 菜单 | **与术语表冲突**（D: 术语表要求"设备配置"） |
| 数据源 | 「数据源」（菜单叫"数据源管理"） | 数据源页 | 页头与菜单不一致 |
| 来源组 | 数据源页第二个 Tab | — | 曾经是独立菜单，现并入 |
| 数据身份 | —— | **UI 上从不出现** | 只在术语表 §2.5 有（D） |

**判读（C）**：用户**没有被要求理解"数据身份"这个词**，但**被要求承担它的后果** —— 即"为什么删了设备数据还在"、"为什么两个设备要合并"。本页是唯一解释入口，而解释只有副标题那 27 个字。

### 4.2 术语表自身与 UI 的现状偏差（自行复核，未引用 2026-09-23 旧结论）

对照 `docs/设计/术语表.md`（D）与本日现状（C）：

| 术语表要求 | 现状 | 结论 |
|---|---|---|
| §4 禁止「设备类型」，替代「设备型号」 | 逻辑设备页表头仍是「**类型**」（`LogicalDeviceList.vue:87`），边缘设备页也是「类型」（`EdgeDeviceList.vue:153`） | **仍违反**（这是新复核，非引用旧文档） |
| §4 禁止单独使用「设备」 | 告警页选项文案「边缘 / 逻辑设备」（`AlertRules.vue:285`）中"设备"是复合词，可接受 | 通过 |
| §2.5 逻辑设备 = 数据身份 | UI 无「数据身份」字样，只有「逻辑设备」 | 未落地到文案 |

---

## 5. 该页真实使用频率 —— **这一节是本次审计对 Lead 最有价值的新证据**

### 5.1 生产有可用的真实计数器（不需要日志）

生产 `GET /metrics` **无需认证**即可读（C: `routes.go:112-116`，作者注释明确"canonical unauthenticated scrape target"），其中 `ehome_http_requests_total{path=...}` 是全进程累计计数器。

```bash
curl -s "http://$PROD_HOST:$PROD_WEB_PORT/metrics" | grep ehome_http_requests_total
```

**采样时刻** 2026-10-23T12:54Z（进程启动 2026-10-23T11:59:32Z，即**累计窗口 55 分钟**）：

| 端点 | 计数（55 分钟内） |
|---|---|
| `GET /api/v1/logical-devices` | **9**（含我自己的探测；干净窗口测量见 5.2） |
| `GET /api/v1/edge-devices` | 4 |
| `GET /api/v1/nodes` | 1 |
| `GET /api/v1/notifications` | 8 |
| `GET /health` | 221（探活，非用户） |

**安静窗口增量测量**（无我发起的请求，2026-10-23T13:15:34Z → 13:18:34Z，180 秒）：

```
quiet_window logical_devices counter: T0=72 -> T1=77  delta=5
```

⇒ **3 分钟 5 次**。这**不是**用户点击 5 次：`fetchList` 一次进页只打 1 次，但`fetchList` 会被 `filterReady` watch、分页、轮询结束时各调一次，且**超时后用户重试**也会重复计数。**口径限制**：计数器分不清"首屏加载"与"重试/翻页"，因此 5 次/3min **不能**换算成"会话数"。

**可确证的部分（T）**：该端点在 55 分钟里被调用 **9 次（含探测）**，在无探测的 3 分钟里 **5 次**。**单用户系统**（`select count(*) from users` = **1**，2026-10-23T13:10Z），所以这 5 次全部来自同一操作者。

> **诚实标注**：我**没能**取到"页面 PV/UV"这种口径 —— 前端是 SPA，nginx 里只有 `/assets/*` 静态请求，无路由级访问日志（本机 nginx 日志 grep `logical-devices` = **0**，因为本机流量走 `ehome-web:8080` 而非 nginx）。**该页真实 PV 未取到**，只有 API 调用次数。

### 5.2 这条证据怎么用 —— 请 Lead 注意它的两面性

- **支持"页面没必要"**：单用户、55 分钟 9 次调用 ⇒ 低频。
- **反对"页面没必要"**：这 9 次里绝大部分**注定失败**（42–110s > 60s 兜底）。**调用次数低可能正是"被延迟劝退"的结果**，不是"需求不存在"的证据。这是**幸存者偏差**，不能单独作为删除依据。

---

## 6. 产品层判断题（任务额外要求）

### 6.0 先回答"前端 60s 是不是兜住后端设计问题" —— **是，且证据链完整**

**证据链（按时间顺序）**：

1. **问题出现**（2026-10-21）：`git show 4c715ea8` 的 P0 描述原文 —— "逻辑设备 16 台全不显示且断言「还没有边缘设备」：接口 42.4s > 全局 10s 超时被当空态"。
2. **当时的修法**（同一提交）：不在后端提速，而在前端加**单请求**超时 ——
   ```
   +const LOGICAL_DEVICE_LIST_TIMEOUT = 60000
   +// per-request timeout：只放宽这一个请求，不改 api/client.ts 的全局 10s
   ```
   （C: `LogicalDeviceList.vue:356`、`api/logicalDevice.ts:100-102`；全局仍 10s，C: `client.ts:46`）
3. **同一补丁被复制到第二处**：数据源页的逻辑设备下拉也有 `DEVICE_OPTIONS_TIMEOUT = 60000`（C: `DataSourceList.vue:556`），注释理由完全相同（"只放宽本请求，不动全局 10s"）。
4. **作者自己写下了这是权宜**：`api/logicalDevice.ts:102` —— **"调用方传 60000 兜住；后端提速后此参数可删。"**
5. **后端从未提速**：自 2026-10-21 至今，`handler_logical_device.go` 的列表富化段**无任何 perf 提交**（`git log --oneline -- backend/internal/api/handler_logical_device.go` 最近 3 条依次是 e2e 测试 / 分页 / 初始实现；无 perf 类）。
6. **而且 60s 现在也不够**（**这是本次审计的实测新证据，推翻"兜住了"的假设**）：

| 参数 | 实测耗时 | 结果 |
|---|---|---|
| `page_size=1` ×3 | 0.021s / 0.008s / 0.008s | 200 |
| `page_size=4` ×3 | 0.051s / 0.026s / 0.028s | 200 |
| **`page_size=16`** ×3 | **100.5s / 104.8s / 130.8s** | 200（全部 > 60s） |
| **`page_size=20`（**前端默认值**）** | **110.6s** | 200 |

采样 2026-10-23T12:59Z–13:15Z，`curl -w %{time_total}`，生产 18090 端口。
⇒ **前端默认 `pageSize=20`（C: `LogicalDeviceList.vue:363`）必然在 60s 处超时**。这个特例补丁**已经被数据规模追上并失效**。

> **这就是"用前端兜住后端设计问题"的定义级证据**：症状（超时）在前端被掩盖 → 根因（后端每设备串行富化 + OR 索引失效）未修 → 数据继续增长 → 补丁再次失效。前端从"兜底"变成了"掩盖"。

### 6.1 如果删掉这个页面，用户损失什么？—— 逐项列举

**前置事实**：页面的用户可触达能力只有 3 项（§1.2）。逐项检查其它入口是否存在：

| 能力 | 别处能触达吗 | 证据 | 删除后的损失 |
|---|---|---|---|
| **看列表**（名称/类型/实例数/数据量/最后数据/保留期） | ❌ 部分：`/edge-device` 页无任何逻辑设备列（`EdgeDeviceList.vue:150-203`，11 列无 logical）；只在**删除弹窗**里能看单台（`DeviceDeleteDialog.vue:36-51`） | C | **完整损失** —— 16 台逻辑设备里 **13 台**在任何其它列表页**不可见**（见 6.2 实测） |
| **合并**（2+ 同类型 → 1） | ❌ 无任何其它入口（全仓 `logicalApi.merge(` 调用点 = **1**，仅本页） | C: `grep -rn 'logicalDeviceApi.merge('` → 1 hit | **完整损失**：v3.3 的核心能力消失 |
| **改保留期** | ❌ 无任何其它入口（全仓 `logicalApi.update` 调用点 = **1**，仅本页） | C: 同上，1 hit | **完整损失**：retention 从"可管理"退化为"只能改环境变量重启" |
| 改名称 | ❌ 同上 | C | 完整损失 |
| 看测点数据 | 本来就没有 | — | 无损失 |
| 创建/删除逻辑设备 | 本来就没有（由边缘设备创建/删除副作用驱动） | C: `handler_edge_device_lifecycle.go:93`、`handler_edge_device.go:1088` | 无损失 |

### 6.2 「只在边缘设备页 + 数据页补齐」能否覆盖需求？—— **不能，两个硬缺口**

**缺口 A：13/16 的逻辑设备在边缘设备页根本不可达**

```sql
-- 生产只读，2026-10-23T13:10Z
with ld as (select id from logical_devices),
     alive as (select logical_device_id, count(*) c from edge_devices
               where deleted_at is null group by 1)
select (select count(*) from ld) total_logical,
       (select count(*) from ld join alive on alive.logical_device_id=ld.id) reachable,
       (select count(*) from ld left join alive on alive.logical_device_id=ld.id where alive.c is null) unreachable;
=> total_logical=16 | reachable=3 | unreachable=13
```

边缘设备页只列存活设备（`GET /edge-devices` 走软删过滤；生产 `deleted_at is null` = **3** 台，`deleted_at is not null` = 7 台）。**13 个逻辑设备没有存活实例**，所以在边缘设备页**看不到任何入口**。要在边缘设备页补齐，必须先造出"已删设备的列表"，那是另一个页面。

**缺口 B：合并的语义无法在边缘设备页表达**

合并是"把 N 个**逻辑身份**合成 1 个"，前提是**这些身份当前都没有存活实例**（C: `merge.go:141-163`，`MergeConflictAliveInstance` 直接 409）。也就是说：**合并恰恰发生在"设备都已删除/换新"之后**。而边缘设备页是"存活设备"的页面 —— 在它上面做合并，等于要求用户在"没有设备"的地方操作"设备的合并"。生产现有 16 个身份里 13 个处于这种状态，正是合并的主战场。

**数据页能否覆盖？** 数据页（`DataPanel.vue`）按**边缘设备 ID** 取数（`unifiedData.ts:54` 的 `device_pk`，`DataPanel.vue:757` 用 `edgeDeviceStore` 列表），**完全没有逻辑设备维度**。它甚至把 `logical_device_id` 列入"元数据数字，不得当读数渲染"的黑名单（C: `DataPanel.vue:454-458`）—— 这是正确的防止误解，但也说明**数据页不承担逻辑设备语义**。

### 6.3 结论（产品判断）

> **删掉 `/logical-device` 会损失三样东西，且三样都无法用"边缘设备页 + 数据页补齐"替代：**
> 1. **16 个逻辑身份里 13 个的唯一可见入口**（T：13/16 不可达，SQL 可复跑）；
> 2. **v3.3 合并能力**（C：全仓唯一调用点）—— 而合并恰好只能在"无存活实例"时进行，"补在边缘设备页"在语义上自相矛盾；
> 3. **retention 的用户侧可管理性**（C：全仓唯一调用点）—— 改保留期是**会硬删数据**的破坏性操作，靠环境变量无法按设备差异化。
>
> **但更该裁决的不是"删不删"，而是"留着它是否名不副实"**：副标题承诺的"统一视图"没有落地（无测点数据、无详情页、无 ID 列），实际能力只有"合并 + 改保留期"两件事撑起一整个一级菜单。**建议方向**：把页面**降级为"数据身份管理"**（名称/保留期/合并），明确去掉"统一视图"的承诺；同时把"看数据"的深链导向 `/data?logical_device=<id>`（需后端补按逻辑身份取数的端点）。

---

## 7. 42 秒的根因（供 Lead 与 T4 交叉验证）

### 7.1 决定性测量：OR 分支让索引失效

**分母**：仅在**最重分区** `unified_data_202610`（`logical_device_id=14` 单身份 27,757,064 行；`=13` 单身份 14,224 行）。
**口径**：`ORDER BY timestamp DESC LIMIT 1`（即 `ScopeTimeRange` 的 last 边界，C: `preview.go:156-172`）。
**时刻**：2026-10-23T13:18–13:22Z。

| 查询形态 | 执行计划 | 耗时 | 说明 |
|---|---|---|---|
| **A) 纯逻辑分支** `WHERE logical_device_id IN (13)` | `Index Only Scan using unified_data_202610_logical_device_id_timestamp_idx`，rows 1 | **16.7 / 10.2 / 10.1 ms** | 索引直达 |
| **B) 实际发出的 OR 形态** `WHERE (logical_device_id IN (13) OR (logical_device_id IS NULL AND device_id IN (13)))` | `Index Scan Backward using unified_data_202610_timestamp_idx` + **Filter**；**Rows Removed by Filter: 29,367,582**；Buffers: hit=1173214 read=277098 | **41.9 s / 35.7 s** | **扫 2936 万行只为找 1 行** |
| C) `device_data` 同款项 | — | **7.3 s** | 第二张表同病 |

⇒ **差 2500 倍以上**（41.9s vs 10.1ms ≈ 4100 倍；35.7s vs 16.7ms ≈ 2100 倍）。根因是 `Scope.Cond()` 的 third branch（C: `scope.go:49-52`）：

```go
return "(logical_device_id IN ? OR (logical_device_id IS NULL AND device_id IN ?))", ...
```

这个 OR 让 PG 无法用 `logical_device_id` 做索引条件，退化为"沿 timestamp 索引倒序扫，逐行 Filter 直到命中"。**对"该身份数据很旧"的设备（生产 id 12/13 停在 10-05/10-06），必须先扫掉 2026-10 分区里所有更新的行** —— 而那个分区有 2936 万行新数据在 id 14/15 名下。

**这解释了 §2 里那个反直觉的测量**：单个设备内部耗时 12 → 44s、13 → 52s，而 14/15 → 1.7s/10s（因为它们的数据就在分区尾部，几行内就命中）。**耗时与"该设备数据有多旧"成正比，与"这台设备有多少数据"无关** —— 这是设计缺陷，不是容量问题。

> **注意**：`history_points.go:19-25` 的作者**已经知道**过这个问题（"§1.6 … 否则 dataScopeCond 的 OR 条件会把 logical_device_id 降级为 Filter, 复合索引完全用不上"），并用 `scope_union.go` 的 `UNION ALL` 两分支绕开。**但 `ScopeTimeRange`（列表富化用）没有采用同一个绕法** —— 同一代码库里两种写法并存，说明这是**遗漏而非取舍**。

### 7.2 与前端 60s 特例的关系（承接 §6.0）

每设备串行调用 `CountInstances` + `EstimateRowCount`（3s 超时）+ `ScopeTimeRange`（两表 × 两个边界 = 4 次最坏查询）。20 台 × 单台最坏 ~50s ⇒ 远超 60s。**前端的 60s 不是"保险"，是"把必然超时推迟到另一个必然超时"**。

---

## 8. 我认为最需要 Lead 裁决的矛盾点

### 矛盾 1：「页面名不副实」vs「它是唯一入口」

- **A 面**：副标题承诺"聚合边缘设备数据，实现统一视图"，但**本页看不到任何一个测点值**、无详情页、无 ID 列；13/16 的身份悬空在边缘设备页之外。它不满足"统一视图"的承诺。
- **B 面**：它同时是**合并**与**改保留期**的全仓唯一入口（C：各 1 个调用点）。
- **裁决点**：该页应**按承诺补齐**（做真正的统一视图：测点数据 + 详情页 + ID 列），还是**按实际收缩承诺**（改名为"数据身份管理"，只保留合并/保留期）？**这两个方向的工程量差一个数量级，且第一个方向要先修 §7 的 OR 索引失效。**

### 矛盾 2：60s 补丁已失效，但现在改哪里？

- **A 面**：60s 特例（本页 + 数据源页下拉两处）已是**已知失效**（实测 110s）。最直接的做法是继续加大超时到 180s。
- **B 面**：加大超时只会让用户**盯着骨架屏 2 分钟**；而根因（`Scope.Cond()` 的 OR 形态，单查询 42s）有**同仓现成解法**（`scope_union.go` 的 UNION ALL，`history_points.go` 已在用）—— 且实测纯分支只需 **10 ms**。
- **裁决点**：把"前端超时"当**参数问题**（继续调大）还是**信号问题**（说明后端该改）？我倾向后者，因为 **10ms vs 42s 的 4200 倍差距不可能是"调参"能解释的**，而且 T4 的实测应该会独立复现同一结论。

---

## 9. 未取到 / 局限（诚实标注）

| 项 | 状态 | 原因 |
|---|---|---|
| 页面真实 PV/UV | **未取到** | SPA 无路由级访问日志；nginx 未代理本机 ehome；只有 API 调用计数（§5.1） |
| 生产用户行为日志 | **未取到** | 后端仅记录 Prometheus 计数，无用户 ID 维度；系统只有 1 个用户 |
| 告警页「逻辑设备」下拉错配的**效果层**验证 | **未做** | 需在浏览器实际创建一条 `target_type=logical_device` 规则；我对生产只做只读操作。**这是 C 级推断，可被推翻**：若后端在某处把 `target_id` 语义纠正，则误判不成立 |
| 合并/保留期功能的实际使用次数 | **未取到为 0** | 生产 `merge_jobs` = **0** 行、`notifications` = **0** 行（含 `retention_expiring`=0、`merge_failed`=0）；`logical_devices` 里 `updated_at<>created_at` = **0** ⇒ **历史上从未有人改过保留期、从未发起过合并、从未产生过 retention 通知** |

> **最后一行是本次审计最值得 Lead 注意的负面证据**：`/logical-device` 唯二的真实能力（合并、改保留期）在生产库里**零使用痕迹**（`merge_jobs`=0、`updated_at=created_at` 全 16 行）。**这不能证明"不需要"**（可能是超时导致用不了），但足以说明：**当前页面在生产中扮演的角色是"只读列表"，而它连这个角色都因 42–110s 延迟而做不到。**

---

## 10. 复跑清单（每条命令都可直接执行）

```bash
# 1) 页面承诺（C）
sed -n '1,10p;79,150p' frontend-shared/src/views/logical-device/LogicalDeviceList.vue

# 2) 全仓唯一调用点（C）—— 返回各 1 行
grep -rn 'logicalDeviceApi.(merge|update|mergePreview)' frontend-shared/src --include=*.vue --include=*.ts | grep -v __tests__

# 3) 60s 特例两处（C）
grep -rn '60000|LOGICAL_DEVICE_LIST_TIMEOUT|DEVICE_OPTIONS_TIMEOUT' frontend-shared/src/api/logicalDevice.ts frontend-shared/src/views/data-source/DataSourceList.vue frontend-shared/src/views/logical-device/LogicalDeviceList.vue

# 4) 裸 ID 展示点（C）
grep -n '逻辑设备 ID|device_id|target_id' frontend-shared/src/views/data-source/DataSourceList.vue frontend-shared/src/views/alert/AlertRules.vue

# 5) 生产计数器（T，无需认证）
. .secrets/prod.env; curl -s "http://$PROD_HOST:$PROD_WEB_PORT/metrics" | grep 'logical-devices'

# 6) 13/16 不可达（T）
# 见 §6.2 的 SQL（只读）

# 7) OR 索引失效（T）
# 见 §7.1 的 EXPLAIN (ANALYZE, BUFFERS)（只读）

# 8) 零使用痕迹（T）
# select (select count(*) from merge_jobs),
#        (select count(*) from notifications),
#        (select count(*) from logical_devices where updated_at<>created_at);
# => 0|0|0
```