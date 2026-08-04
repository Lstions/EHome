package datalifecycle

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestPreviewMerge_TimeRangesAndOverlap(t *testing.T) {
	db := testutil.OpenTestDB(t)
	// src1: base..base+2h (3 行, 每小时), src2: base+1h..base+3h → 重叠。
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 3, base)
	src2 := seedMergeSource(t, db, "src2", 3, base.Add(time.Hour))

	SetSystemRetentionDays(123)
	defer SetSystemRetentionDays(365)

	preview, err := PreviewMerge(context.Background(), db, []uint{src1.ID, src2.ID})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if preview.TargetRetentionDays != 123 {
		t.Errorf("target_retention_days = %d, want snapshot 123", preview.TargetRetentionDays)
	}
	if len(preview.Sources) != 2 {
		t.Fatalf("sources = %d, want 2", len(preview.Sources))
	}
	for _, s := range preview.Sources {
		if s.FirstDataAt == nil || s.LastDataAt == nil {
			t.Errorf("source %d missing time range", s.ID)
			continue
		}
		if !s.OverlapWithOthers {
			t.Errorf("source %d overlap_with_others = false, want true", s.ID)
		}
		if s.RowEstimate == nil || *s.RowEstimate == 0 {
			t.Errorf("source %d row_estimate missing/zero", s.ID)
		}
	}
	// src1 首行 = base; src2 首行 = base+1h。
	if !preview.Sources[0].FirstDataAt.Equal(base) {
		t.Errorf("src1 first = %v, want %v", preview.Sources[0].FirstDataAt, base)
	}
	if !preview.Sources[1].FirstDataAt.Equal(base.Add(time.Hour)) {
		t.Errorf("src2 first = %v, want %v", preview.Sources[1].FirstDataAt, base.Add(time.Hour))
	}
}

func TestPreviewMerge_DisjointNoOverlap(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 2, base)                   // 7/1 00-01h
	src2 := seedMergeSource(t, db, "src2", 2, base.AddDate(0, 0, 10)) // 7/11 起

	preview, err := PreviewMerge(context.Background(), db, []uint{src1.ID, src2.ID})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	for _, s := range preview.Sources {
		if s.OverlapWithOthers {
			t.Errorf("source %d overlap = true, want false (disjoint ranges)", s.ID)
		}
	}
}

func TestPreviewMerge_RejectsSingleSource(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 1, base)
	if _, err := PreviewMerge(context.Background(), db, []uint{src1.ID}); err == nil {
		t.Error("single-source preview should fail")
	}
	if _, err := PreviewMerge(context.Background(), db, []uint{src1.ID, 999}); err == nil {
		t.Error("nonexistent source should fail")
	}
}

// TestPreviewMerge_PendingScopeRows — 预览的时间范围/估算必须覆盖
// 源的完整 scope (含 NULL-logical 旧行兜底)。
func TestPreviewMerge_PendingScopeRows(t *testing.T) {
	db := testutil.OpenTestDB(t)
	base := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	src1 := seedMergeSource(t, db, "src1", 2, base)
	src2 := seedMergeSource(t, db, "src2", 1, base)
	// NULL-logical 更早行 (pre-backfill): 预览首行必须被实例兜底捕获。
	var dev models.EdgeDevice
	db.Unscoped().Where("logical_device_id = ?", src1.ID).First(&dev)
	db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "voltage", Value: 0,
		Timestamp: base.AddDate(0, 0, -30)})

	preview, err := PreviewMerge(context.Background(), db, []uint{src1.ID, src2.ID})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	if !preview.Sources[0].FirstDataAt.Equal(base.AddDate(0, 0, -30)) {
		t.Errorf("src1 first = %v, want NULL-row timestamp via instance fallback",
			preview.Sources[0].FirstDataAt)
	}
}
