package automation

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
)

// TestSunEventNoBackfill 钉死 2026-11-17 用户裁决: sun_event **不做补发**。
//
// 触发只发生在"这一轮 tick 真的跨过事件时刻"(prevTick < fireAt <= now), 且规则的
// 当前配置必须在事件之前就已生效 (configChanged=false)。规则在事件之后才进入求值器
// 缓存 (新建 / 重新启用 / 编辑白名单字段 ⇒ sunStates 被清) 时, 一律登记"本场已处理"、
// 不执行 —— 要立刻执行请用手动触发。
//
// 2026-11-18 P1 的**唯一例外**是"进程启动跨过事件"（部署/崩溃），且必须同时满足：
// 事件刚过去 ≤ sunCatchUpWindow、规则 UpdatedAt 早于 fireAt、库侧没有该事件的审计行。
// 该路径由 sun_offset_catchup_test.go 单独覆盖；本文件覆盖的"配置变更 / 事件后新建"
// 语义没有放宽。
//
// 反向验证: 把 evalSunEventRule 的 prevTick / configChanged 守卫删掉 (退回
// "now >= fireAt 且本日未触发" 的电平判定) → 本文件三条子用例全部变红。
func TestSunEventNoBackfill(t *testing.T) {
	// 与事件同一本地日 (夹具 = 北京 2026-06-21, 日落 19:46), 远离午夜。
	const gap = 2 * time.Hour

	// ① 事件之后**新建**一条规则: 不得补发。
	t.Run("new_rule_after_event", func(t *testing.T) {
		fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
		rec := &sunRecorder{}
		ev := NewEvaluator(fx.db, rec)
		ev.LoadRules()
		ev.evalTimeWindows(fx.eventAt.Add(-30 * time.Minute)) // 建立 prevTick
		ev.evalTimeWindows(fx.eventAt.Add(time.Second))        // 准点触发
		if rec.count() != 1 {
			t.Fatalf("前置: 准点应触发 1 次, got %d", rec.count())
		}

		lat, lon, tzName := fx.lat, fx.lon, "Asia/Shanghai"
		fresh := models.AutomationRule{
			Name: "事件后新建的日落规则", Enabled: true,
			TriggerType:         models.AutomationTriggerSunEvent,
			TriggerEdgeDeviceID: fx.edge.ID,
			TriggerSunKind:      models.AutomationSunKindSunset,
			ActionType:          models.AutomationActionNotification,
			ActionLevel:         "info",
			Latitude:            &lat, Longitude: &lon, Timezone: &tzName,
		}
		if err := fx.db.Create(&fresh).Error; err != nil {
			t.Fatal(err)
		}
		ev.LoadRules()

		ev.evalTimeWindows(fx.eventAt.Add(gap))
		if got := countFor(rec, fresh.ID); got != 0 {
			t.Fatalf("事件后新建的规则补发了 %d 次 —— 裁决: 不补发, 请手动触发", got)
		}
		// 同一个本地日的后续 tick 也不得"补偿"。
		ev.evalTimeWindows(fx.eventAt.Add(gap + time.Minute))
		if got := countFor(rec, fresh.ID); got != 0 {
			t.Fatalf("后续 tick 又补发了 %d 次", got)
		}

		// 真·次日: 跳过不是永久失效 —— 次日日落跨过时必须正常触发。
		nextDay := fx.eventAt.Add(24 * time.Hour)
		nextAt, status, err := sun.SunEvent(fx.lat, fx.lon, fx.tz, nextDay, sun.Sunset)
		if err != nil || status != sun.StatusOK {
			t.Fatalf("次日日落求解失败: %s %v", status, err)
		}
		ev.evalTimeWindows(nextAt.Add(time.Second))
		if got := countFor(rec, fresh.ID); got != 1 {
			t.Fatalf("次日日落应触发 1 次, got %d —— 跳过不得退化成永久失效", got)
		}
	})

	// ② 事件之后**停用再启用** (sunStates / configChanged 被重置): 不得补发。
	t.Run("disable_then_enable_after_event", func(t *testing.T) {
		fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
		rec := &sunRecorder{}
		ev := NewEvaluator(fx.db, rec)
		ev.LoadRules()
		ev.evalTimeWindows(fx.eventAt.Add(-30 * time.Minute))
		ev.evalTimeWindows(fx.eventAt.Add(time.Second))
		if rec.count() != 1 {
			t.Fatalf("前置: 准点应触发 1 次, got %d", rec.count())
		}
		if err := fx.db.Model(&models.AutomationRule{}).Where("id = ?", fx.rule.ID).
			Update("enabled", false).Error; err != nil {
			t.Fatal(err)
		}
		ev.LoadRules()
		if err := fx.db.Model(&models.AutomationRule{}).Where("id = ?", fx.rule.ID).
			Update("enabled", true).Error; err != nil {
			t.Fatal(err)
		}
		ev.LoadRules()
		before := rec.count()
		ev.evalTimeWindows(fx.eventAt.Add(gap))
		if got := rec.count() - before; got != 0 {
			t.Fatalf("停用→启用后补发了 %d 次 —— 裁决: 不补发", got)
		}
	})

	// ③ 事件之后**保存规则**: 改偏移 = sameWindowStateFields 白名单字段。
	//
	// 这条最容易漏: offset 0 → +1min 会把 fireAt **往后挪**到上一轮 tick 之后,
	// 于是 "prevTick < fireAt" 仍成立 —— 只有 configChanged 能拦住。
	t.Run("edit_after_event", func(t *testing.T) {
		fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
		rec := &sunRecorder{}
		ev := NewEvaluator(fx.db, rec)
		ev.LoadRules()
		ev.evalTimeWindows(fx.eventAt.Add(-30 * time.Minute))
		ev.evalTimeWindows(fx.eventAt.Add(time.Second))
		if rec.count() != 1 {
			t.Fatalf("前置: 准点应触发 1 次, got %d", rec.count())
		}
		if err := fx.db.Model(&models.AutomationRule{}).Where("id = ?", fx.rule.ID).
			Update("trigger_sun_offset_min", 1).Error; err != nil {
			t.Fatal(err)
		}
		ev.LoadRules() // 与 Invalidate() 同路径
		before := rec.count()
		ev.evalTimeWindows(fx.eventAt.Add(gap))
		if got := rec.count() - before; got != 0 {
			t.Fatalf("编辑后补发了 %d 次 —— 裁决: 不补发", got)
		}
	})

	// ④ 对照: 事件之前就生效的规则, 跨过事件时**必须**触发 (不能被守卫误杀)。
	t.Run("rule_active_before_event_still_fires", func(t *testing.T) {
		fx := buildSunFixture(t, models.AutomationSunKindSunset, 0)
		rec := &sunRecorder{}
		ev := NewEvaluator(fx.db, rec)
		ev.LoadRules()
		ev.evalTimeWindows(fx.eventAt.Add(-30 * time.Minute))
		ev.evalTimeWindows(fx.eventAt.Add(30 * time.Second))
		if rec.count() != 1 {
			t.Fatalf("事件前生效的规则跨过事件时必须触发 1 次, got %d", rec.count())
		}
	})
}

// countFor 按规则统计触发次数（多规则子用例需要区分是哪一条）。
func countFor(rec *sunRecorder, ruleID uint) int {
	n := 0
	for _, ev := range rec.events {
		if ev.Rule.ID == ruleID {
			n++
		}
	}
	return n
}
