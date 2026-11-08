# L3 rollup：写入侧接线 patch（精确到行号）

> **状态**: 待接线（**未落地**）。L3 的 `RollupCollector` 已实现并有单测，但
> **没有**挂到 `internal/databus/consumers_heavy.go` —— 该文件是 impl-l1 的写范围
> （L1 收窄正在改同一段代码），本任务按纪律不动它。
>
> 本文给出可直接照抄的 patch，任何一位接手者（impl-l1 或后续任务）按此接线即可。
> **接线后必须重跑**：`./internal/databus/...`（L1 的用例）+ `./internal/datalifecycle/...`。
>
> 关联：[遥测rollup-L3-接线与门禁-2026-11-06.md](遥测rollup-L3-接线与门禁-2026-11-06.md)

---

## 0. 为什么现在不能直接接（而不是"懒得接"）

1. `consumers_heavy.go:353-380` 是 L1（`task-2`，impl-l1）的**核心修改点** ——
   它要在这里加"值不变且未到心跳 ⇒ 跳过 INSERT"，并把 `latestSink` 从
   `Create` 成功的 `else` 分支提到 INSERT 之前。两处改动与 rollup 的 `Observe`
   调用点**落在同一段 20 行里**，同时改必然冲突。
2. rollup 的采样点必须与 L1 的"是否真的写这一帧"**解耦**：L1 跳过入库的那一帧
   **仍然要**喂给 rollup（否则分钟均值会漏样本，与原始表口径不一致）。
   这一点必须由 L1 的作者确认位置，不能由我在没有上下文的情况下插。

## 0.5 命名约束（**照抄前必读**：这几个名字会把门禁判红）

`datalifecycle/rollup_retirement_gate_test.go` 的 `rollupGateRetiredSymbols` 是一份
**子串黑名单**（`EnsureRollupTable` / `SetRollupSink` / `rollupSink` / `RollupConsumer` /
`UnifiedDataRollup1m`）：任何生产 Go 源码里出现这些**子串**即判红。它是旧表退役时的
"符号不得复活"断言，但副作用是**新机制不能复用这几个名字** —— 哪怕语义完全不同。

因此 L3 的一律用 **`TelemetryRollup`** 作词根。已用脚本逐个核对（子串匹配，
与门禁同算法）：

| 候选名 | 门禁判定 |
|---|---|
| `SetTelemetryRollupSink` / `telemetryRollupSink` / `TelemetryRollupSink` | ✅ OK |
| `RollupCollector` / `FlushSink` / `Sink()` / `rollupCollector` | ✅ OK（不含 `rollupSink` 子串） |
| `reapRollupPartitions` / `EHOME_TELEMETRY_ROLLUP_WRITE` | ✅ OK |
| `SetRollupSink` | ❌ **红**（撞 `SetRollupSink`） |
| `rollupSink` | ❌ **红**（撞 `rollupSink`，且 `SetRollupSink` 也含它） |

> 注意 `RollupCollector` 之所以安全：黑名单是 `rollupSink`（小写 s 紧接），
> 而 `RollupCollector` 里没有 `rollupSink` 这个连续子串。**判断标准是连续子串，
> 不是"词根像不像"** —— 改动名字前用 `strings.Contains` 对着上面五个词自检一遍。

## 1. 需要的三处改动

### 1.1 `internal/databus/consumers_heavy.go` —— 加一个 sink 字段 + setter

**位置**：`consumers_heavy.go:149` 之后（在 `sourceHealthSink` 字段之后、
`ingestResolver` 之前），保持"4 个 sink 字段相邻"的既有排列：

```go
	// telemetryRollupSink 分钟级宽行预聚合的**旁路**采样回调（L3，设计/遥测存储优化方案-2026-11-06.md §5.2）。
	//
	// 与上面四个 sink 的差别：它只在内存里累加，**不碰数据库**（分钟切换才落库，
	// 由 collector 自己的 flush 驱动）。因此可以在"不写库"的分支上也调用。
	//
	// 与 L1 的「值不变不入库」的关系（重要，别接错位置）：
	//   · L1 跳过 INSERT 的帧 **仍然必须** 调它 —— 把它理解成"喂给统计器"而不是"落库"；
	//   · 若接在"写库成功"分支里，被跳过的帧就不进分钟均值 ⇒ rollup 与原始表口径漂移，
	//     而这个漂移没有任何告警会暴露（图表看起来正常，只是数字偏了）。
	telemetryRollupSink func(edgeDeviceID uint, fields []parser.Field, at time.Time)
```

**位置**：`consumers_heavy.go:222` 之后（`SetSourceHealthSink` 之后），新增 setter：

```go
// SetTelemetryRollupSink 注入分钟级预聚合采样回调（L3，设计/遥测存储优化方案-2026-11-06.md §5.2）。
// 与 alertSink/automationSink 同形（同一次解析后的字段 + 同一时刻）—— 复用既有范式，
// 不新创机制。nil 时跳过（未接线 ⇒ 行为与今天完全一致）。
func (c *SensorParserConsumer) SetTelemetryRollupSink(sink func(edgeDeviceID uint, fields []parser.Field, at time.Time)) {
	c.telemetryRollupSink = sink
}
```

### 1.2 `internal/databus/consumers_heavy.go` —— 在解析成功路径上调用它

**位置**：`:390-392`（`automationSink` 的调用点之后，`sourceHealthSink` 之前）。
**必须与 `automationSink` 同点**：那里是"解析成功 + 同一批 `sensorData` + 同一个 `now`"，
正是分钟聚合需要的输入；而且它与"是否入库"无关（与 L1 的跳过逻辑天然解耦）。

```go
	// L3 分钟级预聚合采样（旁路，不碰库）：与 automationSink 同点挂接。
	// 位置刻意放在"持久化"之后、"更新设备状态"之前 —— 它依赖的信息只有
	// (device.ID, sensorData, now) 三样，与 INSERT 成功与否无关。
	if c.telemetryRollupSink != nil && len(sensorData) > 0 {
		c.telemetryRollupSink(device.ID, sensorData, now)
	}
```

> `now` 已在 `:354` 定义（`now := time.Now()`），`device.ID` 是 EdgeDevice.ID
> （= `unified_data.device_id` 口径），`sensorData` 是 `:337` 的 `toParserFields(...)`。
> 三者都不需要新增变量。

### 1.3 `cmd/server/main.go` —— 装配 collector + sink + flush 定时器

**位置**：`:327`（`api.WarmupLatestValues()` 之后、`alertEvaluator` 之前），
与既有 `nodeMgr.SetLatestSinkFn` / `SetAlertSink` 的接线区相邻：

```go
	// L3 分钟级宽行预聚合（设计/遥测存储优化方案-2026-11-06.md §5.2）。
	// 默认**关闭**（EHOME_TELEMETRY_ROLLUP_WRITE，避免"没验证过的路径默认生效"）。
	// 关闭时不构造 collector、不注入 sink ⇒ 主写入路径零额外开销、行为与今天一致。
	var rollupFlusher func(context.Context) (int, error)
	if rollupWriteEnabled {
		rollupCollector := datalifecycle.NewRollupCollector(db)
		collectSink := rollupCollector.Sink()
		nodeMgr.SetTelemetryRollupSinkFn(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
			samples := make([]datalifecycle.RollupSample, 0, len(fields))
			for i := range fields {
				samples = append(samples, datalifecycle.RollupSample{Name: fields[i].Name, Value: fields[i].Value})
			}
			collectSink(edgeDeviceID, samples, at)
		})
		rollupFlusher = rollupCollector.FlushSink()
		// 分钟级 flush：只落"已结束"的桶（FlushCompleted 内部判定），
		// 失败只 Warn（派生数据，下一轮重试）。
		go func() {
			ticker := time.NewTicker(60 * time.Second)
			defer ticker.Stop()
			for range ticker.C {
				if _, err := rollupFlusher(context.Background()); err != nil {
					logger.Warnf("telemetry rollup flush failed: %v", err)
				}
			}
		}()
	}
```

**注意**（`nodeMgr.SetTelemetryRollupSinkFn` 需要新增）：nodemgr 已有一层 `SetLatestSinkFn`
的转发（`nodemgr` → `SensorParserConsumer.SetLatestSink`）。接线者需要在
`internal/nodemgr/` 里照抄那条转发链路再加一条 `SetTelemetryRollupSinkFn` ——
**这是本 patch 唯一跨到两个包的地方**，因此单独列出来：

| 文件 | 位置 | 要加什么 |
|---|---|---|
| `internal/nodemgr/manager.go`（或既有 `SetLatestSinkFn` 所在文件） | 紧挨 `SetLatestSinkFn`（实测 `:110-118`），加到 `:150` 之后 | `func (m *Manager) SetTelemetryRollupSinkFn(fn func(uint, []parser.Field, time.Time))`，内部 `for _, p := range m.parserConsumers { p.SetTelemetryRollupSink(fn) }`（**必须推送**：只赋值不推送 = 引擎"从未触发"复发的根因，见 `:141-150` 注记） |
| 同上 | 构造函数里 consumer 创建之后 | 无需改（sink 是 setter 注入） |

> 也可**更简单**：不经过 nodemgr，直接在 `main.go` 拿到 consumer 实例注入
> （若 `main` 里有该引用）。但仓库既有范式是走 nodemgr 转发（见 `SetLatestSinkFn`），
> 建议照抄，避免"两条注入路径"。

## 2. 接线后的验收（必须真跑）

```bash
# 1. L1 + databus 的既有用例仍绿（接线不得改变落库行为）
GOW_WORKDIR=.agents/worktrees/telemetry-storage/backend scripts/gow.sh test -count=1 ./internal/databus/...

# 2. L3 自己的用例仍绿
GOW_WORKDIR=.agents/worktrees/telemetry-storage/backend scripts/gow.sh test -count=1 ./internal/datalifecycle/...

# 3. 真实数据对拍（这条才是"接线成功"的硬证据）
#    跑 24h（或若干小时）真实数据：rollup 行的字段统计量 == 直接 SQL 聚合
EHOME_ROLLUP_REALDATA_CSV=/ws/.tmp-telemetry/realdata/unified_data_202611.csv.gz \
GOW_WORKDIR=.agents/worktrees/telemetry-storage/backend scripts/gow.sh test \
  -run TestRollupRealData -v ./internal/datalifecycle/
```

## 3. 接线时必须盯住的三个坑（都是本仓踩过的同型问题）

| 坑 | 表现 | 防法 |
|---|---|---|
| 把 `telemetryRollupSink` 接在"写库成功"分支里 | L1 跳过的帧不进分钟统计 ⇒ rollup 与原始表口径漂移，**无任何告警**（图看起来正常，数偏了） | 按 1.2 的位置接（与 `automationSink` 同点，与入库解耦） |
| 忘记 `nil` 判定 | 未接线时 nil 调用 panic，整个 consumer 崩 | 按 1.2 的 `if c.telemetryRollupSink != nil` |
| flush 用 `time.Tick` 或没有 ticker.Stop | goroutine 泄漏（本仓 `main.go` 既有 ticker 都带 `defer ticker.Stop()`） | 按 1.3 的 `time.NewTicker` + `defer ticker.Stop()` |

## 4. 变更记录

| 日期 | 变更 |
|---|---|
| 2026-11-06 | 初版：三处接线点（sink 字段/setter、解析成功路径调用、main 装配+flush）、验收命令、三个坑 |
