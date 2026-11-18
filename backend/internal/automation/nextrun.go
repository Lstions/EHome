package automation

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
	"ehome/backend/pkg/logger"
)

// =====================================================================
// 「下次执行时间」计算 (2026-11-17)
// =====================================================================
//
// 只读计算: 不改内存态、不写库、不触发动作。由 API 读路径调用
// (handler_automation.go 的 attachNextRuns)。
//
// 为什么必须挂在 Evaluator 上, 而不是写成一个只看数据库的纯函数:
// 判定"下一次到底会不会触发"的唯一事实源是求值器的三份内存态
// (sunStates / windowStates / triggered)。只用 automation_rules 的持久列
// (last_triggered_at) 反推, 在下面三种场景下必然说错:
//
//  1. **规则刚被编辑** —— sameWindowStateFields 不等 ⇒ sunStates 已被清除
//     (evaluator.go 的 LoadRules)。2026-11-17 起 sun_event 改边沿判定、**不补发**
//     (evaluator.go 的 lastClockTick): 清态后当天不会再触发, 所以这里必须能看见
//     "本日未触发"这个内存事实, 才能把下一次正确报到次日;
//  2. **进程刚重启** —— sunStates 为空 + 首 tick 走补发窗口 (≤10min 可能真的
//     触发, 之后不再触发), 反推得"今天还会触发"在窗口外必然说错;
//  3. **手动触发 / 确认执行** —— 2026-11-18 起这两条执行路径也会
//     MarkTriggered 回灌 evaluator.triggered（见 planner.markCooldown），
//     因此内存态是**唯一判据**；数据库里的 last_triggered_at 只在重启时
//     作为种子（evaluator.rebuildCooldowns）。旧实现取 max(内存, 库) 会让
//     手动触发后界面报"冷却中"而求值器仍 armed —— 说不会、实际会。
//
// 因此本文件与 evaluator.go 必须同包、同状态、同一组夹具测试。

// 「即将触发」的判定窗口 = 求值周期本身: "条件已满足但本 tick 还没处理"的
// 最大等待就是一个 tick。落在这个窗口内时, 报一个未来时刻比报"明天同一时刻"
// 更贴近事实 —— 后者会让用户以为今晚不会执行。
//
// 2026-11-18: 不再自留 `const WindowTickInterval() = time.Minute` 副本, 一律调
// evaluator.go 的 WindowTickInterval()。仿真构建会把它改成 2s, 留副本会让
// 预测在仿真里按 1 分钟算 —— 预测与执行必须同源, 常量也一样。

// ruleNextRunState 是计算所需的规则级运行态快照 (锁内复制, 锁外使用)。
type ruleNextRunState struct {
	// sunFiredDate 是 sun_event 已触发的**节点本地日** ("" = 本日未触发)。
	sunFiredDate string
	// windowInside / windowKnown 是 time_window 上次 tick 是否在窗口内,
	// 以及该状态是否已经建立 (进程重启后为 false)。
	windowInside bool
	windowKnown  bool
	// cooldownAnchor 是冷却窗起点: 内存态与持久锚点取**较晚**者。
	cooldownAnchor time.Time
	// clockTick 是求值器上一次时钟 tick 的时刻 (零值 = 进程还没 tick 过)。
	// 用于判断"事件是否落在 (上次 tick, now] 内" —— 那才是会被真正触发的那一轮。
	clockTick time.Time
	// configChanged 表示该规则的判定字段刚变更过 (或刚进入缓存): 事件已过时
	// 求值器会跳过本日, 预测必须给次日而不是"马上"。
	configChanged bool
}

// nextRunCandidate 是单类型算出的"计划时刻" (尚未与阻塞顺延合并)。
type nextRunCandidate struct {
	status string
	at     time.Time // 零值 = 该类型没有可预测的时刻
	basis  string
	detail string
	tz     string
}

// NextRuns 计算一批规则的「下次执行时间」, key = 规则 ID。
//
// rules 可以包含停用规则与不属于求值器缓存的老规则 —— 本函数按传入的数据算,
// 只把内存态当作**补充证据**读取 (按 ID 命中才有值)。
//
// now 由调用方注入 (生产 = time.Now()), 与求值器 tick 的时间源同域。
func (e *Evaluator) NextRuns(rules []models.AutomationRule, now time.Time) map[uint]models.NextRun {
	out := make(map[uint]models.NextRun, len(rules))
	if len(rules) == 0 {
		return out
	}

	// ① 日熔断计数: **一条**分组查询, 不做 N+1。
	//
	// 查询失败时**不得**当成"0 次"（旧实现 fail-open）：那会让界面承诺一个
	// 到点却被 suppressed_daily_limit 抑制的时刻。错误往下传，只把
	// MaxDailyExec>0 的规则降级为「算不出」。
	dailyCounts, dailyErr := e.dailyExecutedCounts(rules, now)

	// ② 内存态一次性快照。锁内只做复制, 计算全部在锁外 —— 与 Evaluate 的 H2
	//    教训同旨: 持锁做慢操作会阻塞所有求值。
	e.mu.RLock()
	sunFired := make(map[uint]string, len(e.sunStates))
	for k, v := range e.sunStates {
		sunFired[k] = v
	}
	windowInside := make(map[uint]bool, len(e.windowStates))
	for k, v := range e.windowStates {
		windowInside[k] = v
	}
	triggered := make(map[uint]time.Time, len(e.triggered))
	for k, v := range e.triggered {
		triggered[k] = v
	}
	clockTick := e.lastClockTick
	configChanged := make(map[uint]bool, len(e.configChanged))
	for k, v := range e.configChanged {
		configChanged[k] = v
	}
	e.mu.RUnlock()

	// ③ 逐规则计算。
	for i := range rules {
		rule := rules[i]
		wasInside, windowKnown := windowInside[rule.ID]
		// 冷却锚点只认内存态。自动/手动/确认三条执行路径都会 MarkTriggered
		// 回灌（2026-11-18 D4），库里的 last_triggered_at 只是重启种子。
		// 若这里再取 max(内存, 库)，手动触发后界面会报"冷却中"而求值器仍 armed。
		anchor := triggered[rule.ID]
		if dailyErr != nil && rule.MaxDailyExec > 0 {
			// 宁可说"算不出"，也不要在界面上编一个会落空的时间。
			out[rule.ID] = models.NextRun{
				Status:     models.NextRunUnavailable,
				Detail:     "日执行计数查询失败，无法判断是否已达日上限（不承诺时刻）",
				ComputedAt: now,
			}
			continue
		}
		st := ruleNextRunState{
			sunFiredDate:   sunFired[rule.ID],
			windowInside:   wasInside,
			windowKnown:    windowKnown,
			cooldownAnchor: anchor,
			clockTick:      clockTick,
			configChanged:  configChanged[rule.ID],
		}
		out[rule.ID] = e.nextRunForRule(rule, now, st, dailyCounts[rule.ID])
	}
	return out
}

// nextRunForRule 单条规则的「下次执行时间」。
func (e *Evaluator) nextRunForRule(rule models.AutomationRule, now time.Time, st ruleNextRunState, dailyExecuted int64) models.NextRun {
	res := models.NextRun{ComputedAt: now}

	// ── 前置: 停用 / 未实现的类型 (这两种情况下算出来的任何时刻都是误导) ──
	if !rule.Enabled {
		res.Status = models.NextRunDisabled
		res.Detail = "规则已停用"
		return res
	}
	if rule.TriggerType == models.AutomationTriggerEvent {
		res.Status = models.NextRunUnsupported
		res.Detail = "事件驱动触发器尚未实现"
		return res
	}

	// ── 阻塞顺延 ──
	// 冷却与日熔断对**所有**类型都生效 (planner.HandleTrigger 的执行约束顺序:
	// 日熔断 → 确认分流 → 动作)。统一在这里算, 再与候选时刻取较晚者。
	var blockedUntil time.Time
	var blockedBasis, blockedDetail string
	if cooldown := cooldownFor(rule); cooldown > 0 && !st.cooldownAnchor.IsZero() {
		if until := st.cooldownAnchor.Add(cooldown); until.After(now) {
			blockedUntil = until
			blockedBasis = models.NextRunBasisCooldown
			blockedDetail = fmt.Sprintf("冷却中，%s 后解除", humanSeconds(until.Sub(now)))
		}
	}
	if rule.MaxDailyExec > 0 && dailyExecuted >= int64(rule.MaxDailyExec) {
		// 切日口径 = **进程时区**的 00:00, 与 planner 的 dayStart 一致
		// (planner.go 用 at.Location())。不得改用规则时区, 否则显示的时刻与现实不符。
		reset := nextLocalMidnight(now)
		if reset.After(blockedUntil) {
			blockedUntil = reset
			blockedBasis = models.NextRunBasisDailyLimit
			blockedDetail = fmt.Sprintf("今日已达上限 %d 次，次日 00:00 恢复", rule.MaxDailyExec)
		}
	}

	// ── 类型分派 ──
	var cand nextRunCandidate
	switch rule.TriggerType {
	case models.AutomationTriggerSunEvent:
		cand = e.nextSunEvent(rule, now, st)
	case models.AutomationTriggerTimeWindow:
		cand = e.nextTimeWindow(rule, now, st)
	case models.AutomationTriggerSensorThreshold:
		// 数据驱动: 由设备上报决定, **不存在**可预测的时刻。
		// 这里绝不编造一个时间 (那是伪科学), 只在冷却期给出"最早可再次触发"。
		cand = nextRunCandidate{
			status: models.NextRunWaitingData,
			detail: "等待设备上报数据满足条件",
			tz:     localZoneName(),
		}
	default:
		cand = nextRunCandidate{
			status: models.NextRunUnsupported,
			detail: "未知的触发器类型: " + rule.TriggerType,
		}
	}

	res.Status = cand.status
	res.Basis = cand.basis
	res.Detail = cand.detail
	res.Timezone = cand.tz
	if cand.at.IsZero() {
		// 没有可预测的**计划**时刻 (数据驱动 / 极昼极夜 / 全天窗口)。
		//
		// 但 sensor_threshold 在冷却期仍要给一个"最早何时能再触发" —— 那是
		// 唯一一个能诚实说出的时间点 (冷却到期后若条件仍满足即触发)。
		// 其余状态 (极昼/极夜/全天窗口) 给时间就是误导, 一律留空。
		if cand.status == models.NextRunWaitingData && blockedUntil.After(now) {
			blocked := blockedUntil
			res.At = &blocked
			res.Basis = blockedBasis
			res.Detail = cand.detail + "；" + blockedDetail
		}
		return res
	}

	at := cand.at
	if blockedUntil.After(at) {
		at = blockedUntil
		res.Basis = blockedBasis
		res.Detail = blockedDetail
	}
	res.At = &at
	return res
}

// nextSunEvent 计算日出/日落规则的下一个 fireAt（= 事件时刻 + 偏移）。
//
// 与 evaluator.evalSunEventRule 的状态机严格对齐（2026-11-18 候选集）：
//   - 候选 = now 所在本地日的前一日/当日/次日，跨午夜偏移不会算错日期；
//   - 每场事件只触发一次（sunStates 记的是**事件日期**，不是 tick 的本地日）；
//   - 首 tick（clockTick 为空）不报"即将触发"：真实是否会触发取决于补发窗口与
//     库侧是否已有记录，读路径不额外查库，保守地报下一个未来时刻；
//   - 极昼/极夜显式返回 no_event，**绝不**用零值时间冒充结果。
func (e *Evaluator) nextSunEvent(rule models.AutomationRule, now time.Time, st ruleNextRunState) nextRunCandidate {
	resolved, err := models.ResolveRuleSunLocation(rule)
	if err != nil {
		return nextRunCandidate{status: models.NextRunUnavailable, detail: "位置未配置: " + err.Error()}
	}
	kind := sun.Kind(rule.TriggerSunKind)
	if !kind.Valid() {
		return nextRunCandidate{status: models.NextRunUnavailable, detail: "日出/日落类型非法: " + rule.TriggerSunKind}
	}
	loc := resolved.Location
	out := nextRunCandidate{tz: resolved.Timezone}
	offset := time.Duration(rule.TriggerSunOffsetMin) * time.Minute

	candidates, err := e.sunCandidatesAround(resolved, now, kind, offset)
	if err != nil {
		out.status = models.NextRunUnavailable
		out.detail = "日出日落计算失败: " + err.Error()
		return out
	}
	if len(candidates) == 0 {
		out.status = models.NextRunNoEvent
		if _, status, lookupErr := e.sunCache.Lookup(resolved.Latitude, resolved.Longitude, loc, now, kind); lookupErr == nil {
			out.detail = solarStatusText(status)
		} else {
			out.detail = "本日无日出/日落事件"
		}
		return out
	}

	// ① 本次 tick 区间内即将触发（与求值器同一判据 prevTick < fireAt <= now）：
	// 该事件尚未被判为已处理，求值器下一轮就会真的提交。
	if !st.configChanged && !st.clockTick.IsZero() {
		for i := range candidates {
			c := &candidates[i]
			if c.fireAt.After(st.clockTick) && !c.fireAt.After(now) && st.sunFiredDate != c.eventDate {
				out.status = models.NextRunScheduled
				out.basis = models.NextRunBasisSun
				out.at = now.Add(WindowTickInterval())
				out.detail = fmt.Sprintf("即将触发（%s 落在本次求值区间内: %s）",
					sunKindText(kind), c.eventAt.In(loc).Format("15:04"))
				return out
			}
		}
	}

	// ② 下一个未来时刻（跳过已处理的事件日）。
	//
	// 只报"今天/明天"这类绝对时刻还不够：跨午夜偏移时事件时刻与触发时刻不在
	// 同一本地日，detail 必须把触发日写出来，否则用户会以为规则配错了。
	for i := range candidates {
		c := &candidates[i]
		if !c.fireAt.After(now) || st.sunFiredDate == c.eventDate {
			continue
		}
		out.status = models.NextRunScheduled
		out.basis = models.NextRunBasisSun
		out.at = c.fireAt
		out.detail = sunNextDetail(kind, *c, loc)
		return out
	}

	// ③ 理论不可达：D+1 的事件必然晚于 now。兜底不承诺时刻（不编造）。
	out.status = models.NextRunNoEvent
	out.detail = "未来 48 小时内无日出/日落事件"
	return out
}

// sunNextDetail 生成"下一次"的可读说明。
//
// 跨午夜偏移时事件时刻与触发时刻不在同一本地日（日落 19:46 + 300min ⇒ 次日
// 00:46 触发）；只写事件时刻会让用户以为规则配错了，必须把触发日也写出来。
func sunNextDetail(kind sun.Kind, c sunEventCandidate, loc *time.Location) string {
	eventAt := c.eventAt.In(loc)
	fireAt := c.fireAt.In(loc)
	if eventAt.Format("2006-01-02") != fireAt.Format("2006-01-02") {
		return fmt.Sprintf("%s %s（%s 触发）", sunKindText(kind),
			eventAt.Format("15:04"), fireAt.Format("01-02 15:04"))
	}
	return fmt.Sprintf("%s %s", sunKindText(kind), eventAt.Format("15:04"))
}

// nextTimeWindow 计算时间窗口规则的下一个边界时刻。
//
// 时区口径（2026-11-18 D7 根治）：窗口按**规则自带时区**解释，与执行侧
// evaluator.evalWindowRule 同一份换算。旧实现用进程时区，而生产容器是 UTC ——
// 东八区用户配 08:00–18:00 实际 16:00–02:00 生效，且界面不提示。
// 空值/非法值 fail-closed（不回落进程时区）：算不出来就说算不出来。
func (e *Evaluator) nextTimeWindow(rule models.AutomationRule, now time.Time, st ruleNextRunState) nextRunCandidate {
	loc, tzName, err := ruleTimezone(rule)
	if err != nil {
		return nextRunCandidate{status: models.NextRunUnavailable, detail: err.Error()}
	}
	locNow := now.In(loc)
	out := nextRunCandidate{tz: tzName}

	startT, err := parseHHMM(rule.TriggerWindowStart)
	if err != nil {
		out.status = models.NextRunUnavailable
		out.detail = "窗口开始时间格式非法: " + rule.TriggerWindowStart
		return out
	}
	endT, err := parseHHMM(rule.TriggerWindowEnd)
	if err != nil {
		out.status = models.NextRunUnavailable
		out.detail = "窗口结束时间格式非法: " + rule.TriggerWindowEnd
		return out
	}

	// start == end ⇒ 全天窗口 (与 isInWindow 同语义)。
	if startT.Equal(endT) {
		if rule.TriggerWindowEdge != models.AutomationWindowInside {
			out.status = models.NextRunNoEvent
			out.detail = "全天窗口（start==end），不会再次" +
				map[string]string{models.AutomationWindowEnter: "进入", models.AutomationWindowExit: "离开"}[rule.TriggerWindowEdge]
			return out
		}
		out.status = models.NextRunScheduled
		out.basis = models.NextRunBasisWindow
		out.at = now.Add(WindowTickInterval())
		out.detail = "全天窗口"
		return out
	}

	inside, err := isInWindow(rule.TriggerWindowStart, rule.TriggerWindowEnd, locNow)
	if err != nil {
		out.status = models.NextRunUnavailable
		out.detail = err.Error()
		return out
	}

	out.status = models.NextRunScheduled
	out.basis = models.NextRunBasisWindow

	switch rule.TriggerWindowEdge {
	case models.AutomationWindowEnter:
		// enter 的求值器判据（evaluator.go: inside && exists && !wasInside）：
		// "不知道上次在哪"（重启/新建）不是触发条件。旧实现在这里把 wKnown=false
		// 当成"刚刚进入"，于是页面报"即将触发"而求值器根本不会执行 ——
		// 正是 2026-11-18 与 sun_event 同一裁决（不补发）后留下的反向漂移。
		if inside && st.windowKnown && !st.windowInside {
			out.at = now.Add(WindowTickInterval())
			out.detail = "即将触发（首次进入窗口）"
			return out
		}
		out.at = nextClockBoundary(locNow, startT.Hour(), startT.Minute())
		out.detail = "窗口开始 " + rule.TriggerWindowStart
	case models.AutomationWindowExit:
		// 求值器判据: !inside && exists && wasInside。没有窗口历史时（刚重启/
		// 刚创建）这一次离开不会被触发，报一个精确时刻就是骗人。
		if !st.windowKnown {
			out.status = models.NextRunUnavailable
			out.detail = "窗口状态未建立（进程刚重启或规则刚创建），本次边界不会触发；下个完整窗口起生效"
			return out
		}
		out.at = nextClockBoundary(locNow, endT.Hour(), endT.Minute())
		out.detail = "窗口结束 " + rule.TriggerWindowEnd
	case models.AutomationWindowInside:
		if inside {
			// 窗内每 tick 都参与求值 (受冷却抑制), 下一次就是下一个 tick。
			out.at = now.Add(WindowTickInterval())
			out.detail = "窗口内持续求值（受冷却抑制）"
			return out
		}
		out.at = nextClockBoundary(locNow, startT.Hour(), startT.Minute())
		out.detail = "窗口开始 " + rule.TriggerWindowStart
	default:
		out.status = models.NextRunUnavailable
		out.detail = "触发沿非法: " + rule.TriggerWindowEdge
	}
	return out
}

// dailyExecutedCounts 返回各规则"当日已执行"次数 (仅 max_daily_exec > 0 的规则)。
//
// 一条分组查询覆盖整页规则 —— 逐规则 Count 会在列表接口里制造 N+1。
// 失败时返回空表 (退化为"不显示日熔断顺延"), 不阻塞列表本身。
func (e *Evaluator) dailyExecutedCounts(rules []models.AutomationRule, now time.Time) (map[uint]int64, error) {
	out := make(map[uint]int64)
	if e.db == nil {
		return out, nil
	}
	ids := make([]uint, 0, len(rules))
	for i := range rules {
		if rules[i].MaxDailyExec > 0 {
			ids = append(ids, rules[i].ID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}

	// 切日口径与 planner.HandleTrigger 一致: 用传入时刻的时区 (进程时区)。
	y, m, d := now.Date()
	dayStart := time.Date(y, m, d, 0, 0, 0, 0, now.Location())

	var rows []struct {
		RuleID uint
		Cnt    int64
	}
	err := e.db.Model(&models.AutomationEvent{}).
		Select("rule_id, COUNT(*) AS cnt").
		Where("rule_id IN ? AND triggered_at >= ? AND result = ?",
			ids, dayStart, models.AutomationResultExecuted).
		Group("rule_id").
		Scan(&rows).Error
	if err != nil {
		logger.Warn("automation: next_run daily count failed", "error", err)
		return nil, err
	}
	for i := range rows {
		out[rows[i].RuleID] = rows[i].Cnt
	}
	return out, nil
}

// localZoneName 返回后端进程时区的 **IANA 名**, 取不到时返回空串。
//
// 为什么不能直接用 time.Local.String(): 未显式设置时 Go 返回的是字面量
// "Local" —— 它**不是**合法的 IANA 名。前端 Intl.DateTimeFormat({timeZone:"Local"})
// 会抛 RangeError 只能退回浏览器时区, 于是这个字段变成"看起来有值、实际不可用"。
// 这不是理论问题: 2026-11-17 真机联调时 list 接口原样返回了 "timezone":"Local"。
//
// 取名字的优先级: TZ 环境变量 → /etc/localtime 软链里的 zoneinfo 段。
// 两者都拿不到就返回空串, 前端按"用浏览器本地时区"处理 (与 time_window
// 本身依赖进程时区的语义一致, 且不会把非法值塞进 Intl)。
var (
	localZoneOnce   sync.Once
	localZoneCached string
)

// localZoneName 只在进程启动时解析一次（2026-11-18 D20）。
//
// 为什么必须记忆化：列表接口无分页，每条规则要算 1~2 次时区；而
// time.LoadLocation 对非 UTC/Local 的名字**不做全局记忆**，每次都走一遍
// zoneinfo 解析。200 条规则 = 200~400 次解析/请求，纯属浪费。
// TZ 与 /etc/localtime 在进程生命周期内不会变，缓存永不失效。
func localZoneName() string {
	localZoneOnce.Do(func() { localZoneCached = resolveLocalZoneName() })
	return localZoneCached
}

func resolveLocalZoneName() string {
	if tz := strings.TrimSpace(os.Getenv("TZ")); tz != "" {
		// TZ 可能是 "Asia/Shanghai", 也可能是 POSIX 串 (如 "CST-8"), 只采纳前者。
		if _, err := time.LoadLocation(tz); err == nil && strings.Contains(tz, "/") {
			return tz
		}
	}
	if link, err := os.Readlink("/etc/localtime"); err == nil {
		const marker = "/zoneinfo/"
		if i := strings.Index(link, marker); i >= 0 {
			name := link[i+len(marker):]
			if _, err := time.LoadLocation(name); err == nil {
				return name
			}
		}
	}
	return ""
}

// nextClockBoundary 返回**严格晚于** now 的下一个 HH:MM 钟面时刻 (进程时区)。
//
// 跨零点窗口因此不需要特殊分支: 22:00-06:00 在 03:00 时, 下一个 start 是
// 今日 22:00, 下一个 end 是今日 06:00 —— 正是自然语言里的"下一个边界"。
func nextClockBoundary(now time.Time, hour, minute int) time.Time {
	y, m, d := now.Date()
	at := time.Date(y, m, d, hour, minute, 0, 0, now.Location())
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	return at
}

// nextLocalMidnight 返回进程时区下的下一个 00:00。
func nextLocalMidnight(now time.Time) time.Time {
	y, m, d := now.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Location()).AddDate(0, 0, 1)
}

// solarStatusText 把极昼/极夜翻译成界面可读的措辞。
func solarStatusText(s sun.EventStatus) string {
	switch s {
	case sun.StatusAlwaysUp:
		return "极昼：本日无日落事件，规则本日不触发"
	case sun.StatusAlwaysDown:
		return "极夜：本日无日出事件，规则本日不触发"
	}
	return "本日无日出/日落事件"
}

// sunKindText 日出/日落的中文名。
func sunKindText(k sun.Kind) string {
	if k == sun.Sunset {
		return "日落"
	}
	return "日出"
}

// humanSeconds 把剩余时长格式化成整秒 (冷却提示用)。
func humanSeconds(d time.Duration) string {
	sec := int64(d / time.Second)
	if d%time.Second != 0 {
		sec++
	}
	if sec < 0 {
		sec = 0
	}
	return fmt.Sprintf("%d 秒", sec)
}
