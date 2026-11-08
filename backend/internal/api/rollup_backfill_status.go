package api

// rollup_backfill_status.go — L3 rollup 历史回填的进度接口（task-22）。
//
// 端点：GET /api/v1/system/rollup-backfill（免鉴权，与 /api/v1/system/backfill 同级）
//
// ============ 为什么单独一个端点，而不是并进 /api/v1/system/backfill ============
//
// 两者的**进度单位根本不同**：帧表回填的单位是「月份分区 + 复合游标」，rollup 回填
// 的单位是「(设备, 日)」。塞进同一个载荷会让 percent 这个字段有两种含义
// （按分区时间跨度加权 vs 按单元计数），前端没法用一个进度条表达。
//
// 而且它们的生命周期不同：帧表回填是一次性的历史迁移（做完就永远 done），
// rollup 回填会在**每次新增设备/新增历史**时重新有活干。混在一起会让
// "done" 这个状态无法解释。
//
// 与 /api/v1/system/maintenance 同属「system 域只读探针」：免鉴权、不含业务数据。
//
// ============ 为什么不 import datalifecycle ============
//
// 与 backfill_status.go 同款约束：api 是路由/协议层，datalifecycle 是执行引擎层。
// 本文件只定义一个**镜像结构体** + 一个窄函数源，由 main.go 做适配。
// 这样引擎改一个字段名只会让 main.go 编译失败（一处、显式），而不是悄悄改变
// 对外 JSON 契约。

import "github.com/gin-gonic/gin"

// RollupBackfillStatusPath 是 rollup 回填进度端点路径。
const RollupBackfillStatusPath = "/api/v1/system/rollup-backfill"

// RollupBackfillSnapshot 是 GET /api/v1/system/rollup-backfill 的 data 载荷。
//
// ⚠ 本类型是 datalifecycle.RollupBackfillProgress 的**镜像**：字段语义必须一致。
type RollupBackfillSnapshot struct {
	// State 见 BackfillState* 常量（复用同一套字面量，语义相同）。
	State string `json:"state"`
	// UnitsTotal 总单元数（单元 = 一台设备的一天）。
	UnitsTotal int `json:"units_total"`
	// UnitsDone 已完成单元数（含跳过的）。
	UnitsDone int `json:"units_done"`
	// UnitsSkipped 因"已回填过"而跳过的单元数 —— 断点续跑的直接证据。
	UnitsSkipped int64 `json:"units_skipped"`
	// BucketsWritten 已写入的分钟桶总数（= rollup 行数）。
	BucketsWritten int64 `json:"buckets_written"`
	// CurrentDay 当前处理的日期（YYYY-MM-DD）；非 running 态为空串。
	CurrentDay string `json:"current_day"`
	// CurrentDevice 当前处理的设备 ID；非 running 态为 0。
	CurrentDevice uint `json:"current_device"`
	// Errors 运行中的非致命问题。**永远是数组**（空时为 []），不会序列化成 null。
	Errors []string `json:"errors"`
}

// RollupBackfillSource 是进度接口读取进度的窄函数源。
//
// nil 安全：未注入时返回 state=idle，**绝不 500** —— 回填没启用不是错误，是常态。
type RollupBackfillSource func() RollupBackfillSnapshot

// snapshotOfRollupBackfill 把 nil 源归一成 idle 快照（Errors 必须是 [] 而非 null）。
func snapshotOfRollupBackfill(src RollupBackfillSource) RollupBackfillSnapshot {
	if src == nil {
		return idleRollupBackfillSnapshot()
	}
	snap := src()
	if snap.Errors == nil {
		snap.Errors = []string{}
	}
	if snap.State == "" {
		snap.State = BackfillStateIdle
	}
	return snap
}

// idleRollupBackfillSnapshot 是「没有回填引擎」时的快照。
func idleRollupBackfillSnapshot() RollupBackfillSnapshot {
	return RollupBackfillSnapshot{State: BackfillStateIdle, Errors: []string{}}
}

// RegisterRollupBackfillStatus 把进度端点挂到引擎上。
//
// ⚠ 必须在 rootHandler.SetEngine 之前调用：gin 的路由树没有读锁，Serve 期间
// AddRoute 是数据竞争（与维护页契约 §2 的红线同一条）。
func RegisterRollupBackfillStatus(r *gin.Engine, src RollupBackfillSource) {
	if r == nil {
		return
	}
	// 免鉴权：注册在 r 上（而不是鉴权组里）。前端需要在任何鉴权状态下都能读进度；
	// 载荷只有状态/计数，没有任何业务数据或凭据。
	r.GET(RollupBackfillStatusPath, func(c *gin.Context) {
		Success(c, snapshotOfRollupBackfill(src))
	})
}
