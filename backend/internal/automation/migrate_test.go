package automation

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// D7 启动迁移：只为缺时区的 time_window 回填，且幂等（用户已选的时区绝不覆盖）。
func TestMigrateTimeWindowTimezoneBackfillsOnlyEmpty(t *testing.T) {
	db := testutil.OpenTestDB(t)
	zone := "Asia/Shanghai"
	emptyTZ := &models.AutomationRule{
		Name: "窗口-无时区", Enabled: true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
	}
	keptTZ := &models.AutomationRule{
		Name: "窗口-已选时区", Enabled: true, Timezone: &zone,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: "08:00", TriggerWindowEnd: "18:00",
		TriggerWindowEdge: models.AutomationWindowEnter,
	}
	sunRow := &models.AutomationRule{
		Name: "日出-无时区(历史脏行)", Enabled: true,
		TriggerType: models.AutomationTriggerSunEvent,
	}
	for _, r := range []*models.AutomationRule{emptyTZ, keptTZ, sunRow} {
		if err := db.Create(r).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := MigrateTimeWindowTimezone(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var got []models.AutomationRule
	if err := db.Find(&got).Error; err != nil {
		t.Fatal(err)
	}
	byID := make(map[uint]models.AutomationRule, len(got))
	for _, r := range got {
		byID[r.ID] = r
	}
	if r := byID[emptyTZ.ID]; r.Timezone == nil || *r.Timezone == "" {
		t.Fatalf("缺时区的 time_window 必须被回填, got %v", r.Timezone)
	}
	if r := byID[keptTZ.ID]; r.Timezone == nil || *r.Timezone != zone {
		t.Fatalf("已有合法时区不得被覆盖, got %v", r.Timezone)
	}
	if r := byID[sunRow.ID]; r.Timezone != nil {
		t.Fatalf("sun_event 行不在本迁移范围, got %v", *r.Timezone)
	}

	// 幂等：再跑一次不得改动任何行（否则每次启动都会 bump updated_at → 清边沿态）。
	before := byID[emptyTZ.ID].UpdatedAt
	if err := MigrateTimeWindowTimezone(db); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var again models.AutomationRule
	if err := db.First(&again, emptyTZ.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !again.UpdatedAt.Equal(before) {
		t.Fatalf("二次迁移不得再写行（updated_at %v -> %v）", before, again.UpdatedAt)
	}
}
