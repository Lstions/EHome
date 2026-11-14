# unified_data 硬切换 — 读路径契约变更记录（2026-11-08）

> 本文件记录**对外契约的实际变化**，供前端与后续维护者查阅。
> 背景：`unified_data` 历史写入已停用（`EHOME_TELEMETRY_UNIFIED_WRITE=false`），
> 其历史读路径按「不做兼容」原则**逐条硬切换**到帧表 / rollup / 维度表。
>
> **2026-11-13 更新（用户裁决「直接 false」→ 当日全量退役）**：
>
> 1. 停写开关的**代码默认值先改为 false**（此前未设置时默认继续写），
>    显式 `true` 也要求表存在，避免新库每帧 `42P01`。
> 2. 随后按用户「完成 unified_data 废除」的裁决，**写入、读取回落、老数据回填、
>    状态端点、前端进度卡、部署开关全部删除**；停写开关本身也随写入路径一起移除
>    （没有写入路径，开关没有意义）。`unified_data` 由运维用
>    `ehomectl retire-unified-data --yes` 显式 DROP。
> 3. 本文件描述的所有读路径**仍然是当前契约**（帧表 / rollup / 维度表 / 最新值缓存），
>    只是不再有"回落 unified_data"这一层。
>
> 实施记录（变更清单、命令、验收、回滚）：
> [分析/unified_data整表退役-实施记录-2026-11-13.md](../分析/unified_data整表退役-实施记录-2026-11-13.md)。

## 为什么必须记录

代码注释会被 grep 到，但**调用方不会去读后端源码**。
而这次变更里有多处「正常运行时行为不变、只在特定窗口下变」的形态 ——
那种变更最容易被误判成 bug，也最容易被漏记。

## 变更清单

| 端点 | 原语义 | 新语义 | 影响窗口 |
|---|---|---|---|
| `GET /nodes/:id/latest` | 缓存 miss ⇒ 回落查 `unified_data`，故「有行就有值」 | **只读缓存**，miss ⇒ 返回空数组 | 刚启动 + 预热未完成时返回 `[]`；预热完成后与旧行为一致 |
| `GET /devices/:id/history` | 裸查 `unified_data` | 帧表原始解码（分段，>8 天或超内存预算回落/413） | 无（数据源切换，形状不变） |
| `GET /unified-data/categories` | `GROUP BY sensor_name` on `unified_data` | `sensor_field_obs` 优先，空则回落 | 无 |
| `GET /devices/:id/sensor-data` | 裸查 `unified_data` | 帧表（自适应窗口 1h→6h→24h） | 无 |
| `GET /unified-data/historical?agg=raw` | 裸查 `unified_data` | 帧表原始解码 | 无 |
| `GET /admin/metrics/summary` 的 `points_stored` | `count(DISTINCT (device_id,timestamp)) on unified_data` | `count(*) on telemetry_frame` | 口径等价（帧表一帧一行） |

## 内存护栏（新增约束）

`/devices/:id/history` 与 `/unified-data/historical?agg=raw` 新增硬上限：

```
帧数 × 该设备字段数 > 3,000,000 行  ⇒  HTTP 413
```

- 按**行数**而非帧数：同样帧数在 100 字段设备上内存是 29 字段设备的 3.4 倍。
- 传 `max_points` **不能**免除检查：降采样在解码之后，堆峰值与响应大小无关。
- 24h 在 29 字段设备上约 246 万行 ⇒ 放行；48h / 168h ⇒ 413。

## 前端需要知道的两件事

1. **`/nodes/:id/latest` 可能在服务刚启动的短时间内返回 `values: []`。**
   这是预期行为，不是故障。稳定运行后不再出现。
2. **`values` 始终是数组，不会是 `null`。** 按 `.length` 消费是安全的。

## 不在本记录范围

- `/unified-data/categories` 等接口的**响应形状**未变（键名、类型、排序均保持）。
- 内部链路（帧表解码 / rollup 重分桶）的等价性由对拍用例保证，不属于对外契约。

## `/latest-data` 与 `/edge-devices/:id/data` 的 sensors 形状（生产实测，2026-11-08）

### 生产现状：device_data 里已经没有 sensors 了

```
device_data 含 "sensors" 键的行       全库 0 行
device_data 含 str_values 的行        全库 0 行（近 24h 也是 0）
device_data 总行数                    7,755,494
剥离任务 device_data_strip_progress   state=done（751 万行处理完，跳过 0 行）
```

近 24h 采样 5000 行只有两种 JSON 形状，**都没有 sensors 键**：

```
channel_id,raw_hex,timestamp                                              ×4415
channel,command_index,edge_device_id,error_code,raw,request_id,sequence   ×585
```

### 所以 sensors 是**读时合成**的

| 情形 | 返回形状 | 是不是故障 |
|---|---|---|
| 帧表可用（正常） | `sensors: [...]` 全部数值字段 | 否 |
| 数值源为空、该行有 str_values | `sensors: [...]` 仅纯字符串字段（`Value=0` + `StringValue`） | **否** |
| 数值源为空、该行也无 str_values | 该行**无 sensors 键**（保持原样） | **否** |

**重要**：上表第二、三行是硬切换后的**正常形状**，不是降级/故障态。
device_data 侧的 sensors 已经被剥离到 0 行，它本来就只存在于帧表与 latest 缓存里。

### 字符串字段的唯一落库位置（必须与「当前为空」一起读）

`hardware_version` / `serial_number` 的 `Value` 恒为 0，真值只在
`device_data.data_json.str_values` —— `unified_data` 无该列，`telemetry_frame` 的
Reading 只有 Name/Value，**物理上不可能**存字符串。

实测：全库 0 行含 `str_values`。原因：这些字段只由**控制动作**
`read_hardware_version` / `read_sn` 产生，而现场从未读过。

即：这是「**还没被走过**」的路径，不是「走不通」的路径。
两个事实缺一个就会得出错误结论：

- 只看到「唯一落库位置」⇒ 以为有大量数据要保护，可能为它保留整条旧读路径
- 只看到「当前 0 行」⇒ 当成死代码删掉，将来第一次读 BMS 版本时静默丢失

因此代码里保留 `mergeStringValues` 的「补齐」逻辑，并由两条用例钉住：

- `TestStringOnlyField_SurvivesWhenValueSourceEmpty`（单行 `/latest-data`）
- `TestStringOnlyField_SurvivesInListPath`（列表 `/edge-devices/:id/data`）

### 前端需要做的事

```ts
// 正确：形状判据，两种「没有数值」都能安全处理
if (dj.sensors && Array.isArray(dj.sensors)) { ... }
```

- sensors **可能整个不存在** —— 不要用 `dj.sensors.length` 直接取值。
- 字符串读数读 `StringValue` 优先，读 `Value` 会显示成 0（看起来像真实读数，比「—」更糟）。
- `raw_hex` 永远保留，是「原始数据」列的唯一来源。
