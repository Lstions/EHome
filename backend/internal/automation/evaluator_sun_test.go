package automation

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// ─────────────────────────────────────────────────────────────────────────
// B3 日出日落求值门禁 (方案 §8 A-1/A-2/A-3/A-14/A-17/A-18)
//
// 全部通过 evalTimeWindows(now) 的**注入时钟**驱动, 不依赖真实等待。
// ─────────────────────────────────────────────────────────────────────────

type sunRecorder struct{ events []TriggerEvent }

func (r *sunRecorder) HandleTrigger(ev TriggerEvent) { r.events = append(r.events, ev) }
func (r *sunRecorder) count() int                    { return len(r.events) }

type sunFixture struct {
	db        *gorm.DB
	node      models.Node
	edge      models.EdgeDevice
	rule      models.AutomationRule
	tz        *time.Location
	eventDate string
	eventAt   time.Time
}

// buildSunFixture 造夹具: 节点带权威坐标与时区, 一条 sun_event 规则挂在其边缘设备上。
func buildSunFixture(t *testing.T, triggerKind string, offsetMin int) *sunFixture {
	t.Helper()
	db := testutil.OpenTestDB(t)
	tzName := "Asia/Shanghai"
	lat, lon := 39.9042, 116.4074
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		t.Fatal(err)
	}
	node := models.Node{NodeID: "node-sun-1", Name: "sun-node", Status: "online",
		Latitude: &lat, Longitude: &lon, Timezone: &tzName}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "sun-edge", NodeID: node.NodeID, ChannelID: ch.ID, Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	date := time.Date(2026, 6, 21, 12, 0, 0, 0, tz)
	eventAt, status, err := sun.SunEvent(lat, lon, tz, date, sun.Kind(triggerKind))
	if err != nil || status != sun.StatusOK {
		t.Fatalf("夹具前提失败: %s %v", status, err)
	}
	rule := models.AutomationRule{
		Name: "sun rule", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSunKind:      triggerKind,
		TriggerSunOffsetMin: offsetMin,
		ActionType:          models.AutomationActionNotification,
		ActionLevel:         "info",
		CooldownSec:         0,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	return &sunFixture{db: db, node: node, edge: edge, rule: rule, tz: tz,
		eventDate: date.Format("2006-01-02"), eventAt: eventAt}
}

// ── A-1: 日出事件只在**到达后**触发 (前后各 1 秒) ─────────────────────────
//
// 反向验证: 把边沿判定改成 now >= fireAt 的恒真版本 → 前置 1 秒会触发 → 变红。
func TestA1SunEventFiresOnlyAfterReached(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunrise, 0)
	rec := &sunRecorder{}
	ev := NewEvaluator(fx.db, rec)
	ev.LoadRules()

	// 先跑一个 tick 让"首 tick 保守"过去 (方案 §6.2), 否则本用例会被重启语义吃掉。
	ev.evalTimeWindows(fx.eventAt.Add(-30 * time.Minute))
	if got := rec.count(); got != 0 {
		t.Fatalf("首 tick 不应触发 (保守语义), got %d", got)
	}

	// 前 1 秒: 不得触发。
	ev.evalTimeWindows(fx.eventAt.Add(-1 * time.Second))
	if got := rec.count(); got != 0 {
		t.Fatalf("事件前 1 秒不得触发 (A-1), got %d 次触发 —— 判定被写成了恒真", got)
	}

	// 后 1 秒: 必须触发**一次**。
	ev.evalTimeWindows(fx.eventAt.Add(1 * time.Second))
	if got := rec.count(); got != 1 {
		t.Fatalf("事件后 1 秒必须触发恰好 1 次 (A-1), got %d", got)
	}
	// 同日本地日再 tick 多次: 不得重复触发 (点事件每日本地日只触发一次)。
	for i := 0; i < 5; i++ {
		ev.evalTimeWindows(fx.eventAt.Add(time.Duration(i+2) * time.Minute))
	}
	if got := rec.count(); got != 1 {
		t.Fatalf("同一本地日重复 tick 触发了 %d 次, 必须恰好 1 次 (方案 §6.2 状态机)", got)
	}
	// 事件确实取自该日该坐标的日出时刻 (而非任意时刻)。
	got := rec.events[0].At
	if got.Sub(fx.eventAt) != time.Second {
		t.Fatalf("触发时刻 %v 与事件 %v 之差应为 1s", got, fx.eventAt)
	}
}

// ── A-2: 偏移分钟生效, **含负偏移** ──────────────────────────────────────
//
// 反向验证: 把符号取反 (fireAt = eventAt - offset) → 断言失败。
func TestA2SunEventOffsetIncludingNegative(t *testing.T) {
	cases := []struct {
		name   string
		offset int
	}{
		{"正偏移 +30", 30},
		{"负偏移 -30", -30},
		{"零偏移", 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fx := buildSunFixture(t, models.AutomationSunKindSunrise, c.offset)
			rec := &sunRecorder{}
			ev := NewEvaluator(fx.db, rec)
			ev.LoadRules()
			ev.evalTimeWindows(fx.eventAt.Add(-90 * time.Minute)) // 过掉首 tick

			want := fx.eventAt.Add(time.Duration(c.offset) * time.Minute)
			// 到点前 1 秒不触发。
			ev.evalTimeWindows(want.Add(-1 * time.Second))
			if rec.count() != 0 {
				t.Fatalf("偏移 %d: 到点前 1 秒触发了 (got %d) —— 偏移未生效或符号反了", c.offset, rec.count())
			}
			// 到点后 1 秒触发。
			ev.evalTimeWindows(want.Add(1 * time.Second))
			if rec.count() != 1 {
				t.Fatalf("偏移 %d: 到点后 1 秒未触发 (got %d), 期望 fireAt=%s",
					c.offset, rec.count(), want.Format(time.RFC3339))
			}
		})
	}
}

// ── A-3: 重启后首 tick **不补触发**已过事件 ──────────────────────────────
//
// 反向验证: 把首 tick 改成"now >= eventTime 即触发" → 变红。
// 这是方案 §6.2 明确要求被测试钉死的语义。
func TestA3RestartFirstTickDoesNotBackfire(t *testing.T) {
	fx := buildSunFixture(t, models.AutomationSunKindSunrise, 0)
	rec := &sunRecorder{}
	ev := NewEvaluator(fx.db, rec) // 模拟重启: 新建 Evaluator, sunStates 为空
	ev.LoadRules()

	// 事件已过 2 小时 (重启前就发生过): 首 tick 绝不允许补触发。
	ev.evalTimeWindows(fx.eventAt.Add(2 * time.Hour))
	if got := rec.count(); got != 0 {
		t.Fatalf("重启后首 tick 补触发了已过事件 %d 次 (A-3/方案 §6.2 必须保守不触发)", got)
	}
	// 且该日本地日后续 tick 也不得触发 (首 tick 已登记当日状态)。
	for i := 0; i < 3; i++ {
		ev.evalTimeWindows(fx.eventAt.Add(2*time.Hour + time.Duration(i+1)*time.Minute))
	}
	if got := rec.count(); got != 0 {
		t.Fatalf("重启当日后续 tick 触发了 %d 次, 仍须为 0 (保守窗口不只是一个 tick)", got)
	}

	// 但**次日**的日出必须能正常触发 —— 保守不等于永久失效。
	// (次日事件由同一规则在下一个本地日重新求值。)
	nextDay := fx.eventAt.Add(24 * time.Hour)
	nextAt, status, err := sun.SunEvent(*fx.node.Latitude, *fx.node.Longitude, fx.tz, nextDay, sun.Sunrise)
	if err != nil || status != sun.StatusOK {
		t.Fatalf("次日事件求解失败: %s %v", status, err)
	}
	ev.evalTimeWindows(nextAt.Add(1 * time.Second))
	if got := rec.count(); got != 1 {
		t.Fatalf("次日本地日的日出必须正常触发 1 次, got %d —— 保守语义不得变成永久失效", got)
	}
}

// ── A-14: 极昼/极夜不触发 + 显式审计 (且禁止 clamp) ──────────────────────
//
// 夹具用朗伊尔城 (78.22°N): 2026-06-21 极昼、2026-12-21 极夜。
// 反向验证: 把余弦项 clamp 到 [-1,1] → 会算出假时刻 → 触发 → 变红。
func TestA14PolarDayNightSuppressedWithAudit(t *testing.T) {
	cases := []struct {
		name     string
		date     time.Time
		wantKind string
	}{
		{"极昼 2026-06-21", time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC), models.AutomationSunKindSunrise},
		{"极夜 2026-12-21", time.Date(2026, 12, 21, 12, 0, 0, 0, time.UTC), models.AutomationSunKindSunset},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			db := testutil.OpenTestDB(t)
			tzName := "Arctic/Longyearbyen"
			lat, lon := 78.22, 15.65
			tz, err := time.LoadLocation(tzName)
			if err != nil {
				t.Fatal(err)
			}
			node := models.Node{NodeID: "node-polar", Name: "polar", Status: "online",
				Latitude: &lat, Longitude: &lon, Timezone: &tzName}
			if err := db.Create(&node).Error; err != nil {
				t.Fatal(err)
			}
			ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
			if err := db.Create(&ch).Error; err != nil {
				t.Fatal(err)
			}
			edge := models.EdgeDevice{Name: "polar-edge", NodeID: node.NodeID, ChannelID: ch.ID, Enabled: true, Status: "active"}
			if err := db.Create(&edge).Error; err != nil {
				t.Fatal(err)
			}
			rule := models.AutomationRule{
				Name: "polar rule", Enabled: true,
				TriggerType:         models.AutomationTriggerSunEvent,
				TriggerEdgeDeviceID: edge.ID,
				TriggerSunKind:      c.wantKind,
				ActionType:          models.AutomationActionNotification,
				ActionLevel:         "info",
			}
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			rec := &sunRecorder{}
			ev := NewEvaluator(db, rec)
			ev.LoadRules()

			// 该日本地正午前后各 tick 一次 (含跨过首 tick)。
			noon := time.Date(c.date.Year(), c.date.Month(), c.date.Day(), 12, 0, 0, 0, tz)
			ev.evalTimeWindows(noon.Add(-time.Hour))
			ev.evalTimeWindows(noon)
			ev.evalTimeWindows(noon.Add(time.Hour))
			for i := 0; i < 3; i++ {
				ev.evalTimeWindows(noon.Add(time.Duration(i+2) * time.Hour))
			}

			// 不得触发 (极昼极夜不触发, O-6)。
			if got := rec.count(); got != 0 {
				t.Fatalf("%s: 极昼/极夜不得触发任何动作, got %d 次 —— 余弦项被 clamp 了?", c.name, got)
			}
			// 必须落**显式审计** (不得静默 return), 且同一本地日只落一条。
			var evs []models.AutomationEvent
			if err := db.Where("rule_id = ? AND result = ?", rule.ID,
				models.AutomationResultSuppressedNoSolarEvent).Find(&evs).Error; err != nil {
				t.Fatal(err)
			}
			if len(evs) != 1 {
				t.Fatalf("%s: 应落恰好 1 条 suppressed_no_solar_event 审计 (每日一条, 防刷量), got %d —— "+
					"静默 return 正是 C5 要消除的形态", c.name, len(evs))
			}
			if evs[0].Detail == "" {
				t.Fatalf("%s: 审计 Detail 必须说明原因 (前端据此显示\"极昼/极夜, 本日无日出事件\")", c.name)
			}
		})
	}
}

// ── A-17: windowStates 按**内容白名单**清理 ─────────────────────────────
//
// 反向验证: 把清理改回"仅按 id 存在性" → 本用例变红。
// 且必须验证**非白名单字段**变化**不**清状态 (否则每 30 秒兜底 LoadRules 会
// 让边沿反复触发, 比原缺陷更危险)。
func TestA17WindowStatesClearedOnContentChange(t *testing.T) {
	db := testutil.OpenTestDB(t)
	rule := models.AutomationRule{
		Name: "win", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "09:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
		ActionType:        models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	tz, _ := time.LoadLocation("Asia/Shanghai")
	inWindow := time.Date(2026, 6, 21, 8, 30, 0, 0, tz)

	// 第一次 tick: enter 规则在窗口内 → 触发, 并写入 windowStates。
	ev.evalTimeWindows(inWindow)
	if rec.count() != 1 {
		t.Fatalf("窗口内首次 tick 应触发 1 次, got %d", rec.count())
	}
	ev.mu.RLock()
	_, hasState := ev.windowStates[rule.ID]
	ev.mu.RUnlock()
	if !hasState {
		t.Fatal("求值后应写入 windowStates")
	}

	// 改窗口内容 (仍在缓存中, 未移除): 状态必须被清。
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("trigger_window_start", "07:00").Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.mu.RLock()
	_, stillHas := ev.windowStates[rule.ID]
	ev.mu.RUnlock()
	if stillHas {
		t.Fatal("窗口字段变化后 windowStates 必须被清除 (A-17) —— 清理仍只按 id 存在性")
	}

	// ── 反向的另一半: 非白名单字段变化**不得**清状态 ──
	// (Start() 每 30s 兜底 LoadRules; 若按任一差异清理, 状态会每 30 秒被清空,
	//  边沿反复触发 —— 比原缺陷更危险。)
	ev2 := NewEvaluator(db, rec)
	ev2.LoadRules()
	ev2.evalTimeWindows(inWindow)
	ev2.mu.RLock()
	_, persisted := ev2.windowStates[rule.ID]
	ev2.mu.RUnlock()
	if !persisted {
		t.Fatal("前置: 求值后应有状态")
	}
	// 只改名字 (非白名单字段)。
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", rule.ID).
		Update("name", "renamed").Error; err != nil {
		t.Fatal(err)
	}
	ev2.LoadRules()
	ev2.mu.RLock()
	_, survived := ev2.windowStates[rule.ID]
	ev2.mu.RUnlock()
	if !survived {
		t.Fatal("仅 name 变化不得清除 windowStates —— 白名单过宽会让边沿每 30 秒反复触发")
	}
}

// ── A-18: 坐标变更使缓存失效 (调用计数断言) ─────────────────────────────
//
// 反向验证: 缓存键去掉坐标 → 计数不增 → 变红。
func TestA18CoordinateChangeInvalidatesCache(t *testing.T) {
	db := testutil.OpenTestDB(t)
	tzName := "Asia/Shanghai"
	lat, lon := 39.9042, 116.4074
	tz, _ := time.LoadLocation(tzName)
	node := models.Node{NodeID: "node-cache", Name: "n", Status: "online",
		Latitude: &lat, Longitude: &lon, Timezone: &tzName}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "e", NodeID: node.NodeID, ChannelID: ch.ID, Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	rule := models.AutomationRule{
		Name: "cache rule", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSunKind:      models.AutomationSunKindSunrise,
		ActionType:          models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()
	at := time.Date(2026, 6, 21, 5, 0, 0, 0, tz)

	// 第一次求值: 必然计算 (含次日预热)。
	ev.evalTimeWindows(at)
	calls1 := ev.sunCache.ComputeCalls()
	if calls1 == 0 {
		t.Fatal("首次求值必须调用计算函数")
	}
	// 同坐标再 tick (移到同日本地日的另一时刻): 必须全部命中缓存。
	ev.evalTimeWindows(at.Add(1 * time.Minute))
	if got := ev.sunCache.ComputeCalls(); got != calls1 {
		t.Fatalf("同坐标同日应命中缓存, 计算次数 %d → %d", calls1, got)
	}

	// 改坐标: 必须重算。
	newLat := 31.23
	if err := db.Model(&models.Node{}).Where("id = ?", node.ID).
		Update("latitude", newLat).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.evalTimeWindows(at.Add(2 * time.Minute))
	if got := ev.sunCache.ComputeCalls(); got <= calls1 {
		t.Fatalf("节点坐标变更后必须重算 (A-18), 计算次数仍为 %d", got)
	}
}

// ── 归日口径: 必须按**节点时区**归日, 不是后端进程时区 ────────────────────
func TestSunEventUsesNodeTimezoneForDayBoundary(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// 乌鲁木齐几何位置配 Asia/Shanghai: 当地钟面日落约 21:56, 与后端 UTC 差 8 小时。
	tzName := "Asia/Shanghai"
	lat, lon := 43.83, 87.62
	tz, _ := time.LoadLocation(tzName)
	node := models.Node{NodeID: "node-tz", Name: "n", Status: "online",
		Latitude: &lat, Longitude: &lon, Timezone: &tzName}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "e", NodeID: node.NodeID, ChannelID: ch.ID, Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	rule := models.AutomationRule{
		Name: "tz rule", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSunKind:      models.AutomationSunKindSunset,
		ActionType:          models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()

	dayStart := time.Date(2026, 6, 21, 0, 0, 0, 0, tz)
	eventAt, status, err := sun.SunEvent(lat, lon, tz, dayStart, sun.Sunset)
	if err != nil || status != sun.StatusOK {
		t.Fatalf("前提失败: %s %v", status, err)
	}
	ev.evalTimeWindows(dayStart) // 首 tick
	ev.evalTimeWindows(eventAt.Add(1 * time.Second))
	if rec.count() != 1 {
		t.Fatalf("按节点时区求值的日落应触发 1 次, got %d", rec.count())
	}
	// 触发时刻必须落在节点本地日的当日 (归日口径 = 节点时区)。
	if ld := rec.events[0].At.In(tz).Format("2006-01-02"); ld != "2026-06-21" {
		t.Fatalf("触发归日错误: %s (必须按节点时区归日)", ld)
	}
}

// ── 运行期缺坐标: 不触发 + 审计 (fail-closed, 绝不回落 UTC) ──────────────
func TestSunEventMissingLocationFailsClosedWithAudit(t *testing.T) {
	db := testutil.OpenTestDB(t)
	node := models.Node{NodeID: "node-nocoord", Name: "n", Status: "online"} // 无坐标无时区
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "e", NodeID: node.NodeID, ChannelID: ch.ID, Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	rule := models.AutomationRule{
		Name: "nocoord", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSunKind:      models.AutomationSunKindSunrise,
		ActionType:          models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	rec := &sunRecorder{}
	ev := NewEvaluator(db, rec)
	ev.LoadRules()
	at := time.Date(2026, 6, 21, 12, 0, 0, 0, time.UTC)
	ev.evalTimeWindows(at)
	ev.evalTimeWindows(at.Add(time.Minute))
	if rec.count() != 0 {
		t.Fatalf("无坐标/时区时不得触发 (fail-closed), got %d", rec.count())
	}
	var cnt int64
	db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultSuppressedNoSolarEvent).
		Count(&cnt)
	if cnt == 0 {
		t.Fatal("无坐标/时区时必须落显式审计 (不得静默失败)")
	}
}
