package api

// handler_automation_location_test.go — 坐标来源元数据（2026-11-14）。
//
// 用户裁决：规则坐标只有两个来源（ip / city），时区随位置自动带出。后端不负责
// 解析"IP/城市 → 坐标"（那是 UI 调 /system/geo/* 完成的），但要**落库来源与展示名**，
// 并拒绝非法来源 —— 否则审计上无法回答"这条规则的坐标是谁给的"。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

func TestCreateSunRulePersistsLocationMeta(t *testing.T) {
	r, db := newSunRouter(t)
	edgeID := seedSunNode(t, db, "loc-meta-1", nil, nil, nil)

	body := sunRuleBody(edgeID, map[string]interface{}{
		"location_source": "city",
		"location_label":  "上海（中国）",
	})
	code, resp := postSunRule(t, r, body)
	if code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", code, resp)
	}
	var rule models.AutomationRule
	if err := db.Order("id DESC").First(&rule).Error; err != nil {
		t.Fatal(err)
	}
	if rule.LocationSource != "city" || rule.LocationLabel != "上海（中国）" {
		t.Fatalf("落库来源 = %q/%q", rule.LocationSource, rule.LocationLabel)
	}
	// 响应里也要带（编辑页据此回显当前来源）。
	if !strings.Contains(resp, `"location_source":"city"`) || !strings.Contains(resp, "上海（中国）") {
		t.Fatalf("响应缺少来源字段: %s", resp)
	}
}

func TestCreateSunRuleRejectsInvalidLocationSource(t *testing.T) {
	r, db := newSunRouter(t)
	edgeID := seedSunNode(t, db, "loc-meta-2", nil, nil, nil)

	body := sunRuleBody(edgeID, map[string]interface{}{"location_source": "gps"})
	code, resp := postSunRule(t, r, body)
	if code != http.StatusBadRequest {
		t.Fatalf("非法来源未被拒: %d %s", code, resp)
	}
	if !strings.Contains(resp, "location_source") {
		t.Fatalf("错误文案没说清字段: %s", resp)
	}
	var n int64
	db.Model(&models.AutomationRule{}).Count(&n)
	if n != 0 {
		t.Fatalf("被拒的请求不得落库，实际 %d 行", n)
	}
}

func TestUpdateSunRuleLocationLabel(t *testing.T) {
	r, db := newSunRouter(t)
	edgeID := seedSunNode(t, db, "loc-meta-3", nil, nil, nil)
	body := sunRuleBody(edgeID, map[string]interface{}{
		"location_source": "ip", "location_label": "旧标签",
	})
	if code, resp := postSunRule(t, r, body); code != http.StatusOK && code != http.StatusCreated {
		t.Fatalf("创建失败: %d %s", code, resp)
	}
	var rule models.AutomationRule
	if err := db.Order("id DESC").First(&rule).Error; err != nil {
		t.Fatal(err)
	}

	payload, _ := json.Marshal(map[string]interface{}{"location_label": "新标签", "location_source": "ip"})
	req := httptest.NewRequest(http.MethodPut,
		"/api/v1/automation-rules/"+strconv.FormatUint(uint64(rule.ID), 10), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("更新失败: %d %s", rec.Code, rec.Body.String())
	}
	var got models.AutomationRule
	if err := db.First(&got, rule.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.LocationLabel != "新标签" || got.LocationSource != "ip" {
		t.Fatalf("更新后 = %q/%q", got.LocationSource, got.LocationLabel)
	}
}
