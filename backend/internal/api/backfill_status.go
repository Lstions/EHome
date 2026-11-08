package api

// backfill_status.go — 老数据回填进度接口（契约 docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §4.6）。
//
// 端点：GET /api/v1/system/backfill（免鉴权，与 /api/v1/system/maintenance 同级）
//
// ============ 为什么需要它 ============
//
// 回填是**后台长任务**（生产 3.42M 帧 / 20~40 分钟），且与 ready 解耦：
// 服务 ready 之后回填可能还在跑（契约 §4.6）。前端维护页此时已经放行，
// 用户在正常页面里完全看不到"系统还在搬老数据" —— 没有这个接口，回填期间
// 的高 IO / 慢查询就会被当成"系统变卡了"，而运维没有任何地方能确认它在进展。
//
// ============ 三个设计约束（都来自已踩过的坑）============
//
// **1) api 包不 import internal/backfill（沿用 maintenance.go 的窄接口范式）**
//
// 理由与维护页那份完全一致：api 是路由/协议层，backfill 是执行引擎层。
// 让前者依赖后者，等于把「HTTP 载荷形状」与「回填引擎的内部结构体」焊死 ——
// 引擎改一个字段名就会破前端契约，且 api 的测试要拖着整个 backfill 包
// （含 gorm/telemetry 依赖）一起编译。
// 代价是多一层镜像类型，必须用测试锁住 JSON key 集合
// （TestBackfillSnapshotJSONShapeMatchesContract），否则两边会静默漂移。
//
// **2) 载荷的每个 key 恒定出现，不用 omitempty**
//
// 契约给的形状是 state/percent/current_partition/frames_done/frames_total/
// eta_seconds/retention_permanent/errors 八项。这里刻意**全部不加 omitempty**：
//   · 前端做 `status.frames_total ?? "-"` 这种防御会在每个字段上重复八遍，
//     而 omission 又会与"值恰好是零值"混在一起 —— 0 到底表示"未知"还是"真的 0"
//     在客户端无法区分；
//   · 契约把 0 定义成"未知"（见 FramesTotal/ETASeconds 注释），既然 0 有明确
//     语义，就没有"不该出现"的场景，恒定出现才是稳定契约。
// 因此：frames_total=0 ⇒ 未知；eta_seconds=0 ⇒ 未知；errors=[] ⇒ 无错误。

import (
	"math"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// BackfillStatusPath 是回填进度端点路径（契约 §4.6）。
//
// 与 /api/v1/system/maintenance 同属「system 域只读探针」：免鉴权、不含业务数据。
// 用常量而不是散落的字面量：main.go 的注入、测试、将来可能的鉴权白名单要指向
// 同一个字符串，写错一处就变成 404 而没人发现。
const BackfillStatusPath = "/api/v1/system/backfill"

// 回填 State 字面量（与 internal/backfill 的 State* 常量**逐字一致**）。
//
// 为什么在 api 侧重复定义：见文件头约束 1（不 import backfill）。
// 一致性由 TestBackfillStateLiteralsMatchEngine 的跨包断言守着 —— 测试依赖
// 可以跨包，生产依赖不行。
const (
	// BackfillStateIdle 未开始（含"根本没有回填引擎"这一情形）。
	BackfillStateIdle = "idle"
	// BackfillStateRunning 正在回填。
	BackfillStateRunning = "running"
	// BackfillStateDone 全部完成。
	BackfillStateDone = "done"
	// BackfillStateFailed 上次失败（断点保留，可重试）。
	BackfillStateFailed = "failed"
	// BackfillStateDisabled 被环境变量关闭（运维应急阀）。
	BackfillStateDisabled = "disabled"
)

// BackfillSnapshot 是 GET /api/v1/system/backfill 的 data 载荷（契约 §4.6）。
//
// ⚠ 本类型是 internal/backfill.Progress 的**镜像**：字段名与 JSON tag 必须与
// 契约 §4.6 逐字一致。刻意比 Progress **少**两个字段：
//   - rows_done：契约载荷没有它（行数是内部对账量，对外无意义）；
//   - partitions：逐分区明细（运维排障用），不是进度条需要的形状。
//
// 需要它们时再走运维 CLI / 日志，不要往这个高频轮询的探针里塞大数组。
type BackfillSnapshot struct {
	// State 见 BackfillState* 常量。
	State string `json:"state"`
	// Percent 0..100，按分区时间跨度加权（引擎侧计算，单调不回退）。
	Percent float64 `json:"percent"`
	// CurrentPartition 当前正在回填的分区名，如 unified_data_202610。
	// 非 running 态为空串。
	CurrentPartition string `json:"current_partition"`
	// FramesDone 已回填帧数（跨全部分区累计）。
	FramesDone int64 `json:"frames_done"`
	// FramesTotal 总帧数；**0 = 未知**（精确总数要 count(DISTINCT device_id,
	// timestamp) 全表扫，成本高且对进度条无必要 —— 见引擎侧 recomputePercent 的论证）。
	FramesTotal int64 `json:"frames_total"`
	// ETASeconds 剩余秒数估算；**0 = 未知**（样本不足 / 非运行态 / 无法外推）。
	ETASeconds int64 `json:"eta_seconds"`
	// RetentionPermanent 当前是否为「永久保留」模式。
	//
	// 为什么进度接口要暴露它：回填完成后老 unified_data 分区是否 DROP 是**人工
	// 决策**（契约 §7 裁决 2），而永久保留模式下"删老数据"直接违背用户配置。
	// 运维在按下"释放 15 GB"之前必须能在界面上看到这一点。
	RetentionPermanent bool `json:"retention_permanent"`
	// Errors 运行中的非致命问题（配置非法 / 单帧编码失败等）。
	// **永远是数组**（空时为 []），不会序列化成 null。
	Errors []string `json:"errors"`
}

// BackfillSource 是回填进度接口读取进度的窄接口。
//
// 生产实现由 main.go 用 *backfill.Runner 适配（见 cmd/server/main.go 的
// backfillProgressSource）。测试用 fake。
// nil 安全：未注入时接口返回 state="idle"，**绝不 500** —— 回填没启用不是错误，
// 是常态（契约 §4.6 与任务要求 3）。
type BackfillSource interface {
	BackfillProgress() BackfillSnapshot
}

// BackfillSourceFunc 让普通函数直接充当 BackfillSource（便于 main.go 注入）。
type BackfillSourceFunc func() BackfillSnapshot

// BackfillProgress 实现 BackfillSource。
func (f BackfillSourceFunc) BackfillProgress() BackfillSnapshot {
	if f == nil {
		return idleBackfillSnapshot()
	}
	return f()
}

// idleBackfillSnapshot 是"没有回填可报告"时的标准载荷。
//
// 单独抽成函数而不是各调用点手写零值：Errors 必须是 [] 而不是 nil
// （nil 会序列化成 null，前端 .length 直接炸），这个细节只在一处维护。
func idleBackfillSnapshot() BackfillSnapshot {
	return BackfillSnapshot{State: BackfillStateIdle, Errors: []string{}}
}

// snapshotOfBackfill 读取回填进度；nil source 返回 idle 快照。
//
// 同时做两件归一化（都在 API 边界做，而不是要求引擎侧保证）：
//  1. State 为空 ⇒ idle（引擎未初始化/异常返回空串时不要把空串漏给前端）；
//  2. Errors 为 nil ⇒ []（见 idleBackfillSnapshot 的理由）。
func snapshotOfBackfill(src BackfillSource) BackfillSnapshot {
	if src == nil {
		return idleBackfillSnapshot()
	}
	snap := src.BackfillProgress()
	if snap.State == "" {
		snap.State = BackfillStateIdle
	}
	if snap.Errors == nil {
		snap.Errors = []string{}
	}
	return snap
}

// RegisterBackfillStatus 在给定引擎上注册回填进度端点。
//
// 刻意做成"注册函数"而不是塞进 SetupRoutes 的 option switch：
//   - SetupRoutes 的 option 机制是**具体类型**匹配（routes.go 的 switch），
//     加一个新 option 要改那个文件；而本端点只需要一个函数值，没必要动路由主文件；
//   - 调用点在 main.go 的回填装配块里 —— 那里才同时拿得到 Runner 与引擎，
//     把"谁提供进度"和"谁注册路由"放在同一个视野内，读代码时不用来回跳。
//
// ⚠ 调用时机：必须在 rootHandler.SetEngine(r) **之前**（r 还没开始 Serve）。
// gin 的路由树没有读锁，Serve 期间 AddRoute 是数据竞争 —— 这与维护页契约 §2
// 的红线是同一条。
func RegisterBackfillStatus(r *gin.Engine, src BackfillSource) {
	if r == nil {
		return
	}
	r.GET(BackfillStatusPath, func(c *gin.Context) {
		// 免鉴权：注册在 r 上（而不是 r.Group("/api/v1") + JWTAuthWithDB 那棵树里）。
		// 理由与 /api/v1/system/maintenance 一致：前端需要在任何鉴权状态下都能读进度；
		// 载荷只有阶段/计数/耗时，没有任何业务数据或凭据。
		Success(c, snapshotOfBackfill(src))
	})
}

// ==================== ETA 外推 ====================

// BackfillETATracker 从连续进度快照外推剩余秒数（并发安全）。
//
// **为什么 ETA 不在引擎里算**：internal/backfill 的 Progress.ETASeconds 字段
// 留了位置但引擎没有填（它没有"总量"概念，见 recomputePercent 的论证）。
// 而推算 ETA 需要的正是「时间 → 已处理行」这个序列，接口层每次轮询都天然
// 拿得到一个样本 —— 把状态放在读侧，引擎零改动，也不受"回填是否启用"影响。
//
// **为什么用行速率而不是帧速率**（任务要求 2）：
//
//	帧的实际工作量差异极大 —— device14 一帧 29 字段（29 行 unified_data），
//	device13 一帧 1 字段。同样处理 1000 帧，前者要扫 29000 行、后者 1000 行。
//	按帧速率外推会在跨设备/跨分区时给出量级错误的 ETA；行数是引擎真正消费的
//	工作量，与实际耗时同量纲。
//
// **为什么用累计平均而不是相邻两次采样**：
//
//	进度接口会被高频轮询（前端 2s 一次），相邻采样的 dt 很小，任何一次抖动
//	（一批刚好落库、一次 GC）都会把瞬时速率放大成几倍，ETA 在 30s 和 30min
//	之间跳。ETA 只需要量级正确，累计平均天然平滑且无需引入魔数窗口。
//	代价是对速率突变（如从大分区切到小分区）反应慢 —— 可接受。
type BackfillETATracker struct {
	mu sync.Mutex

	// active 表示基线有效（上一次 Observe 处于可外推状态）。
	active bool
	// baseRows/baseAt 是基线的已处理行数与时点。
	baseRows int64
	baseAt   time.Time
}

// NewBackfillETATracker 构造一个空的 ETA 推算器（首次 Observe 只建基线，返回 0）。
func NewBackfillETATracker() *BackfillETATracker {
	return &BackfillETATracker{}
}

// Reset 清空基线（回填重启 / 状态切走时调用，避免用上一轮的速率算这一轮）。
func (t *BackfillETATracker) Reset() {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.active = false
	t.baseRows = 0
	t.baseAt = time.Time{}
}

// Observe 喂入一个样本并返回当前 ETA（秒）；无法推算时返回 0。
//
// 入参用**引擎原始量**而不是 BackfillSnapshot：行数 rowsDone 不在对外载荷里
// （契约 §4.6 没有它），但外推必须用它，所以从注入点直接传。
//
// 返回 0 的情形（都是"未知"而不是"马上完成"，调用方据此显示"—"）：
//   - 非 running 态（idle/done/failed/disabled），并**清空基线**；
//   - percent 不在 (0,100) 开区间（0 = 还没开始，100 = 已完成/无法再细分）；
//   - 样本不足两枚，或两次之间行数没有增加（刚开始第一批 / 卡住）。
func (t *BackfillETATracker) Observe(state string, rowsDone int64, percent float64, now time.Time) int64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	// 非运行态必须清基线：下一次 running（例如失败后重试）的基准时点要重新取，
	// 否则会把"停机的那段时间"算进速率里，ETA 被压低成乐观值。
	if state != BackfillStateRunning || percent <= 0 || percent >= 100 || rowsDone <= 0 {
		t.active = false
		t.baseRows = 0
		t.baseAt = time.Time{}
		return 0
	}

	if !t.active {
		t.active = true
		t.baseRows = rowsDone
		t.baseAt = now
		return 0
	}

	elapsed := now.Sub(t.baseAt).Seconds()
	deltaRows := rowsDone - t.baseRows
	if elapsed <= 0 || deltaRows <= 0 {
		// 行数回退（进程重启后从 watermark 继续、计数从库里重读）也走这里：
		// 不重建基线会让速率长期为负 —— 交给下一次 Reset/非 running 兜底。
		return 0
	}

	rowsPerSecond := float64(deltaRows) / elapsed
	// 剩余行数由百分比反推：percent 是「已完成跨度 / 总跨度」，
	// 故 剩余 = 已处理 × (100-p)/p。这里假设行在分区内均匀分布 —— 对"还要多久"
	// 这个量级问题足够；不均匀带来的偏差远小于 ETA 本身的不确定性。
	remainingRows := float64(rowsDone) * (100 - percent) / percent
	eta := remainingRows / rowsPerSecond
	if math.IsNaN(eta) || math.IsInf(eta, 0) || eta < 0 {
		return 0
	}
	// 夹到一周：超过这个量级说明速率样本还没意义（例如刚跑完第一批，
	// 反推出的剩余大得离谱），给前端一个"未知"比给一个天文数字更有用。
	const maxETA = int64(7 * 24 * 3600)
	if eta > float64(maxETA) {
		return 0
	}
	return int64(math.Round(eta))
}
