package api

// time_window 求值器落地后解禁创建 (B2): 字段齐全即放行。event 触发器仍创建禁配
// (语义未冻结)。sensor_threshold 正常放行。validateAutomationRuleFields 为纯函数
// (返回错误串), 直接单测无需 HTTP 栈。

import (
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

func u32p(v uint) *uint          { return &v }
func f64p(v float64) *float64    { return &v }

// 合法的 sensor_threshold 全字段 (动作=notification 最简), 作基准用例。
func validSensorThresholdArgs() (string, string, string, *uint, string, string, *float64,
	string, string, string, *uint, string, string, bool) {
	return "r", models.AutomationTriggerSensorThreshold, models.AutomationActionNotification,
		u32p(1), "illuminance", models.AlertComparatorGT, f64p(500),
		"", "", "", // window*
		nil, "", models.AlertLevelInfo, // action*
		false // requireConfirmed
}

func TestValidateTimeWindowCreateAccepted(t *testing.T) {
	// time_window 创建: 字段齐全应放行 (求值器已实现, B2 解禁)。
	msg := validateAutomationRuleFields(
		"r", models.AutomationTriggerTimeWindow, models.AutomationActionNotification,
		nil, "", "", nil,
		"08:00", "18:00", models.AutomationWindowEnter, // window 字段齐全
		nil, "", models.AlertLevelInfo,
		false, false /* isUpdate=false 创建 */)
	if msg != "" {
		t.Fatalf("time_window create must be accepted, got %q", msg)
	}

	// time_window 更新存量规则: 同样放行。
	msg = validateAutomationRuleFields(
		"r", models.AutomationTriggerTimeWindow, models.AutomationActionNotification,
		nil, "", "", nil,
		"08:00", "18:00", models.AutomationWindowEnter,
		nil, "", models.AlertLevelInfo,
		false, true /* isUpdate=true 更新 */)
	if msg != "" {
		t.Fatalf("time_window update existing rule must pass, got %q", msg)
	}
}

func TestValidateEventCreateRejected(t *testing.T) {
	msg := validateAutomationRuleFields(
		"r", models.AutomationTriggerEvent, models.AutomationActionNotification,
		nil, "", "", nil,
		"", "", "",
		nil, "", models.AlertLevelInfo,
		false, false)
	if !strings.Contains(msg, "event") {
		t.Fatalf("event create must be rejected, got %q", msg)
	}
}

func TestValidateSensorThresholdAccepted(t *testing.T) {
	name, tt, at, ted, sn, cmp, thr, ws, we, wedge, adid, aid, alvl, rc := validSensorThresholdArgs()
	if msg := validateAutomationRuleFields(name, tt, at, ted, sn, cmp, thr, ws, we, wedge, adid, aid, alvl, rc, false); msg != "" {
		t.Fatalf("valid sensor_threshold must pass, got %q", msg)
	}
}

// §5.3 校验补强: cooldown_sec / max_daily_exec 边界。
func TestValidateAutomationRuleConstraints(t *testing.T) {
	tests := []struct {
		name        string
		cooldownSec *int
		maxDailyExec int
		wantErr     bool
	}{
		{"cooldown nil ok", nil, 0, false},
		{"cooldown 0 ok", intp(0), 0, false},
		{"cooldown 86400 ok", intp(86400), 0, false},
		{"cooldown -1 reject", intp(-1), 0, true},
		{"cooldown 86401 reject", intp(86401), 0, true},
		{"max_daily_exec 0 ok", nil, 0, false},
		{"max_daily_exec 1000 ok", nil, 1000, false},
		{"max_daily_exec -1 reject", nil, -1, true},
		{"max_daily_exec 1001 reject", nil, 1001, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := validateAutomationRuleConstraints(tt.cooldownSec, tt.maxDailyExec)
			if tt.wantErr && msg == "" {
				t.Fatalf("expect error, got empty")
			}
			if !tt.wantErr && msg != "" {
				t.Fatalf("expect pass, got %q", msg)
			}
		})
	}
}

func intp(v int) *int { return &v }

