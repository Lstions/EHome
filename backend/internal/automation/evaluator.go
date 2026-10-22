// Package automation 实现自动化策略引擎 (设计/自动化策略引擎方案.md v0.1)。
//
// 挂接点裁决 (同 alert §5.1.2): 复用 SensorParserConsumer 解析后回调,
// 与 alert.Evaluator 并列不合并 — 告警=人感知只通知 (firing/resolved),
// 策略=系统处置 (armed→triggered→cooldown 三态)。求值器只判定"是否触发",
// 触发后的冷却/熔断/确认分流/动作执行全部委托 Planner, 保持单向数据流。
package automation

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
	"ehome/backend/internal/sun"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

const maxWindowSamples = 1000

// cooldownFor 是 cooldown_sec 语义的唯一判定处: 冷却期时长, 返回 0 表示不冷却。
//
// 为什么必须是唯一实现 (负债 D-5): 该字段曾在两条读路径上语义相反 —— evaluator
// 把 0 当"用默认 300s"(有冷却), planner 把 0 当"不冷却"(>0 才判定)。同一个用户
// 配置在自动触发与手动触发下行为分叉, 根因就是"同一语义有两份实现"。
//
// 语义裁决: 0 = 不冷却 (与前端 :min="0" 的引导、与 MaxDailyExec 的 0=不限同族)。
// 负数属无效配置 (API 契约拒绝 [-1]), 与 0 同样按不冷却处理 (fail-open 不误触)。
// "未配置 = 300" 的默认 300 由应用层在创建时写入 (handler_automation.go 的
// Create), 不在这里兜底 —— 兜底会让"显式 0"再次无法表达。
func cooldownFor(rule models.AutomationRule) time.Duration {
	if rule.CooldownSec <= 0 {
		return 0
	}
	return time.Duration(rule.CooldownSec) * time.Second
}

// sample 滑动窗口单点 (同 alert 先例)。
type sample struct {
	at        time.Time
	satisfied bool
	// identity 是窗口来源身份（S1/S2：窗口不得跨来源拼接）。
	// 存量单设备 = {kind:0, edgeDeviceID=Evaluate 入参}；来源组见 source.go。
	identity sourceIdentity
}

// TriggerEvent 触发事件: 求值器产出, Planner 消费。
//
// 来源证据（M0，方案 §12.2.1 / F-9）只由求值器经 newTriggerEvent 填写；planner F4
// 只认这些字段，**不得**回读规则配置列（切换后配置列不反映"这次触发是谁"）。
type TriggerEvent struct {
	Rule       models.AutomationRule
	Value      float64 // 触发时值 (sensor_threshold)
	At         time.Time
	WindowEdge string // time_window: enter|exit (sensor_threshold 为 "")

	// ── 来源证据 ──
	SourceKind         string    // ""=存量单设备；source_group=来源组
	SourceGroupID      uint      // 来源组 ID（SourceKind=source_group 时非 0）
	SourceEdgeDeviceID uint      // 触发帧的物理设备（存量=Evaluate 入参；组=触发成员）
	TransitionSeq      uint64    // 触发时来源快照的切换序号（一致性 token）
	SourceAt           time.Time // 该成员值的时间戳（F4 staleness 判据）

	// membersJSON 是 composite 触发时的分量快照（automation_events.members_json 审计）。
	// 非导出：不是对外契约字段，只由 newTriggerEvent 填充、planner 写事件列。
	membersJSON string
}

// TriggerHandler Planner 消费接口 (避免 automation→service 编译期反向依赖)。
type TriggerHandler interface {
	HandleTrigger(ev TriggerEvent)
}

// Evaluator 策略求值器: 规则缓存 + 每规则滑动窗口 + armed/triggered 状态机。
//
// 状态机 (裁决 5, 与告警不同):
//
//	armed → triggered: 连续满足 DurationSec (DurationSec=0 直通)
//	triggered → armed: CooldownSec 到期 (时间维度, 不是条件复位!)
//
// ⚠️ armed 恢复条件是冷却到期, 不是条件复位 — "光照弱"持续整夜, 触发一次
// 开灯后进入冷却, 冷却到期后若条件仍满足可再次触发; 若等条件复位(天亮),
// 则次日黄昏无法再触发。这是与告警 firing→resolved 最本质的区别。
type Evaluator struct {
	db      *gorm.DB
	handler TriggerHandler

	mu sync.RWMutex
	// rules 规则缓存 (仅 enabled 且 trigger_type IN (sensor_threshold, time_window)), key: rule ID。
	rules map[uint]models.AutomationRule
	// windows 每规则滑动窗口, key: rule ID (sensor_threshold 专用)。
	windows map[uint][]sample
	// triggered 规则当前是否处于 triggered 态 (冷却中)。
	triggered map[uint]time.Time // rule ID → 触发时刻
	// windowStates time_window 规则上次 tick 是否在窗口内 (rule ID → wasInside)。
	// 重启后清空 — 首次 tick 以当前是否在窗口内为基准 (保守: enter 规则首次 tick
	// 若在窗口内则触发, exit 规则首次 tick 若在窗口内则不触发)。
	windowStates map[uint]bool

	// sunStates 记录 sun_event 规则"本节点本地日是否已触发过" (rule ID → 本地日期)。
	//
	// 为什么是日期而不是 bool: 日出日落是瞬时点事件, 1 分钟 tick 会在事件时刻之后
	// 的每一分钟命中同一个事件。记录"已触发的本地日期"后, 日期翻转即天然表示
	// "新一日可再触发", 不需要额外的跨日清理逻辑 (方案 §6.2 状态机)。
	//
	// 重启后清空 + **首 tick 保守不触发** (见 evalSunEventRule):
	// 与 windowStates 的区间边沿语义不同, 点事件无法区分"重启前已触发"与
	// "重启错过了", 故一律按保守处理 (方案 §6.2)。
	sunStates map[uint]string

	// sunCache 是日出日落计算的进程内缓存 (key 含 lat/lon/tzName/localDate/kind)。
	// 纯内存, 重启后首次 tick 重算 —— 重算成本是一次纯函数调用, 无外部依赖
	// (方案 §3.1.4: 与 windowStates 重启清空同族, 可接受)。
	sunCache *sun.Cache

	// sunLocationErr 记录"当前缓存里哪些 sun_event 规则解析坐标/时区失败",
	// 用于**每天只落一条**审计 (防刷量), key=ruleID → 已落审计的本地日期。
	sunLocationAudit map[uint]string

	// firstSunTick 标记"本进程尚未执行过任何 sun_event tick"。
	//
	// 语义 (方案 §6.2, A-3 钉死): 首个 tick 只**登记状态、不触发**。理由:
	// 重启后无法区分"事件已过且已触发过"与"重启错过了事件", 而冷却锚点
	// last_triggered_at 已在 rebuildCooldowns 恢复 —— 若当日已触发过, 冷却会拦;
	// 若未触发过, 说明重启发生在事件时刻之前, 此时 now >= eventTime 不成立。
	// 故"保守不触发"与锚点恢复配合后不会漏触发真实事件。
	firstSunTick bool

	// planProvider 是数据字段解析层（sensorfield）控制面（设计 §7.2）。
	// nil = 未接线：字面量继续走历史 matchField（G-6 逐字节等价），特殊形态
	// fail-closed（不触发 + 审计）。由 main.go 经 SetPlanProvider 注入。
	planProvider PlanProvider

	// plans 是编译产物旁路缓存，key=(ruleID,slot,deviceID)（不挂 rule 结构体，
	// 防 models→sensorfield 包环；见 selector.go 文件头裁决）。
	plans *planCache

	// sourceResolver 来源组解析器（sourceref.Registry 注入；nil=未接线）。
	// 来源组规则在 nil 时 fail-closed 并落 source_resolver_not_configured 审计，
	// **不得**静默跳过（否则上线顺序错误=静默失效）。存量规则不依赖它。
	sourceResolver SourceResolver
	// latestValueBySensorFn 是复合组逐成员取值的注入点（生产=api.LatestValueBySensor）。
	// 与 planner 的同名注入点语义一致；求值器只用它做 composite 分量读取。
	latestValueBySensorFn func(deviceID uint, sensorName string) (models.UnifiedData, bool)
	// deviceIntervalFn 设备采集间隔（S8 gap 上限=2×间隔；nil/0 ⇒ 不启用 S8）。
	deviceIntervalFn func(edgeDeviceID uint) time.Duration
	// rulesByGroup 是 groupID → ruleID 反向索引（切换钩子按组清窗）。
	rulesByGroup map[uint][]uint
	// sourceAudit 是来源失败审计节流表（key: ruleID+result+reason）。
	sourceAudit map[sourceAuditKey]time.Time

	// auditMu/unresolvedAudit 是"无法求值"审计的 5 分钟节流表
	// （key: ruleID + selectorHash，窄复核要求按 selector 维度去重）。
	auditMu         sync.Mutex
	unresolvedAudit map[unresolvedKey]time.Time
	// statsMu/ruleStats 是 ehome_rule_never_resolved / _never_fired 的进程内口径
	// （快照持久列由 impl-wiring 的 models 批次提供；本批语义一致）。
	statsMu   sync.Mutex
	ruleStats map[uint]*ruleStat

	stopCh chan struct{}
	once   sync.Once
}

// NewEvaluator 构造求值器并全量加载 enabled 规则缓存。
func NewEvaluator(db *gorm.DB, handler TriggerHandler) *Evaluator {
	e := &Evaluator{
		db:               db,
		handler:          handler,
		rules:            make(map[uint]models.AutomationRule),
		windows:          make(map[uint][]sample),
		triggered:        make(map[uint]time.Time),
		windowStates:     make(map[uint]bool),
		sunStates:        make(map[uint]string),
		sunCache:         sun.NewCache(),
		sunLocationAudit: make(map[uint]string),
		firstSunTick:     true,
		plans:            newPlanCache(),
		unresolvedAudit:  make(map[unresolvedKey]time.Time),
		sourceAudit:      make(map[sourceAuditKey]time.Time),
		rulesByGroup:     make(map[uint][]uint),
		ruleStats:        make(map[uint]*ruleStat),
		stopCh:           make(chan struct{}),
	}
	e.LoadRules()
	e.rebuildCooldowns()
	return e
}

// rebuildCooldowns 启动重建冷却状态 (H1 修复, 方案 §9 R2): triggered map 纯内存,
// 重启清空后冷却期规则被误判 armed, 条件仍满足时立即重触发。
//
// 清理前置条件 A (docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md):
// 重建的【唯一持久锚点】是 automation_rules.last_triggered_at, 与 automation_events
// 【物理解耦】。旧实现直接查 "SELECT rule_id, MAX(triggered_at) FROM automation_events
// WHERE result='executed'", 于是事件表一旦启用保留策略清理, 被删掉 executed 行的规则
// 在重启后被当作"从未触发"(armed) → 条件仍满足时立即重触发 → 多发真实设备动作。
// 那不是"丢审计", 是"改变系统行为", 所以锚点必须落在清理器碰不到的地方。
//
// 兼容既有数据: 锚点列为空而事件表有历史的规则, 由 backfillCooldownAnchors 一次性回填
// (锚点列上线前就存在的触发历史), 之后锚点自持。
func (e *Evaluator) rebuildCooldowns() {
	var rules []models.AutomationRule
	if err := e.db.Select("id", "last_triggered_at").Find(&rules).Error; err != nil {
		logger.Warn("automation: failed to load cooldown anchors", "error", err)
		return
	}
	var needBackfill []uint
	e.mu.Lock()
	for _, r := range rules {
		if r.LastTriggeredAt != nil {
			e.triggered[r.ID] = *r.LastTriggeredAt
			continue
		}
		needBackfill = append(needBackfill, r.ID)
	}
	e.mu.Unlock()

	if len(needBackfill) == 0 {
		return
	}
	e.backfillCooldownAnchors(needBackfill)
}

// backfillCooldownAnchors 一次性迁移: 对锚点为空的规则, 用事件表历史
// (MAX(triggered_at) WHERE result='executed') 回填 automation_rules.last_triggered_at。
//
// 两条硬约束 (缺一即让前置条件 A 白做):
//  1. 【只填空】—— 已有锚点的规则绝不覆盖。写成无条件 UPDATE ... = (SELECT MAX(...))
//     会让事件表的清理重新影响冷却, 等于把缺陷搬回来。
//  2. 【先库后存】—— 只有 UPDATE 真正落库成功才写内存 triggered; 且 UPDATE 带
//     "last_triggered_at IS NULL" 条件, 与并发触发的锚点写入互不覆盖。
func (e *Evaluator) backfillCooldownAnchors(ruleIDs []uint) {
	rows := []struct {
		RuleID uint
		MaxAt  *string // SQLite MAX(timestamp) 返回字符串, 需手动解析
	}{}
	if err := e.db.Model(&models.AutomationEvent{}).
		Select("rule_id, MAX(triggered_at) AS max_at").
		Where("rule_id IN ? AND result = ?", ruleIDs, models.AutomationResultExecuted).
		Group("rule_id").Scan(&rows).Error; err != nil {
		logger.Warn("automation: failed to backfill cooldown anchors", "error", err)
		return
	}
	for _, r := range rows {
		if r.MaxAt == nil {
			continue
		}
		at, err := parseDBTime(*r.MaxAt)
		if err != nil {
			logger.Warn("automation: backfill cooldown anchor parse time failed",
				"rule_id", r.RuleID, "raw", *r.MaxAt, "error", err)
			continue
		}
		if err := e.db.Model(&models.AutomationRule{}).
			Where("id = ? AND last_triggered_at IS NULL", r.RuleID).
			Update("last_triggered_at", at).Error; err != nil {
			logger.Warn("automation: backfill cooldown anchor failed",
				"rule_id", r.RuleID, "error", err)
			continue
		}
		e.mu.Lock()
		e.triggered[r.RuleID] = at
		e.mu.Unlock()
	}
}

// parseDBTime 解析 SQLite 返回的时间字符串 (gorm sqlite driver 写 time.Time
// 的默认格式为 RFC3339Nano, 但 MAX() 聚合可能返回空格分隔格式, 逐一尝试)。
func parseDBTime(s string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		"2006-01-02 15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999Z07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	var err error
	var t time.Time
	for _, l := range layouts {
		if t, err = time.Parse(l, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, err
}

// LoadRules 全量加载 enabled 的 sensor_threshold + time_window + sun_event 规则
// (CRUD 写路径经 Invalidate 即时调用; Start 兜底刷新仅作保险)。
// time_window / sun_event 规则不挂传感器回调, 由**同一个** ticker 求值 (方案 §3.1.5)。
//
// 清理语义 (收口既有缺陷 A-17): 规则的窗口/日出日落状态必须按**内容白名单**
// 清理, 而不是只在"规则从缓存移除"时清理。原因: 规则仍在缓存但窗口字段被改
// (例如 08:00→09:00) 时, 旧状态会让边沿判定用错误的基准。
//
// ⚠️ 必须**显式白名单**, 不能"任何差异都清": Start() 每 30s 兜底调用本函数,
// 按 UpdatedAt 或"全部字段"比较会让无关字段的漂移每 30 秒清空一次状态,
// 边沿反复触发 —— 比原缺陷更危险 (方案 §3.1.5 末段)。
func (e *Evaluator) LoadRules() {
	var rules []models.AutomationRule
	if err := e.db.Where("enabled = ? AND trigger_type IN ?",
		true, []string{
			models.AutomationTriggerSensorThreshold,
			models.AutomationTriggerTimeWindow,
			models.AutomationTriggerSunEvent,
		}).Find(&rules).Error; err != nil {
		logger.Warn("automation: failed to load rules", "error", err)
		return
	}
	next := make(map[uint]models.AutomationRule, len(rules))
	for _, r := range rules {
		next[r.ID] = r
	}
	e.mu.Lock()
	prev := e.rules
	e.rules = next
	// ── 求值状态清理（分载体，窄复核 R2-followup 2026-10-21）──
	//
	// sensor_threshold 的窗口样本存的是"旧配置下算出的 satisfied 布尔"
	// （sample{at,satisfied}，无原始值 ⇒ 无法重算），因此触发语义（阈值/比较符/
	// duration/条件/selector/绑定设备/触发类型）任一变化都必须清窗口，否则：
	//   ① 改阈值/比较符 ⇒ 旧 satisfied 继续生效（漏触发/误触发）；
	//   ② 缩短 DurationSec ⇒ 凭旧样本立即满足（隐含误触发）；
	//   ③ 改绑定设备 ⇒ **窗口不按设备分区**，A 的连续满足 + B 的单条满足可凑出
	//      触发（真误触发，窄复核 F1#⑤）；
	//   ④ conditions_json 变化 ⇒ 样本来自旧条件门控（求解器条件不满足就不写窗口）；
	//   ⑤ selector 变化 ⇒ satisfied 来自旧物理量；
	//   ⑥ TriggerType 切换 ⇒ 旧窗口在切回时会复活。
	//
	// ⚠ 载体必须独立：sameWindowStateFields 服务的是 time_window/sun 的
	// windowStates/sunStates；把传感器字段塞进去会因无关字段漂移误清边沿态
	// （A-17 同类风险）。故这里用独立比较器 sameSensorTriggerState。
	//
	// 级联（F3 裁决）：**triggered（冷却锚）保留**。清它等于绕过 CooldownSec
	// 重复下发真实设备动作；保留后行为是"冷却到期后重新积累 DurationSec"（安全方向）。
	// 白名单**排除** UpdatedAt/Name/CooldownSec/MaxDailyExec/动作集合：它们是簿记
	// 字段（last_triggered_at 也会 bump UpdatedAt），纳入会导致 30s 兜底刷新每 30s
	// 清一次窗口 ⇒ DurationSec 永不满足（A-17 教训的镜像，漏触发方向）。
	for id := range e.windows {
		old, wasPresent := prev[id]
		nextRule, stillPresent := next[id]
		if !stillPresent || !wasPresent || !sameSensorTriggerState(old, nextRule) {
			delete(e.windows, id)
		}
	}
	for id := range e.triggered {
		if _, ok := next[id]; !ok {
			delete(e.triggered, id)
		}
	}
	// 状态清理: 规则移除 → 清; 规则仍在但**影响求值的字段**变化 → 也清。
	for id := range e.windowStates {
		if old, ok := prev[id]; !ok || !sameWindowStateFields(old, next[id]) {
			delete(e.windowStates, id)
		}
	}
	for id := range e.sunStates {
		if old, ok := prev[id]; !ok || !sameWindowStateFields(old, next[id]) {
			delete(e.sunStates, id)
		}
	}
	for id := range e.sunLocationAudit {
		if _, ok := next[id]; !ok {
			delete(e.sunLocationAudit, id)
		}
	}
	// 来源组反向索引：groupID → 规则集（切换钩子按组清窗；S4 保留冷却锚）。
	byGroup := make(map[uint][]uint)
	for id, r := range next {
		if ref := r.SourceRef(); ref.IsGroup() {
			byGroup[ref.GroupID] = append(byGroup[ref.GroupID], id)
		}
	}
	e.rulesByGroup = byGroup
	e.mu.Unlock()
	// 设计 §7.2：LoadRules/Invalidate 后清空编译产物（selector/epoch 变化惰性重编译）。
	// 兜底刷新每 30s 一次，重编译是纯内存操作（零 DB），热路径仍靠缓存。
	e.plans.reset()
	// 规则级指标口径随规则集合重建（保留仍存在规则的"已解析/已触发"进度）。
	e.rebuildRuleStats(next)
	// 清理已删除规则的审计节流条目，避免 map 无界增长。
	e.auditMu.Lock()
	for key := range e.unresolvedAudit {
		if _, ok := next[key.ruleID]; !ok {
			delete(e.unresolvedAudit, key)
		}
	}
	for key := range e.sourceAudit {
		if _, ok := next[key.ruleID]; !ok {
			delete(e.sourceAudit, key)
		}
	}
	e.auditMu.Unlock()
}

// sameSensorTriggerState 是 sensor_threshold 求值状态的**内容白名单**（窄复核 R2）。
//
// 只含"决定 satisfied 布尔是否有意义"的字段；排除一切簿记字段
// （UpdatedAt / Name / CooldownSec / MaxDailyExec / 动作集合 / last_triggered_at）。
// TriggerEdgeDeviceID 必须在列：窗口按 ruleID 存、不按设备分区（F1#⑤ 真误触发）。
// TriggerType 必须在列：类型切换后旧窗口不得在切回时复活。
func sameSensorTriggerState(prev, next models.AutomationRule) bool {
	return prev.TriggerType == next.TriggerType &&
		prev.TriggerSensorName == next.TriggerSensorName &&
		prev.TriggerComparator == next.TriggerComparator &&
		prev.TriggerThreshold == next.TriggerThreshold &&
		prev.TriggerDurationSec == next.TriggerDurationSec &&
		prev.TriggerEdgeDeviceID == next.TriggerEdgeDeviceID &&
		prev.ConditionsJSON == next.ConditionsJSON &&
		// 来源绑定（M0）：绑组/换组/换锚成员都改变窗口语义 ⇒ 必须清窗。
		sourceKindEq(prev.SourceKind, next.SourceKind) &&
		uintPtrEq(prev.SourceGroupID, next.SourceGroupID)
}

// sameWindowStateFields 是"影响窗口/日出日落判定"的字段**白名单**比较 (A-17)。
//
// 任一字段变化即返回 false ⇒ 调用方清除该规则的窗口状态与日出日落状态。
// 白名单必须**显式枚举** (方案 §3.1.5): 用 reflect.DeepEqual 比整个结构体会把
// UpdatedAt、Name 等非语义字段的漂移也算成变化 —— 而 LoadRules 每 30 秒被兜底
// 调用一次, 那会让状态每 30 秒清空, 边沿反复触发 (比原缺陷更危险)。
//
// 为什么这些字段: 它们全部直接参与 evalWindowRule / evalSunEventRule 的判定。
//   - TriggerType: 规则在两个时钟驱动路径之间迁移时, 旧状态语义完全不适用;
//   - TriggerWindowStart/End/Edge: 窗口几何;
//   - TriggerSunEventKind/TriggerSunOffsetMin/lat/lon/tz: 日出日落时刻与归日;
//   - Enabled: 停用再启用后不应继承停用前的边沿状态。
//
// 刻意**不含** UpdatedAt / Name / CooldownSec / MaxDailyExec / 动作集合:
// 它们不影响"上一次 tick 在不在窗口内"这一状态的语义 (冷却与熔断各自有独立
// 状态与判定, 不依赖 windowStates)。
func sameWindowStateFields(a, b models.AutomationRule) bool {
	return a.TriggerType == b.TriggerType &&
		a.TriggerWindowStart == b.TriggerWindowStart &&
		a.TriggerWindowEnd == b.TriggerWindowEnd &&
		a.TriggerWindowEdge == b.TriggerWindowEdge &&
		a.TriggerSunKind == b.TriggerSunKind &&
		a.TriggerSunOffsetMin == b.TriggerSunOffsetMin &&
		floatPtrEq(a.Latitude, b.Latitude) &&
		floatPtrEq(a.Longitude, b.Longitude) &&
		strPtrEq(a.Timezone, b.Timezone) &&
		a.Enabled == b.Enabled
}

func floatPtrEq(a, b *float64) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func strPtrEq(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Invalidate CRUD 写路径直接失效重载缓存。
func (e *Evaluator) Invalidate() {
	e.LoadRules()
}

// SetPlanProvider 注入数据字段解析层控制面（设计 §7.2 / §9.2#1/#2）。
//
// nil 是**显式支持的合法状态**：既有测试与未接线部署不注入时，求值继续走历史
// matchField（G-6：纯字面量行为逐字节不变）；特殊形态在没有解析层时无法求值，
// 按 fail-closed 不触发并落审计（不猜测、不降级为字面量，设计 §11）。
func (e *Evaluator) SetPlanProvider(p PlanProvider) {
	e.mu.Lock()
	e.planProvider = p
	e.mu.Unlock()
	e.plans.reset()
}

// planProviderSnapshot 读 planProvider（Evaluate 会在多 shard 并发调用）。
func (e *Evaluator) planProviderSnapshot() PlanProvider {
	if c := planProviderSnapshotHook.Load(); c != nil { // 测试注入计数；生产恒 nil
		c.Add(1)
	}
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.planProvider
}

// Start 启动兜底刷新 goroutine (30s 周期, 仅作保险)。
func (e *Evaluator) Start() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.LoadRules()
			}
		}
	}()
}

// Stop 停止兜底刷新 + time_window ticker。
func (e *Evaluator) Stop() {
	e.once.Do(func() { close(e.stopCh) })
}

// windowTickEnvVar 是 time_window 求值周期的环境变量名。
const windowTickEnvVar = "EHOME_WINDOW_TICK_INTERVAL"

// WindowTickInterval 返回 time_window 触发器的求值周期。
//
// 生产默认 1 分钟：time_window 是时钟驱动的边沿触发器，语义上就是"每分钟看一眼
// 窗口边界"。收紧它只是增加无谓的求值次数，没有任何产品收益，因此默认值不动。
//
// 允许环境变量覆盖的唯一原因是仿真套件（2026-10-04 性能优化）：
// 140 个场景里有一批必须"等到下一个 tick"才能观测边沿，1 分钟周期让它们各自
// 真等 30~180s —— 实测全量 781s 里 605s（77%）花在这几条上。
// 把周期调小后它们全部回到秒级。
//
// 为什么不改变判据：走的是**同一条** evalTimeWindows 路径、同一个时间源
// （time.Now()），只是更频繁地求值。对"边沿是否只触发一次""冷却是否被遵守"
// 这类不变式，求值更频繁只会更容易暴露错误实现（一个"每 tick 都刷"的 bug
// 会更早被观测到），不会放松任何断言。
//
// 解析失败或非正值一律回落默认值：这个变量不该有让服务起不来的能力。
func WindowTickInterval() time.Duration {
	if raw := strings.TrimSpace(os.Getenv(windowTickEnvVar)); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		} else {
			logger.Warnf("automation: 忽略非法的 %s=%q（应为正 duration，如 2s），回落 %s",
				windowTickEnvVar, raw, time.Minute)
		}
	}
	return time.Minute
}

// StartWindowTicker 启动 time_window 触发器独立求值 ticker（周期见 WindowTickInterval）。
// 与 sensor_threshold 的 Evaluate 路径完全独立 — 时钟驱动不挂传感器解析回调。
// ctx 取消时优雅退出 (main.go 接线用)。
func (e *Evaluator) StartWindowTicker(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(WindowTickInterval())
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-e.stopCh:
				return
			case <-ticker.C:
				e.evalTimeWindows(time.Now())
			}
		}
	}()
}

// EvaluateClockRulesAt 以**注入时刻**执行一次时钟驱动规则求值 (time_window + sun_event)。
//
// 与 StartWindowTicker 走的是**同一条** evalTimeWindows 路径、同一个状态机，唯一差别
// 是时间源由调用方给定。存在的理由与 windowTickEnvVar 同族 (见 WindowTickInterval 注释):
// 时钟驱动规则要么真等 1 分钟 tick、要么等到真实日出时刻，端到端/仿真测试无法承受。
// 注入时刻只影响"何时看一眼"，不改变任何判据（边沿仍只触发一次、每日本地日仍限一次）。
//
// 生产代码不得调用它 —— 生产路径的唯一入口是 StartWindowTicker。
func (e *Evaluator) EvaluateClockRulesAt(now time.Time) {
	e.evalTimeWindows(now)
}

// evalTimeWindows 对所有时钟驱动规则 (time_window + sun_event) 执行一次 tick 求值。
// 由 StartWindowTicker 每 1min 调用; 时间源一律用后端本地时间 (time.Now())。
//
// sun_event **复用本 ticker, 不新增 ticker** (方案 §3.1.5): 新增 ticker 会让两条
// 时钟驱动路径的求值时刻错开, 冷却判定与日熔断的"当日"口径出现竞态。
func (e *Evaluator) evalTimeWindows(now time.Time) {
	e.mu.RLock()
	var windowRules, sunRules []models.AutomationRule
	for _, r := range e.rules {
		switch r.TriggerType {
		case models.AutomationTriggerTimeWindow:
			windowRules = append(windowRules, r)
		case models.AutomationTriggerSunEvent:
			sunRules = append(sunRules, r)
		}
	}
	// 首个 sun_event tick 的"保守不触发"标记在此原子翻转: 本 tick 内所有规则
	// 都按"首 tick"处理, 下一个 tick 起正常求值。
	firstSunTick := e.firstSunTick
	if len(sunRules) > 0 {
		e.firstSunTick = false
	}
	e.mu.RUnlock()

	for _, rule := range windowRules {
		e.evalWindowRule(rule, now)
	}
	for _, rule := range sunRules {
		e.evalSunEventRule(rule, now, firstSunTick)
	}
}

// evalSunEventRule 单条 sun_event 规则求值 (方案 §6.2 状态机)。
//
// 状态机:
//
//	未求值 ──(tick 且 now >= eventTime+offset)──▶ 已触发
//	未求值 ──(tick 且 now <  eventTime+offset)──▶ 未求值
//	已触发 ──(节点本地日期翻转)─────────────────▶ 未求值 (新一日)
//	任意   ──(极昼/极夜)───────────────────────▶ 本日不触发 + 显式审计
//
// 三条硬约束:
//  1. **首 tick 保守不触发** (firstSunTick): 重启后无法区分"事件已过且已触发"
//     与"重启错过了", 一律不补触发 (方案 §6.2)。A-3 门禁钉死。
//  2. 每节点本地日**只触发一次**: 由 sunStates[ruleID] = 本地日期 保证。
//  3. 极昼/极夜**不静默**: 落一条显式审计 (suppressed_no_solar_event, O-6),
//     每日本事件只落一条 (防刷量)。
func (e *Evaluator) evalSunEventRule(rule models.AutomationRule, now time.Time, firstSunTick bool) {
	resolved, _, err := e.resolveRuleSun(rule)
	if err != nil {
		// 运行期解析失败 (节点被删/坐标被清空/时区名失效): 不触发 + 审计,
		// **绝不回落 UTC 或 0 坐标** (选型 §6.2/§6.3: 回落会让窗口静默偏移)。
		e.recordSunNoEvent(rule, now, "resolve_failed: "+err.Error())
		return
	}

	kind := sun.Kind(rule.TriggerSunKind)
	if !kind.Valid() {
		e.recordSunNoEvent(rule, now, "invalid_trigger_sun_kind: "+rule.TriggerSunKind)
		return
	}

	eventAt, status, err := e.sunCache.Lookup(resolved.Latitude, resolved.Longitude,
		resolved.Location, now, kind)
	if err != nil {
		e.recordSunNoEvent(rule, now, "compute_failed: "+err.Error())
		return
	}
	if status != sun.StatusOK {
		// 极昼/极夜 (O-6): 不触发 + 显式审计 (前端可见), 每日本事件一条。
		e.recordSunNoEvent(rule, now, string(status))
		return
	}

	// 偏移分钟 (可负 = 提前)。范围由 handler 校验; 运行期若见到越界值按不触发处理。
	offset := rule.TriggerSunOffsetMin
	if offset < -sunMaxOffsetMin || offset > sunMaxOffsetMin {
		e.recordSunNoEvent(rule, now, fmt.Sprintf("offset_out_of_range: %d", offset))
		return
	}
	fireAt := eventAt.Add(time.Duration(offset) * time.Minute)

	// 节点本地日 (归日口径 = 节点时区, 不是后端进程时区; 方案 §3.1.3)。
	localDate := now.In(resolved.Location).Format("2006-01-02")

	e.mu.Lock()
	// 冷却判定 (与 sensor_threshold / time_window 共用 triggered map 与 cooldownFor)。
	if firedAt, isTriggered := e.triggered[rule.ID]; isTriggered {
		if cooldown := cooldownFor(rule); cooldown > 0 && now.Sub(firedAt) < cooldown {
			e.mu.Unlock()
			return // 冷却期内不触发 (冷却语义与其它触发类型一致)
		}
		delete(e.triggered, rule.ID)
	}
	// 本日是否已触发过 (日期翻转即自然复位)。
	alreadyFiredToday := e.sunStates[rule.ID] == localDate

	// **首 tick 保守不触发** (方案 §6.2 / A-3)。
	//
	// 精确语义 (必须严格照方案): 保守**只适用于"事件时刻已经过去"的情形** ——
	// 此时无法区分"重启前已触发过"与"重启错过了", 故不补触发, 并把该本地日标记
	// 为已处理 (否则下一 tick 仍会认为"本日未触发"而补触发, 保守窗口就退化成一个
	// tick)。
	//
	// 反之, 若首 tick 时**事件尚未到达** (now < fireAt), 说明重启发生在事件之前
	// —— 这没有任何歧义, 后续 tick 必须正常触发, 否则每到事件前重启一次就永远
	// 不触发。该条件初版遗漏, 被 A-1/A-2 门禁抓住 (前置 tick 曾把全天标记为已触发)。
	if firstSunTick && !now.Before(fireAt) {
		e.sunStates[rule.ID] = localDate
		e.mu.Unlock()
		logger.Infof("automation: sun_event 首 tick 保守跳过已过事件 rule_id=%d event=%s (方案 §6.2: 重启不补触发)",
			rule.ID, fireAt.Format(time.RFC3339))
		return
	}

	// 边沿判定: 只在 [now-1tick, now] 跨过 fireAt 时触发, 且本日未触发过。
	// 用 "!now.Before(fireAt)" + 本日未触发 两个条件 —— 前者覆盖"到点且已过",
	// 后者保证同一本地日只触发一次。绝不使用 "now >= fireAt 恒真" 这种写法。
	if now.Before(fireAt) || alreadyFiredToday {
		if !now.Before(fireAt) {
			// 本日已触发过: 记录但不重复提交 (sunStates 已持有该日期)。
			e.mu.Unlock()
			return
		}
		e.mu.Unlock()
		return
	}
	e.sunStates[rule.ID] = localDate
	e.triggered[rule.ID] = now
	e.mu.Unlock()

	if e.handler != nil {
		e.handler.HandleTrigger(newTriggerEvent(rule, now, 0, "", nil))
	}
}

// sunMaxOffsetMin 是日出日落偏移分钟的取值范围上界 (与 handler V2 校验同源)。
const sunMaxOffsetMin = 720

// resolveRuleSun 解析规则生效的坐标与时区 (规则覆盖 → 节点权威 → 失败)。
//
// 归属解析的**唯一实现**在 models.ResolveSunLocation** (保存校验与运行期求值
// 共用同一套顺序), 本方法只负责"把规则的触发设备/动作设备映射到节点行"。
func (e *Evaluator) resolveRuleSun(rule models.AutomationRule) (models.SunLocation, models.SunLocationSource, error) {
	node, err := e.nodeForRule(rule)
	if err != nil {
		return models.SunLocation{}, models.SunLocationSource{}, err
	}
	return models.ResolveRuleSunLocation(rule, node)
}

// nodeForRule 找出该规则对应的节点行 (日出日落坐标的权威载体)。
//
// 口径 (节点级为权威 + 规则级可空覆盖, O-3):
//   - 优先用触发设备 (sensor_threshold 语义) 的节点;
//   - 否则用**动作设备**的节点 —— sun_event 规则通常不配触发设备, 动作设备
//     (例如某台 BMS) 所在节点才是它的物理安装位置;
//   - 都取不到时返回 nil (此时只有规则级覆盖值可用; 仍缺则 fail-closed)。
//
// 为什么不用规则表新增 node_id 列: 方案的列清单 (§5.1.2) 未含该列, 且引入它
// 会与"节点级为权威"的推导重复 (设备→节点已可确定)。这符合本批"不过度设计"。
func (e *Evaluator) nodeForRule(rule models.AutomationRule) (*models.Node, error) {
	edgeDeviceID := rule.TriggerEdgeDeviceID
	if edgeDeviceID == 0 {
		edgeDeviceID = rule.ActionDeviceID
	}
	if edgeDeviceID == 0 {
		return nil, nil
	}
	var dev models.EdgeDevice
	if err := e.db.Select("id", "node_id").First(&dev, edgeDeviceID).Error; err != nil {
		return nil, fmt.Errorf("lookup edge device %d: %w", edgeDeviceID, err)
	}
	if dev.NodeID == "" {
		return nil, nil
	}
	var node models.Node
	if err := e.db.Where("node_id = ?", dev.NodeID).First(&node).Error; err != nil {
		return nil, fmt.Errorf("lookup node %q: %w", dev.NodeID, err)
	}
	return &node, nil
}

// recordSunNoEvent 落一条"本日无日出/日落事件/无法求值"的显式审计 (O-6)。
//
// 为什么必须显式落审计而不是静默 return: 高纬用户看到规则整天没触发、事件表
// 里什么也没有, 会认为规则坏了 —— 这正是任务书 C5 描述的静默失败形态, 新能力
// 不得复制它。前端事件表据此显示"极昼/极夜, 本日无日出事件"。
//
// 节流: 同一规则同一**本地日**只落一条 (否则每 1 分钟 tick 都会写一行, 极夜
// 期间 1440 行/天)。与 recordSuppressed 的同窗节流同旨。
func (e *Evaluator) recordSunNoEvent(rule models.AutomationRule, now time.Time, reason string) {
	localDate := now.UTC().Format("2006-01-02")
	if resolved, _, err := e.resolveRuleSun(rule); err == nil && resolved.Location != nil {
		localDate = now.In(resolved.Location).Format("2006-01-02")
	}

	e.mu.Lock()
	if e.sunLocationAudit[rule.ID] == localDate {
		e.mu.Unlock()
		return
	}
	e.sunLocationAudit[rule.ID] = localDate
	e.mu.Unlock()

	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: now,
		Result:      models.AutomationResultSuppressedNoSolarEvent,
		Detail:      "no solar event today: " + reason,
		CreatedAt:   now,
	}
	if err := e.db.Create(&ev).Error; err != nil {
		logger.Warn("automation: failed to record suppressed_no_solar_event",
			"rule_id", rule.ID, "reason", reason, "error", err)
	}
}

// evalWindowRule 单条 time_window 规则状态机: 判定当前时刻是否在窗口内,
// 根据 edge 语义 (enter/exit/inside) 决定是否触发, 受 CooldownSec 抑制。
func (e *Evaluator) evalWindowRule(rule models.AutomationRule, now time.Time) {
	inside, err := isInWindow(rule.TriggerWindowStart, rule.TriggerWindowEnd, now)
	if err != nil {
		logger.Warn("automation: invalid time window format",
			"rule_id", rule.ID, "start", rule.TriggerWindowStart, "end", rule.TriggerWindowEnd, "error", err)
		return
	}

	e.mu.Lock()
	wasInside, exists := e.windowStates[rule.ID]
	// 更新窗口状态 (无论是否触发都要记录本次状态)。
	e.windowStates[rule.ID] = inside

	// 冷却判定 (与 sensor_threshold 共用 triggered map 与 cooldownFor 判定)。
	cooldown := cooldownFor(rule)
	if firedAt, isTriggered := e.triggered[rule.ID]; isTriggered {
		if cooldown > 0 && now.Sub(firedAt) < cooldown {
			e.mu.Unlock()
			return // 冷却期内, 不重复触发
		}
		delete(e.triggered, rule.ID) // 冷却到期, 回 armed
	}
	e.mu.Unlock()

	// 边沿触发判定。
	var shouldTrigger bool
	var edge string
	switch rule.TriggerWindowEdge {
	case models.AutomationWindowEnter:
		// enter: 上次不在窗口内、本次在窗口内 → 触发。
		// 重启后首次 tick (exists=false): 保守处理 — 当前在窗口内则触发。
		shouldTrigger = inside && (!exists || !wasInside)
		edge = models.AutomationWindowEnter
	case models.AutomationWindowExit:
		// exit: 上次在窗口内、本次不在窗口内 → 触发。
		// 重启后首次 tick (exists=false): 当前不在窗口内则触发 (保守: 无法判断是否在窗口内, 不触发)。
		shouldTrigger = !inside && exists && wasInside
		edge = models.AutomationWindowExit
	case models.AutomationWindowInside:
		// inside: 窗口内每次 tick 都参与求值 (受 CooldownSec 抑制)。
		shouldTrigger = inside
		edge = models.AutomationWindowInside
	default:
		return
	}

	if !shouldTrigger {
		return
	}

	// armed → triggered: 记录触发时刻, 提交 Planner。
	e.mu.Lock()
	e.triggered[rule.ID] = now
	e.mu.Unlock()

	if e.handler != nil {
		e.handler.HandleTrigger(newTriggerEvent(rule, now, 0, edge, nil))
	}
}

// isInWindow 判定当前时刻是否在时间窗口内。
// start/end 为 "HH:MM" 格式; 窗口可跨零点 (start > end 时跨日)。
// 返回 (是否在窗口内, 解析错误)。
func isInWindow(start, end string, now time.Time) (bool, error) {
	startT, err := parseHHMM(start)
	if err != nil {
		return false, fmt.Errorf("parse start %q: %w", start, err)
	}
	endT, err := parseHHMM(end)
	if err != nil {
		return false, fmt.Errorf("parse end %q: %w", end, err)
	}

	// 当日窗口时间点 (本地时间)。
	year, month, day := now.Date()
	startToday := time.Date(year, month, day, startT.Hour(), startT.Minute(), 0, 0, now.Location())
	endToday := time.Date(year, month, day, endT.Hour(), endT.Minute(), 0, 0, now.Location())

	if startToday.Equal(endToday) {
		// start == end: 视为全天窗口 (24h), 始终在内。
		return true, nil
	}
	if startToday.Before(endToday) {
		// 非跨零点窗口: [start, end)。
		return !now.Before(startToday) && now.Before(endToday), nil
	}
	// 跨零点窗口: [start, 24:00) ∪ [00:00, end)。
	// 等价于: now >= start || now < end。
	return !now.Before(startToday) || now.Before(endToday), nil
}

// parseHHMM 解析 "HH:MM" 格式为 time.Time (仅取时分)。
func parseHHMM(s string) (time.Time, error) {
	return time.Parse("15:04", s)
}

// Evaluate 解析后回调入口: 对 edgeDeviceID 的物理量 fields 逐规则求值。
// 挂接点 = databus consumers_heavy.go 新增 SetAutomationSink, 与 alertSink 并列。
func (e *Evaluator) Evaluate(edgeDeviceID uint, fields []parser.Field, at time.Time) {
	if len(fields) == 0 || edgeDeviceID == 0 {
		return
	}
	// H2 修复 (锁放大): RLock 内只快照 rules 浅拷贝, resolveLogicalID 的 SQL 往返
	// 移到 RUnlock 之后 — 写锁等待会阻塞所有 RLock, DB 慢查询时求值 stall。
	e.mu.RLock()
	rules := make([]models.AutomationRule, 0, len(e.rules))
	for _, r := range e.rules {
		rules = append(rules, r)
	}
	e.mu.RUnlock()

	target := edgeDeviceID
	var logicalID uint
	resolved := false
	for _, r := range rules {
		// 来源组规则：匹配/过滤由 sourceref 快照决定（按 kind 分流，裁决 1）。
		//   primary_backup：只收 active 成员帧（备成员帧在此丢弃，S1 的前提）；
		//   composite      ：任一成员帧驱动整组求值（CombineGroup）。
		if ref := r.SourceRef(); ref.IsGroup() {
			m, ok := e.matchSourceRule(r, ref, target, at)
			if !ok {
				continue
			}
			if m.Kind == sourceref.KindComposite {
				e.evalCompositeRule(r, m, target, fields, at)
				continue
			}
			e.evalRuleWithSource(r, target, fields, at, identityForFrame(m, target),
				&triggerSource{
					kind:          sourceref.GroupKindSourceGroup,
					groupID:       m.GroupID,
					edgeDeviceID:  target,
					transitionSeq: m.TransitionSeq,
					sourceAt:      at,
				})
			continue
		}
		// 存量单设备/逻辑设备匹配（逐字节保持原逻辑）:
		// TriggerEdgeDeviceID=0 表示任意设备上报该字段即触发 (不推荐, 文档标注);
		// 否则必须精确命中上报设备 (或其逻辑身份)。
		if r.TriggerEdgeDeviceID != 0 && r.TriggerEdgeDeviceID != target {
			if !resolved {
				logicalID = e.resolveLogicalID(target)
				resolved = true
			}
			if logicalID == 0 || r.TriggerEdgeDeviceID != logicalID {
				continue
			}
		}
		e.evalRule(r, target, fields, at)
	}
}

// resolveLogicalID 解析边缘设备的最终逻辑身份 (合并链跟随, 与 alert 同链)。
func (e *Evaluator) resolveLogicalID(edgeDeviceID uint) uint {
	var dev models.EdgeDevice
	if err := e.db.Select("id", "logical_device_id").First(&dev, edgeDeviceID).Error; err != nil {
		return 0
	}
	if dev.LogicalDeviceID == nil || *dev.LogicalDeviceID == 0 {
		return 0
	}
	target, err := datalifecycle.ResolveMergeTarget(e.db, *dev.LogicalDeviceID)
	if err != nil {
		logger.Warn("automation: resolve merge target failed", "edge_device_id", edgeDeviceID, "error", err)
		return 0
	}
	return target
}

// evalRule 单规则状态机: armed → triggered (连续满足 DurationSec) → 冷却到期回 armed。
//
// deviceID 是本次上报的**边缘设备 ID**（Evaluate 入参），用于解析层 plan 的
// (device, selector) 编译 —— 不能用 rule.TriggerEdgeDeviceID（规则可能绑逻辑设备）。
func (e *Evaluator) evalRule(rule models.AutomationRule, deviceID uint, fields []parser.Field, at time.Time) {
	// trigger 求值走解析层（literal 路径内部与 matchField 逐字节等价）。
	// ok=false（本批无该字段 / unbound / kill switch）⇒ 不更新窗口、不触发。
	triggerOK, satisfied, value := e.evalSelector(rule.ID, 0, deviceID,
		rule.TriggerSensorName, rule.TriggerComparator, rule.TriggerThreshold, fields, at)
	if !triggerOK {
		return
	}
	if !e.conditionsSatisfied(rule, deviceID, fields, at) {
		return
	}
	e.applyTrigger(rule, at, value, satisfied, legacyIdentity(deviceID),
		legacyTriggerSource(deviceID, at), e.sourceGap(deviceID))
}

// evalRuleWithSource 是 primary_backup 规则的单帧路径：selector 在本帧求值，
// 窗口身份与冷却/触发沿用同一条状态机（applyTrigger），只是 identity 带组信息。
func (e *Evaluator) evalRuleWithSource(rule models.AutomationRule, deviceID uint,
	fields []parser.Field, at time.Time, ident sourceIdentity, src *triggerSource) {
	triggerOK, satisfied, value := e.evalSelector(rule.ID, 0, deviceID,
		rule.TriggerSensorName, rule.TriggerComparator, rule.TriggerThreshold, fields, at)
	if !triggerOK {
		return
	}
	if !e.conditionsSatisfied(rule, deviceID, fields, at) {
		return
	}
	e.applyTrigger(rule, at, value, satisfied, ident, src, e.sourceGap(deviceID))
}

// conditionsSatisfied 求值附加条件（全部 AND；任一无法判定/不满足 ⇒ false）。
// 非法 conditions_json 属配置错误：log + 不触发（fail-closed，与存量一致）。
func (e *Evaluator) conditionsSatisfied(rule models.AutomationRule, deviceID uint,
	fields []parser.Field, at time.Time) bool {
	conds, err := rule.ParseConditions()
	if err != nil {
		logger.Warn("automation: invalid conditions_json", "rule_id", rule.ID, "error", err)
		return false
	}
	for i, c := range conds {
		condOK, condSatisfied, _ := e.evalSelector(rule.ID, uint8(i+1), deviceID,
			c.SensorName, c.Comparator, c.Threshold, fields, at)
		if !condOK || !condSatisfied {
			return false // 任一条件无法判定/不满足 则不触发（混合规则整条 fail-closed）
		}
	}
	return true
}

// applyTrigger 是 armed → triggered 状态机的唯一实现（三路复用：存量单帧、
// 主备组成员帧、复合整组结果）。
//
// S1/S3：写样本前若 identity 与窗口最后一条不一致 ⇒ 丢弃旧段（**不依赖切换钩子**，
// 钩子丢失只影响清窗时延，不影响正确性）。S4：清窗保留 triggered 冷却锚。
// S8：pruneWindow 增加跨长断流判据（maxGap=2×采集间隔；0=不启用）。
func (e *Evaluator) applyTrigger(rule models.AutomationRule, at time.Time, value float64,
	satisfied bool, ident sourceIdentity, src *triggerSource, maxGap time.Duration) {
	cooldown := cooldownFor(rule) // 0 = 不冷却 (cooldownFor 是唯一判定处)

	e.mu.Lock()
	// 冷却到期判定优先于求值: 到期即回 armed, 本批可参与新一轮触发。
	// cooldown==0 (不冷却) 时直接清掉 triggered 态, 不做抑制也不落 suppressed 审计
	// —— 没有冷却期就没有"被冷却压制"这回事 (recordSuppressed 只在 cooldown>0 时可达)。
	if firedAt, isTriggered := e.triggered[rule.ID]; isTriggered {
		if cooldown > 0 && at.Sub(firedAt) < cooldown {
			e.mu.Unlock()
			e.recordSuppressed(rule, firedAt, at, value) // F3: 冷却命中落审计 (防抖可观测, 同窗节流)
			return                                       // 冷却期内, 不更新窗口不重复触发
		}
		delete(e.triggered, rule.ID) // 冷却到期或本就不冷却 (0), 回 armed
	}

	win := e.windows[rule.ID]
	// S1（身份不匹配即清段）：同一条规则在来源切换/重启/组变更后不得拼接旧样本。
	if len(win) > 0 && !win[len(win)-1].identity.equal(ident) {
		win = nil
	}
	win = append(win, sample{at: at, satisfied: satisfied, identity: ident})
	win = pruneWindow(win, rule.TriggerDurationSec, at, maxGap)
	e.windows[rule.ID] = win

	continuous := satisfied && windowSatisfied(win, rule.TriggerDurationSec, at)
	if !continuous {
		e.mu.Unlock()
		return
	}

	// armed → triggered: 记录触发时刻, 本批提交 Planner。
	e.triggered[rule.ID] = at
	e.mu.Unlock()

	e.markRuleFired(rule.ID)
	if e.handler != nil {
		// Value 语义：literal 为原始上报值（与今天一致）；特殊 selector 为
		// predicate 参与比较的值（family/reducer 的语义值），仅用于展示/审计。
		e.handler.HandleTrigger(newTriggerEvent(rule, at, value, "", src))
	}
}

// recordSuppressed F3 修复 (方案 §3.4①): 冷却命中时落一条 result=suppressed_cooldown
// 审计事件 — 此前该常量全仓 0 写入点, 触发历史看不到被冷却压制的触发 (防抖不可观测)。
// 字段写法对齐 planner.recordRet (fail-open: 写失败只告警不阻塞求值)。
//
// 同窗节流 (主 Agent 复审补): Evaluate 每帧上报都触发 (databus consumers_heavy.go:337),
// 冷却期内高频越阈会每帧落一行刷量 (1s 上报 × 1h 冷却 = 3600 行)。故同一冷却窗
// (自窗起点 windowStart 起) 只落首条 suppressed_cooldown, 后续命中查询到已有即跳过。
func (e *Evaluator) recordSuppressed(rule models.AutomationRule, windowStart, at time.Time, value float64) {
	// 同一冷却窗内已有 suppressed_cooldown 行则跳过 (节流, 防高频上报刷量)。
	var cnt int64
	if err := e.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ? AND triggered_at >= ?",
			rule.ID, models.AutomationResultSuppressedCooldown, windowStart).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      models.AutomationResultSuppressedCooldown,
		Detail:      "cooldown active",
		CreatedAt:   at,
	}
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if err := e.db.Create(&ev).Error; err != nil {
		logger.Warn("automation: failed to record suppressed_cooldown event",
			"rule_id", rule.ID, "error", err)
	}
}

// matchField / compare / pruneWindow / windowSatisfied 与 alert 同源同语义
// (避免 alert→automation 或反向的编译期依赖, 各自持有私有副本)。

func matchField(fields []parser.Field, name string) (float64, bool) {
	for i := range fields {
		if fields[i].Name == name {
			return fields[i].Value, true
		}
	}
	return 0, false
}

func compare(op string, value, threshold float64) bool {
	switch op {
	case models.AlertComparatorGT:
		return value > threshold
	case models.AlertComparatorGTE:
		return value >= threshold
	case models.AlertComparatorLT:
		return value < threshold
	case models.AlertComparatorLTE:
		return value <= threshold
	case models.AlertComparatorEQ:
		return value == threshold
	case models.AlertComparatorNEQ:
		return value != threshold
	default:
		return false
	}
}

// pruneWindow 仅保留 [now-DurationSec, now] 内的样本并限制上限。
// 滚动窗口语义 (2026-08-23 探针实锤修复): 保留 cutoff 前最后一个样本作为窗口
// 起点 —— 否则稀疏采样 (如每 5s 一点, duration=30s) 时窗口恰跨满 duration 的
// 边界样本被裁, windowSatisfied 的 span 判定 (now-win[0].at >= duration) 永不满足。
// 对照 alert pruneWindow 同位置缺陷 (alert 测试用密集采样未暴露)。
func pruneWindow(win []sample, durationSec int, now time.Time, maxGap time.Duration) []sample {
	if len(win) == 0 {
		return win
	}
	if durationSec <= 0 {
		// DurationSec=0 直通: 只保留最新样本 (窗口无意义)。
		return win[len(win)-1:]
	}
	cutoff := now.Add(-time.Duration(durationSec) * time.Second)
	keep := 0
	for keep < len(win) && win[keep].at.Before(cutoff) {
		keep++
	}
	// 保留 cutoff 前最后一个样本: 它是窗口的实际起点 (span 判定基准)。
	if keep > 0 {
		keep--
	}
	win = win[keep:]
	// S8（方案 F-7 写死算法）：跨长断流不得拼接 —— 相邻样本间隔 > maxGap
	// （2×采集间隔）时，更早的样本不能作为窗口起点。实现取"最近一次断流之后"
	// 的那一段（从尾部向前找第一个断点），而不是只比较首对样本。
	// maxGap<=0 表示采集间隔取不到 ⇒ 不启用该判据（保持存量行为）。
	if maxGap > 0 {
		start := 0
		for i := len(win) - 1; i > 0; i-- {
			if win[i].at.Sub(win[i-1].at) > maxGap {
				start = i
				break
			}
		}
		win = win[start:]
	}
	if len(win) > maxWindowSamples {
		win = win[len(win)-maxWindowSamples:]
	}
	return win
}

// windowSatisfied 连续满足判定: DurationSec=0 直通 (本点满足即触发);
// 否则窗口内所有样本都满足且窗口跨度 ≥ DurationSec。
func windowSatisfied(win []sample, durationSec int, now time.Time) bool {
	if durationSec <= 0 {
		return true
	}
	if len(win) == 0 {
		return false
	}
	for i := range win {
		if !win[i].satisfied {
			return false
		}
	}
	return now.Sub(win[0].at) >= time.Duration(durationSec)*time.Second
}
