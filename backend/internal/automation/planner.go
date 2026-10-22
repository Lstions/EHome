package automation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Planner 触发后的执行编排器 (设计/自动化策略引擎方案.md §4.2):
// 冷却/日熔断 → 确认分流 → 动作执行, 全部在此收敛, 求值器不感知。
//
// 铁律 1: 动作执行唯一入口 = commandexec.Service.Create, 白得 9 项
// availability gate + 幂等 + verify; Planner 不持有 dispatcher/MQTT。
//
// 铁律 3: 系统 actor 归因 — Reason = "automation:<rule_id>:<rule_name>",
// ActorUserID = SystemActorID (main.go 注入的内置系统用户, 禁止登录)。
//
// 铁律 4: require_confirmed=true 的规则不直接执行, 仅生成"建议执行"通知
// + IssueConfirmation 占位 (人工确认后走既有 confirmation 链路)。
type Planner struct {
	db        *gorm.DB
	cmdSvc    *commandexec.Service
	broadcast func(eventType string, payload any) // nil 时跳过 WS 广播

	// actorMu 保护 systemActorID 的惰性解析与缓存。HandleTrigger 会被多个
	// worker 并发调用, 首次解析必须串行化 (顺带保证只查一次库)。
	// 注意: 持锁期间做一次 DB 查询可接受 —— 只有未命中缓存的首次调用才会查。
	actorMu sync.Mutex
	// systemActorID 内置系统主体用户 ID (subject_key=system_admin)。
	// 0 = "尚未解析", 不是有效 actor: 首次自动 device_action 触发时由
	// resolveSystemActorID 惰性解析并缓存 (见 NewPlanner 注释)。
	systemActorID uint

	// latestValueFn 数据层时序化 (方案 v3.4 §3.2.4): 最新值查询回调,
	// 默认走 api.LatestValue; 测试注入内存实现。nil 时跳过 F4 条件复核。
	latestValueFn func(deviceID uint) (models.UnifiedData, bool)

	// latestValueBySensorFn（设计 §9.2#4 新增，不改旧签名）按 (设备, 物理名) 取
	// 最新值，用于 literal selector 的 F4 复核：旧 latestValueFn 每设备只有一条记录，
	// 无法回答"这个物理名的最新值"。nil 时回落 latestValueFn（既有语义）。
	latestValueBySensorFn func(deviceID uint, sensorName string) (models.UnifiedData, bool)

	// latestValuesFn 按设备取最新一帧多槽值：family/@semantic/virtual selector 的
	// 复核输入（设计 §9.2#4）。nil 且 selector 为特殊形态时 fail-closed。
	latestValuesFn func(deviceID uint) []models.UnifiedData

	// planProvider 解析层控制面（F4 复核按 selector 求值，修 AU-12）。
	planProvider PlanProvider

	// sourceResolver 来源组解析器（F4 按触发证据校验成员身份；nil=未接线）。
	// 组规则在 nil 时 FC-4 fail-closed（source_resolver_not_configured），
	// 存量规则不依赖它。
	sourceResolver SourceResolver
	// sourceHealthFn 是「来源降级可见性」回调（写 source_group_health）。
	// F4 判定 source_member_changed/source_stale/source_degraded 时调用，
	// 与求值期同一语义（main.go 注入，实现方负责节流）。
	sourceHealthFn func(groupID uint, reason string)

	// nowFn 可注入时钟 (测试用), 默认 time.Now。
	nowFn func() time.Time

	// notifier 通知写入+投递入口 (D-1 步骤 3, 设计/外发通知通道.md §2.1 取向 A):
	// main.go 经 SetNotifier 注入 notify.Dispatcher。
	// **nil 是显式支持的合法状态**: 既有测试 NewPlanner(db, svc, nil, id) 不注入时,
	// 回落为直接写 notifications 表 —— 行为与改造前逐字节等价 (见 createNotification)。
	notifier notify.Notifier
}

// NewPlanner 构造编排器。systemActorID 是 users 表内置系统主体用户
// (subject_key=system_admin AND retired_at IS NULL) 的 ID; main.go 的启动期
// 预检若命中则作为初值传入, 省掉首次惰性查询。
//
// 允许传 0, 语义是"尚未解析"而非有效 actor: 全新安装时该用户由
// POST /api/v1/auth/initialize 在进程启动**之后**创建, 启动期解析必然拿不到。
// 真正的取值在首次自动 device_action 触发时由 resolveSystemActorID 惰性解析并缓存,
// **绝不能**把启动期的 0 冻结成长期状态 —— 那会让自动 device_action 在进程整个
// 生命周期内被 commandexec 的 ActorUserID==0 fail-closed 校验拒绝, 直到重启才自愈。
func NewPlanner(db *gorm.DB, cmdSvc *commandexec.Service, broadcast func(string, any), systemActorID uint) *Planner {
	return &Planner{
		db:            db,
		cmdSvc:        cmdSvc,
		broadcast:     broadcast,
		systemActorID: systemActorID,
		nowFn:         time.Now,
		latestValueFn: nil, // 由 SetLatestValueFn 注入 (main.go 接线), 避免 automation→api 编译期依赖
	}
}

// SetLatestValueFn 注入最新值查询回调 (数据层时序化 v3.4 §3.2.4)。
// 与 databus 的 latestSink 同点挂接, 避免 automation→api 编译期依赖。
func (p *Planner) SetLatestValueFn(fn func(deviceID uint) (models.UnifiedData, bool)) {
	p.latestValueFn = fn
}

// SetLatestValueBySensorFn 注入"按物理名取最新值"回调（设计 §9.2#4；修 AU-12）。
//
// 生产实现是 api.LatestValueBySensor。不直接 import api：保持 automation 对 api 的
// 零编译期依赖（与 SetLatestValueFn 同款二阶段注入）。
func (p *Planner) SetLatestValueBySensorFn(fn func(deviceID uint, sensorName string) (models.UnifiedData, bool)) {
	p.latestValueBySensorFn = fn
}

// SetLatestValuesFn 注入"按设备取最新一帧多槽值"回调（family/@semantic/virtual 复核用）。
// 生产实现是 api.LatestValues。
func (p *Planner) SetLatestValuesFn(fn func(deviceID uint) []models.UnifiedData) {
	p.latestValuesFn = fn
}

// SetPlanProvider 注入解析层控制面（selector 编译/谓词求值；kill switch 也在其中）。
func (p *Planner) SetPlanProvider(prov PlanProvider) {
	p.planProvider = prov
}

// SetSourceResolver 注入来源组解析器（F4 身份校验；prompt 明确禁止回读配置列）。
func (p *Planner) SetSourceResolver(r SourceResolver) {
	p.sourceResolver = r
}

// SetSourceHealthFn 注入「来源降级可见性」回调（与 Evaluator 同签名、同语义）。
//
// F4 失败也意味着组来源已不可用于该决策（例如触发后 active 被切走/证据过期），
// 属于方案 §7.1 E-1/E-2 要求"降级必须可见"的范围；否则运维在
// GET /source-groups/:id/health 只会看到"一切正常"（e2e TestSG_E3 缺口）。
func (p *Planner) SetSourceHealthFn(fn func(groupID uint, reason string)) {
	p.sourceHealthFn = fn
}

// SetNotifier 注入"通知写入 + 投递"入口 (D-1 步骤 3, main.go 接线)。
//
// 契约: 注入后通知经它落库并顺带外发投递; 传 nil 显式回落为直接写 notifications 表。
// 沿用本包既有的二阶段 setter 注入范式 (SetLatestValueFn) 与窄接口取向, 避免
// automation→(外发 HTTP 引擎) 的编译期依赖。
func (p *Planner) SetNotifier(n notify.Notifier) {
	p.notifier = n
}

// createNotification 落库一条通知 (D-1 步骤 3: 经注入的 Notifier 或直接落库)。
//
// nil 回落是**显式设计**: 既有测试直接 NewPlanner(db, ...) 不注入, 必须保持
// 改造前的行为 (直接 Create + 同一条指标与日志), 不得 panic。
//
// 以下三个前提使"注入后同步投递"安全 (见 notify/dispatcher.go 的投递时机裁决):
//   - 本函数**不在任何事务内**被调用 (notifydailyLimitOnce / notifyConfirmation /
//     notifyAction 全部在 recordRet 的独立 db.Create 返回后才调用);
//   - 返回 true 表示已拿到自增 ID, 调用方据此广播 WS 载荷 (契约: 广播的必须是
//     库里真实存在的通知行), 因此要求同步落库 —— 不能用 CreateAsync;
//   - Dispatcher 自身 fail-open 且带 recover, 通知路径不会掀翻 Planner 控制流。
func (p *Planner) createNotification(ctx context.Context, n *models.Notification, scope string) bool {
	if p.notifier != nil {
		p.notifier.Create(ctx, n)
		return n.ID != 0
	}
	if err := p.db.Create(n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "notifications").Inc()
		logger.Warn("automation: failed to create notification", "scope", scope, "error", err)
		return false
	}
	return true
}

// HandleTrigger 实现 TriggerHandler 接口, 由 Evaluator 在 armed→triggered 时调用。
// 执行约束顺序 (裁决 5): MaxDailyExec 日熔断 → require_confirmed 确认分流 → 动作。
func (p *Planner) HandleTrigger(ev TriggerEvent) {
	rule := ev.Rule
	at := ev.At
	if at.IsZero() {
		at = p.nowFn()
	}

	// ── 日熔断: MaxDailyExec > 0 时统计当日 executed/pending_confirm 行数 ──
	if rule.MaxDailyExec > 0 {
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		var cnt int64
		if err := p.db.Model(&models.AutomationEvent{}).
			// F6: 口径改为只 count(executed) — pending_confirm 不占日限额 (方案 §3.2 语义)
			Where("rule_id = ? AND triggered_at >= ? AND result = ?",
				rule.ID, dayStart, models.AutomationResultExecuted).
			Count(&cnt).Error; err == nil && int(cnt) >= rule.MaxDailyExec {
			eventID := p.recordRet(rule, at, ev.Value, models.AutomationResultSuppressedDailyLimit,
				"", fmt.Sprintf("daily limit %d reached", rule.MaxDailyExec), &ev)
			// F5: 补发 warning 级通知, 同日同规则只发一次 (幂等: 查当日是否已发过)
			p.notifyDailyLimitOnce(rule, at, eventID)
			return
		}
	}

	// ── 确认分流: require_confirmed=true 只生成建议执行通知 ──
	if rule.RequireConfirmed {
		// Y5 TOCTOU（裁决 B）：pending 事件落动作集合归一化哈希快照，confirm 时比对，
		// 不一致即拒绝（ErrConfirmActionChanged）。否则"触发后改动作、再点确认"会执行
		// 用户从未确认过的新动作。
		actions := p.ruleActions(rule)
		eventID := p.recordPendingConfirm(rule, at, ev.Value, actions, &ev)
		p.notifyConfirmation(rule, at, ev.Value, eventID)
		return
	}

	// ── 动作分发 (多动作: 按 seq 升序串行执行, 方案 §3.2.4 / §6.3) ──
	//
	// 动作集合以 automation_rule_actions 子表为权威 (M1); 子表为空时回落到
	// 单动作镜像列 —— 回填迁移尚未跑到的历史库也能正确执行, 不依赖迁移已执行。
	actions := p.ruleActions(rule)
	// F4: 执行前复核条件 — 触发到执行间条件可能已失效 (DurationSec 长规则安全相关)。
	// 任一条件不满足则落 condition_changed 事件并返回, 不执行任何动作。
	// 只在动作集合含 device_action 时复核: 纯通知规则不消耗设备状态, 与改造前一致。
	if hasDeviceAction(actions) {
		result, reason, ok := p.checkConditionsStillSatisfiedWithSource(rule, ev)
		if !ok {
			if result == "" {
				result = models.AutomationResultConditionChanged
			}
			p.record(rule, at, ev.Value, result, "", reason, &ev)
			return
		}
	}
	p.executeActions(rule, at, ev.Value, actions, &ev)
}

// hasDeviceAction 报告动作集合里是否存在 device_action。
func hasDeviceAction(actions []models.AutomationRuleAction) bool {
	for _, a := range actions {
		if a.ActionType == models.AutomationActionDeviceAction {
			return true
		}
	}
	return false
}

// ruleActions 加载规则的动作集合 (子表为权威, 方案 §3.2.3 M1)。
//
// 读路径回落 (兼容期, 方案 §3.4 D2): 子表**为空**时用单动作镜像列合成 seq=1
// 动作。这保证两件事: ① 回填迁移尚未跑到的历史库不会被当作"无动作规则";
// ② 单动作老规则在迁移前后的行为逐字节等价。
//
// 注意"为空"与"查询失败"都回落: 查询失败时回落是 fail-safe 方向 —— 用镜像列
// 执行"用户最后一次保存的动作", 比静默不执行更符合用户预期 (且镜像列是双写期
// 的真实投影, 不是猜测值)。
func (p *Planner) ruleActions(rule models.AutomationRule) []models.AutomationRuleAction {
	var actions []models.AutomationRuleAction
	err := p.db.Where("rule_id = ?", rule.ID).Order("seq ASC").Find(&actions).Error
	if err != nil {
		logger.Warn("automation: load rule actions failed, falling back to mirror columns",
			"rule_id", rule.ID, "error", err)
		return []models.AutomationRuleAction{models.AutomationActionFromRule(rule)}
	}
	if len(actions) == 0 {
		return []models.AutomationRuleAction{models.AutomationActionFromRule(rule)}
	}
	return actions
}

// ── 系统 actor 惰性解析 (P0: 全新安装后自动 device_action 永久失败) ──

// resolveSystemActorID 返回内置系统主体用户 ID, 首次调用时解析并缓存。
//
// 为什么必须惰性: main.go 的启动期预检发生在 POST /api/v1/auth/initialize 之前,
// 全新安装时 users 表还是空的; 若把启动期的 0 当终值冻结, 自动 device_action 会在
// 进程整个生命周期内被 commandexec 的 ActorUserID==0 fail-closed 校验拒绝
// (事件恒为 failed_dispatch "invalid command request"), 只有重启才自愈。
//
// 并发: 用 actorMu 串行化解析 (HandleTrigger 可能被多个 worker 并发调用),
// 已缓存非 0 时走快路径直接返回, 后续触发不再查库。
func (p *Planner) resolveSystemActorID() (uint, error) {
	p.actorMu.Lock()
	defer p.actorMu.Unlock()
	if p.systemActorID != 0 {
		return p.systemActorID, nil
	}
	var admin models.User
	if err := p.db.
		Where("subject_key = ? AND retired_at IS NULL", models.SystemAdminSubjectKey).
		First(&admin).Error; err != nil {
		return 0, fmt.Errorf("active system_admin user not found: %w", err)
	}
	p.systemActorID = admin.ID
	logger.Info("automation: resolved system actor lazily", "user_id", admin.ID)
	return admin.ID, nil
}

// nextTriggerSeq 返回该规则**当日第几次触发决策**, 用作幂等键的 trigger_seq 段。
//
// 口径 = 当日该规则已落库的 automation_events 行数 + 1。
//
// 为什么不能用"当日 executed 行数 + 1" (P1-A / P2-B 的根因): 部分失败**不写**
// executed 行, 于是"失败 → 修配置 → 重试"时 trigger_seq 与上一次完全相同, 第 1 个
// 动作的 Create 直接命中上次的幂等键:
//
//   - 参数与上次相同 → replay 返回旧结果, planner 记 executed=成功, 而本次实际
//     只有后面的动作真正下发 (静默少执行, 安全相关);
//   - 参数被用户改过 → RequestHash 不同 → ErrIdempotencyCollision, 该规则当天
//     永久 failed_dispatch, 直到次日 (P2-B)。
//
// 计数单位换成"触发决策"后, 每一次进入动作循环都拿到一个**新**的 trigger_seq,
// 键不再重合 —— 重试必然真的重新下发。
//
// 为什么把被抑制的事件也算进来: 计数的唯一职责是"单调递增且不复用", 不是"统计
// 成功次数"。suppressed_* / condition_changed / failed_* 全部计入只会让序号跳得更
// 快, 失败方向是"跳号"而不是"复用" (宁可跳号, 不可复用)。
//
// 并发边界 (诚实声明): 本函数是"读-计数-加一"式, 同一规则被两个 worker 同时触发
// 时仍有极小概率取到同一个 trigger_seq —— 该窗口在改造前就存在 (旧口径同样是先查
// 后写), 不是本批引入。命中时 command_executions 的 (scope, key) 唯一索引与
// RequestHash 比对会把它挡成 ErrIdempotencyCollision (fail-closed, 绝不静默少执行)。
// 规则级执行串行化不在本批范围。

// nextTriggerSeq 返回"当日该规则第几次触发"（幂等键的 trigger_seq 段）。
//
// ⚠ 2026-10-21 修正：此前统计的是当日**全部** automation_events 行数，把被抑制的
// 求值 tick 也算成了触发 —— 冷却期内的每个 tick 都会落一条 suppressed_cooldown，
// 于是同日第二次执行的序号会从 2 跳到 3（SIM-ACTN-004 实测 1、3）。
// 幂等键的语义是"第几次触发"，被抑制的 tick 不是新触发，必须排除。
//
// 保留 suppressed_* 之外的一切结果：executed / partial_failure / failed_* /
// confirmation_required / pending_confirm / notification / condition_changed 都是
// 一次真实触发，排除它们会让两次不同触发共用同一个 trigger_seq，进而命中
// commandexec 的幂等重放（把第二次执行静默吞掉）—— 那正是 A-6 要防的反例。
func (p *Planner) nextTriggerSeq(ctx context.Context, ruleID uint, at time.Time, triggerSource string) int64 {
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	suppressed := []string{
		models.AutomationResultSuppressedCooldown,
		models.AutomationResultSuppressedDailyLimit,
		models.AutomationResultSuppressedNoSolarEvent,
	}
	q := p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND triggered_at >= ?", ruleID, dayStart).
		Where("result NOT IN ?", suppressed)
	if triggerSource != "" {
		q = q.Where("trigger_source = ?", triggerSource)
	}
	var n int64
	_ = q.Count(&n).Error
	return n + 1
}

// executeActions 串行执行规则的动作集合 (方案 §3.2.4 / §6.3 多动作状态机)。
//
// 语义 (O-1 裁决 (a), 实施计划 §0 用户裁决):
//   - 按 seq **升序串行**执行, 任一动作完成后才开始下一个 (不并行);
//   - 第 k 个动作失败 ⇒ **中止剩余动作**, 已执行的不回滚 (物理世界无法撤销);
//   - 整体结果: 全部成功 → executed; 第 k 个失败且 k<N → partial_failure
//     (记 action_seq=k); 第 1 个失败 → 按原因落 failed_dispatch / failed_gate /
//     confirmation_required (action_seq=1);
//   - 冷却锚点只在**全部动作成功**后写入, 且取触发时刻 at (不取动作完成时刻)
//     —— 见 recordExecuted 的既有裁决, A-16 钉死该语义;
//   - **部分失败不写冷却锚点** (显式裁决, 方案 §3.2.4「部分失败的锚点」/ P2-C):
//     锚点表达"该规则完整触发过"; 只执行一部分时写锚点会让规则被静默冷却整个
//     冷却窗, 用户无法在窗口内重试恢复。失败方向: 宁可让用户重试 (审计可见),
//     不可静默冷却掉规则;
//   - 日熔断按 rule 计数 (HandleTrigger 入口已判, 本函数不参与), A-15 钉死。
//
// 幂等键 (方案 §3.2.4 / §3.4 D10):
//
//	automation:<rule_id>:<yyyymmdd>:<trigger_seq>:<action_seq>
//
// 为什么必须加 action_seq 段: 同一规则的两个动作若指向**同一设备的同一
// action_id** (例如同一 pin 的两次 set), commandexec 的幂等 scope
// (user:<actor>:edge:<edge>:action:<action_id>:v:<version>) 完全相同。不加
// action_seq 时第二个动作会命中第一行的幂等键: 参数相同 → replay 静默吞掉
// (少执行一个动作), 参数不同 → ErrIdempotencyCollision (规则误报失败)。
// 两种情况都不可接受, A-5 用反例钉死。
func (p *Planner) executeActions(rule models.AutomationRule, at time.Time, value float64,
	actions []models.AutomationRuleAction, src *TriggerEvent) {
	if len(actions) == 0 {
		p.record(rule, at, value, models.AutomationResultFailedDispatch, "",
			"rule has no actions", src)
		return
	}

	// 触发序号 = 当日该规则的第几次触发决策 (P1-A/P2-B 修复, 见 nextTriggerSeq)。
	triggerSeq := p.nextTriggerSeq(context.Background(), rule.ID, at, "")

	// 系统 actor 惰性解析: 全新安装下该用户在进程启动后才创建, 这里才第一次拿得到。
	// 解析失败 = 自动路径被禁用 (未初始化 / 主体被停用): 照常留审计, 但 Detail 写
	// 真实原因 (不是笼统的 "invalid command request"), 并补一条面向用户的通知。
	var actorID uint
	if hasDeviceAction(actions) {
		var err error
		actorID, err = p.resolveSystemActorID()
		if err != nil {
			eventID := p.recordRet(rule, at, value, models.AutomationResultFailedDispatch, "",
				"system actor unavailable: "+err.Error(), src)
			p.notifySystemActorUnavailableOnce(at, eventID, err)
			return
		}
	}

	executed := 0
	lastCommandID := ""
	notified := 0
	for i := range actions {
		act := actions[i]
		if act.ActionType == models.AutomationActionNotification {
			// 通知动作无设备侧副作用, 失败无"半完成"风险; 但 notifyAction 本身
			// fail-open (写失败只告警), 因此这里不把通知计入可失败动作。
			p.notifyActionFor(rule, act, at, value)
			notified++
			continue
		}

		params, err := parseActionParams(act)
		if err != nil {
			p.recordActionFailure(rule, at, value, act, executed, len(actions), false,
				models.AutomationResultFailedDispatch, "invalid action_params: "+err.Error(), src)
			return
		}
		idemKey := fmt.Sprintf("automation:%d:%s:%d:%d",
			rule.ID, at.Format("20060102"), triggerSeq, act.Seq)
		exec, _, err := p.cmdSvc.Create(context.Background(), commandexec.CreateInput{
			EdgeDeviceID:   act.ActionDeviceID,
			ActorUserID:    actorID,
			ActorKind:      commandexec.ActorKindSystem,
			ActionID:       act.ActionID,
			Params:         params,
			IdempotencyKey: idemKey,
			Reason:         fmt.Sprintf("automation:%d:%s", rule.ID, rule.Name),
			// O-2 规则级授权 (实施计划 §0.1): 只有该规则真的持有用户显式授权
			// (risk_acknowledged_at 非空) 才允许风险动作跳过确认门。NULL = 未授权,
			// 风险动作仍会被确认门拦住并落 confirmation_required (绝不静默执行)。
			RiskAcknowledged: rule.RiskAcknowledgedAt != nil,
		})
		if err != nil {
			p.recordActionFailure(rule, at, value, act, executed, len(actions), true,
				classifyDeviceActionError(err), err.Error(), src)
			return
		}
		executed++
		lastCommandID = exec.CommandID
	}

	// ── 结果码分流: 纯通知 ≠ 执行 ──
	//
	// 为什么必须分流 (2026-10-18 真机闭环实测发现): 动作循环把整个 switch 换成
	// 循环后, 纯通知规则会掉进下面的 recordExecuted, 落 result='executed' 且
	// command_id 为空。这不只是显示问题, 它同时改变了四件事:
	//   ① 日熔断只 count(executed) ⇒ 纯通知规则开始占 MaxDailyExec 配额 (旧行为不限);
	//   ② recordExecuted 同事务写 last_triggered_at ⇒ 通知规则开始写冷却锚点 (旧行为不写);
	//   ③ datalifecycle 把 notification 归遥测档(30 天)、executed 归执行档(400 天)
	//      ⇒ 保留期语义被顺手改掉;
	//   ④ evaluator.backfillCooldownAnchors 按 result='executed' 回填 ⇒ 通知规则被带上锚点。
	// 而接口设计文档明文写着「纯通知动作成功记 notification (非 executed)」。
	//
	// 分流判据是**本次是否真的下发过设备动作**, 不是"规则类型是什么":
	//   · 只有通知动作 (executed==0 且 notified>0) ⇒ notification, 不写锚点;
	//   · 至少一个设备动作成功 ⇒ executed + 锚点 (保持既有语义);
	//   · 一个动作都没有 ⇒ 上面已按 failed_dispatch 返回, 到不了这里。
	if executed == 0 && notified > 0 {
		p.record(rule, at, value, models.AutomationResultNotification, "",
			fmt.Sprintf("notified via %d action(s)", notified), src)
		return
	}

	// 至少一个设备动作成功 → executed (+ 冷却锚点, 取触发时刻 at)。
	p.recordExecuted(rule, at, value, lastCommandID, src)
}

// recordActionFailure 记录第 k 个动作失败时的事件行 (方案 §6.3 状态机)。
//
// 结果码选择 (与 §6.3 表格逐条对应):
//   - k == 1 (第一个动作就失败, executed==0): 整体完全失败, 按原因落
//     failed_dispatch / failed_gate / confirmation_required;
//   - k > 1 (前面已有动作成功): **部分失败** —— 设备已被改了一半, 是危险状态,
//     必须与"完全失败"区分, 落 partial_failure 并记 action_seq=k。
//
// action_seq 写入规则: 只在"存在动作维度"时写 (本函数恒写), 规则级事件
// (熔断/冷却/条件失效/纯通知) 保持 NULL。
func (p *Planner) recordActionFailure(rule models.AutomationRule, at time.Time, value float64,
	act models.AutomationRuleAction, executed, total int, hasActionDim bool,
	result, detail string, src *TriggerEvent) {
	if executed > 0 {
		result = models.AutomationResultPartialFailure
		detail = fmt.Sprintf("第 %d/%d 个动作失败, 已中止剩余动作 (已成功 %d 个): %s",
			act.Seq, total, executed, detail)
	}
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      result,
		Detail:      detail,
		CreatedAt:   at,
	}
	applySourceColumns(&ev, src)
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if hasActionDim {
		seq := act.Seq
		ev.ActionSeq = &seq
	}
	if err := p.db.Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record action failure event",
			"rule_id", rule.ID, "action_seq", act.Seq, "error", err)
		return
	}
	p.broadcastAutomationEvent(rule, ev, value, "")
}

// parseActionParams 解析动作行的参数 JSON (fail-closed)。
func parseActionParams(act models.AutomationRuleAction) (json.RawMessage, error) {
	if act.ActionParamsJSON == "" {
		return json.RawMessage("{}"), nil
	}
	raw := json.RawMessage(act.ActionParamsJSON)
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid action_params json")
	}
	return raw, nil
}

// recordExecuted 落 result='executed' 事件, 并在【同一事务】内更新规则行的冷却锚点
// (清理前置条件 A, docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md)。
//
// 为什么必须同事务: 事件行与锚点共同表达"该规则在 at 时刻触发过"这一个事实。
// 分两次写会留下崩溃窗口 —— 事件落了锚点没落 → 重启后冷却提前解除 → 设备动作多发;
// 锚点落了事件没落 → 审计说没触发过但系统确实冷却了 (本测试注入失败即断言前者)。
//
// fail-open 边界: 与 recordRet 一致, 写失败只告警不阻塞控制流 —— 但【两者一起失败】,
// 不会出现"一半事实"。返回事件 ID (0=写失败), 供需要透传的调用方使用。
func (p *Planner) recordExecuted(rule models.AutomationRule, at time.Time, value float64,
	commandID string, src *TriggerEvent) uint {
	var eventID uint
	err := p.db.Transaction(func(tx *gorm.DB) error {
		ev := models.AutomationEvent{
			RuleID:      rule.ID,
			TriggeredAt: at,
			Result:      models.AutomationResultExecuted,
			CommandID:   commandID,
			CreatedAt:   at,
		}
		applySourceColumns(&ev, src)
		if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
			ev.TriggerValue = &value
		}
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		// 锚点取 at (本次触发时刻), 不取 DB now(): 求值器的冷却起点就是触发时刻,
		// 用落库时间会让重启后的冷却窗比崩溃前更长 (保守偏差, 同样是行为改变)。
		if err := tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
			Update("last_triggered_at", at).Error; err != nil {
			return err
		}
		eventID = ev.ID
		return nil
	})
	if err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record executed event + cooldown anchor",
			"rule_id", rule.ID, "error", err)
		return 0
	}
	if p.broadcast != nil {
		p.broadcast("automation_event", gin.H{
			"rule_id":    rule.ID,
			"rule_name":  rule.Name,
			"event_id":   eventID,
			"result":     models.AutomationResultExecuted,
			"value":      value,
			"command_id": commandID,
		})
	}
	return eventID
}

// checkConditionsStillSatisfied F4 条件复核（保留旧签名，供既有测试/调用方使用）。
//
// 语义：返回 (原因, 是否仍满足)。unresolved_selector 与 condition_changed 在旧签名里
// 都表现为 ok=false；需要区分结果码的调用方用 checkConditionsStillSatisfiedDetailed。
func (p *Planner) checkConditionsStillSatisfied(rule models.AutomationRule) (string, bool) {
	_, reason, ok := p.checkConditionsStillSatisfiedDetailed(rule)
	return reason, ok
}

// selectorOutcome 是一次 F4 复核的单 selector 结论。
type selectorOutcome struct {
	ok         bool   // 是否拿到了可用于复核的值
	satisfied  bool   // ok=true 时：条件是否仍满足
	value      float64
	reason     string
	unresolved bool // true = selector 解析不到（落 unresolved_selector，而不是 condition_changed）
}

// checkConditionsStillSatisfiedDetailed F4 条件复核（修 AU-12）。
//
// 原实现的缺陷（AU-12）：固定用"每设备最后一条最新值"复核，special selector（@soc /
// family / virtual）既无法命中（名字不等 ⇒ trigger 一律拒绝；conditions 一律跳过），
// 于是 selector 规则在 F4 阶段被静默拦死/漏判。修法（设计 §9.2#4）：
//   - literal：优先 (device, 物理名) 精确最新值（api.LatestValueBySensor）；
//     未接线时回落旧单值缓存（保持既有语义）；
//   - family/@semantic/virtual：用最新一帧多槽值 + 解析层 plan + EvalPredicate；
//   - 取不到 ⇒ fail-closed 并落审计（unresolved_selector / field_resolver_disabled）。
//
// 返回 (result, reason, ok)：ok=false 时调用方决定落哪条事件；result="" 表示
// condition_changed（条件已失效），否则是解析层专用的结果码。
// checkConditionsStillSatisfiedDetailed 保留旧签名（既有测试/调用方）：
// 等价于"无触发证据的存量规则"路径；生产 HandleTrigger 走
// checkConditionsStillSatisfiedWithSource（带 SourceEdgeDeviceID 证据）。
func (p *Planner) checkConditionsStillSatisfiedDetailed(rule models.AutomationRule) (string, string, bool) {
	return p.checkConditionsStillSatisfiedWithSource(rule, newTriggerEvent(rule, time.Time{}, 0, "", nil))
}

// checkConditionsStillSatisfiedDetailedLegacy 是 M0 之前的实现（仅作行为参照保留，
// 生产与测试入口都走上面的委托版本；删除会让"F4 有两份实现"的漂移风险重现，
// 因此保留但不再被调用）。**不要在此新增逻辑**。
func (p *Planner) checkConditionsStillSatisfiedDetailedLegacy(rule models.AutomationRule) (string, string, bool) {
	// 未接线（旧构造/单测）：跳过复核，保持既有行为。
	if p.latestValueFn == nil && p.latestValueBySensorFn == nil &&
		p.latestValuesFn == nil && p.planProvider == nil {
		return "", "", true
	}
	conds, err := rule.ParseConditions()
	if err != nil {
		return "", fmt.Sprintf("invalid conditions_json: %v", err), false
	}
	// 触发器设备 ID 为 0 时无法定位最新值, 跳过复核 (不推荐配置, 见模型注释)
	if rule.TriggerEdgeDeviceID == 0 {
		return "", "", true
	}
	deviceID := rule.TriggerEdgeDeviceID

	// Trigger 条件复核 (仅 sensor_threshold; time_window 由 evaluator 保证)。
	if rule.TriggerType == models.AutomationTriggerSensorThreshold {
		out := p.evalSelectorLatest(deviceID, rule.TriggerSensorName, rule.TriggerComparator, rule.TriggerThreshold)
		if out.unresolved {
			return resolverResultCode(out.reason), out.reason, false
		}
		if !out.ok {
			return "", out.reason, false
		}
		if !out.satisfied {
			return "", fmt.Sprintf("trigger condition no longer satisfied: %s %.2f vs threshold %.2f",
				rule.TriggerComparator, out.value, rule.TriggerThreshold), false
		}
	}

	// 附加条件复核 (全部 AND)。
	for _, c := range conds {
		out := p.evalSelectorLatest(deviceID, c.SensorName, c.Comparator, c.Threshold)
		if out.unresolved {
			return resolverResultCode(out.reason), out.reason, false
		}
		if !out.ok {
			// 历史语义：literal 附加条件无法复核时跳过（不阻断执行）；但特殊 selector
			// 的"取不到"就是解析失败，必须 fail-closed（AU-12 的另一半）。
			if isSpecialSelector(c.SensorName) {
				return ResultUnresolvedSelector, out.reason, false
			}
			continue
		}
		if !out.satisfied {
			return "", fmt.Sprintf("condition %s %s %.2f no longer satisfied: latest=%.2f",
				c.SensorName, c.Comparator, c.Threshold, out.value), false
		}
	}
	return "", "", true
}

// resolverResultCode 把解析层失败原因映射为 automation_events.result。
// kill switch 关闭 ⇒ field_resolver_disabled（运维处置不同），其余 ⇒ unresolved_selector。
func resolverResultCode(reason string) string {
	if strings.Contains(reason, "disabled") {
		return ResultFieldResolverDisabled
	}
	return ResultUnresolvedSelector
}

// evalSelectorLatest 按 selector 形态取"最新值"并复核一个比较条件。
func (p *Planner) evalSelectorLatest(deviceID uint, selector, comparator string, threshold float64) selectorOutcome {
	if isSpecialSelector(selector) {
		if p.planProvider == nil {
			return selectorOutcome{unresolved: true, reason: "field_resolver_not_configured"}
		}
		if !p.planProvider.Enabled() {
			return selectorOutcome{unresolved: true, reason: "field_resolver_disabled"}
		}
		if p.latestValuesFn == nil {
			return selectorOutcome{unresolved: true, reason: "latest_frame_unavailable"}
		}
		plan, err := p.planProvider.Compile(deviceID, selector)
		if err != nil {
			return selectorOutcome{unresolved: true, reason: "compile_failed:" + sensorfield.ErrorCode(err)}
		}
		verdict := sensorfield.EvalPredicate(plan, readingsFromUnified(p.latestValuesFn(deviceID)),
			sensorfield.CmpOp(comparator), threshold)
		if !verdict.OK {
			return selectorOutcome{unresolved: true, reason: verdict.Reason}
		}
		return selectorOutcome{ok: true, satisfied: verdict.Satisfied, value: verdict.Value}
	}

	// literal（含 legacy 值）：优先精确到物理名的最新值。
	if p.latestValueBySensorFn != nil {
		rec, found := p.latestValueBySensorFn(deviceID, selector)
		if !found || rec.SensorName != selector {
			return selectorOutcome{reason: fmt.Sprintf("latest value unavailable for sensor %q on edge_device_id=%d", selector, deviceID)}
		}
		return selectorOutcome{ok: true, satisfied: compare(comparator, rec.Value, threshold), value: rec.Value}
	}
	// 回落旧单值缓存：必须比对传感器名（防"拿 humidity 的值比温度阈值"）。
	if p.latestValueFn != nil {
		rec, found := p.latestValueFn(deviceID)
		if !found {
			return selectorOutcome{reason: fmt.Sprintf("latest value unavailable for edge_device_id=%d", deviceID)}
		}
		if rec.SensorName != selector {
			return selectorOutcome{reason: fmt.Sprintf("trigger sensor %q not in latest-value cache (cache holds %q); "+
				"cannot re-verify, refusing to execute", selector, rec.SensorName)}
		}
		return selectorOutcome{ok: true, satisfied: compare(comparator, rec.Value, threshold), value: rec.Value}
	}
	return selectorOutcome{reason: "latest value source unavailable"}
}

// readingsFromUnified 把最新值快照转成解析层帧视图（保留顺序；零业务依赖）。
func readingsFromUnified(values []models.UnifiedData) []sensorfield.Reading {
	if len(values) == 0 {
		return nil
	}
	out := make([]sensorfield.Reading, len(values))
	for i := range values {
		out[i] = sensorfield.Reading{Name: values[i].SensorName, Value: values[i].Value, Unit: values[i].Unit}
	}
	return out
}

// isGateError 判定 commandexec 返回错误是否属于 availability gate fail-closed
// (gate 拒绝 = 预期安全行为, 与 dispatch 传输失败在审计上区分)。
//
// 必须用 errors.Is 而不是比对错误文本: commandexec 的 gate 拒绝返回的是哨兵
// ErrActionUnavailable ("action is unavailable for this device", service.go:27),
// 旧实现的 len(msg)>=18 && msg[:18]=="action unavailable" 与它**恒不相等**
// ("action is unavail" != "action unavailable") —— failed_gate 因此全仓没有生产者,
// 门禁拒绝被错误地记成 failed_dispatch。
//
// Create 的所有失败出口都在 gorm 事务回调里直接 return 哨兵本身(service.go:449-531):
//   - 动作目录未命中 / 任一 availability gate 不通过 → ErrActionUnavailable (原样返回)
//   - 参数不可解析 → fmt.Errorf("%w: %v", ErrInvalidParams, err) (包装, 本就不是 gate)
//   - 其余 gate (确认制/近认证) → 各自的哨兵
//
// gorm 的 Transaction 用 defer 直接 return 回调的 err, 不再包一层, 因此 errors.Is
// 能可靠穿透。此处不再保留文案兜底: 文案比对正是这个缺陷的成因, 留着只会再次腐烂。
func isGateError(err error) bool {
	return errors.Is(err, commandexec.ErrActionUnavailable)
}

// isConfirmationGateError 判定 commandexec 返回错误是否来自**确认门**。
//
// 覆盖三个哨兵 (方案 §3.2.5 第 2 步):
//   - ErrConfirmationRequired: 没有 token / token 为空 → 需要人工确认;
//   - ErrRecentAuthRequired:   近认证窗口已过 → 需要用户重新验证身份;
//   - ErrConfirmationInvalid:  token 无效/过期/已用 → 需要重新走确认流程。
//
// 注意这不是把确认门归为 availability gate —— failed_gate 仍然只表示 availability
// gate 的 fail-closed。确认门是**预期安全行为**(需要人), 必须与传输失败、门禁拒绝
// 在审计上三分, 否则自动路径遇确认门会继续落 failed_dispatch, 前端拿不到可行动信号
// (这正是 C5 缺陷)。
func isConfirmationGateError(err error) bool {
	return errors.Is(err, commandexec.ErrConfirmationRequired) ||
		errors.Is(err, commandexec.ErrRecentAuthRequired) ||
		errors.Is(err, commandexec.ErrConfirmationInvalid)
}

// classifyDeviceActionError 把 commandexec.Create 的错误映射到 automation_events
// 的 result 枚举。**唯一的错误分类入口** —— 自动路径与手动路径共用, 避免两处
// 各自演化出不一致的映射 (本文件里 isGateError 死分支的教训)。
//
// 顺序说明: 先判确认门再判 availability gate。两者在 Create 内互斥 (availability
// gate 循环在确认门之前短路返回 ErrActionUnavailable, 走到确认门说明 gate 全过),
// 因此该顺序不会遮蔽 failed_gate 的既有语义; 写成 switch 也让"新增一类错误"必须
// 显式落到某个枚举, 而不是被兜底吞掉。
func classifyDeviceActionError(err error) string {
	switch {
	case isConfirmationGateError(err):
		return models.AutomationResultConfirmationRequired
	case isGateError(err):
		return models.AutomationResultFailedGate
	default:
		return models.AutomationResultFailedDispatch
	}
}

// automationActionSetHash 是动作集合的**归一化**哈希（Y5 TOCTOU 快照）。
//
// 归一化要求（Lead 2026-10-21 裁决）：按 (type, target, params) 排序后哈希，忽略
// 展示字段（enabled/action_level/seq 重排本身）—— 只改通知级别不该让 pending 失效。
// 用无分隔符歧义的定长编码（长度前缀），避免 "a|b" 与 "a" + "b" 撞哈希。
func automationActionSetHash(actions []models.AutomationRuleAction) string {
	type entry struct {
		typ     string
		target  uint
		action  string
		params  string
	}
	entries := make([]entry, 0, len(actions))
	for _, a := range actions {
		if a.ActionType != models.AutomationActionDeviceAction {
			continue // 通知动作没有物理副作用，不参与"动作是否变了"的判定
		}
		entries = append(entries, entry{
			typ:    a.ActionType,
			target: a.ActionDeviceID,
			action: a.ActionID,
			params: canonicalActionParams(a.ActionParamsJSON),
		})
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].target != entries[j].target {
			return entries[i].target < entries[j].target
		}
		if entries[i].action != entries[j].action {
			return entries[i].action < entries[j].action
		}
		if entries[i].params != entries[j].params {
			return entries[i].params < entries[j].params
		}
		return entries[i].typ < entries[j].typ
	})
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%d:%s|%d:%s|%d:%s|%d:%s;", len(e.typ), e.typ, len(e.action), e.action,
			len(e.params), e.params, 0, "")
		fmt.Fprintf(h, "%d;", e.target)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// canonicalActionParams 把 params JSON 规范化为稳定字符串（键序无关）。
// 非法 JSON 原样返回（哈希仍可用于"变了"的判定）。
func canonicalActionParams(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "{}"
	}
	var v interface{}
	if err := json.Unmarshal([]byte(trimmed), &v); err != nil {
		return trimmed
	}
	canonical, err := json.Marshal(v)
	if err != nil {
		return trimmed
	}
	return string(canonical)
}

// recordPendingConfirm 落 pending_confirm 事件（含动作集合哈希快照，Y5）。
func (p *Planner) recordPendingConfirm(rule models.AutomationRule, at time.Time, value float64,
	actions []models.AutomationRuleAction, src *TriggerEvent) uint {
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      models.AutomationResultPendingConfirm,
		Detail:      "awaiting manual confirmation",
		ActionHash:  automationActionSetHash(actions),
		CreatedAt:   at,
	}
	applySourceColumns(&ev, src)
	// Y5 + D-8：pending 期间来源可能切换，confirm 必须能证明"来源没变"。
	// 存量单设备规则 source_hash 为空（不启用比对，保持兼容）。
	if src != nil && src.SourceKind != "" {
		ev.SourceHash, ev.SourceEvidence = p.pendingSourceSnapshot(rule, src)
	}
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if err := p.db.Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record pending_confirm event", "rule_id", rule.ID, "error", err)
		return 0
	}
	p.broadcastAutomationEvent(rule, ev, value, "")
	return ev.ID
}

// record 落审计行 (fail-open: 写失败只计指标不阻塞, 对齐 alert 风格)。
func (p *Planner) record(rule models.AutomationRule, at time.Time, value float64,
	result, commandID, detail string, src *TriggerEvent) {
	p.recordRet(rule, at, value, result, commandID, detail, src)
}

// recordRet 同 record, 但返回落库事件 ID (0=写失败)。确认分流需 eventID 透传通知。
func (p *Planner) recordRet(rule models.AutomationRule, at time.Time, value float64,
	result, commandID, detail string, src *TriggerEvent) uint {
	ev := models.AutomationEvent{
		RuleID:      rule.ID,
		TriggeredAt: at,
		Result:      result,
		CommandID:   commandID,
		Detail:      detail,
		CreatedAt:   at,
	}
	// 来源归因（方案 §4.4）：group 规则的事件必须能回答"谁供的数"。
	applySourceColumns(&ev, src)
	if value != 0 || rule.TriggerType == models.AutomationTriggerSensorThreshold {
		ev.TriggerValue = &value
	}
	if err := p.db.Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record event", "rule_id", rule.ID, "error", err)
		return 0
	}
	p.broadcastAutomationEvent(rule, ev, value, commandID)
	return ev.ID
}

// broadcastAutomationEvent 广播 automation_event (既有 WS 契约, 方案 §4.3)。
//
// action_seq 只在非空时进入载荷: 规则级事件不带该键, 保持与改造前逐字节兼容的
// 载荷形状; 多动作失败事件额外带 action_seq 供前端精确展示"第几个动作失败"。
func (p *Planner) broadcastAutomationEvent(rule models.AutomationRule, ev models.AutomationEvent,
	value float64, commandID string) {
	if p.broadcast == nil {
		return
	}
	payload := gin.H{
		"rule_id":    rule.ID,
		"rule_name":  rule.Name,
		"event_id":   ev.ID,
		"result":     ev.Result,
		"value":      value,
		"command_id": commandID,
	}
	if ev.ActionSeq != nil {
		payload["action_seq"] = *ev.ActionSeq
	}
	p.broadcast("automation_event", payload)
}

// notificationBroadcastPayload 构造自动化通知的 WS 广播载荷 (负债 D-3 契约):
// 顶层 = 通知实体本身, 与前端 api/notification.ts 的 Notification 接口、
// GET /notifications 列表项同形 (id/type/title/description/message/source/
// source_id/read/created_at); 自动化领域详情 (rule_id/event_id/...) 收进嵌套
// detail 字段, 不与通知字段平铺混淆。
//
// 调用前提: 通知行已 Create 成功 (n.ID != 0) —— 只有拿到自增 ID 才广播,
// 否则前端会插入一条库里不存在的通知且永远无法标记已读。
func notificationBroadcastPayload(n models.Notification, detail gin.H) gin.H {
	return gin.H{
		// ── 通知实体 (与列表项同形) ──
		"id":          n.ID,
		"type":        n.Type,
		"title":       n.Title,
		"description": n.Description,
		"message":     n.Message,
		"source":      n.Source,
		"source_id":   n.SourceID,
		"read":        n.Read,
		"created_at":  n.CreatedAt,
		// ── 自动化领域详情 (嵌套, 不平铺) ──
		"detail": detail,
	}
}

// notifyDailyLimitOnce F5 日熔断 warning 通知, 同日同规则只发一次 (幂等)。
// 复用 notifyConfirmation 的 Notification 构造模式, level=warning。
func (p *Planner) notifyDailyLimitOnce(rule models.AutomationRule, at time.Time, eventID uint) {
	dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	var cnt int64
	// 幂等闸: 当日已发过该规则的 daily_limit warning 通知则跳过
	if err := p.db.Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ? AND created_at >= ?",
			"automation_rule", fmt.Sprintf("%d", rule.ID),
			"策略日熔断: "+rule.Name, dayStart).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	desc := fmt.Sprintf("策略「%s」已达日执行上限 (MaxDailyExec=%d), 今日后续触发将被抑制 (event_id=%d)",
		rule.Name, rule.MaxDailyExec, eventID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelWarning),
		Title:       "策略日熔断: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if !p.createNotification(context.Background(), &n, "daily_limit") {
		return
	}
	if p.broadcast != nil {
		// 载荷 = 通知实体本身 (负债 D-3, 与 alert 同契约): 前端收到即可插入列表;
		// 自定义事件名保留 (前端按语义给出差异化提示), 自动化领域详情进嵌套 detail。
		p.broadcast("automation_daily_limit", notificationBroadcastPayload(n, gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"limit":     rule.MaxDailyExec,
		}))
	}
}

// 系统 actor 不可用通知的幂等键 (查询与落库共用同一组常量)。
// 与 notifyDailyLimitOnce 同样是 (source, source_id, title) 三元组幂等, 但**不按天重置**:
// "未初始化 / 主体被停用"是持续性的系统状态, 不是每日配额; 同一原因只发一条,
// 避免每次触发都往通知中心刷 (修好前每次触发的原因完全相同)。
const (
	automationSystemActorSource   = "automation_system"
	automationSystemActorSourceID = "system_admin"
	automationSystemActorTitle    = "自动化执行已禁用: 系统主体用户不可用"
)

// notifySystemActorUnavailableOnce 系统 actor 解析失败的告警通知 (同一原因只发一次)。
//
// 为什么必须通知: 解析失败时自动 device_action 全部静默失败 (事件只落审计表),
// 用户会误以为策略生效。这里是唯一面向用户的提示 —— 级别用 critical (type=error),
// 与"整条自动化执行链被禁用"的严重度相称, 比日熔断的 warning 更醒目。
func (p *Planner) notifySystemActorUnavailableOnce(at time.Time, eventID uint, cause error) {
	var cnt int64
	// 幂等闸: 已有同 (source, source_id, title) 通知则跳过 (全部历史, 不按天)
	if err := p.db.Model(&models.Notification{}).
		Where("source = ? AND source_id = ? AND title = ?",
			automationSystemActorSource, automationSystemActorSourceID, automationSystemActorTitle).
		Count(&cnt).Error; err == nil && cnt > 0 {
		return
	}
	desc := fmt.Sprintf("自动化 device_action 执行已被禁用: 无法解析系统主体用户 (subject_key=%s)。"+
		"若系统尚未初始化, 请完成初始化 (POST /api/v1/auth/initialize); 若已初始化, "+
		"请检查该主体是否被停用 (retired_at)。原因: %v (event_id=%d)",
		models.SystemAdminSubjectKey, cause, eventID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelCritical),
		Title:       automationSystemActorTitle,
		Message:     desc,
		Description: desc,
		Source:      automationSystemActorSource,
		SourceID:    automationSystemActorSourceID,
		Read:        false,
		CreatedAt:   at,
	}
	if !p.createNotification(context.Background(), &n, "system_actor_unavailable") {
		return
	}
	if p.broadcast != nil {
		p.broadcast("automation_system_actor_unavailable", notificationBroadcastPayload(n, gin.H{
			"event_id": eventID,
			"reason":   cause.Error(),
		}))
	}
}

// notifyConfirmation require_confirmed=true 的"建议执行"通知 (铁律 4)。
// eventID 透传给前端, 人工据此调 POST /automation-events/:id/confirm 定位该事件。
func (p *Planner) notifyConfirmation(rule models.AutomationRule, at time.Time, value float64, eventID uint) {
	desc := fmt.Sprintf("策略「%s」已触发 (值 %.2f), 动作 %s 待人工确认执行 (event_id=%d)", rule.Name, value, rule.ActionID, eventID)
	n := models.Notification{
		Type:        models.NotificationType(models.AlertLevelWarning),
		Title:       "策略待确认: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if !p.createNotification(context.Background(), &n, "pending_confirm") {
		return
	}
	if p.broadcast != nil {
		p.broadcast("automation_pending_confirm", notificationBroadcastPayload(n, gin.H{
			"rule_id":   rule.ID,
			"rule_name": rule.Name,
			"event_id":  eventID,
			"action_id": rule.ActionID,
			"value":     value,
		}))
	}
}

// notifyAction action=notification 的纯通知动作。
func (p *Planner) notifyAction(rule models.AutomationRule, at time.Time, value float64) {
	p.notifyActionFor(rule, models.AutomationActionFromRule(rule), at, value)
}

// notifyActionFor 多动作路径下的纯通知动作 (动作行携带自己的 action_level)。
//
// 与 notifyAction 的差异只有动作来源: 多动作下 level 取自**该动作行**,
// 而不是规则的镜像列。两者共用同一段落库逻辑, 避免两份通知实现漂移。
func (p *Planner) notifyActionFor(rule models.AutomationRule, act models.AutomationRuleAction,
	at time.Time, value float64) {
	level := act.ActionLevel
	if level == "" {
		level = models.AlertLevelInfo
	}
	desc := fmt.Sprintf("策略「%s」触发通知 (值 %.2f)", rule.Name, value)
	n := models.Notification{
		Type:        models.NotificationType(level),
		Title:       "策略通知: " + rule.Name,
		Message:     desc,
		Description: desc,
		Source:      "automation_rule",
		SourceID:    fmt.Sprintf("%d", rule.ID),
		Read:        false,
		CreatedAt:   at,
	}
	if !p.createNotification(context.Background(), &n, "notification_action") {
		return
	}
}

// 防止 json 未使用告警 (ParseActionParams 返回 json.RawMessage 已用, 此处仅为显式 import 对称)。
var _ = json.RawMessage{}

// ── 确认制闭环 (裁决 4, 设计/自动化确认制闭环实现方案.md) ──

// pendingConfirmTTL pending_confirm 事件人工确认窗口, 超时由 StartCleanup 置 expired。
const pendingConfirmTTL = 24 * time.Hour

// cleanupInterval 超时清扫周期 (独立于 evaluator ticker, 因 evaluator 只缓存
// sensor_threshold 规则, time_window 事件会漏扫且职责错位)。
const cleanupInterval = 5 * time.Minute

// ── 手动触发 (POST /api/v1/automation-rules/:id/trigger) ──

// TriggerRule 手动触发错误哨兵 (handler 据此映射 HTTP 状态码)。
var (
	ErrTriggerRuleNotFound = errors.New("automation rule not found")
	ErrTriggerRuleDisabled = errors.New("automation rule is disabled")
)

// TriggerRule 手动触发一条自动化规则 (手动触发端点)。
//
// 与自动触发 (HandleTrigger) 的差异:
//   - 跳过条件评估: 用户点击即确认, 不查 F4 conditions
//   - 跳过确认制:   require_confirmed=true 的规则也直接执行 (点击按钮=人工确认)
//   - 保留安全门禁: cooldown / max_daily_exec / 日熔断 仍然生效
//   - 审计标记:     TriggerSource = manual, Reason 带触发者 ID
//
// 返回落库的 AutomationEvent (含 result), 调用方据此返回 HTTP 200/409。
func (p *Planner) TriggerRule(ctx context.Context, ruleID, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	var rule models.AutomationRule
	if err := p.db.WithContext(ctx).First(&rule, ruleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return models.AutomationEvent{}, ErrTriggerRuleNotFound
		}
		return models.AutomationEvent{}, err
	}
	if !rule.Enabled {
		return models.AutomationEvent{}, ErrTriggerRuleDisabled
	}

	at := p.nowFn()

	// ── 日熔断: MaxDailyExec > 0 时统计当日 executed 行数 (与自动触发同口径) ──
	if rule.MaxDailyExec > 0 {
		dayStart := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
		var cnt int64
		if err := p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
			Where("rule_id = ? AND triggered_at >= ? AND result = ?",
				rule.ID, dayStart, models.AutomationResultExecuted).
			Count(&cnt).Error; err == nil && int(cnt) >= rule.MaxDailyExec {
			ev := models.AutomationEvent{
				RuleID:        rule.ID,
				TriggeredAt:   at,
				TriggerSource: models.AutomationTriggerSourceManual,
				Result:        models.AutomationResultSuppressedDailyLimit,
				Detail:        fmt.Sprintf("daily limit %d reached", rule.MaxDailyExec),
				CreatedAt:     at,
			}
			_ = p.db.WithContext(ctx).Create(&ev).Error
			p.notifyDailyLimitOnce(rule, at, ev.ID)
			return ev, nil
		}
	}

	// ── 冷却抑制: 查最近一次 executed/pending_confirm 的 triggered_at,
	//    与自动触发 evaluator 的内存 cooldown 等价 (planner 侧 DB 兜底)。
	//    判定走 cooldownFor — 与 evaluator 同一份实现 (负债 D-5: 两路径曾语义相反,
	//    evaluator 把 0 当默认 300s, 此处把 0 当不冷却); 0 表示不冷却, 直接跳过。 ──
	if cooldown := cooldownFor(rule); cooldown > 0 {
		// 冷却基线优先读规则行锚点 (清理前置条件 A): automation_events 会被保留策略
		// 清理, 拿它当唯一基线会让"删审计"变成"冷却提前解除"。锚点为空时 (锚点列
		// 上线前的历史数据 / 回填尚未跑) 才回落到事件表, 保持既有行为不失。
		lastAt, ok := p.cooldownBase(ctx, rule)
		if ok {
			if at.Sub(lastAt) < cooldown {
				remaining := cooldown - at.Sub(lastAt)
				ev := models.AutomationEvent{
					RuleID:        rule.ID,
					TriggeredAt:   at,
					TriggerSource: models.AutomationTriggerSourceManual,
					Result:        models.AutomationResultSuppressedCooldown,
					Detail:        fmt.Sprintf("cooldown active (%.0fs remaining)", remaining.Seconds()),
					CreatedAt:     at,
				}
				_ = p.db.WithContext(ctx).Create(&ev).Error
				return ev, nil
			}
		}
	}

	// ── 动作分发 ──
	switch rule.ActionType {
	case models.AutomationActionNotification:
		p.notifyAction(rule, at, 0)
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultNotification,
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	case models.AutomationActionDeviceAction:
		return p.executeManualDeviceAction(ctx, rule, at, actorID, sourceIP)
	default:
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultFailedDispatch,
			Detail:        "unknown action_type: " + rule.ActionType,
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	}
}

// cooldownBase 返回某规则冷却窗的起算时刻 (是否存在)。
//
// 读取优先级 (清理前置条件 A):
//  1. automation_rules.last_triggered_at —— 唯一持久锚点, 清理器不碰;
//  2. 回落 automation_events "最近一条 executed/pending_confirm" —— 仅用于锚点列
//     上线前的历史数据 (回填尚未跑到时), 保持既有行为不失。
//
// 语义差异说明: 锚点记录【触发】时刻 (executed 落库那一刻的 at); 旧事件表兜底口径
// 把 pending_confirm 也算作冷却起点。回落分支保留该口径以便与旧数据对齐, 主分支
// 只认锚点 —— 一旦锚点被回填/写入, 行为即收敛到 evaluator 的触发时刻语义。
func (p *Planner) cooldownBase(ctx context.Context, rule models.AutomationRule) (time.Time, bool) {
	if rule.LastTriggeredAt != nil {
		return *rule.LastTriggeredAt, true
	}
	var lastEv models.AutomationEvent
	err := p.db.WithContext(ctx).
		Where("rule_id = ? AND result IN ?", rule.ID,
			[]string{models.AutomationResultExecuted, models.AutomationResultPendingConfirm}).
		Order("triggered_at DESC").First(&lastEv).Error
	if err != nil {
		return time.Time{}, false
	}
	return lastEv.TriggeredAt, true
}

// executeManualDeviceAction 手动触发的 device_action 执行 (多动作: 串行, 同 §6.3)。
// 跳过 F4 条件复核 (用户已确认), 但走全 commandexec.Service.Create 的
// availability gate + 幂等 + 审计链路。
//
// 幂等键: automation:manual:<rule_id>:<yyyymmdd>:<trigger_seq>:<action_seq> ——
// 与自动路径同构 (方案 §3.2.4), 手动命名空间独立, 同样必须带 action_seq 段,
// 否则同一规则两个指向同设备同 action_id 的动作会互相吞掉 (A-5 的同款反例)。
func (p *Planner) executeManualDeviceAction(ctx context.Context, rule models.AutomationRule,
	at time.Time, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	actions := p.ruleActions(rule)
	if len(actions) == 0 {
		ev := models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        models.AutomationResultFailedDispatch,
			Detail:        "rule has no actions",
			CreatedAt:     at,
		}
		_ = p.db.WithContext(ctx).Create(&ev).Error
		return ev, nil
	}

	// 幂等键: 手动触发独立命名空间, 当日触发序号 (口径同自动路径, 见 nextTriggerSeq)。
	triggerSeq := p.nextTriggerSeq(ctx, rule.ID, at, models.AutomationTriggerSourceManual)

	newManualEvent := func(result, detail, commandID string, actionSeq *int) models.AutomationEvent {
		return models.AutomationEvent{
			RuleID:        rule.ID,
			TriggeredAt:   at,
			TriggerSource: models.AutomationTriggerSourceManual,
			Result:        result,
			CommandID:     commandID,
			Detail:        detail,
			ActionSeq:     actionSeq,
			CreatedAt:     at,
		}
	}

	executed := 0
	lastCommandID := ""
	for i := range actions {
		act := actions[i]
		if act.ActionType == models.AutomationActionNotification {
			p.notifyActionFor(rule, act, at, 0)
			continue
		}
		params, err := parseActionParams(act)
		if err != nil {
			return p.recordManualActionFailure(ctx, rule, at, act, executed, len(actions),
				models.AutomationResultFailedDispatch, "invalid action_params: "+err.Error())
		}
		idempotencyKey := fmt.Sprintf("automation:manual:%d:%s:%d:%d",
			rule.ID, at.Format("20060102"), triggerSeq, act.Seq)
		exec, _, err := p.cmdSvc.Create(ctx, commandexec.CreateInput{
			EdgeDeviceID:   act.ActionDeviceID,
			ActorUserID:    actorID,
			ActionID:       act.ActionID,
			Params:         params,
			IdempotencyKey: idempotencyKey,
			SourceIP:       sourceIP,
			Reason:         fmt.Sprintf("automation:manual:%d:%s:by_user:%d", rule.ID, rule.Name, actorID),
		})
		if err != nil {
			return p.recordManualActionFailure(ctx, rule, at, act, executed, len(actions),
				classifyDeviceActionError(err), err.Error())
		}
		executed++
		lastCommandID = exec.CommandID
	}

	ev := newManualEvent(models.AutomationResultExecuted, "", lastCommandID, nil)
	// 手动触发同样要更新冷却锚点 (清理前置条件 A): 手动执行成功也是"该规则触发过"
	// 这一事实, 且 planner.TriggerRule 的手动冷却判定正是读这个锚点。
	if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&ev).Error; err != nil {
			return err
		}
		return tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
			Update("last_triggered_at", at).Error
	}); err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record manual executed event + cooldown anchor",
			"rule_id", rule.ID, "error", err)
	}
	if p.broadcast != nil {
		p.broadcast("automation_event", gin.H{
			"rule_id":        rule.ID,
			"rule_name":      rule.Name,
			"event_id":       ev.ID,
			"result":         ev.Result,
			"command_id":     lastCommandID,
			"trigger_source": models.AutomationTriggerSourceManual,
		})
	}
	return ev, nil
}

// recordManualActionFailure 手动路径的动作失败事件 (部分失败语义同自动路径)。
func (p *Planner) recordManualActionFailure(ctx context.Context, rule models.AutomationRule,
	at time.Time, act models.AutomationRuleAction, executed, total int,
	result, detail string) (models.AutomationEvent, error) {
	seq := act.Seq
	if executed > 0 {
		result = models.AutomationResultPartialFailure
		detail = fmt.Sprintf("第 %d/%d 个动作失败, 已中止剩余动作 (已成功 %d 个): %s",
			act.Seq, total, executed, detail)
	}
	ev := models.AutomationEvent{
		RuleID:        rule.ID,
		TriggeredAt:   at,
		TriggerSource: models.AutomationTriggerSourceManual,
		Result:        result,
		Detail:        detail,
		ActionSeq:     &seq,
		CreatedAt:     at,
	}
	if err := p.db.WithContext(ctx).Create(&ev).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("automation_planner", "automation_events").Inc()
		logger.Warn("automation: failed to record manual action failure",
			"rule_id", rule.ID, "action_seq", act.Seq, "error", err)
	}
	p.broadcastAutomationEvent(rule, ev, 0, "")
	return ev, nil
}

// ConfirmEvent 错误哨兵 (handler 据此映射 HTTP 状态码)。
var (
	ErrConfirmEventNotFound = errors.New("automation event not found")
	ErrConfirmNotPending    = errors.New("automation event is not awaiting confirmation")
	ErrConfirmRuleMissing   = errors.New("automation rule no longer exists")
	ErrConfirmActionChanged = errors.New("automation rule action changed, cannot confirm")
	ErrConfirmInvalidParams = errors.New("automation rule action params invalid")
	ErrConfirmEventExpired  = errors.New("automation event confirmation window expired")
)

// ConfirmEvent 人工确认执行 pending_confirm 事件 (路径 B: 操作者 confirm 当下即铸即销 token)。
//
// **多动作语义与自动/手动路径同构** (P2-E 修复, 评审 verify-b2): 按 seq 升序串行执行
// 动作集合, 每个动作各自即铸即销一个 token; 失败中止剩余; 全部成功 executed /
// 部分成功 partial_failure / 首个失败按原因落 failed_*。
//
// 修复前的缺陷: 本函数只 Create 一次且幂等键是 automation:confirm:<ruleID>:<eventID>
// (无 action_seq), 2 动作规则人工确认后只下发 1 行 command_executions —— 用户在卡片上
// 看到 2 个动作、实际执行 1 个, 第 2 个被静默丢弃。
//
// 幂等防重: 每个动作的键 automation:confirm:<ruleID>:<eventID>:<action_seq>, 与
// (事件, 动作) 1:1; 重复 confirm 同 eventID 命中 command_executions 唯一索引走 replay
// 只读; 条件 UPDATE (result='pending_confirm') 作应用层第一道防重闸。
// action_seq 段是必需的 (与 A-5 同源): 两个动作指向同设备同 action_id 时 scope 相同,
// 缺该段会让第 2 个动作被 replay 吞掉或报 ErrIdempotencyCollision。
//
// TOCTOU: 现读 rule 最新动作, rule 被删时 fail-closed 报错; action_type 不再是
// device_action 或动作集合为空时同样 fail-closed (错误语义与改造前一致)。
//
// ⚠️ 部分失败**不写冷却锚点** (已裁决, 见方案 §3.2.4 «部分失败的锚点» 与 P2-C):
// 只执行了一部分动作不构成"该规则已完整触发", 写锚点会让规则被静默冷却。
func (p *Planner) ConfirmEvent(ctx context.Context, eventID, actorID uint, sourceIP string) (models.AutomationEvent, error) {
	var ev models.AutomationEvent
	if err := p.db.WithContext(ctx).First(&ev, eventID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ev, ErrConfirmEventNotFound
		}
		return ev, err
	}
	if ev.Result == models.AutomationResultExpired {
		return ev, ErrConfirmEventExpired
	}
	if ev.Result != models.AutomationResultPendingConfirm {
		return ev, ErrConfirmNotPending
	}
	// 双保险: 落库时刻 triggered_at 已超窗 (清扫可能尚未跑到) 也按超时拒绝。
	if p.nowFn().Sub(ev.TriggeredAt) > pendingConfirmTTL {
		return ev, ErrConfirmEventExpired
	}

	var rule models.AutomationRule
	if err := p.db.WithContext(ctx).First(&rule, ev.RuleID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return ev, ErrConfirmRuleMissing
		}
		return ev, err
	}
	if rule.ActionType != models.AutomationActionDeviceAction {
		return ev, ErrConfirmActionChanged
	}
	actions := p.ruleActions(rule)
	if len(actions) == 0 {
		return ev, ErrConfirmActionChanged
	}
	// Y5 TOCTOU（裁决 B）：pending 时快照的动作集合必须与**当前**动作集合一致。
	// 只比对非空快照（ActionHash 列上线前的历史 pending 事件为空，保持兼容）。
	if ev.ActionHash != "" {
		if current := automationActionSetHash(actions); current != ev.ActionHash {
			return ev, ErrConfirmActionChanged
		}
	}
	// D-8 来源 TOCTOU：pending 期间组状态（active/成员/版本）变化 ⇒ 用户确认的
	// 决策依据已不存在，拒绝执行（与动作变更同语义）。空串 = 存量/单设备事件，跳过。
	if ev.SourceHash != "" {
		current, ok := p.currentSourceSnapshot(rule)
		if !ok || current != ev.SourceHash {
			logger.Warn("automation: confirm rejected, source group changed while pending",
				"rule_id", rule.ID, "event_id", ev.ID)
			return ev, ErrConfirmActionChanged
		}
	}

	reason := fmt.Sprintf("automation:confirm:%d", rule.ID)
	// 冷却锚点 (清理前置条件 A) 用原事件的 TriggeredAt (pending_confirm 落库时刻),
	// 而不是 now(): 冷却窗起点是"策略判定该触发"的时刻, 与自动/手动路径同语义。
	flipExecuted := func(tx *gorm.DB, commandID string) error {
		res := tx.Model(&models.AutomationEvent{}).
			Where("id = ? AND result = ?", ev.ID, models.AutomationResultPendingConfirm).
			Updates(map[string]interface{}{
				"result":     models.AutomationResultExecuted,
				"command_id": commandID,
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return nil // 并发 confirm/expired: 未翻转, 不动锚点
		}
		return tx.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
			Update("last_triggered_at", ev.TriggeredAt).Error
	}

	executed := 0
	lastCommandID := ""
	for i := range actions {
		act := actions[i]
		if act.ActionType == models.AutomationActionNotification {
			// 与自动路径一致: 通知动作无设备侧副作用, 不消耗 token, 不计入可失败动作。
			p.notifyActionFor(rule, act, ev.TriggeredAt, 0)
			continue
		}
		params, err := parseActionParams(act)
		if err != nil {
			return p.recordConfirmFailure(ctx, &ev, rule, act, executed, len(actions),
				models.AutomationResultFailedDispatch, err.Error(), err)
		}
		// 即铸: 操作者 confirm 当下为**该动作**铸造 (近认证门由 IssueConfirmation
		// 内核对操作者 LastLoginAt 强制)。
		grant, err := p.cmdSvc.IssueConfirmation(ctx, commandexec.ConfirmationInput{
			EdgeDeviceID: act.ActionDeviceID,
			ActorUserID:  actorID,
			ActionID:     act.ActionID,
			Params:       params,
			Reason:       reason,
			SourceIP:     sourceIP,
		})
		if err != nil {
			return p.recordConfirmFailure(ctx, &ev, rule, act, executed, len(actions),
				classifyDeviceActionError(err), err.Error(), err)
		}
		// 即销: Create 携 token 消费, 走全 availability gate + 幂等 + 审计 + readback。
		exec, _, err := p.cmdSvc.Create(ctx, commandexec.CreateInput{
			EdgeDeviceID:      act.ActionDeviceID,
			ActorUserID:       actorID,
			ActionID:          act.ActionID,
			Params:            params,
			IdempotencyKey:    fmt.Sprintf("automation:confirm:%d:%d:%d", rule.ID, ev.ID, act.Seq),
			SourceIP:          sourceIP,
			ConfirmationToken: grant.Token,
			Reason:            reason,
		})
		if err != nil {
			return p.recordConfirmFailure(ctx, &ev, rule, act, executed, len(actions),
				classifyDeviceActionError(err), err.Error(), err)
		}
		executed++
		lastCommandID = exec.CommandID
	}

	// 全部动作成功 → 翻转事件为 executed + 写冷却锚点 (同一事务)。
	if err := p.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return flipExecuted(tx, lastCommandID)
	}); err != nil {
		logger.Warn("automation: failed to flip event + cooldown anchor",
			"event_id", ev.ID, "rule_id", rule.ID, "error", err)
	}
	p.db.WithContext(ctx).First(&ev, ev.ID)
	return ev, nil
}

// recordConfirmFailure 记录人工确认路径的动作失败 (方案 §6.3 状态机)。
//
// 语义与 recordActionFailure / recordManualActionFailure 一致: 首个动作失败 = 完全
// 失败 (按原因落 failed_*), 第 k>1 个失败 = partial_failure + action_seq=k。
// **部分失败不写冷却锚点** (P2-C 裁决): 规则没有完整执行完, 锚点会静默冷却它。
//
// cause 是**透传给调用方**的原始错误 (Create 的 gate/确认门/参数错误)。为什么必须
// 透传而不是吞掉: handler 依赖它映射 HTTP 状态码 (ErrRecentAuthRequired → 403 等),
// 且既有契约 TestConfirmEventCreateFailureFlipsFailed 断言"不吞错也不留 pending 悬置"。
// 落库失败只告警 (与 recordActionFailure 同款 fail-open), 不覆盖 cause。
func (p *Planner) recordConfirmFailure(ctx context.Context, ev *models.AutomationEvent,
	rule models.AutomationRule, act models.AutomationRuleAction, executed, total int,
	result, detail string, cause error) (models.AutomationEvent, error) {
	seq := act.Seq
	if executed > 0 {
		result = models.AutomationResultPartialFailure
		detail = fmt.Sprintf("第 %d/%d 个动作失败, 已中止剩余动作 (已成功 %d 个): %s",
			act.Seq, total, executed, detail)
	}
	err := p.db.WithContext(ctx).Model(&models.AutomationEvent{}).
		Where("id = ? AND result = ?", ev.ID, models.AutomationResultPendingConfirm).
		Updates(map[string]interface{}{
			"result":     result,
			"detail":     detail,
			"action_seq": seq,
		}).Error
	if err != nil {
		logger.Warn("automation: failed to record confirm action failure",
			"event_id", ev.ID, "action_seq", act.Seq, "error", err)
	}
	p.db.WithContext(ctx).First(ev, ev.ID)
	// 返回 cause (调用方的动作错误), 不是落库错误: handler 据此决定 HTTP 状态码。
	return *ev, cause
}

// StartCleanup 启动 pending_confirm 超时清扫 goroutine (24h 未确认 → expired)。
// 幂等: 重复调用安全 (每轮全量 UPDATE 超窗行)。ctx 取消即退出。
func (p *Planner) StartCleanup(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	p.sweepExpiredPending()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.sweepExpiredPending()
		}
	}
}

// sweepExpiredPending 单轮清扫: result='pending_confirm' AND triggered_at < now-24h。
// 走 (result) 与 (triggered_at) 索引, 全表量小无性能压力。
func (p *Planner) sweepExpiredPending() {
	cutoff := p.nowFn().Add(-pendingConfirmTTL)
	res := p.db.Model(&models.AutomationEvent{}).
		Where("result = ? AND triggered_at < ?", models.AutomationResultPendingConfirm, cutoff).
		Updates(map[string]interface{}{
			"result": models.AutomationResultExpired,
			"detail": "pending_confirm 超时未确认 (24h)",
		})
	if res.Error != nil {
		logger.Warn("automation: pending_confirm sweep failed", "error", res.Error)
		return
	}
	if res.RowsAffected > 0 {
		logger.Info("automation: expired pending_confirm events", "count", res.RowsAffected)
	}
}

// TrimSpace 显式引用 (confirm 链路对 reason 做 TrimSpace 校验对称, 防未使用告警)。
var _ = strings.TrimSpace
