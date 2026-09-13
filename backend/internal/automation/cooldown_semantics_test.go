package automation

// 负债 D-5 (cooldown_sec 零值语义矛盾) 的行为守护。
//
// 缺陷: 同一字段在两条读路径上语义相反 ——
//   evaluator.evalRule / evalWindowRule: CooldownSec<=0 → defaultCooldown(300s) (有冷却)
//   planner.TriggerRule:                 CooldownSec>0 才查冷却        (0=不冷却)
// 修复后统一为「0 = 不冷却」(与前端 :min="0" 的引导、与 MaxDailyExec 的 0=不限同族),
// 且两条路径共用同一判定函数 cooldownFor —— 语义只留一份实现。
//
// 变异自证: 把 evaluator 的判定改回 "<=0 → defaultCooldown" 本文件
// TestCooldownZeroMeansNoCooldownAcrossPaths 与
// TestEvaluatorZeroCooldownRecordsNoSuppressedEvent 必红。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// cooldownWindowRule 造表驱动用的 time_window/inside 规则: 窗口 08:00-18:00,
// 窗口内每 tick 都求值, 因此"某时刻是否再次触发"即等价于"是否处于冷却中"。
// 动作用 notification, 不触达 commandexec/通知落库 (纯求值器路径)。
func cooldownWindowRule(id uint, cooldownSec int) models.AutomationRule {
	return models.AutomationRule{
		ID:                 id,
		Name:               fmt.Sprintf("冷却语义 %ds", cooldownSec),
		Enabled:            true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00",
		TriggerWindowEnd:   "18:00",
		TriggerWindowEdge:  models.AutomationWindowInside,
		CooldownSec:        cooldownSec,
		ActionType:         models.AutomationActionNotification,
		ActionLevel:        models.AlertLevelInfo,
	}
}

// cooldownManualRule 造手动触发探针用的 device_action 规则。必须是会落
// result=executed 的动作 (low_read): planner 的冷却基线只查
// result IN (executed, pending_confirm), notification 动作不参与。
func cooldownManualRule(edgeID uint, cooldownSec int) models.AutomationRule {
	return models.AutomationRule{
		Name:                fmt.Sprintf("手动冷却探针 %ds", cooldownSec),
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edgeID,
		TriggerSensorName:   "illuminance",
		TriggerComparator:   "gt",
		TriggerThreshold:    500,
		CooldownSec:         cooldownSec,
		ActionType:          models.AutomationActionDeviceAction,
		ActionDeviceID:      edgeID,
		ActionID:            "low_read",
		ActionParamsJSON:    "{}",
	}
}

// evaluatorFires 推进一次 time_window tick, 返回本次是否触发。
// inside 边沿下"本次没触发" ⇔ "此刻处于冷却中"。
func evaluatorFires(t *testing.T, ev *Evaluator, h *captureHandler, at time.Time) bool {
	t.Helper()
	before := len(h.events)
	ev.evalTimeWindows(at)
	return len(h.events) > before
}

// TestCooldownZeroMeansNoCooldownAcrossPaths 核心一致性测试 (表驱动 0/1/300/86400):
// 同一条 cooldown_sec 规则, evaluator (自动路径, 内存 triggered map) 与
// planner (手动路径, DB 兜底冷却) 在同一 elapsed 时刻必须给出相同结论。
// cooldown_sec=0 的期望是【两条路径都不冷却】。
//
// 采样点随冷却时长给出, 每个 case 都覆盖"冷却中"与"冷却到期"两侧 (单点断言会漏判):
//   - 0: 30s/130s 两点都不冷却 (旧语义当 300s 时, 这两点会双双红 — D-5 判据)
//   - 1: 500ms 冷却中, 30s 已到期
//   - 300/86400: 冷却中, 到期后回 armed
func TestCooldownZeroMeansNoCooldownAcrossPaths(t *testing.T) {
	type probe struct {
		delta       time.Duration
		wantCooling bool
	}
	cases := []struct {
		cooldownSec int
		probes      []probe
	}{
		{0, []probe{{30 * time.Second, false}, {130 * time.Second, false}}},
		{1, []probe{{500 * time.Millisecond, true}, {30 * time.Second, false}}},
		{300, []probe{{30 * time.Second, true}, {130 * time.Second, true}, {301 * time.Second, false}}},
		{86400, []probe{{30 * time.Second, true}, {130 * time.Second, true}, {86401 * time.Second, false}}},
	}

	for _, tc := range cases {
		t.Run(fmt.Sprintf("cooldown_%d", tc.cooldownSec), func(t *testing.T) {
			// ── 自动路径: evaluator (内存冷却) ──
			db := newTestDB(t)
			h := &captureHandler{}
			ev := NewEvaluator(db, h)
			rule := cooldownWindowRule(1, tc.cooldownSec)
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			var stored models.AutomationRule
			if err := db.First(&stored, rule.ID).Error; err != nil {
				t.Fatal(err)
			}
			if stored.CooldownSec != tc.cooldownSec {
				t.Fatalf("前置条件: 规则 cooldown_sec 落库 %d, 期望 %d "+
					"(0 存不进库时本测试无从谈起)", stored.CooldownSec, tc.cooldownSec)
			}
			ev.LoadRules()

			now := time.Now()
			t0 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
			if !evaluatorFires(t, ev, h, t0) {
				t.Fatal("首次 tick 未触发 (冷却判定错误地压制了首次触发)")
			}

			// ── 手动路径: planner.TriggerRule (DB 兜底冷却) ──
			// 冻结可变时钟: 首次触发在 base 建立冷却基线, 之后按与自动路径相同的
			// delta 采样, 两条路径的 elapsed 严格对齐。
			p, edge := setupConfirmPlanner(t)
			newAdminOperator(t, p.db, 7)
			base := time.Now()
			cur := base
			p.nowFn = func() time.Time { return cur }
			mr := cooldownManualRule(edge.ID, tc.cooldownSec)
			if err := p.db.Create(&mr).Error; err != nil {
				t.Fatal(err)
			}
			var mstored models.AutomationRule
			if err := p.db.First(&mstored, mr.ID).Error; err != nil {
				t.Fatal(err)
			}
			if mstored.CooldownSec != tc.cooldownSec {
				t.Fatalf("前置条件: 手动规则 cooldown_sec 落库 %d, 期望 %d",
					mstored.CooldownSec, tc.cooldownSec)
			}
			first, err := p.TriggerRule(context.Background(), mr.ID, 7, "127.0.0.1")
			if err != nil {
				t.Fatalf("首次手动触发报错: %v", err)
			}
			if first.Result != models.AutomationResultExecuted {
				t.Fatalf("首次手动触发 result=%s, 期望 executed (冷却基线未建立)",
					first.Result)
			}

			for _, pr := range tc.probes {
				autoCooling := !evaluatorFires(t, ev, h, t0.Add(pr.delta))
				if autoCooling != pr.wantCooling {
					t.Fatalf("自动路径 cooldown_sec=%d: 触发后 %s 冷却中=%v, 期望 %v",
						tc.cooldownSec, pr.delta, autoCooling, pr.wantCooling)
				}

				cur = base.Add(pr.delta)
				second, err := p.TriggerRule(context.Background(), mr.ID, 7, "127.0.0.1")
				if err != nil {
					t.Fatalf("手动触发 (delta=%s) 报错: %v", pr.delta, err)
				}
				manualCooling := second.Result == models.AutomationResultSuppressedCooldown
				if manualCooling != pr.wantCooling {
					t.Fatalf("手动路径 cooldown_sec=%d: 触发后 %s result=%s (冷却中=%v), 期望冷却中=%v",
						tc.cooldownSec, pr.delta, second.Result, manualCooling, pr.wantCooling)
				}

				// ── 两路径结论必须一致 (负债 D-5 的判据) ──
				if autoCooling != manualCooling {
					t.Fatalf("cooldown_sec=%d (delta=%s): 两路径结论不一致 — "+
						"evaluator 冷却中=%v, planner 冷却中=%v",
						tc.cooldownSec, pr.delta, autoCooling, manualCooling)
				}
			}
		})
	}
}

// TestCooldownForIsSingleSourceOfTruth 守护"同一语义只有一份实现":
// 判定收敛到纯函数 cooldownFor, 两条路径都调用它。
// 0 (与负数等无效配置) → 0 = 不冷却; 正数 → 原值。
func TestCooldownForIsSingleSourceOfTruth(t *testing.T) {
	cases := []struct {
		sec  int
		want time.Duration
	}{
		{0, 0},
		{-1, 0},
		{1, time.Second},
		{300, 300 * time.Second},
		{86400, 86400 * time.Second},
	}
	for _, tc := range cases {
		if got := cooldownFor(models.AutomationRule{CooldownSec: tc.sec}); got != tc.want {
			t.Fatalf("cooldownFor(cooldown_sec=%d) = %s, 期望 %s", tc.sec, got, tc.want)
		}
	}
}

// TestEvaluatorZeroCooldownRecordsNoSuppressedEvent 审计语义守护:
// 0 = 不冷却 ⇒ 不存在冷却期 ⇒ 绝不落 suppressed_cooldown 事件 (recordSuppressed
// 只在"确有冷却且命中"时才该被调用), 且同一时刻反复越阈时每次都触发。
// 正对照 (cooldown_sec=60) 证明该断言非空洞: 有冷却时审计事件照常落一条。
func TestEvaluatorZeroCooldownRecordsNoSuppressedEvent(t *testing.T) {
	cases := []struct {
		name           string
		cooldownSec    int
		wantTriggers   int
		wantSuppressed int64
	}{
		{"cooldown_0_no_suppressed_event", 0, 3, 0},
		{"cooldown_60_still_records_suppressed_event", 60, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			h := &captureHandler{}
			ev := NewEvaluator(db, h)
			rule := automationRule(1, "illuminance", "gt", 500, 0)
			rule.CooldownSec = tc.cooldownSec
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			ev.LoadRules()

			at := time.Now()
			ev.Evaluate(1, illuminanceField(600), at)
			ev.Evaluate(1, illuminanceField(600), at.Add(time.Second))
			ev.Evaluate(1, illuminanceField(600), at.Add(2*time.Second))

			if len(h.events) != tc.wantTriggers {
				t.Fatalf("cooldown_sec=%d: 触发 %d 次, 期望 %d 次",
					tc.cooldownSec, len(h.events), tc.wantTriggers)
			}
			var cnt int64
			if err := db.Model(&models.AutomationEvent{}).
				Where("rule_id = ? AND result = ?", rule.ID,
					models.AutomationResultSuppressedCooldown).Count(&cnt).Error; err != nil {
				t.Fatalf("统计 suppressed_cooldown 失败: %v", err)
			}
			if cnt != tc.wantSuppressed {
				t.Fatalf("cooldown_sec=%d: 落 %d 条 suppressed_cooldown, 期望 %d 条",
					tc.cooldownSec, cnt, tc.wantSuppressed)
			}
		})
	}
}
