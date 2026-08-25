package api

// F1 完整性评审修复回归: time_window 触发器创建时 fail-closed 拒绝 (求值 ticker 整体缺失,
// 创建即死规则永不触发)。event 触发器同样创建禁配 (语义未冻结)。sensor_threshold 正常放行。
// validateAutomationRuleFields 为纯函数 (返回错误串), 直接单测无需 HTTP 栈。

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

func TestValidateTimeWindowCreateRejected(t *testing.T) {
	// time_window 创建: 字段齐全也应被 fail-closed 拒绝 (求值器未实现)。
	msg := validateAutomationRuleFields(
		"r", models.AutomationTriggerTimeWindow, models.AutomationActionNotification,
		nil, "", "", nil,
		"08:00", "18:00", models.AutomationWindowEnter, // window 字段齐全
		nil, "", models.AlertLevelInfo,
		false, false /* isUpdate=false 创建 */)
	if !strings.Contains(msg, "time_window") {
		t.Fatalf("time_window create must be rejected, got %q", msg)
	}

	// time_window 更新存量规则: 不拦 (灰度兼容)。
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
