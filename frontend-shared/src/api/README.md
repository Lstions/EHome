# `api/` 目录约定

> 本文件只记录**跨文件、易丢失的判据与坑**，不重复各文件自己的 JSDoc。

## 1. 时序数据的「行」与「帧」（2026-11-07 起，**读任何计数类字段前必读**）

`unified_data` 自 2026-11-07 起按 **「值变化 + 60s 心跳」** 落库（L1 收窄，已无条件生效、无开关）。
因此**行数不再等于采样量**，二者可以差 4~5 倍：

| 概念 | 定义 | L1 后是否缩小 |
|---|---|---|
| **行（row）** | `unified_data` 里的一行 = 一帧里的**一个物理量** | ✅ 显著缩小（值不变不写） |
| **帧（frame）** | 同一设备同一 `timestamp` 的一批物理量（生产端一帧内所有字段用同一个 `now` 写入） | ❌ 不缩小（L1 只减少"一帧写几个字段"） |

**读字段时的正确姿势**：

- 需要「采了多少」⇒ 用**帧**口径：`COUNT(DISTINCT device_id, timestamp)` 或后端已改好口径的字段
  （`/overview` 的 `data_count_today`、`/metrics/summary` 的 `data.points_stored` 都已是帧数）。
- 需要「库里有多少行」⇒ 才可以读行数，且**必须在 UI 上写明「行」**，否则用户会读成数据丢失。
- `/metrics/summary` 的 `data.points_collected` 是 **Prometheus Counter（进程启动以来）**，
  数的是「解析成功的事件（帧）」，**不受 L1 影响**。它与 `points_stored` **不可相减**：
  窗口不同（进程内 vs 全表累计）+ 过滤不同（后者跳过值不变帧）。

改动历史与实测数据：`docs/设计/遥测存储-阶段0与L1实现记录-2026-11-06.md` §3.5 / §3.7。

## 2. `/sensor-fields`：`missing_known` 不是「设备缺报」

`POST /api/v1/sensor-fields/preview` 的响应自 2026-11-07 起新增两个字段：

- `sparse_frames` (bool)：该设备的帧是否「稀疏」（同一帧里字段数明显少于 `matched`）。
- `frame_semantics` (string)：给人看的口径说明原文。

**`missing_known` 的语义已变**：它现在表达 **「本帧未落库的字段」**，
**不等于**「设备没上报该字段」。举例：BMS 一帧 29 个字段，若本次只有 1 个字段变化，
库里就只留 1 行 ⇒ `missing_known` 会把这帧算成缺 28 个 —— 这是 L1 的**正常结果**，不是设备故障。

> ⚠️ **前端目前零消费这两个接口**（`grep -rn "sensor-fields" frontend-shared/src` = 0 命中），
> 所以当前没有任何误判面。**将来若要做「设备缺报 / 字段缺失」类 UI 或告警**：

**必须用观测口径，不能用 preview 的缺员判定**：

```
GET /api/v1/sensor-fields?edge_device_id=<id>&source=obs
```

`source=obs` 只返回**观测到过**的字段（后端 `handler_sensor_fields.go` 的 `sourceFilter == "obs"` 分支），
按上报计数，**不受 L1 影响**；而 `source=decl` 只看声明集合。
反之，`preview` 的 `missing_known` 只适合回答「这一帧里哪些字段**没落库**」这种存储侧问题。

## 3. 维护态接口必须用独立 axios 实例

`api/system.ts` 的 `getMaintenanceStatus()` 走**独立 `axios.create` 实例**（无拦截器）。
原因：`api/client.ts` 的响应拦截器在「有本地 token + 401」时会清 session 并硬跳 `/login`，
而维护期「未登录」是正常态、后端也可能正在启动返回 4xx/5xx —— 复用全局实例会把维护页直接弹成登录页。
详见该文件头注释与 `src/views/maintenance/useStartupGate.ts`。

## 4. 历史数据回填接口（已退役，2026-11-13）

`unified_data` 整表退役后，老数据回填（`unified_data → telemetry_frame`）的
引擎、状态表与 `GET /api/v1/system/backfill` 端点已一并删除。本文件不再
描述该接口；如需确认线上表是否已清理，使用 `ehomectl retire-unified-data`
（默认只读校验）。
