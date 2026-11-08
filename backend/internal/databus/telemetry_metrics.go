package databus

// telemetry_metrics.go — L1 写入收窄的可观测性（方案 §3.3/§7；2026-11-07 起无条件生效）。
//
// # 为什么指标定义在 databus 包里而不是 pkg/metrics
//
// 沿用本仓既有的「包自持指标」范式（字段观测层也是这么做的：指标与它的唯一写入点
// 放在一起，减少跨包漂移；同名 promauto 指标 init 期重复注册会 panic，因此命名必须
// 全局唯一）。
//
// ⚠ 源码级门禁（cmd/server/field_resolver_test.go 的 TestFieldResolverWiringGate）
// 扫描 internal/databus/*.go 全部文件，**任何**对字段观测层的引用（含注释、字符串）
// 都会判红：数据面必须零依赖该层。本文件因此只 import prometheus 与本包类型。
//
// # 标签基数
//
// 只有一个**有界枚举**标签，禁止把 device_id / sensor_name 放进 label（高基数）：
//
//	decision = write_first | write_changed | write_heartbeat | skip
//
// 历史上还有第二个 mode 标签（shadow | enforced）用于影子模式核对命中率；
// 2026-11-07 起收窄无条件生效、影子模式删除，该维度随之删除 —— 留着它会让
// 每一个查询都要先想「我该看哪个 mode」，而实际只有一个。
//
// 判读：decision=skip 的占比就是收窄命中率，与真实数据回归基线
// （device14 5.27% / device15 36.92% / 合计 17.74%）对账。

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 写入收窄的 decision 标签取值。
const (
	telemetryDecisionFirst     = "write_first"     // 进程内首次见到该 (设备, 字段) ⇒ 必写
	telemetryDecisionChanged   = "write_changed"   // 值变化 ⇒ 必写
	telemetryDecisionHeartbeat = "write_heartbeat" // 距上次落库 ≥ 心跳 ⇒ 强制写
	telemetryDecisionSkip      = "skip"            // 值未变且未到心跳
)

// TelemetryWriteGateRowsTotal 按 decision 统计写入收窄的逐行决策。
//
// decision=skip 就是**真正没写进库**的行数（没有影子模式之后不再有"假想跳过"）。
var TelemetryWriteGateRowsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
	Name: "ehome_telemetry_write_gate_rows_total",
	Help: "unified_data row decisions of the L1 write gate, by decision",
}, []string{"decision"})

// TelemetryWriteGateTrackedFields 是收窄状态表当前跟踪的 (设备, 字段) 数量。
//
// 它是一张**进程内**表：多副本部署时每个副本各有一份（当前单容器，无此问题，
// 方案 §3.3「多实例」一行）。这个 gauge 用来发现状态表异常增长（设备/字段泄漏）
// 或意外清零（进程重启 ⇒ 首帧全量写，方案 §3.3「进程重启」一行）。
var TelemetryWriteGateTrackedFields = promauto.NewGauge(prometheus.GaugeOpts{
	Name: "ehome_telemetry_write_gate_tracked_fields",
	Help: "In-process (device, field) entries tracked by the L1 write gate",
})
