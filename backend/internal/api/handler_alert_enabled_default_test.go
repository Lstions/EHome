package api

// 告警规则「显式创建为禁用」不得被静默存成启用（回归锁）。
//
// 缺陷（2026-08-23 探针实锤于 automation、2026-09-16 本处修复）：
// `models.AlertRule.Enabled` 曾带 GORM tag `default:true`。GORM 会把 bool 零值 false
// 在 INSERT 时**替换成 DB 默认值 true** ⇒ 请求 `enabled:false` 的规则被存成启用。
// 实测（修复前）：POST `enabled:false` ⇒ 响应 `enabled:true`，且 DB 行 `Enabled=true`。
//
// 为什么必须连 DB 断言而不只断言响应：handler 里构造的结构体本身是对的
// （`Enabled: req.Enabled == nil || *req.Enabled`）——**污染发生在 GORM 落库那一刻**，
// 只看 HTTP 响应在修复前同样会看到 true，但只看响应无法区分「handler 算错」与「落库被覆盖」。
// 故本文件同时断言响应与**回读的 DB 行**。
//
// 为什么这个缺陷危险：用户以为创建了一条「暂不启用」的规则，实际它**立刻生效** ——
// 告警会被真实触发（可能正是用户想避免的误报）。
//
// See docs/设计/自动化策略引擎接口设计.md §6（automation 同源缺陷的修复记录）。

import (
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// TestAlertRule_CreateDisabledStaysDisabled 是本次修复的核心断言。
func TestAlertRule_CreateDisabledStaysDisabled(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.AlertRule{}); err != nil {
		t.Fatalf("migrate alert_rule: %v", err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), db, nil)

	created := alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1,
		"sensor_name": "temperature", "comparator": "gt",
		"threshold": 50.0, "duration_sec": 0, "level": "warning",
		"enabled": false, "name": "显式禁用",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create must 201, got %d %s", created.Code, created.Body.String())
	}

	code, data := decodeEnvelope(t, created)
	if code != http.StatusCreated {
		t.Fatalf("bad envelope code=%d", code)
	}
	var respRule models.AlertRule
	if err := json.Unmarshal(data, &respRule); err != nil {
		t.Fatal(err)
	}
	if respRule.Enabled {
		t.Errorf("响应里 enabled 应为 false（请求显式传了 false），实际 %v", respRule.Enabled)
	}

	// 关键：回读 DB —— 落库被 GORM default 覆盖只会在这里暴露
	var row models.AlertRule
	if err := db.Where("name = ?", "显式禁用").First(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if row.Enabled {
		t.Errorf("DB 行 enabled 应为 false —— 若为 true，说明 GORM 的 default:true tag " +
			"把显式 false 覆盖成了启用（用户以为没启用，实际已生效）")
	}
}

// TestAlertRule_CreateOmittedDefaultsToEnabled 钉住另一半语义：
// 不传 enabled ⇒ 默认启用（与 automation 一致，fail-closed 由应用层赋值）。
func TestAlertRule_CreateOmittedDefaultsToEnabled(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.AlertRule{}); err != nil {
		t.Fatalf("migrate alert_rule: %v", err)
	}
	r := ginNew()
	registerAlertRoutes(r.Group("/api/v1"), db, nil)

	created := alertReq(t, r, "POST", "/api/v1/alert-rules", map[string]any{
		"target_type": "edge_device", "target_id": 1,
		"sensor_name": "temperature", "comparator": "gt",
		"threshold": 50.0, "duration_sec": 0, "level": "warning",
		"name": "未传enabled",
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("create must 201, got %d %s", created.Code, created.Body.String())
	}
	var row models.AlertRule
	if err := db.Where("name = ?", "未传enabled").First(&row).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !row.Enabled {
		t.Errorf("未传 enabled 时应默认启用（应用层赋值），实际 Enabled=false")
	}
}

// TestAlertRule_ModelHasNoDefaultTrueTag 是**源码判据**，作为行为断言的补充：
// 直接盯住「tag 里不得再出现 default:true」—— 因为该 tag 还会让 AutoMigrate
// 给列建 `DEFAULT true`，直连 SQL 的写入（不经 handler）同样会被污染，
// 而那种路径行为测试覆盖不到。
func TestAlertRule_ModelHasNoDefaultTrueTag(t *testing.T) {
	field, ok := reflect.TypeOf(models.AlertRule{}).FieldByName("Enabled")
	if !ok {
		t.Fatalf("AlertRule 没有 Enabled 字段")
	}
	tag := field.Tag.Get("gorm")
	if strings.Contains(tag, "default") {
		t.Errorf("AlertRule.Enabled 的 gorm tag 不得含 default（当前 %q）—— "+
			"会给列建 DEFAULT true，使显式 false 被静默覆盖成启用", tag)
	}
}
