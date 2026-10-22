package api

// 来源组保存期校验 + 删除守卫测试（方案 §7.3 V-9/V-13、§12.9 F-3/F-5；SG-01/SG-03/SG-20）。
//
// 覆盖：
//   - 五态绑定非法输入（models.ValidateSourceRefColumns）× API 400 + error_code；
//   - 组不存在 / 组停用 / 空组（锚点=0）/ cooldown 低于推导下限 ⇒ 400；
//   - 合法绑组 ⇒ 200 且锚点双写（automation trigger_edge_device_id；alert target_type/target_id）；
//   - 边缘设备被组引用 ⇒ 单删/批量删 409，文案含组名；移除成员后可删；
//   - 逻辑设备（经其下边缘设备）被组引用 ⇒ 合并 409。
//
// 数据库用 SQLite 内存库（setupTestDB），组表与规则表按域自补 AutoMigrate。

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newSourceGroupGuardAPI(t *testing.T) (*gin.Engine, *gorm.DB, *datasource.GroupService) {
	t.Helper()
	db := setupTestDB(t)
	if err := db.AutoMigrate(
		&models.SourceGroup{}, &models.SourceGroupMember{},
		&models.SourceGroupFailoverLog{}, &models.SourceGroupHealth{},
		&models.SensorFieldObs{},
		&models.AlertRule{}, &models.AutomationRule{}, &models.AutomationRuleAction{},
	); err != nil {
		t.Fatalf("migrate source group tables: %v", err)
	}
	gsvc := datasource.NewGroupService(db, datasource.Options{})
	// 让保存期校验使用同一实例（未注入时会按 db 惰性构造，也指向同一 db）；
	// 显式注入同时验证 SetRuleSourceGroupService 这条接线。
	SetRuleSourceGroupService(gsvc)
	t.Cleanup(func() { SetRuleSourceGroupService(nil) })

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerAlertRoutes(v1, db, nil)
	registerAutomationRoutes(v1, db, nil, nil, nil, nil)
	// 边缘设备路由在注册期即取 nodeMgr.EventBus()（handler_edge_device.go:306），必须给非 nil
	// manager；构造方式复用 handler_edge_device_crud_test.go 的既有范式。
	driverRegistry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(driverRegistry)
	nodeMgr := nodemgr.NewManager(db, nil, nil, nil, nil, driverRegistry)
	registerEdgeDeviceRoutes(v1, db, nodeMgr, driverRegistry)
	registerLogicalDeviceRoutes(v1, db)
	return r, db, gsvc
}

// createGuardEdgeDevice 造一台"已上报 illuminance"的边缘设备（V-5 通过）。
func createGuardEdgeDevice(t *testing.T, db *gorm.DB, name string) models.EdgeDevice {
	t.Helper()
	ed := models.EdgeDevice{Name: name, NodeID: "node-1", ChannelID: 1, IntervalMs: 5000, Enabled: true}
	if err := db.Create(&ed).Error; err != nil {
		t.Fatalf("create edge device: %v", err)
	}
	if err := db.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: "illuminance"}).Error; err != nil {
		t.Fatalf("create sensor field obs: %v", err)
	}
	return ed
}

// createGuardGroup 造一个主备组（成员=给定边缘设备）。
func createGuardGroup(t *testing.T, gsvc *datasource.GroupService, name string, members ...models.EdgeDevice) *models.SourceGroup {
	t.Helper()
	inputs := make([]datasource.GroupMemberInput, 0, len(members))
	for i, m := range members {
		inputs = append(inputs, datasource.GroupMemberInput{EdgeDeviceID: m.ID, Priority: 10 - i})
	}
	g, err := gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: name, Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance",
		Members: inputs,
	})
	if err != nil {
		t.Fatalf("create source group: %v", err)
	}
	return g
}

func doGuardJSON(t *testing.T, r *gin.Engine, method, path string, body any) (int, map[string]any) {
	t.Helper()
	var reader *bytes.Reader
	if body == nil {
		reader = bytes.NewReader(nil)
	} else {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		reader = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var env map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, w.Body.String())
	}
	return w.Code, env
}

func guardErrCode(env map[string]any) string {
	if s, ok := env["error_code"].(string); ok {
		return s
	}
	return ""
}

// automationRuleBody 是合法的 sensor_threshold 规则体（动作=notification）。
func guardAutomationRuleBody() map[string]any {
	return map[string]any{
		"name":                 "guard-rule",
		"trigger_type":         models.AutomationTriggerSensorThreshold,
		"trigger_sensor_name":  "illuminance",
		"trigger_comparator":   models.AlertComparatorGT,
		"trigger_threshold":    500,
		"trigger_duration_sec": 0,
		"action_type":          models.AutomationActionNotification,
		"action_level":         models.AlertLevelInfo,
		"cooldown_sec":         30,
	}
}

func guardAlertRuleBody() map[string]any {
	return map[string]any{
		"name":        "guard-alert",
		"sensor_name": "illuminance",
		"comparator":  models.AlertComparatorGT,
		"threshold":   500,
		"level":       models.AlertLevelInfo,
	}
}

// 1) 五态绑定非法输入各自 400 + 正确 error_code。
func TestSourceGroupReferenceValidationFiveStates(t *testing.T) {
	r, _, _ := newSourceGroupGuardAPI(t)

	cases := []struct {
		name     string
		kind     any
		groupID  any
		wantCode string
	}{
		{"kind unknown", "sensor_group", nil, models.SourceRefCodeKindInvalid},
		{"group missing while kind=source_group", models.SourceKindSourceGroup, nil, models.SourceRefCodeGroupMissing},
		{"orphan group id", nil, 7, models.SourceRefCodeKindOrphan},
		{"edge_device with group id", models.SourceKindEdgeDevice, 7, models.SourceRefCodeKindInvalid},
		{"source_kind empty with group", "", 7, models.SourceRefCodeKindOrphan},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := guardAutomationRuleBody()
			if tc.kind != nil {
				body["source_kind"] = tc.kind
			}
			if tc.groupID != nil {
				body["source_group_id"] = tc.groupID
			}
			status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d body=%v", status, env)
			}
			if got := guardErrCode(env); got != tc.wantCode {
				t.Fatalf("expected error_code %q, got %q (env=%v)", tc.wantCode, got, env)
			}
		})
	}
}

// 2) 组不存在 / 停用 / 空组 / cooldown 低于下限 ⇒ 400；合法 ⇒ 200 且双写锚点。
func TestSourceGroupReferenceValidationGroupState(t *testing.T) {
	r, db, gsvc := newSourceGroupGuardAPI(t)
	ed := createGuardEdgeDevice(t, db, "light-1")
	group := createGuardGroup(t, gsvc, "组-主备", ed)

	t.Run("group not found", func(t *testing.T) {
		body := guardAutomationRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = 99999
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusBadRequest || guardErrCode(env) != models.SourceRefCodeGroupMissing {
			t.Fatalf("expected 400 source_group_missing, got %d %v", status, env)
		}
	})

	t.Run("group disabled", func(t *testing.T) {
		db.Model(&models.SourceGroup{}).Where("id = ?", group.ID).Update("enabled", false)
		defer db.Model(&models.SourceGroup{}).Where("id = ?", group.ID).Update("enabled", true)
		body := guardAutomationRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = group.ID
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusBadRequest || guardErrCode(env) != SourceRefCodeGroupDisabled {
			t.Fatalf("expected 400 source_group_disabled, got %d %v", status, env)
		}
	})

	t.Run("cooldown below floor", func(t *testing.T) {
		body := guardAutomationRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = group.ID
		body["cooldown_sec"] = 1 // 成员 IntervalMs=5000 ⇒ 推导下限 5s（F-3）
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusBadRequest || guardErrCode(env) != SourceRefCodeCooldownFloor {
			t.Fatalf("expected 400 cooldown floor, got %d %v", status, env)
		}
	})

	t.Run("empty group anchor is zero", func(t *testing.T) {
		emptyGroup, err := gsvc.CreateGroup(datasource.CreateGroupInput{
			Name: "组-空", Kind: models.SourceGroupKindComposite, Category: "illuminance",
			Combine: models.CombineAvg, Members: []datasource.GroupMemberInput{{EdgeDeviceID: ed.ID}},
		})
		if err != nil {
			t.Fatalf("create composite group: %v", err)
		}
		if err := db.Where("group_id = ?", emptyGroup.ID).Delete(&models.SourceGroupMember{}).Error; err != nil {
			t.Fatalf("clear members: %v", err)
		}
		body := guardAutomationRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = emptyGroup.ID
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusBadRequest || guardErrCode(env) != SourceRefCodeEmptyAnchor {
			t.Fatalf("expected 400 source_group_empty_anchor, got %d %v", status, env)
		}
	})

	t.Run("valid automation binding double-writes anchor", func(t *testing.T) {
		body := guardAutomationRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = group.ID
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d %v", status, env)
		}
		var rule models.AutomationRule
		if err := db.Where("name = ?", "guard-rule").First(&rule).Error; err != nil {
			t.Fatalf("load rule: %v", err)
		}
		if rule.SourceKind == nil || *rule.SourceKind != string(models.SourceKindSourceGroup) {
			t.Fatalf("source_kind not persisted: %+v", rule.SourceKind)
		}
		if rule.SourceGroupID == nil || *rule.SourceGroupID != group.ID {
			t.Fatalf("source_group_id not persisted: %+v", rule.SourceGroupID)
		}
		if rule.TriggerEdgeDeviceID != ed.ID {
			t.Fatalf("anchor not double-written to trigger_edge_device_id: got %d want %d",
				rule.TriggerEdgeDeviceID, ed.ID)
		}
	})

	t.Run("valid alert binding double-writes target", func(t *testing.T) {
		body := guardAlertRuleBody()
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = group.ID
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/alert-rules", body)
		if status != http.StatusCreated {
			t.Fatalf("expected 201, got %d %v", status, env)
		}
		var rule models.AlertRule
		if err := db.Where("name = ?", "guard-alert").First(&rule).Error; err != nil {
			t.Fatalf("load alert rule: %v", err)
		}
		if rule.SourceKind == nil || *rule.SourceKind != string(models.SourceKindSourceGroup) {
			t.Fatalf("alert source_kind not persisted")
		}
		if rule.TargetType != models.AlertTargetEdgeDevice || rule.TargetID != ed.ID {
			t.Fatalf("alert anchor not double-written: target_type=%q target_id=%d (want edge_device/%d)",
				rule.TargetType, rule.TargetID, ed.ID)
		}
	})

	// B1 终审收口：cooldown 推导下限必须对 composite 同样生效（F-3 无 kind 限制；
	// 复合组 N 个成员帧交错驱动，cooldown=0 会让同一次物理变化多次动作）。
	composite, err := gsvc.CreateGroup(datasource.CreateGroupInput{
		Name: "组-复合-floor", Kind: models.SourceGroupKindComposite, Category: "illuminance",
		Combine: models.CombineSum, Members: []datasource.GroupMemberInput{{EdgeDeviceID: ed.ID}},
	})
	if err != nil {
		t.Fatalf("create composite group: %v", err)
	}
	t.Run("composite cooldown below floor also rejected", func(t *testing.T) {
		body := guardAutomationRuleBody()
		body["name"] = "guard-rule-composite-0"
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = composite.ID
		body["cooldown_sec"] = 0
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusBadRequest || guardErrCode(env) != SourceRefCodeCooldownFloor {
			t.Fatalf("composite cooldown=0 应 400 %s，got %d %v", SourceRefCodeCooldownFloor, status, env)
		}
	})
	t.Run("composite cooldown at floor accepted", func(t *testing.T) {
		body := guardAutomationRuleBody()
		body["name"] = "guard-rule-composite-ok"
		body["source_kind"] = models.SourceKindSourceGroup
		body["source_group_id"] = composite.ID
		body["cooldown_sec"] = 5 // 成员 IntervalMs=5000 ⇒ 推导下限 5s
		status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/automation-rules", body)
		if status != http.StatusOK {
			t.Fatalf("composite cooldown=5 应保存成功，got %d %v", status, env)
		}
	})
}

// 3) 边缘设备被来源组引用 ⇒ 单删/批量删 409；移除成员后可删。
func TestEdgeDeviceDeleteReferencedByGroup(t *testing.T) {
	r, db, gsvc := newSourceGroupGuardAPI(t)
	ed := createGuardEdgeDevice(t, db, "light-del")
	group := createGuardGroup(t, gsvc, "组-引用设备", ed)

	status, env := doGuardJSON(t, r, http.MethodDelete, fmt.Sprintf("/api/v1/edge-devices/%d", ed.ID), nil)
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d %v", status, env)
	}
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, group.Name) {
		t.Fatalf("409 message must contain group name %q, got %q", group.Name, msg)
	}

	// 批量删除同样整体 409。
	status, env = doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-delete",
		map[string]any{"ids": []uint{ed.ID}})
	if status != http.StatusConflict {
		t.Fatalf("batch delete expected 409, got %d %v", status, env)
	}

	// 移除成员后可删。
	if err := db.Where("group_id = ? AND edge_device_id = ?", group.ID, ed.ID).
		Delete(&models.SourceGroupMember{}).Error; err != nil {
		t.Fatalf("remove member: %v", err)
	}
	status, env = doGuardJSON(t, r, http.MethodDelete, fmt.Sprintf("/api/v1/edge-devices/%d", ed.ID), nil)
	if status != http.StatusOK {
		t.Fatalf("expected 200 after member removed, got %d %v", status, env)
	}
}

// 3b) B2/I6 终审收口：停用"组被规则引用"的成员设备 ⇒ 409。
func TestEdgeDeviceDisableReferencedByGroup(t *testing.T) {
	r, db, gsvc := newSourceGroupGuardAPI(t)
	ed := createGuardEdgeDevice(t, db, "light-disable")
	group := createGuardGroup(t, gsvc, "组-停用守卫", ed)
	// 组被一条 automation 规则引用（source_group_id 指向该组）。
	kind := string(models.SourceKindSourceGroup)
	rule := models.AutomationRule{
		Name: "ref-rule", Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold, TriggerSensorName: "illuminance",
		TriggerComparator: models.AlertComparatorGT, TriggerThreshold: 500,
		TriggerEdgeDeviceID: ed.ID, CooldownSec: 300,
		SourceKind: &kind, SourceGroupID: &group.ID,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	status, env := doGuardJSON(t, r, http.MethodPut, fmt.Sprintf("/api/v1/edge-devices/%d", ed.ID),
		map[string]any{"enabled": false})
	if status != http.StatusConflict {
		t.Fatalf("expected 409 when disabling referenced group member, got %d %v", status, env)
	}
	if msg, _ := env["message"].(string); !strings.Contains(msg, group.Name) {
		t.Fatalf("409 message must contain group name %q, got %q", group.Name, msg)
	}
	// 移除规则后守卫放行（直接断言 helper，避免依赖 update handler 的其它校验）。
	if err := db.Delete(&rule).Error; err != nil {
		t.Fatalf("delete rule: %v", err)
	}
	ginCtx, _ := gin.CreateTestContext(nil)
	if !guardEdgeDeviceDisable(ginCtx, db, ed.ID) {
		t.Fatalf("移除规则后应允许停用（guard 仍返回 false）")
	}
}

// 4) 逻辑设备（经其下边缘设备间接判断）被组引用 ⇒ 合并 409。
func TestLogicalDeviceDeleteReferencedByGroup(t *testing.T) {
	r, db, gsvc := newSourceGroupGuardAPI(t)
	ld := models.LogicalDevice{Name: "逻辑设备-1"}
	if err := db.Create(&ld).Error; err != nil {
		if err := db.Create(&models.LogicalDevice{Name: "逻辑设备-1"}).Error; err != nil {
			t.Fatalf("create logical device: %v", err)
		}
	}
	var loaded models.LogicalDevice
	db.Last(&loaded)
	ed := createGuardEdgeDevice(t, db, "light-ld")
	ed.LogicalDeviceID = &loaded.ID
	if err := db.Save(&ed).Error; err != nil {
		t.Fatalf("set logical_device_id: %v", err)
	}
	group := createGuardGroup(t, gsvc, "组-逻辑引用", ed)

	status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/logical-devices/merge",
		map[string]any{"target_name": "合并目标", "source_ids": []uint{loaded.ID}})
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d %v", status, env)
	}
	msg, _ := env["message"].(string)
	if !strings.Contains(msg, group.Name) {
		t.Fatalf("409 message must contain group name %q, got %q", group.Name, msg)
	}

	// 移除成员后守卫放行（直接断言 helper，不依赖 MergeDevices 的其它校验）。
	if err := db.Where("group_id = ?", group.ID).Delete(&models.SourceGroupMember{}).Error; err != nil {
		t.Fatalf("remove member: %v", err)
	}
	if info, err := findGroupReferencesByLogicalDevice(db, loaded.ID); err != nil || !info.empty() {
		t.Fatalf("after member removal references must be empty, got %+v err=%v", info, err)
	}
}
