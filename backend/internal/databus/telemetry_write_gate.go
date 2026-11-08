package databus

// telemetry_write_gate.go — L1「值不变不入库 + 心跳」（方案 §7 阶段1）。
//
// # 规则（方案 §3.1 原文；2026-11-07 起无条件生效）
//
// 对每台设备、每个字段维护「最后**已落库**值 + 落库时间」：
//   - 字段值变化            ⇒ 该行必须写；
//   - 距上次落库 ≥ 心跳间隔 ⇒ 强制写一帧（心跳，保证时间轴连续、图表不空档）；
//   - 否则                  ⇒ 跳过入库（但内存缓存/告警/自动化/WS 推送照常更新）。
//
// # 没有开关
//
// 2026-11-07 用户裁决「完成并打开 L1/L2，同时删除开关」：write_on_change 这个
// 布尔与其环境变量已**删除**，本文件不再有「影子模式」分支 —— filter() 永远返回
// 收窄后的行。理由是读侧口径（avg 聚合 / 行计数）已按 L1 生效改造过
// （internal/api 的 D1/D2/D3），留一个能关掉收窄的 kill-switch 只会让读侧变成错的。
//
// # 三个必须守住的语义
//
//  1. **写库成功才推进状态**。Skip 的判据是「与上次*已落库*值相同」，如果写入失败
//     也把状态推进到新值，那么"变化"会被永久判成"未变化"，该值此后再也不会落库
//     （方案 §3.3 缓存一致性）。因此 commit 只在 Create 成功后调用。
//  2. **首帧全量写**。状态表是进程内的，重启后为空 ⇒ 每个 (设备, 字段) 第一次见到
//     都必然写（方案 §3.3 进程重启）。这也是 decision=write_first 存在的原因：
//     它是可观测的"全量写了一遍"，不是异常。
//  3. **分片安全**。状态表按 (设备, 字段) 建键，而 bus 的分片保证「同一设备的事件
//     永远进同一个 shard」（shard.go 的 FNV 哈希 + per-device 顺序），所以每个
//     shard 各持一份状态表不会出现同一设备被两个 shard 交叉判定。

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/config"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// telemetryFieldKey 是状态表键：一台设备的一个物理量。
type telemetryFieldKey struct {
	deviceID uint
	sensor   string
}

// telemetryFieldState 是「最后成功落库的值 + 时间」。
type telemetryFieldState struct {
	value float64
	at    time.Time
}

// telemetryWritePolicy 是 L1 的运行时策略（由 config.TelemetryConfig 归一而来）。
//
// 只有一个字段：收窄本身已无条件生效，剩下的可调项就是心跳粒度。
type telemetryWritePolicy struct {
	// heartbeat 心跳间隔，永不为 0。
	heartbeat time.Duration
}

// defaultTelemetryWritePolicy 是零依赖的保守策略：默认心跳。
//
// 注意它**不是**「关掉收窄」的兜底 —— 收窄没有关闭态。它只保证忘记注入心跳时
// 用的是配置默认值（60s），而不是 0（0 会让每帧都判成心跳，收窄失效）。
func defaultTelemetryWritePolicy() telemetryWritePolicy {
	return telemetryWritePolicy{
		heartbeat: config.DefaultTelemetryHeartbeatSeconds * time.Second,
	}
}

// telemetryPolicyOverride 让测试固定策略。生产路径由 resolveTelemetryWritePolicy
// 从 config.Load() 读取（与 nodemgr.parserShardCount 同一个范式：本仓无热加载，
// 启动时读一次即可）。测试设置后必须 defer 复位 —— 包级变量会跨用例串味。
var telemetryPolicyOverride *telemetryWritePolicy

// resolveTelemetryWritePolicy 从配置解析 L1 策略。
func resolveTelemetryWritePolicy() telemetryWritePolicy {
	if telemetryPolicyOverride != nil {
		return *telemetryPolicyOverride
	}
	cfg := config.Load()
	if cfg == nil { // Load 从不返回 nil，这里只是防御
		return defaultTelemetryWritePolicy()
	}
	return telemetryWritePolicy{heartbeat: cfg.TelemetrySettings().Heartbeat()}
}

// telemetryFrameStat 是一帧的逐行决策统计（驱动指标与测试断言）。
type telemetryFrameStat struct {
	Total     int
	First     int
	Changed   int
	Heartbeat int
	Skipped   int
}

// writes 返回本帧**应当落库**的行数（= 实际落库行数，没有再分「假想值」）。
func (s telemetryFrameStat) writes() int { return s.First + s.Changed + s.Heartbeat }

// telemetryWriteGate 是 L1 收窄的决策器 + 进程内「最后已落库值」状态表。
//
// 零值不可用；用 newTelemetryWriteGate 构造。所有导出/包内方法都对 nil 接收者
// 安全（nil = 未装配，退化为全量写），这样测试里手工构造的 consumer 不会 panic。
type telemetryWriteGate struct {
	policy telemetryWritePolicy

	mu   sync.Mutex
	last map[telemetryFieldKey]telemetryFieldState
}

// newTelemetryWriteGate 构造收窄决策器。
func newTelemetryWriteGate(p telemetryWritePolicy) *telemetryWriteGate {
	if p.heartbeat <= 0 {
		p.heartbeat = defaultTelemetryWritePolicy().heartbeat
	}
	return &telemetryWriteGate{policy: p, last: make(map[telemetryFieldKey]telemetryFieldState)}
}

// telemetryWriteGateMaxTrackedFields 是状态表的硬上界。
//
// 为什么需要：状态表按 (设备, 字段) 增长，而设备行可能被硬删（purge）后其条目
// 无人回收。上界的作用是「内存不无界」，越界时按心跳的 10 倍窗口淘汰最旧条目；
// 淘汰只会让该字段下一帧被**多写一次**（safe direction），绝不会造成漏写。
const telemetryWriteGateMaxTrackedFields = 200000

// filter 决定本帧哪些行必须落库。
//
// 返回**真的要** INSERT 的行（records 的子集，保持原顺序），以及逐决策统计。
//
// nil 接收者（未装配）时返回 records 全量，统计标记为全量写 —— 这样「忘记注入」
// 只会让收窄不生效（多写），不会丢数据。
func (g *telemetryWriteGate) filter(deviceID uint, records []models.UnifiedData, at time.Time) (
	persist []models.UnifiedData, stat telemetryFrameStat) {

	stat.Total = len(records)
	if g == nil {
		stat.First = len(records) // 未装配时"每行都是首帧"，指标不会静默消失
		return records, stat
	}

	persist = make([]models.UnifiedData, 0, len(records))

	g.mu.Lock()
	for i := range records {
		rec := records[i]
		key := telemetryFieldKey{deviceID: deviceID, sensor: rec.SensorName}
		prev, seen := g.last[key]
		switch {
		case !seen:
			stat.First++
			persist = append(persist, rec)
		case rec.Value != prev.value:
			// NaN 与任何值都不相等（含 NaN 自身）⇒ 必然走这一支，绝不会被跳过。
			stat.Changed++
			persist = append(persist, rec)
		case at.Sub(prev.at) >= g.policy.heartbeat:
			stat.Heartbeat++
			persist = append(persist, rec)
		default:
			stat.Skipped++
		}
	}
	g.mu.Unlock()

	return persist, stat
}

// commit 在**写库成功后**推进「最后已落库值 + 时间」。
//
// 调用纪律：只在 Create 返回 nil 之后调用。写失败时调用会把新值记成"已落库"，
// 于是同一值的后续帧会被判成"未变化"而跳过 ⇒ 该值永久丢失（方案 §3.3）。
func (g *telemetryWriteGate) commit(deviceID uint, written []models.UnifiedData, at time.Time) {
	if g == nil || len(written) == 0 {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.last == nil {
		g.last = make(map[telemetryFieldKey]telemetryFieldState)
	}
	for i := range written {
		g.last[telemetryFieldKey{deviceID: deviceID, sensor: written[i].SensorName}] = telemetryFieldState{
			value: written[i].Value,
			at:    at,
		}
	}
	if len(g.last) > telemetryWriteGateMaxTrackedFields {
		g.evictLocked(at)
	}
	TelemetryWriteGateTrackedFields.Set(float64(len(g.last)))
}

// evictLocked 淘汰心跳窗口 10 倍之外的条目（只在越过上界时调用）。
// 淘汰的后果只有"下一帧多写一次"，不会漏写。
func (g *telemetryWriteGate) evictLocked(now time.Time) {
	cutoff := now.Add(-10 * g.policy.heartbeat)
	for key, st := range g.last {
		if st.at.Before(cutoff) {
			delete(g.last, key)
		}
	}
}

// reset 清空状态表（仅测试用：包级策略会跨用例串味）。
func (g *telemetryWriteGate) reset() {
	if g == nil {
		return
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	g.last = make(map[telemetryFieldKey]telemetryFieldState)
}

// record 把一帧的决策打进指标。
//
// 没有 mode 维度了（不再有影子/真跳之分）：decision 直接就是最终裁决。
func (g *telemetryWriteGate) record(stat telemetryFrameStat) {
	inc := func(decision string, n int) {
		if n <= 0 {
			return
		}
		TelemetryWriteGateRowsTotal.WithLabelValues(decision).Add(float64(n))
	}
	inc(telemetryDecisionFirst, stat.First)
	inc(telemetryDecisionChanged, stat.Changed)
	inc(telemetryDecisionHeartbeat, stat.Heartbeat)
	inc(telemetryDecisionSkip, stat.Skipped)
}

// EnvUnifiedWrite 是 unified_data 历史写入的停写开关（2026-11-08）。
//
// 默认 **true**（保持现状）。设为 false 后：
//
//	unified_data          停止 INSERT          ← 唯一应该停的
//	telemetry_frame       L2 帧表照常写入       ← 接手数据源
//	telemetry_rollup_1m   L3 分钟桶照常写入     ← 接手数据源
//	最新值缓存            照常更新               ← /latest-data 依赖它
//	告警/自动化/数据源健康 照常回调
//
// 这是「退休 unified_data 历史写入」的第一步：**只停写，不删表**。
// 已有数据保留 45 天作为过渡缓冲，任何遗漏的回落路径在过渡期内仍能工作。
const EnvUnifiedWrite = "EHOME_TELEMETRY_UNIFIED_WRITE"

// resolveUnifiedWriteEnabled 在**构造时**读取开关（不做运行时可写，避免 data race）。
//
// 解析失败或未设置 => true（保持现状）。**失败必须倾向于「继续写」**：
// 停错了会让数据不再落库，而多写一帧的代价远小于此。
func resolveUnifiedWriteEnabled() bool {
	v := strings.TrimSpace(os.Getenv(EnvUnifiedWrite))
	if v == "" {
		return true
	}
	enabled, err := strconv.ParseBool(v)
	if err != nil {
		logger.Warnf("databus: 无法解析 %s=%q（%v），按「继续写入」处理", EnvUnifiedWrite, v, err)
		return true
	}
	if !enabled {
		logger.Infof("databus: unified_data 历史写入已**停用**（%s=false）——"+
			"帧表/rollup/最新值缓存仍在工作，读取路径不受影响", EnvUnifiedWrite)
	}
	return enabled
}
