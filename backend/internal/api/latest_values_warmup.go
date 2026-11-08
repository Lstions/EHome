package api

// latest_values_warmup.go — 最新值缓存的**启动回填**查询（task-24 步骤 2）。
//
// ============ 为什么要给 warmup 加时间窗 ============
//
// 原查询（无时间界）：
//
//	SELECT u.* FROM unified_data u
//	INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data
//	            WHERE device_id IS NOT NULL ORDER BY device_id, created_at DESC) latest
//	  ON u.device_id = latest.device_id AND u.created_at = latest.created_at
//
// 子查询要在**每台设备的全部历史行**上算 DISTINCT ON。unified_data 按 (id, timestamp)
// 分区，分区键不是 created_at，所以这条查询**无法分区裁剪**：
// 设备跑得越久、历史越多，启动越慢，而且是**每次重启**都要付一次。
//
// 加时间窗后：WHERE created_at >= since ⇒ 仍需扫窗口内的行，但窗口是常数，
// 启动成本从「随历史线性增长」变成「随设备数 x 窗口内行数」——在数据增长后才有意义。
//
// ============ 窗口取 24h 的依据（不是拍脑袋）============
//
// warmup 的目的只有一个：**服务刚启动时 latest 缓存不要是空的**，避免每台设备首次
// /overview、设备列表、/nodes/:id/latest 都回落 SQL。
//
// 所以窗口必须满足：**>= 设备的最长静默间隔**。设备离线后再上线，缓存仍应命中。
//
// 实测参考：
//	· 活跃设备      1~2 秒一帧（生产 device14/15）
//	· 夜间/休眠设备 可能几小时不报
//	· 24h 覆盖「当天所有还在服役的设备」的绝大多数场景
//
// ⚠ 窗口**不是**正确性参数，只是预热范围：落在窗口外的设备，
//   warmup 不载入 => 该设备首次请求走 Miss 回落 SQL => **结果相同**，只是慢一次。
//   所以取小了不会出错，只会少预热几台离线设备；取大了则启动变慢。
//
// 调整方式：EHOME_LATEST_WARMUP_LOOKBACK（见 LatestValuesWarmupLookbackFromEnv）。

// ============ 已知限制：没有 created_at 索引，本轮**故意不修** ============
//
// EXPLAIN 实测（2026-11-07，ehome 库 179,532 行）：
//
//	无时间窗  Gather + Parallel Hash Join（3 worker）  actual time=100.6..113.6ms
//	24h 窗    Hash Join（子查询行数降到窗口内）        actual time=8.3..10.7ms
//
// 但执行计划里子查询仍是 **Seq Scan + Sort**：unified_data 的索引是
// pkey / device_id / timestamp / (device_id,timestamp) / (device_id,sensor,timestamp)，
// **没有 created_at**，而本查询 ORDER BY device_id, created_at DESC。
// 时间窗减少的是**进入排序的行数**，不是扫描本身。
//
// 更根本的解法是加索引 (device_id, created_at DESC)。**本轮故意不做**，理由：
//
//	unified_data 的历史写入正在被退休（task-24 的整个目标）
//	=> 给一张即将不再被读的表加索引 = 只增加写成本与磁盘，没有收益
//
// 所以这里保留 Seq Scan + 时间窗。**下一个人看到 EXPLAIN 不要以为是漏了。**
// 若将来 unified_data 因故继续长期写入，那时再评估索引，而不是现在。

import (
	"os"
	"strconv"
	"time"
)

// LatestValuesWarmupLookback 是启动回填的回看窗口，默认 24h。
//
// 为什么默认 24h 而不是更短：warmup 是「让缓存不为空」，短了会把夜间休眠的设备漏掉，
// 那些设备恰恰最需要预热（它们不会有实时写入来顺带更新缓存）。
// 为什么不是更长：再长也覆盖不到「离线超过一天」的设备，那类设备本来就该走 Miss 路径。
const LatestValuesWarmupLookback = 24 * time.Hour

// LatestValuesWarmupLookbackFromEnv 读取 EHOME_LATEST_WARMUP_LOOKBACK。
//
// 支持 Go duration 写法（24h / 90m）与纯小时数（24）。非法值回落默认且不报错 ——
// 预热窗口是性能参数，不该让服务启动失败。
func LatestValuesWarmupLookbackFromEnv() time.Duration {
	v := os.Getenv("EHOME_LATEST_WARMUP_LOOKBACK")
	if v == "" {
		return LatestValuesWarmupLookback
	}
	if d, err := time.ParseDuration(v); err == nil && d > 0 {
		return d
	}
	if h, err := strconv.Atoi(v); err == nil && h > 0 {
		return time.Duration(h) * time.Hour
	}
	return LatestValuesWarmupLookback
}

// LatestValuesWarmupRows 已删除（2026-11-08，unified_data DROP）。
//
// 它原来是启动预热的**第一个来源**：从 unified_data 取「每台设备最新时刻的全部物理量」。
// 两件事让它可以删：
//
//	① 数据源不存在了：unified_data 已 DROP，这条 SQL 报 42P01（生产实测：启动时 1 条）；
//	② 它本来就已经是**纯冗余**：main.go:613 的 WarmupLatestValuesFromFrames（帧表）
//	   才是唯一有效的预热来源 —— 帧表覆盖全部有帧的设备，而 unified_data 尾部
//	   在停写后根本不会有新行。
//
// 连带删除的还有 SetLatestValuesSource / warmupSource / WarmupLatestValues：
// 「留一个没人注入的注入点」比留死代码更误导（见 latest_value_cache.go 的说明）。
