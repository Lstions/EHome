package api

// A-13 / V2 / V3 / V4 保存校验门禁 (方案 §4.1.1, 用户裁决 O-7 fail-closed)。
//
// 走**真实 HTTP 路由** + 真实节点/设备行: 校验的判据本身就是"能否从规则值或
// 节点值解析出坐标与时区", 打桩会把被测逻辑换掉。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func f64pv(v float64) *float64 { return &v }

// newSunRouter 装配 automation-rules 路由。catalog 传 nil = 跳过动作目录校验
// (本文件只测 sun_event 的坐标/时区校验, 与动作目录无关)。
func newSunRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
		&models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate automation: %v", err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, nil, nil, nil, nil)
	return r, db
}

// seedSunNode 造一个带坐标/时区的节点 + 通道 + 边缘设备, 返回 edge device ID。
// 传入 nil 坐标即"未配置"。
func seedSunNode(t *testing.T, db *gorm.DB, nodeID string, lat, lon *float64, tz *string) uint {
	t.Helper()
	node := models.Node{NodeID: nodeID, Name: nodeID, Status: "online",
		Latitude: lat, Longitude: lon, Timezone: tz}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "edge-" + nodeID, NodeID: node.NodeID, ChannelID: ch.ID,
		Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	return edge.ID
}

// postSunRule 发一条 sun_event 创建请求, 返回 HTTP 状态码与响应体。
func postSunRule(t *testing.T, r *gin.Engine, body map[string]interface{}) (int, string) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/automation-rules", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

// sunRuleBody 造一条最简 sun_event 规则 (notification 动作, 不涉及风险授权)。
//
// 2026-11-13 起坐标与时区是**必填**（节点位置已废除），因此默认带上三项；
// 需要验证"缺坐标被拒"的用例用 sunRuleBodyNoLocation 或 delete 对应键。
func sunRuleBody(edgeDeviceID uint, extra map[string]interface{}) map[string]interface{} {
	body := map[string]interface{}{
		"name":              "日落规则",
		"trigger_type":      models.AutomationTriggerSunEvent,
		"trigger_sun_kind":  models.AutomationSunKindSunset,
		"action_type":       models.AutomationActionNotification,
		"action_level":      "info",
		"action_device_id":  edgeDeviceID,
		"require_confirmed": false,
		"latitude":          39.9,
		"longitude":         116.4,
		"timezone":          "Asia/Shanghai",
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// sunRuleBodyNoLocation 造一条**不带坐标/时区**的 sun_event 规则（extra 可补单项）。
func sunRuleBodyNoLocation(edgeDeviceID uint, extra map[string]interface{}) map[string]interface{} {
	body := map[string]interface{}{
		"name":              "日落规则",
		"trigger_type":      models.AutomationTriggerSunEvent,
		"trigger_sun_kind":  models.AutomationSunKindSunset,
		"action_type":       models.AutomationActionNotification,
		"action_level":      "info",
		"action_device_id":  edgeDeviceID,
		"require_confirmed": false,
	}
	for k, v := range extra {
		body[k] = v
	}
	return body
}

// ── A-13: 规则缺坐标/时区时保存被拒 (fail-closed) ───────────────────────
//
// 2026-11-13 用户裁决「不需要节点位置」后，坐标与时区**只来自规则行**：
// 缺任一项都必须 400，节点上即使配了值也不再继承。
//
// 反向验证: 把校验改成"回落默认坐标/UTC" → 本用例变红。
func TestA13SunEventRejectedWithoutCoordinates(t *testing.T) {
	cases := []struct {
		name       string
		drop       []string
		wantSubstr string
	}{
		{"缺三项", []string{"latitude", "longitude", "timezone"}, "latitude"},
		{"缺纬度", []string{"latitude"}, "latitude"},
		{"缺经度", []string{"longitude"}, "longitude"},
		{"缺时区", []string{"timezone"}, "timezone"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r, db := newSunRouter(t)
			// 节点**配齐**坐标：用来证明"节点值不再被继承"。
			lat, lon, tz := 39.9, 116.4, "Asia/Shanghai"
			edgeID := seedSunNode(t, db, "node-a13", &lat, &lon, &tz)
			body := sunRuleBody(edgeID, nil)
			for _, k := range c.drop {
				delete(body, k)
			}
			code, resp := postSunRule(t, r, body)
			if code != 400 {
				t.Fatalf("规则缺坐标/时区必须 400 拒绝保存 (fail-closed), got %d: %s", code, resp)
			}
			if !strings.Contains(resp, "invalid_automation_rule") {
				t.Fatalf("错误码应为 invalid_automation_rule, got %s", resp)
			}
			if !strings.Contains(resp, c.wantSubstr) {
				t.Fatalf("错误信息应指出缺少 %q, got %s", c.wantSubstr, resp)
			}
			// 关键反向断言: **不得**静默回落到默认时区/默认坐标。
			var n int64
			db.Model(&models.AutomationRule{}).Count(&n)
			if n != 0 {
				t.Fatalf("被拒的规则不得落库, got %d 行 —— 说明校验只是告警没拦住", n)
			}
		})
	}
}

// 节点配了坐标也不能再"救活"一条没填坐标的规则（O-3 继承路径已废除）。
func TestA13bNodeLocationNoLongerInherited(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tz := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-inherit", &lat, &lon, &tz)
	code, resp := postSunRule(t, r, sunRuleBodyNoLocation(edgeID, nil))
	if code != 400 {
		t.Fatalf("节点有坐标但规则没填，仍必须 400（不再继承节点）, got %d: %s", code, resp)
	}
	if !strings.Contains(resp, "节点位置已废除") {
		t.Fatalf("错误信息必须说明节点位置已废除（否则用户会去节点页找配置）, got %s", resp)
	}
}

// ── V2: trigger_sun_kind 必填且合法 ─────────────────────────────────────
func TestV2SunKindRequiredAndValid(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tzName := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-v2", &lat, &lon, &tzName)

	t.Run("缺 kind", func(t *testing.T) {
		body := sunRuleBody(edgeID, nil)
		delete(body, "trigger_sun_kind")
		code, resp := postSunRule(t, r, body)
		if code != 400 || !strings.Contains(resp, "trigger_sun_kind") {
			t.Fatalf("缺 trigger_sun_kind 必须 400, got %d: %s", code, resp)
		}
	})
	t.Run("非法 kind", func(t *testing.T) {
		code, resp := postSunRule(t, r, sunRuleBody(edgeID,
			map[string]interface{}{"trigger_sun_kind": "noon"}))
		if code != 400 || !strings.Contains(resp, "trigger_sun_kind") {
			t.Fatalf("非法 trigger_sun_kind 必须 400, got %d: %s", code, resp)
		}
	})
	t.Run("合法 kind 放行", func(t *testing.T) {
		for _, k := range []string{models.AutomationSunKindSunrise, models.AutomationSunKindSunset} {
			code, resp := postSunRule(t, r, sunRuleBody(edgeID,
				map[string]interface{}{"trigger_sun_kind": k, "name": "ok-" + k}))
			if code != 200 {
				t.Fatalf("合法 kind %q 应放行, got %d: %s", k, code, resp)
			}
		}
	})
}

// ── V2: 偏移范围 [-720, 720] ───────────────────────────────────────────
func TestV2SunOffsetRange(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tzName := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-off", &lat, &lon, &tzName)

	for _, c := range []struct {
		offset int
		ok     bool
	}{
		{0, true}, {720, true}, {-720, true},
		{721, false}, {-721, false},
	} {
		code, resp := postSunRule(t, r, sunRuleBody(edgeID,
			map[string]interface{}{"trigger_sun_offset_min": c.offset,
				"name": "off-" + strings.TrimSpace(strings.ReplaceAll(strings.TrimSpace(strings.Repeat("x", 1)), "x", "")) + itoa(c.offset)}))
		if c.ok && code != 200 {
			t.Fatalf("偏移 %d 应放行, got %d: %s", c.offset, code, resp)
		}
		if !c.ok && (code != 400 || !strings.Contains(resp, "trigger_sun_offset_min")) {
			t.Fatalf("偏移 %d 必须 400, got %d: %s", c.offset, code, resp)
		}
	}
}

// ── V3: 坐标范围必须被校验 (规则级覆盖) ─────────────────────────────────
//
// 反向验证: 删掉范围校验 → 越界值被接受 → 变红。
func TestV3SunCoordinateRange(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tzName := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-v3", &lat, &lon, &tzName)

	for _, c := range []struct {
		name     string
		lat, lon float64
		ok       bool
	}{
		{"纬度 +90", 90, 116.4, true},
		{"纬度 -90", -90, 116.4, true},
		{"经度 +180", 39.9, 180, true},
		{"经度 -180", 39.9, -180, true},
		{"纬度 90.1", 90.1, 116.4, false},
		{"纬度 -90.1", -90.1, 116.4, false},
		{"经度 180.1", 39.9, 180.1, false},
		{"经度 -180.1", 39.9, -180.1, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			code, resp := postSunRule(t, r, sunRuleBody(edgeID,
				map[string]interface{}{"latitude": c.lat, "longitude": c.lon,
					"name": "v3-" + c.name}))
			if c.ok && code != 200 {
				t.Fatalf("%s 应放行, got %d: %s", c.name, code, resp)
			}
			if !c.ok {
				if code != 400 {
					t.Fatalf("%s 必须 400, got %d: %s", c.name, code, resp)
				}
				if !strings.Contains(resp, "latitude") && !strings.Contains(resp, "longitude") {
					t.Fatalf("%s 错误信息应指出坐标越界, got %s", c.name, resp)
				}
			}
		})
	}
}

// ── V4: timezone 必须能 LoadLocation 成功 ───────────────────────────────
//
// 反向验证 (已实测): 让**唯一事实源** models.ValidateTimezone 在 LoadLocation
// 失败时回落 UTC (而不是报错) → 本用例变红。
//
// ⚠️ 诚实标注一层冗余: 本函数里的显式 V4 与 models.ResolveRuleSunLocation 内部
// 的时区校验**功能重叠**。变异测试实测: 只删掉这里的显式 V4 而保留解析器内的
// 校验, 本用例仍然全绿 (解析器会拦住)。即"显式 V4 层"是删掉也不改变行为的冗余
// 层。保留它的唯一理由: 它能给出更精确的错误信息 (直接指出时区名非法, 而不是
// 包在"无法解析坐标与时区"里)。真正的单点防线是 ValidateTimezone, 那条由上面
// 的反向验证钉死。此处不假装它是独立门禁。
func TestV4SunTimezoneMustParse(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tzName := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-v4", &lat, &lon, &tzName)

	t.Run("非法时区被拒", func(t *testing.T) {
		for _, bad := range []string{"Asia/Shanghi", "Mars/Olympus", "UTC+8", "Shanghai"} {
			code, resp := postSunRule(t, r, sunRuleBody(edgeID,
				map[string]interface{}{"timezone": bad, "name": "v4-" + bad}))
			if code != 400 || !strings.Contains(resp, "timezone") {
				t.Fatalf("非法时区 %q 必须 400, got %d: %s", bad, code, resp)
			}
		}
	})
	t.Run("合法时区放行", func(t *testing.T) {
		for _, good := range []string{"UTC", "Asia/Shanghai", "America/New_York", "Arctic/Longyearbyen"} {
			code, resp := postSunRule(t, r, sunRuleBody(edgeID,
				map[string]interface{}{"timezone": good, "name": "v4ok-" + good}))
			if code != 200 {
				t.Fatalf("合法时区 %q 应放行, got %d: %s", good, code, resp)
			}
		}
	})
}

// ── 规则级覆盖必须能"补足"节点级缺失 (O-3 的价值) ────────────────────────
func TestSunRuleLevelOverrideSatisfiesMissingNodeConfig(t *testing.T) {
	r, db := newSunRouter(t)
	// 节点**完全没有**坐标/时区。
	edgeID := seedSunNode(t, db, "node-override", nil, nil, nil)
	code, resp := postSunRule(t, r, sunRuleBody(edgeID, map[string]interface{}{
		"latitude":  31.23,
		"longitude": 121.47,
		"timezone":  "Asia/Shanghai",
	}))
	if code != 200 {
		t.Fatalf("规则级覆盖应能补足节点级缺失 (O-3), got %d: %s", code, resp)
	}
	// 落库值必须与请求一致 (而不是被静默丢弃)。
	var rule models.AutomationRule
	if err := db.First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if rule.Latitude == nil || *rule.Latitude != 31.23 {
		t.Fatalf("latitude 未落库: %v", rule.Latitude)
	}
	if rule.Timezone == nil || *rule.Timezone != "Asia/Shanghai" {
		t.Fatalf("timezone 未落库: %v", rule.Timezone)
	}
	// 与 sun_event 无关的字段不得被污染。
	if rule.TriggerSunKind != models.AutomationSunKindSunset {
		t.Fatalf("trigger_sun_kind 未落库: %q", rule.TriggerSunKind)
	}
}

// ── 只给一半坐标必须被拒 (避免"规则纬度 + 默认经度"的非法组合) ──────────
func TestSunRuleHalfCoordinateOverrideRejected(t *testing.T) {
	r, db := newSunRouter(t)
	lat, lon, tzName := 39.9, 116.4, "Asia/Shanghai"
	edgeID := seedSunNode(t, db, "node-half", &lat, &lon, &tzName)

	for _, c := range []map[string]interface{}{
		{"latitude": 31.23, "timezone": "Asia/Shanghai"},
		{"longitude": 121.47, "timezone": "Asia/Shanghai"},
	} {
		code, resp := postSunRule(t, r, sunRuleBodyNoLocation(edgeID, c))
		if code != 400 {
			t.Fatalf("只给一半坐标必须 400 (避免物理上不存在的组合), got %d: %s", code, resp)
		}
		if !strings.Contains(resp, "同时提供") {
			t.Fatalf("错误信息应说明需要同时提供, got %s", resp)
		}
	}
}

// ── 非 sun_event 规则不受本次新增校验影响 (回归护栏) ─────────────────────
func TestNonSunTriggersUnaffectedBySunValidation(t *testing.T) {
	r, _ := newSunRouter(t)
	body := map[string]interface{}{
		"name":                 "time window 规则",
		"trigger_type":         models.AutomationTriggerTimeWindow,
		"trigger_window_start": "08:00",
		"trigger_window_end":   "18:00",
		"trigger_window_edge":  models.AutomationWindowEnter,
		"action_type":          models.AutomationActionNotification,
		"action_level":         "info",
	}
	code, resp := postSunRule(t, r, body)
	if code != 200 {
		t.Fatalf("time_window 规则不应受 sun_event 校验影响, got %d: %s", code, resp)
	}
}

func strptr(s string) *string { return &s }
