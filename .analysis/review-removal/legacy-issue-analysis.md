# H 审核与深度分析：data_sources 手工端点 vs 自动投影的语义冲突

> **审核者**: req-analyst（task-12）
> **立场**: 独立复核。Lead 给的三条现象我**自己重跑了一遍**，结论有确认、有修正、也有两条 Lead 未提到的发现。
> **基线**: 工作区 HEAD `c76744d3`（2026-10-23 20:53）+ 未提交的移除改动（180 files, +4892/−18095）
> **环境**: 服务 `ehome-verify-srv`（容器内 8080，DB=`ehome_verify`）；PG `ehome-postgres`
> **时刻**: 2026-10-24 10:25–11:08 +0800（服务端时间为 UTC）
> **纪律**: 全程只读代码 + 只写 `ehome_verify`；未动 `ehome` 库、未动生产、未 commit

---

## 0. 结论（先给判断）

**这个冲突是真实的，但比 Lead 描述的更窄、也更清楚：**

1. **它是一个「死端点」问题，不是一个「数据被破坏」问题。**
   `data_sources` 在移除后**没有任何数据面消费者**（grep 证明见 §5）。它唯一真实的前端使用是
   `GET /data-sources/overview`（字段目录，只读）。所以手工切主备**即使成功也没有任何效果**可言。

2. **真正的语义失效点是 4 个写端点 + 1 个不可达状态**，根因不是投影器缺守卫，而是
   **唯一索引从三元组缩为二元组**（`uniq_ds_source` 3 列 → 2 列）使「同组多行 standby 候选」在
   **数据模型层不可能存在**。这是**本次移除引入的**（M2 时不可能）。

3. **`managed_by` 的设计缺口是真的，但它的严重性被我实测下调了**：
   Lead 说「手工改 status 会被覆盖」——**确认**；
   但 Lead 未说的是：**`name`/`priority`/`description`/`config`/`max_fail_count`/`is_primary` 改完不会被覆盖，会一直留着**。
   这产生一个**新的不一致**：`is_primary=false` + `status=active` 可以并存并被 (`overview` 之外) 读到。

4. **不阻塞发布**（理由见 §7），但**强烈建议删除这 4 个写端点 + 3 个不可达死分支**，因为
   它们现在是「看起来能用、实际永远失败、且会误导后来者」的陷阱，且测试还在为它们背书。

**对 Lead 现象描述的修正**（重要，见 §2.5）：
- 现象 2 的 409 文案，Lead 记作 `no healthy standby candidate`；**实际是**
  `no healthy standby candidate for (edge_device_id=N, category=X); specify a successor first`（`service.go:553`）。
  语义无差，但**这个 409 存在的前提**（同组有 standby 候选）在二元组唯一索引下**恒不成立**——
  即它是**100% 必失败**，不是"有时失败"。
- Lead 说「`Reset` 要求 `status='error'`，而 `StatusError` 无任何写入者」——**确认**，
  但需补充：**`fail_count` 在 data_sources 上也无人写入**，所以不是"缺一个写入者"，而是**整条熔断链路已死**。

---

## 1. 环境校正（先自证探针，避免"打错目标"）

### 1.1 主机 8080 不是 verify 服务（这是我踩到的第一个坑）

```
$ docker ps --format '{{.Names}}	{{.Ports}}' | grep ehome
ehome-web	0.0.0.0:8080->8080/tcp, ...      <-- 宿主机 8080 是 ehome-web（旧镜像）
ehome-verify-srv	                          <-- 无端口映射，net=container:ehome-postgres
```

**若直接用 `curl 127.0.0.1:8080`，打到的是 ehome-web**，它会返回
`/api/v1/logical-devices 200 + 真实数据`——**看起来像"逻辑设备没被删"**。
实测证据（错误探针，**不可作为结论**）：

```
$ curl -s -H "Authorization: Bearer $TOK" http://127.0.0.1:8080/api/v1/logical-devices
{"code":200,"data":{"items":[{"id":1,"identity_key":"sn3001_rain:UART1",...}],"total":2}}
```

**正确的 verify 服务必须经 `docker exec ehome-verify-srv` 访问**（它与 postgres 共享 netns）。
更正后同一路径返回 404 —— 与源码一致（`routes.go` 已删 `registerLogicalDeviceRoutes`）。

```
$ docker exec -e http_proxy= ... ehome-verify-srv sh -c 'wget ... http://127.0.0.1:8080/api/v1/logical-devices'
HTTP/1.1 404 Not Found
```

### 1.2 容器内 wget 受 HTTP_PROXY 劫持，需显式清空

```
$ docker inspect ehome-verify-srv ... | grep -i proxy
HTTP_PROXY=http://192.168.20.6:10808
NO_PROXY=localhost,127.0.0.1,192.168.20.0/24
```

NO_PROXY 列了 127.0.0.1，但 busybox wget **仍走了代理**，导致所有路径（含 `/health`）返回
`503 Service Unavailable`。清空 proxy 后恢复正常：

```
$ docker exec -e http_proxy= -e https_proxy= -e HTTP_PROXY= -e HTTPS_PROXY= ehome-verify-srv \
    sh -c 'wget -q -O - --server-response http://127.0.0.1:8080/health'
HTTP/1.1 200 OK
{"commit":"unknown","status":"ok"}
```

**教训**：任何「所有端点都 503」的观测，先怀疑探针（代理/端口），不要当成产品缺陷。

### 1.3 ⚠ 环境存在**第二个投影写入者**（影响复现可解释性）

```
$ docker ps -a --format '{{.Names}}	{{.Status}}' | grep ehome
ehome-review-srv	alpine:3.20	Up About a minute      <-- 2026-10-24T02:40:49Z 启动
$ docker inspect ehome-review-srv --format '{{.Config.Env}}' | grep DB_NAME
EHOME_DB_NAME=ehome_verify          <-- 同一个库！
$ docker logs ehome-review-srv | head -3
Config: DB=127.0.0.1:5432/ehome_verify, API=:8081
```

⇒ `ehome-verify-srv`(8080) 与 `ehome-review-srv`(8081) **同时**对 `ehome_verify` 做 60s 对账。
两进程各自独立 ticker、互不知情。我的复现因此需要**注意观测窗口**（见 §2.4 的一次误判）。
**这本身也是一个真实部署风险**（多副本无选主，见 §6 附注），但不在本任务范围。

---

## 2. 三条现象逐条复现

### 现象 1：手工置 `status='disabled'` 被投影器改回 `active` —— **确认**

```
=== T0 2026-10-24T10:40:25+08:00 ===
$ psql ... -Atc "SELECT id,status,managed_by,updated_at FROM data_sources WHERE id=16;"
16|active|auto|2026-10-24 02:40:13.077409+00

$ psql ... -c "UPDATE data_sources SET status='disabled' WHERE id=16;"
UPDATE 1
16|disabled|auto|2026-10-24 02:40:13.077409+00

=== 每 15s 轮询 ===
t+ 15s  disabled | auto | 02:40:13
t+ 30s  active   | auto | 02:40:50.768835   <-- 被改回
t+ 45s  active   | auto | 02:40:50.768835
...
t+105s  active   | auto | 02:41:50.788533
```

**恢复窗口 ≈ 25–37s**（在 02:40:50 的 tick 被覆盖）。Lead 说"75 秒后变回"，方向正确；
精确值取决于 tick 相位，**不是固定 75s**。

**代码根因**（`backend/internal/datasource/projection.go:229-241`）：
状态不一致走 `updates` 分支，无条件写 `"status": status`，**不检查"当前值是否是人的干预"**。

### 现象 2：`POST /data-sources/:id/deactivate` 恒 409 —— **确认，且是 100% 必失败**

```
$ for id in 16 17 21; do POST /api/v1/data-sources/$id/deactivate; done
=== POST /data-sources/16/deactivate ===  HTTP/1.1 409 Conflict
=== POST /data-sources/17/deactivate ===  HTTP/1.1 409 Conflict
=== POST /data-sources/21/deactivate ===  HTTP/1.1 409 Conflict
```

对照 `activate` 成功（因为 target 已 active 会短路返回）：
```
POST /data-sources/16/activate  -> HTTP/1.1 200 OK  (status=active)
```

**409 文案原文**（`service.go:553`）：
```
no healthy standby candidate for (edge_device_id=%d, category=%s); specify a successor first
```
（Lead 记作 `no healthy standby candidate`，截断了后半句。语义无差。）

**为什么是 100% 必失败**：候选查询要求 `status=standby` 同键行（`service.go:548-551`），
而 `uniq_ds_source` 唯一索引**禁止**同键第二行存在（§3.1 实测）。⇒ **候选集恒为空**。

### 现象 3：`DELETE` 成功后 ~70s 以新 id 重建 —— **确认**

```
=== before ===
16|1|rainfall | 17|2|rsoc | 21|1|battery_voltage

$ DELETE /api/v1/data-sources/21   -> HTTP/1.1 200 OK
$ GET    /api/v1/data-sources/21   -> HTTP/1.1 404 Not Found   (确认真删了)

=== 每 15s 轮询 ===
t+ 15s: 16/rainfall, 17/rsoc, 22/battery_voltage   <-- 以 id=22 重生
t+ 90s: 16/rainfall, 17/rsoc, 22/battery_voltage

=== 服务端日志 ===
2026-10-24T02:45:50.090Z INFO source_projection: 对账变化 sources=3 created=1 updated=0 disabled=0 skipped_manual=0
```

`created_at = 2026-10-24 02:45:50.084661+00`，与日志 tick 完全对齐。
**删除→重建窗口 ≈ 30–55s**（取决于 tick 相位），Lead 说 70s，量级一致。

**注意**：DELETE 对 `managed_by='auto'` 的行**没有任何拒绝逻辑**（`service.go:306-360` 只处理 active 时的候选降级），
所以任何有 `admin` 权的人都能删，然后被静默重建——**id 会漂移**（21→22）。
若外部有任何地方记录了 data_source id，会失效。（当前无消费者，故暂无实际影响。）

### 2.4 一次**误判**记录（write-up 的诚实要求）

我第一次测「字段是否被覆盖」时得到 `name`/`priority` **存活**，与"id 16 名字曾被重置成 `e1·rainfall`"的观察矛盾。
排查发现：那是 **Lead 早前测试留下的状态**（`name='用户改的名字'` 后来被 `PUT` 重置），**不是我观测的覆盖**。
**这正是「记录是缓存不是事实」的陷阱**——不能拿前一状态的快照当本轮的差分。
改用「先写 → 立即读 → 轮询」的受控序列后，结论见 §4。

### 2.5 对 Lead 描述的修正汇总

| Lead 描述 | 我的实测 | 差异性质 |
|---|---|---|
| 「75 秒后变回 active」 | 25–37s（tick 相位决定） | 量级对，精确值不固定 |
| 409 文案 `no healthy standby candidate` | `... for (edge_device_id=N, category=X); specify a successor first` | 截断 |
| 「`StatusError` 无任何写入者」 | 正确，且 `fail_count` 同样无人写 ⇒ 整条熔断链已死 | 需加强 |
| 「投影器覆盖了人工操作」（泛指） | **仅 `status`（及 `is_primary`，且仅在 status 变化时）被覆盖**；其余字段存活 | **需收窄** |

---

## 3. 根因分类（回答 Q2/Q3）

### 3.1 决定性证据：唯一索引三元组 → 二元组

```
=== M2 提交 e8d11b82（2026-10-23 13:00）时 ===
$ git show e8d11b82:backend/internal/models/models.go | grep uniq_ds_source
343:  DeviceID uint `...uniqueIndex:uniq_ds_source,priority:1`  // LogicalDevice.ID
350:  Category string `...uniqueIndex:uniq_ds_source,priority:2`
351:  EdgeDeviceID uint `...uniqueIndex:uniq_ds_source,priority:3`
=> 3 个列参与唯一索引 = (device_id, category, edge_device_id)

=== 现在（工作区） ===
$ grep uniq_ds_source backend/internal/models/models.go
322:  Category string `...uniqueIndex:uniq_ds_source,priority:2`
323:  EdgeDeviceID uint `...uniqueIndex:uniq_ds_source,priority:1`
=> 2 个列参与 = (edge_device_id, category)
```

**M2 时的语义**：同一 (逻辑设备, 类别) 下可以有**多条**行，各自对应不同 `edge_device_id` ⇒
主备多行（active + standby）**可以存在**，`Deactivate` 能找到候选。
**移除后的语义**：组键退化为 (edge_device_id, category) ⇒ **一个键恒一行** ⇒ 候选必然为空。

**实测佐证**（在 verify 库真的插一条备用行）：
```
$ INSERT INTO data_sources (category, edge_device_id, name, status, managed_by)
  VALUES ('rsoc', 2, 'dup-standby', 'standby', 'auto');
ERROR:  duplicate key value violates unique constraint "uniq_ds_source"
DETAIL:  Key (edge_device_id, category)=(2, rsoc) already exists.
```

⇒ **不是投影器"覆盖了人工决定"，而是"人工决定的目标状态在数据模型里不存在"。**

### 3.2 分类裁定

| 选项 | 裁定 | 依据 |
|---|---|---|
| (a) 数据模型变更导致端点语义失效 | **✅ 主因** | 3 列→2 列唯一索引（§3.1）；`ux_ds_one_active` 也按新键重建（`dbschema/source_group.go:41-48`） |
| (b) 端点本来就是遗留死代码 | **✅ 次因** | 主备状态机已在 M3 删除（`0596f885`），`MarkSuccess/ScanStale/maybeFailover` 全删；4 个端点只剩壳 |
| (c) 投影器缺「不覆盖人工决定」守卫 | **✅ 独立缺陷**，但**范围比 Lead 说的小** | 只覆盖 `status`/`is_primary`；且这是 **M2 时就存在的缺口**，不是本次引入 |

**关键区分（回答 Q3）**：
- **「3 列→2 列」是本次移除引入的** ⇒ 现象 2（409 必失败）与现象 3 的"id 漂移"由它导致。
- **「投影器覆盖 status」在 M2 就存在** ⇒ 现象 1 不因本次移除而产生。
  证据：M2 的 `projection.go` 分组已按 `(device_id, category, edge_device_id)` 三元组建 `autoByKey`，
  **同样**只跳过 manual 行，不保护"人对 auto 行的干预"。M2 时代这个缺口**几乎不可触发**
  （因为当时 1 个逻辑设备通常只有 1 条 auto 行，改 status 也少），移除后仍不可触发
  ——**触发它需要人手工改 auto 行，而 UI 已经没有任何入口能改**（§5）。

---

## 4. 影响面清单（回答 Q4）

### 4.1 四个被 Lead 点名的端点

| 端点 | 当前行为（实测） | 为什么会这样 | 级别 |
|---|---|---|---|
| `POST /data-sources/:id/activate` | **200**（已 active 短路返回） | `service.go:479-482` | P2（无害） |
| `POST /data-sources/:id/deactivate` | **409 恒失败** | 无 standby 候选（§3.1） | **P1** |
| `POST /data-sources/:id/reset` | **400 恒失败** | 要求 `status=error`，而 `error` 无写入者 | **P1** |
| `DELETE /data-sources/:id` | **200 后 30–55s 重建**（id 漂移） | 投影器 `created=1` | **P1** |

### 4.2 Lead 未点名的**其他**冲突字段（回答 Q4 的「PUT 会不会被覆盖」）

**实测：不会被覆盖。** 受控序列（先写→立即读→75s 后读）：

```
$ UPDATE data_sources SET name='KEEP-ME', priority=77 WHERE id=17;
$ UPDATE data_sources SET status='disabled'          WHERE id=17;
immediately: KEEP-ME/77/disabled/auto
t+ 15s:      KEEP-ME/77/active/auto      <-- 只有 status 被改回
t+ 90s:      KEEP-ME/77/active/auto      <-- name/priority 一直是 KEEP-ME/77
```

`PUT /api/v1/data-sources/16` 同步验证：
```
请求: {"priority":55,"is_primary":false,"name":"PUT-NAME"}
响应: 200  name=PUT-NAME priority=55 is_primary=false status=active
75s 后: name=PUT-NAME priority=55 is_primary=false status=active
```

| 字段 | 会被投影覆盖？ | 依据 |
|---|---|---|
| `status` | **会** | `projection.go:230`（status 变化分支）/ `:254`（stale → disabled） |
| `is_primary` | **会，但只在 status 也变化时** | `projection.go:231` 与 status 同 map |
| `derived_at` / `last_success` | 会（预期内） | `:219-226` |
| `name` / `priority` / `description` / `config` / `max_fail_count` | **不会** | 不在任何 `updates`/`touch` map 里 |

**⇒ 由此产生一个新的不一致（Lead 未提，我新增）**：
`is_primary` 只在 `status` 同时变化时才被重写。因此可以存在
`is_primary=false` + `status=active` 的**自相矛盾行**（我实测造出来了，并存活 75s+）。
该字段无数据面消费者（grep：仅 `handler_data_source.go` 的 DTO 与 `simulation`），
所以只影响显示，但**语义上是「这条来源既权威又不权威」**。

### 4.3 `Reset` 不可达的加强证据

```
$ grep -rn "StatusError" --include=*.go backend/internal/datasource/service.go
31:  StatusError = "error"                       <-- 定义
607: if ds.Status != StatusError {               <-- 唯一读点（Reset 前置条件）
（无任何写 data_sources.status='error' 的代码）
```

`group_service.go:420` 的 `countFailure` 返回 `StatusError`，但只服务**来源组路径**
（`group_scan.go:106`），写的是 `source_group_members.status`，**不是 `data_sources.status`**。
`data_sources` 的 `fail_count` 同样无写入者 ⇒ **熔断链路整体已死**。

---

## 5. 前端可达性（回答 Q5）

**结论：4 个端点在 UI 上完全不可达；且不仅"不可达"，连 store 方法都无调用方。**

```
$ for m in createDataSource updateDataSource deleteDataSource activateSource deactivateSource resetSource; do
    grep -rn "$m" --include=*.vue --include=*.ts . | grep -v __tests__ \
      | grep -vc "stores/dataSource.ts\|api/dataSource.ts"; done
createDataSource     0
updateDataSource     0
deleteDataSource     0
activateSource       0
deactivateSource     0
resetSource          0
```

```
$ ls views/data-source/
DataSourcePage.vue  OverviewTab.vue  __tests__/
$ ls views/data-source/DataSourceList.vue
ls: cannot access '.../DataSourceList.vue': No such file or directory     <-- 已删除
$ grep -n "el-tab-pane" views/data-source/DataSourcePage.vue
15:  <el-tab-pane label="字段目录" name="overview">
29:  <el-tab-pane label="来源组" name="groups">
```

且页面显式声明只读（`DataSourcePage.vue:11`）：
> 「这里**刻意没有**任何新建/编辑/删除按钮 —— 不是隐藏，而是本 Tab 的…」

**区分「路由可达」与「代码里还有引用」**：
- **路由可达**：`POST /data-sources/:id/{activate,deactivate,reset}` 仍在 `routes.go` 注册（`handler_data_source.go:60-62`），
  任何持 JWT 的调用方（curl/脚本/未来前端）都能打到 ⇒ **API 面暴露，UI 面为零**。
- **代码引用**：仅剩 `stores/dataSource.ts` + `api/dataSource.ts` + 测试。
  这三个 store 方法成了**纯死代码**（无消费方，仅被自身单测覆盖）。

⇒ 这是最典型的「**假活代码**」：测试全绿（§6）、类型检查通过、UI 上不存在，但**语义已经错了**。

---

## 6. 下游消费者核查（决定严重性）

```
$ grep -rn "DataSource\|data_sources" --include=*.go backend/internal/automation backend/internal/alert \
    | grep -v _test
（空）
```

规则引擎读的是**别的列**：
```
backend/internal/models/automation.go:175: TriggerEdgeDeviceID uint   // sensor_threshold 目标设备
backend/internal/models/automation.go:185: SourceGroupID *uint
backend/internal/models/alert.go:69:       SourceGroupID *uint
```

`data_sources` 的**唯一真实读者**是 `GET /data-sources/overview`（`handler_source_overview.go:153`）：
```
q := db.Model(&models.DataSource{}).Where("status <> ?", datasource.StatusDisabled)
```

**这条 `WHERE` 是全部影响的落点**：
手工把某行 `status` 置 `disabled` ⇒ **该字段从「字段目录」页消失**；
30s 后被投影改回 `active` ⇒ **又出现**。用户看到的是「字段在目录里闪一下」。

⇒ **数据面零影响**（不改采集、不改规则、不改告警），仅「字段目录」显示抖动。
**这就是我把严重性定为 P1（不阻塞发布）而非 P0 的依据。**

---

## 7. 与来源组的关系（回答 Q7）：手工干预能力已完整迁移

`GroupService` 公开方法（`group_service.go` / `group_state.go` / `group_crud.go`）：

| 能力 | 方法 | 行 |
|---|---|---|
| 手工激活成员 | `ActivateGroupMember` | `group_state.go:320` |
| 手工停用成员 | `DeactivateGroupMember` | `group_state.go:374` |
| 组 CRUD | `CreateGroup / UpdateGroup / DeleteGroup / GetGroup(List)` | `group_crud.go:235/344/564/668/699` |
| 健康 / 切换日志 | `Health / FailoverLogs` | `group_crud.go:809/794` |
| 自动主备 | `MarkSuccess / MarkFailure / maybeGroupFailover` | `group_state.go:33/99`、`group_service.go:202` |

**API 面已注册**（`handler_source_group.go:107-108`）：
```
group.POST("/:id/activate",   activateSourceGroup(gsvc))
group.POST("/:id/deactivate", deactivateSourceGroup(gsvc))
```

**且已接入 UI**（与 data_sources 的死端点形成鲜明对比）：
```
$ grep -rn "activateGroup" --include=*.vue frontend-shared/src
views/source-group/SourceGroupList.vue:515:    await store.activateGroup(group.id)
```

测试规模：`backend/internal/datasource/` 共 14 个 test 文件、**83 个 `Test` 函数**（含组路径差分/归一化/事件/自检夹具）。

⇒ **用户需要的手工干预在来源组上全部具备，且真正可用。**
⇒ `data_sources` 上的 4 个手工写端点**是纯冗余**，保留它们只会让人误以为"这里也能切主备"。

---

## 8. 测试现状（为什么"全绿"掩盖了问题）

```
$ go test ./internal/api/ -run "TestDataSourceDeactivate|TestDataSourceReset|..." -v
--- PASS: TestDataSourceUpdate_WhitelistAndGroupPreserved
--- PASS: TestDataSourceDelete_NotFoundAndSuccess
--- PASS: TestDataSourceDelete_ActiveRemovesSoleGroupRow
--- PASS: TestDataSourceActivate
--- PASS: TestDataSourceDeactivate
--- PASS: TestDataSourceReset
ok  ehome/backend/internal/api  0.093s

$ go test ./internal/datasource/ -run "TestReconcile|TestDataSourceDeactivate|TestDataSourceReset"
ok  ehome/backend/internal/datasource  0.058s
```

**但这些测试测的是"孤立的 Service"，没有一个把投影器并列跑**：
- `TestDataSourceDeactivate`（`handler_data_source_test.go:436`）**自己承认**不可达：
  > 「组内唯一成员是 active 且无候选 ⇒ R6 冲突（该分支现在是唯一可达路径）」
- `TestDataSourceReset`（`:466`）同样自认：
  > 「组键含设备 ⇒ 一个组恒一行，"组内有 active 时重置为 standby"的分支不可达」
- 且它在测试里**手工伪造 `StatusError`**（`:479` `Updates(...status: StatusError)`）——
  而生产代码里没有任何路径能造出这个状态 ⇒ **测试锁住了一条生产不可达的分支**。
- `TestReconcile_NeverOverwritesManual`（`projection_test.go:172`）只测 **manual 行**，
  **没有** `TestReconcile_ManualEditOfAutoRowIsPreserved` 这类用例 ⇒ 缺口无门禁。

⇒ **这是「假绿」的标准形态**：测试通过 ≠ 功能可用。测试把"必然失败的路径"当成了"正确行为"来断言。

---

## 9. 方案评估矩阵与推荐

前提（§6）：`data_sources` **无数据面消费者**，唯一读者是只读`/overview`；
手工干预能力已在 `source_groups` 具备且 UI 可达（§7）。

| 方案 | 做什么 | 优点 | 风险 | 工作量 |
|---|---|---|---|---|
| **A 删除 4 端点 + Service 对应方法 + 死分支** | 删 `activate/deactivate/reset/delete` 路由与 handler；删 `Service.{Activate,Deactivate,Reset,Delete}` 及 `pickCandidate/cooldownActive` 等死辅助；`GET` 保留 | 根除"假活代码"；`uniq_ds_source` 二元组语义自洽；少 ~200 行 + 3 个 403/409 分支 | 前端 store 3 方法成悬空（应同步删）；有外部脚本调用会 404（当前无证据有） | **小**（后端 ~1 人日 + 前端 0.5） |
| **B 保留但 409 + 解释** | 对 auto 行拒绝并说明"主备请用来源组" | 保留 API 兼容；错误可读 | 4 个端点仍是**永远失败**的端点 —— 比 404 更糟（调用方会以为是临时故障）；仍留着死状态机 | 中 |
| **C 给投影器加「人工干预优先」守卫** | 新增 `manual_override` 标记；或 PUT 时把 `managed_by` 提升为 `manual` | 保住"人能改 auto 行"的能力；修 Q3 缺口 | **解决不了根因**：`deactivate` 仍无候选（那是索引问题，不是覆盖问题）；引入新状态又回到"双层模型"；且 UI 无入口去设这个标记 | **大** |
| **D 只登记不动** | 写进台账 | 零成本 | 下一个人会继续被 `PASS` 的测试误导；死端点长期存在 | 零 |

### 推荐：**A**（删除），并把 B 的错误文案作为**过渡期**（若必须保留一个版本）

**理由（按证据权重）**：
1. **没有消费者**（§6 grep 为空）⇒ 保留的唯一价值是"兼容"，而无兼容对象。
2. **手工干预已在来源组完整具备且 UI 可达**（§7）⇒ 删除不损失任何用户能力。
3. **C 治不了根因**：现象 2 的根因是 `uniq_ds_source` 二元组，加 `manual_override` 之后
   `Deactivate` 依然找不到 standby 候选 —— 这个方案会让人误以为修好了。
4. **测试必须同步改**（§8）：删端点时要删掉那 3 个"断言必然失败"的用例，
   否则会留下"删了代码但测试还在测鬼"的第二重假绿。

**若 Lead 选 D（本轮不动）**，最低限度应做两件事（否则下一个审核者会重复踩）：
- 在 `handler_data_source.go` 4 个端点处加注释：「**已知恒失败：组键二元化后无 standby 候选，待删除**」；
- 把 `TestDataSourceDeactivate/Reset` 改成 `t.Skip("生产不可达：组键二元化")+` 显式标记，
  不要让 `PASS` 继续伪装"功能正常"。

---

## 10. 我认为 Lead 描述有误/需修正的地方（明确列出）

1. **「投影器覆盖了人工操作」范围过宽** —— 实测只有 `status`（连带 `is_primary`）被覆盖；
   `name/priority/description/config/max_fail_count` **全部存活**（§4.2）。
2. **「75 秒后变回」不精确** —— 是 25–37s（tick 相位决定），不是固定值。
3. **409 文案被截断** —— 完整串见 §2 现象 2。
4. **根因应上移到索引**：现象 2/3 的主因是「`uniq_ds_source` 从 3 列缩为 2 列」，
   不是"投影器缺守卫"。**投影器缺口只解释现象 1**，而现象 1 在 M2 时就有。
5. **环境坑（会被下一个人重复踩）**：宿主机 8080 = `ehome-web`（旧镜像，**仍有 `/logical-devices`**）；
   verify 服务必须经 `docker exec`；容器内 wget 必须清 `HTTP_PROXY`。
6. **存在第二个投影写入者 `ehome-review-srv`(8081)** 写同一 `ehome_verify` 库 ——
   任何对此库的时序结论都要考虑双 ticker。

---

## 11. 附注：多副本无选主（顺带发现，不在本任务范围）

```
ehome-verify-srv (8080)  ─┐  两者都对 ehome_verify 做 60s 全量对账
ehome-review-srv (8081)  ─┘  无选主/无租约/无 advisory lock
```
投影器是幂等的，**功能上安全**；但两进程各自 `s.now()` 不同、ticker 相位不同，
会在日志里产生**双份 `created=1`/`updated=1`**，让"改动到底由谁触发"无法归因。
建议（不阻塞）：投影器加 advisory lock，或明确"单副本运行"约束。

---

## 附录：复跑命令

```bash
# 1) verify 服务正确入口（主机 8080 是 ehome-web，不是它）
docker exec -e http_proxy= -e https_proxy= -e HTTP_PROXY= -e HTTPS_PROXY= \
  ehome-verify-srv sh -c 'wget -q -O - --server-response http://127.0.0.1:8080/health'

# 2) 唯一索引（现象 2 根因）
docker exec ehome-postgres psql -U ehome -d ehome_verify \
  -c "SELECT indexname, indexdef FROM pg_indexes WHERE tablename='data_sources';"
docker exec ehome-postgres psql -U ehome -d ehome_verify \
  -c "INSERT INTO data_sources (category,edge_device_id,name,status,managed_by) \
      VALUES ('rsoc',2,'dup','standby','auto');"   # -> uniq_ds_source 违例

# 3) 现象 1：status 被改回
docker exec ehome-postgres psql -U ehome -d ehome_verify -c "UPDATE data_sources SET status='disabled' WHERE id=16;"
sleep 40 && docker exec ehome-postgres psql -U ehome -d ehome_verify -Atc "SELECT status FROM data_sources WHERE id=16;"

# 4) 现象 3：删除后重建（nc 发 DELETE，容器内无 curl）
printf 'DELETE /api/v1/data-sources/21 HTTP/1.1\r\nHost: 127.0.0.1:8080\r\nAuthorization: Bearer %s\r\nConnection: close\r\n\r\n' "$TOK" | nc 127.0.0.1 8080 | head -1
docker logs --since 3m ehome-verify-srv | grep source_projection

# 5) 索引历史（三元组→二元组）
git show e8d11b82:backend/internal/models/models.go | grep uniq_ds_source   # 3 行
grep uniq_ds_source backend/internal/models/models.go                      # 2 行

# 6) 下游消费者（空 = 无）
grep -rn "DataSource\|data_sources" --include=*.go backend/internal/automation backend/internal/alert | grep -v _test

# 7) 前端可达性（全 0 = 死代码）
cd frontend-shared/src && for m in activateSource deactivateSource resetSource deleteDataSource; do
  printf "%s " "$m"; grep -rn "$m" --include=*.vue --include=*.ts . | grep -v __tests__ | grep -vc "stores/dataSource.ts\|api/dataSource.ts"; done
```
