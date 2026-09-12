package datasource

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

func testBase() time.Time {
	return time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
}

// newTestService 用 SQLite 内存库构造 Service，并注入收集通知的闭包。
func newTestService(t *testing.T, now *time.Time) (*Service, *gorm.DB, *[]models.Notification) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	svc := New(db, Options{
		Cooldown:     5 * time.Minute,
		MinResidency: 2 * time.Minute,
		Staleness:    5 * time.Minute,
		ScanInterval: time.Hour,
		Now:          func() time.Time { return *now },
	})
	notes := make([]models.Notification, 0)
	svc.SetNotifier(func(n models.Notification) { notes = append(notes, n) })
	return svc, db, &notes
}

func mustCreate(t *testing.T, svc *Service, in CreateInput) *models.DataSource {
	t.Helper()
	ds, err := svc.Create(in)
	if err != nil {
		t.Fatalf("Create(%+v): %v", in, err)
	}
	return ds
}

func reloadDS(t *testing.T, db *gorm.DB, id uint) models.DataSource {
	t.Helper()
	var ds models.DataSource
	if err := db.First(&ds, id).Error; err != nil {
		t.Fatalf("reload data source %d: %v", id, err)
	}
	return ds
}

func activeCount(t *testing.T, db *gorm.DB, deviceID uint, category string) int64 {
	t.Helper()
	return countRows(t, db, &models.DataSource{}, "device_id = ? AND category = ? AND status = ?", deviceID, category, StatusActive)
}

func countRows(t *testing.T, db *gorm.DB, model interface{}, query string, args ...interface{}) int64 {
	t.Helper()
	var n int64
	q := db.Model(model)
	if query != "" {
		q = q.Where(query, args...)
	}
	if err := q.Count(&n).Error; err != nil {
		t.Fatalf("count %T (%s): %v", model, query, err)
	}
	return n
}

func failoverLogs(t *testing.T, db *gorm.DB, deviceID uint, category string) []models.FailoverLog {
	t.Helper()
	var logs []models.FailoverLog
	if err := db.Where("device_id = ? AND category = ?", deviceID, category).Order("id ASC").Find(&logs).Error; err != nil {
		t.Fatalf("query failover logs: %v", err)
	}
	return logs
}

// R1: MarkSuccess 更新 last_success/fail_count，error→standby，不抢占 active。
func TestR1_MarkSuccessUpdatesWithoutStealingActive(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Name: "a"})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Name: "b"})
	if a.Status != StatusActive || b.Status != StatusStandby {
		t.Fatalf("initial statuses: a=%s b=%s", a.Status, b.Status)
	}
	if err := db.Model(&models.DataSource{}).Where("id = ?", b.ID).Update("status", StatusError).Error; err != nil {
		t.Fatalf("force b error: %v", err)
	}

	at := base.Add(time.Minute)
	svc.MarkSuccess(1, []string{"temperature"}, at)

	ra := reloadDS(t, db, a.ID)
	if ra.LastSuccess == nil || !ra.LastSuccess.Equal(at) {
		t.Fatalf("R1 last_success=%v want %v", ra.LastSuccess, at)
	}
	if ra.FailCount != 0 {
		t.Fatalf("R1 fail_count=%d want 0", ra.FailCount)
	}
	if ra.Status != StatusActive {
		t.Fatalf("R1 must not steal active, got %s", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusError {
		t.Fatalf("R1 must not touch other edge device, b=%s", rb.Status)
	}

	svc.MarkSuccess(2, []string{"temperature"}, at)
	rb := reloadDS(t, db, b.ID)
	if rb.Status != StatusStandby || rb.FailCount != 0 || rb.LastSuccess == nil {
		t.Fatalf("R1 error→standby failed: status=%s fail=%d last=%v", rb.Status, rb.FailCount, rb.LastSuccess)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("R1 active count=%d want 1", got)
	}

	svc.MarkSuccess(1, []string{"humidity"}, at.Add(time.Hour))
	ra = reloadDS(t, db, a.ID)
	if !ra.LastSuccess.Equal(at) {
		t.Fatalf("R1 category filter failed: last_success=%v", ra.LastSuccess)
	}
}

// R2: MarkFailure 对 active|standby 计次并写健康事件；disabled/error 跳过。
func TestR2_MarkFailureIncrementsAndWritesHealth(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 3})
	now = base.Add(time.Minute)
	svc.MarkFailure(1, TriggerDeviceOffline)

	ra := reloadDS(t, db, a.ID)
	if ra.FailCount != 1 {
		t.Fatalf("R2 fail_count=%d want 1", ra.FailCount)
	}
	if ra.Status != StatusActive {
		t.Fatalf("R2 status=%s want active", ra.Status)
	}
	if ra.LastFailure == nil || !ra.LastFailure.Equal(now) {
		t.Fatalf("R2 last_failure=%v want %v", ra.LastFailure, now)
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", a.ID, healthStatusFailure); got != 1 {
		t.Fatalf("R2 health failure rows=%d want 1", got)
	}

	d := mustCreate(t, svc, CreateInput{DeviceID: 2, Category: "humidity", EdgeDeviceID: 7})
	if err := db.Model(&models.DataSource{}).Where("id = ?", d.ID).Update("status", StatusDisabled).Error; err != nil {
		t.Fatal(err)
	}
	e := mustCreate(t, svc, CreateInput{DeviceID: 3, Category: "voltage", EdgeDeviceID: 8})
	if err := db.Model(&models.DataSource{}).Where("id = ?", e.ID).Update("status", StatusError).Error; err != nil {
		t.Fatal(err)
	}
	svc.MarkFailure(7, TriggerDeviceOffline)
	svc.MarkFailure(8, TriggerDeviceOffline)
	if rd := reloadDS(t, db, d.ID); rd.FailCount != 0 {
		t.Fatalf("R2 disabled source counted: %d", rd.FailCount)
	}
	if re := reloadDS(t, db, e.ID); re.FailCount != 0 {
		t.Fatalf("R2 error source counted: %d", re.FailCount)
	}
}

// R2: 非 active 的 standby 来源熔断不得抢占/复制 active 语义。
// 组内已存在 active 时属"无需切换"场景 (与 R3 仅对 active 变 error 触发切换一致)。
// 子用例 TwoSources 复现"两条来源"形态；子用例 WithStandbyCandidate 额外放一条
// standby 候选，专门守护"组内已有 active 时不得再提升候选"——删除 maybeFailover
// 的"组内已有 active"守卫会在此子用例下产生两条 active。
func TestR2_StandbyFailureDoesNotCreateSecondActive(t *testing.T) {
	base := testBase()
	now := base

	t.Run("TwoSources", func(t *testing.T) {
		now = base
		svc, db, _ := newTestService(t, &now)
		a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 10, MaxFailCount: 1})
		b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5, MaxFailCount: 1})
		if a.Status != StatusActive || b.Status != StatusStandby {
			t.Fatalf("seed: a=%s b=%s", a.Status, b.Status)
		}

		svc.MarkFailure(2, TriggerDeviceOffline)

		if rb := reloadDS(t, db, b.ID); rb.Status != StatusError {
			t.Fatalf("B status=%s want error", rb.Status)
		}
		if ra := reloadDS(t, db, a.ID); ra.Status != StatusActive {
			t.Fatalf("A status=%s want active", ra.Status)
		}
		if got := activeCount(t, db, 1, "temperature"); got != 1 {
			t.Fatalf("active count=%d want 1", got)
		}
		if logs := failoverLogs(t, db, 1, "temperature"); len(logs) != 0 {
			t.Fatalf("unexpected failover logs: %+v", logs)
		}
	})

	t.Run("WithStandbyCandidate", func(t *testing.T) {
		now = base
		svc, db, _ := newTestService(t, &now)
		a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 10, MaxFailCount: 1})
		b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5, MaxFailCount: 1})
		c := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 3, Priority: 1, MaxFailCount: 1})
		if a.Status != StatusActive || b.Status != StatusStandby || c.Status != StatusStandby {
			t.Fatalf("seed: a=%s b=%s c=%s", a.Status, b.Status, c.Status)
		}

		svc.MarkFailure(2, TriggerDeviceOffline)

		if rb := reloadDS(t, db, b.ID); rb.Status != StatusError {
			t.Fatalf("B status=%s want error", rb.Status)
		}
		if ra := reloadDS(t, db, a.ID); ra.Status != StatusActive {
			t.Fatalf("A status=%s want active (must not be replaced)", ra.Status)
		}
		if rc := reloadDS(t, db, c.ID); rc.Status != StatusStandby {
			t.Fatalf("C status=%s want standby (must not be promoted)", rc.Status)
		}
		if got := activeCount(t, db, 1, "temperature"); got != 1 {
			t.Fatalf("active count=%d want 1", got)
		}
		if logs := failoverLogs(t, db, 1, "temperature"); len(logs) != 0 {
			t.Fatalf("unexpected failover logs: %+v", logs)
		}
	})
}

// R3: active→error 时按 priority DESC, id ASC 选健康候选自动接替。
func TestR3_AutoFailoverPicksHighestPriority(t *testing.T) {
	base := testBase()
	now := base
	svc, db, notes := newTestService(t, &now)

	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5})
	c := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 3, Priority: 9})
	if a.Status != StatusActive || b.Status != StatusStandby || c.Status != StatusStandby {
		t.Fatalf("seed statuses: %s/%s/%s", a.Status, b.Status, c.Status)
	}

	now = base.Add(time.Minute)
	svc.MarkFailure(1, TriggerDeviceOffline)

	if ra := reloadDS(t, db, a.ID); ra.Status != StatusError {
		t.Fatalf("R3 failing active=%s want error", ra.Status)
	}
	if rc := reloadDS(t, db, c.ID); rc.Status != StatusActive {
		t.Fatalf("R3 highest priority candidate=%s want active", rc.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusStandby {
		t.Fatalf("R3 lower priority=%s want standby", rb.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("R3 active count=%d want 1", got)
	}

	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 1 {
		t.Fatalf("R3 failover logs=%d want 1", len(logs))
	}
	if logs[0].Reason != ReasonAuto || logs[0].Trigger != TriggerDeviceOffline || logs[0].FromSourceID != a.ID || logs[0].ToSourceID != c.ID {
		t.Fatalf("R3 log mismatch: %+v", logs[0])
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", c.ID, healthStatusTransition); got != 1 {
		t.Fatalf("R3 transition health=%d want 1", got)
	}

	if len(*notes) != 1 {
		t.Fatalf("R3 notifications=%d want 1", len(*notes))
	}
	n := (*notes)[0]
	if n.Type != notifyTypeWarning || n.Source != notifySource || n.SourceID != fmt.Sprintf("%d", c.ID) {
		t.Fatalf("R3 notification fields wrong: %+v", n)
	}
	for _, want := range []string{"temperature", fmt.Sprintf("来源 %d", a.ID), fmt.Sprintf("来源 %d", c.ID), ReasonAuto, TriggerDeviceOffline} {
		if !strings.Contains(n.Message, want) {
			t.Fatalf("R3 notification %q missing %q", n.Message, want)
		}
	}
}

// R4: 无健康候选时组降级，状态不变且通知被调用。
func TestR4_DegradedWhenNoCandidate(t *testing.T) {
	base := testBase()
	now := base
	svc, db, notes := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})

	now = base.Add(time.Minute)
	svc.MarkFailure(1, TriggerDeviceOffline)

	if ra := reloadDS(t, db, a.ID); ra.Status != StatusError {
		t.Fatalf("R4 status=%s want error", ra.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 0 {
		t.Fatalf("R4 active count=%d want 0", got)
	}
	if got := countRows(t, db, &models.FailoverLog{}, ""); got != 0 {
		t.Fatalf("R4 failover logs=%d want 0", got)
	}
	if got := countRows(t, db, &models.DataSource{}, "status = ?", StatusActive); got != 0 {
		t.Fatalf("R4 unexpected active rows=%d", got)
	}
	if len(*notes) != 1 {
		t.Fatalf("R4 notifications=%d want 1", len(*notes))
	}
	n := (*notes)[0]
	if n.Type != notifyTypeWarning || !strings.Contains(n.Message, "无可用备用来源") || !strings.Contains(n.Message, "(degraded)") {
		t.Fatalf("R4 degraded notification wrong: %+v", n)
	}
}

// R5: 停用 active 且有候选 → 候选接替，原来源 disabled，reason=manual_deactivate。
func TestR5_DeactivateActivePromotesCandidate(t *testing.T) {
	base := testBase()
	now := base
	svc, db, notes := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 7})

	if _, err := svc.Deactivate(a.ID); err != nil {
		t.Fatalf("R5 deactivate: %v", err)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusDisabled {
		t.Fatalf("R5 original=%s want disabled", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("R5 candidate=%s want active", rb.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("R5 active count=%d want 1", got)
	}
	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 1 || logs[0].Reason != ReasonManualDeactivate || logs[0].FromSourceID != a.ID || logs[0].ToSourceID != b.ID {
		t.Fatalf("R5 log mismatch: %+v", logs)
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", b.ID, healthStatusTransition); got != 1 {
		t.Fatalf("R5 transition health=%d want 1", got)
	}
	if len(*notes) != 1 || (*notes)[0].Type != notifyTypeInfo || !strings.Contains((*notes)[0].Message, ReasonManualDeactivate) {
		t.Fatalf("R5 notification wrong: %+v", *notes)
	}
}

// R6: 停用 active 且无候选 → ErrConflict，状态不变。
func TestR6_DeactivateActiveWithoutCandidateConflicts(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})

	if _, err := svc.Deactivate(a.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("R6 err=%v want ErrConflict", err)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusActive {
		t.Fatalf("R6 status=%s want active", ra.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("R6 active count=%d want 1", got)
	}
}

// R7: activate 手动切换，原 active→standby，写 reason=manual，幂等。
func TestR7_ActivateManualSwitch(t *testing.T) {
	base := testBase()
	now := base
	svc, db, notes := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5})

	got, err := svc.Activate(b.ID)
	if err != nil {
		t.Fatalf("R7 activate: %v", err)
	}
	if got.ID != b.ID || got.Status != StatusActive {
		t.Fatalf("R7 returned %+v", got)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusStandby {
		t.Fatalf("R7 previous active=%s want standby", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("R7 target=%s want active", rb.Status)
	}
	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 1 || logs[0].Reason != ReasonManual || logs[0].FromSourceID != a.ID || logs[0].ToSourceID != b.ID {
		t.Fatalf("R7 log mismatch: %+v", logs)
	}
	if len(*notes) != 1 || (*notes)[0].Type != notifyTypeInfo || !strings.Contains((*notes)[0].Message, ReasonManual) {
		t.Fatalf("R7 notification wrong: %+v", *notes)
	}

	// 幂等：已 active 再 activate 不产生新日志。
	if _, err := svc.Activate(b.ID); err != nil {
		t.Fatalf("R7 idempotent activate: %v", err)
	}
	if logs2 := failoverLogs(t, db, 1, "temperature"); len(logs2) != 1 {
		t.Fatalf("R7 idempotent produced extra logs: %d", len(logs2))
	}
}

// R7 (v1.1): disabled 是人工可逆状态，activate 即重新启用；写 failover_logs(reason=manual)
// 与 health(transition)。
func TestR7_ActivateDisabledReenablesSource(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2})

	// B 为 standby，停用走 R8 → disabled，不写切换日志/健康事件。
	if _, err := svc.Deactivate(b.ID); err != nil {
		t.Fatalf("deactivate standby: %v", err)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusDisabled {
		t.Fatalf("b=%s want disabled", rb.Status)
	}
	if got := countRows(t, db, &models.FailoverLog{}, ""); got != 0 {
		t.Fatalf("deactivate standby wrote %d logs want 0", got)
	}

	now = base.Add(time.Minute)
	got, err := svc.Activate(b.ID)
	if err != nil {
		t.Fatalf("activate disabled: %v want nil", err)
	}
	if got.ID != b.ID || got.Status != StatusActive {
		t.Fatalf("activate disabled returned %+v", got)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("b=%s want active", rb.Status)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusStandby {
		t.Fatalf("a=%s want standby", ra.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("active count=%d want 1", got)
	}
	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 1 || logs[0].Reason != ReasonManual || logs[0].FromSourceID != a.ID || logs[0].ToSourceID != b.ID {
		t.Fatalf("log mismatch: %+v", logs)
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", b.ID, healthStatusTransition); got < 1 {
		t.Fatalf("activate transition health rows for b=%d want >=1", got)
	}
}

// R7 (v1.1): 组内无 active 时，disabled 来源可被 activate 提升为权威；
// 覆盖 Deactivate(R5) 与 Activate 两条切换路径的 health(transition) 留痕。
func TestR7_ActivateDisabledWhenNoActive(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2})

	// Deactivate A（active 且有候选 B）→ B 接替、A disabled。
	if _, err := svc.Deactivate(a.ID); err != nil {
		t.Fatalf("deactivate a: %v", err)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusDisabled {
		t.Fatalf("a=%s want disabled", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("b=%s want active", rb.Status)
	}

	// Deactivate B（active 且无候选）→ R6 409，状态不变。
	if _, err := svc.Deactivate(b.ID); !errors.Is(err, ErrConflict) {
		t.Fatalf("deactivate b err=%v want ErrConflict", err)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("409 must not mutate b, got %s", rb.Status)
	}

	// Activate A（disabled → active），B → standby。
	now = base.Add(time.Minute)
	got, err := svc.Activate(a.ID)
	if err != nil {
		t.Fatalf("activate disabled when no active: %v want nil", err)
	}
	if got.ID != a.ID || got.Status != StatusActive {
		t.Fatalf("activate returned %+v", got)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusActive {
		t.Fatalf("a=%s want active", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusStandby {
		t.Fatalf("b=%s want standby", rb.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("active count=%d want 1", got)
	}
	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 2 {
		t.Fatalf("failover logs=%d want 2", len(logs))
	}
	if logs[0].Reason != ReasonManualDeactivate || logs[0].FromSourceID != a.ID || logs[0].ToSourceID != b.ID {
		t.Fatalf("first log mismatch: %+v", logs[0])
	}
	if logs[1].Reason != ReasonManual || logs[1].FromSourceID != b.ID || logs[1].ToSourceID != a.ID {
		t.Fatalf("second log mismatch: %+v", logs[1])
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", b.ID, healthStatusTransition); got < 1 {
		t.Fatalf("R5 deactivate transition health rows for b=%d want >=1", got)
	}
	if got := countRows(t, db, &models.DataSourceHealth{}, "source_id = ? AND status = ?", a.ID, healthStatusTransition); got < 1 {
		t.Fatalf("activate transition health rows for a=%d want >=1", got)
	}
}

// R8: 停用非 active 直接 disabled，不影响 active。
func TestR8_DeactivateNonActiveSetsDisabled(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2})

	if _, err := svc.Deactivate(b.ID); err != nil {
		t.Fatalf("R8 deactivate: %v", err)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusDisabled {
		t.Fatalf("R8 status=%s want disabled", rb.Status)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusActive {
		t.Fatalf("R8 active changed to %s", ra.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 1 {
		t.Fatalf("R8 active count=%d want 1", got)
	}
}

// R9: reset 仅对 error；恢复 standby/fail_count=0，保留 last_failure；组内无 active 则提升。
func TestR9_ResetError(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2})
	lastFail := base.Add(-time.Hour)
	if err := db.Model(&models.DataSource{}).Where("id = ?", b.ID).
		Updates(map[string]interface{}{"status": StatusError, "fail_count": 5, "last_failure": lastFail}).Error; err != nil {
		t.Fatal(err)
	}

	got, err := svc.Reset(b.ID)
	if err != nil {
		t.Fatalf("R9 reset: %v", err)
	}
	if got.Status != StatusStandby || got.FailCount != 0 {
		t.Fatalf("R9 status=%s fail=%d want standby/0", got.Status, got.FailCount)
	}
	if got.LastFailure == nil || !got.LastFailure.Equal(lastFail) {
		t.Fatalf("R9 last_failure not preserved: %v", got.LastFailure)
	}
	if activeCount(t, db, 1, "temperature") != 1 {
		t.Fatalf("R9 should not change active while one exists")
	}

	// 组内无 active → 提升为 active。
	c := mustCreate(t, svc, CreateInput{DeviceID: 2, Category: "humidity", EdgeDeviceID: 1})
	if err := db.Model(&models.DataSource{}).Where("id = ?", c.ID).Update("status", StatusError).Error; err != nil {
		t.Fatal(err)
	}
	got2, err := svc.Reset(c.ID)
	if err != nil {
		t.Fatalf("R9 reset promote: %v", err)
	}
	if got2.Status != StatusActive {
		t.Fatalf("R9 promote got %s want active", got2.Status)
	}

	// 非 error → ErrInvalidRequest。
	if _, err := svc.Reset(a.ID); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("R9 non-error reset err=%v want ErrInvalidRequest", err)
	}
}

// R10: 冷却期内自动切换被抑制，仅写健康事件与通知。
func TestR10_CooldownSuppressesAutoFailover(t *testing.T) {
	base := testBase()
	now := base
	svc, db, notes := newTestService(t, &now)
	mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5})
	c := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 3, Priority: 9, MaxFailCount: 1})

	svc.MarkFailure(1, TriggerDeviceOffline)
	if rc := reloadDS(t, db, c.ID); rc.Status != StatusActive {
		t.Fatalf("R10 setup failover failed: c=%s", rc.Status)
	}
	*notes = (*notes)[:0]

	now = base.Add(time.Minute) // < Cooldown 5m
	svc.MarkFailure(3, TriggerStaleData)

	if rc := reloadDS(t, db, c.ID); rc.Status != StatusError {
		t.Fatalf("R10 c=%s want error", rc.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusStandby {
		t.Fatalf("R10 b=%s want standby", rb.Status)
	}
	if got := activeCount(t, db, 1, "temperature"); got != 0 {
		t.Fatalf("R10 active=%d want 0 (suppressed)", got)
	}
	if got := countRows(t, db, &models.FailoverLog{}, ""); got != 1 {
		t.Fatalf("R10 logs=%d want 1", got)
	}

	var hs []models.DataSourceHealth
	if err := db.Where("source_id = ? AND status = ?", c.ID, healthStatusTransition).Find(&hs).Error; err != nil {
		t.Fatal(err)
	}
	found := false
	for _, h := range hs {
		if strings.Contains(h.Message, "cooldown") {
			found = true
		}
	}
	if !found {
		t.Fatalf("R10 missing cooldown health event: %+v", hs)
	}
	if len(*notes) != 1 || !strings.Contains((*notes)[0].Message, "cooldown") {
		t.Fatalf("R10 notification wrong: %+v", *notes)
	}
}

// R11: 最小驻留期内不做停滞计次。
func TestR11_MinResidencySkipsStaleAccumulation(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})

	// 数据早已停滞，但来源刚刚成为 active（UpdatedAt=base）。
	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).UpdateColumn("last_success", base.Add(-time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).UpdateColumn("updated_at", base).Error; err != nil {
		t.Fatal(err)
	}

	switched, err := svc.ScanStale(base)
	if err != nil {
		t.Fatalf("R11 ScanStale: %v", err)
	}
	if switched != 0 {
		t.Fatalf("R11 switched=%d want 0", switched)
	}
	ra := reloadDS(t, db, a.ID)
	if ra.FailCount != 0 || ra.Status != StatusActive {
		t.Fatalf("R11 fail_count=%d status=%s", ra.FailCount, ra.Status)
	}
}

// ScanStale: 超过 staleness 且超过最小驻留 → 计次并自动切换。
func TestScanStaleTriggersStaleFailover(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})
	b := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 5})

	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).UpdateColumn("updated_at", base.Add(-10*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).UpdateColumn("last_success", base.Add(-10*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	switched, err := svc.ScanStale(base)
	if err != nil {
		t.Fatalf("ScanStale: %v", err)
	}
	if switched != 1 {
		t.Fatalf("switched=%d want 1", switched)
	}
	if ra := reloadDS(t, db, a.ID); ra.Status != StatusError {
		t.Fatalf("stale source=%s want error", ra.Status)
	}
	if rb := reloadDS(t, db, b.ID); rb.Status != StatusActive {
		t.Fatalf("standby=%s want active", rb.Status)
	}
	logs := failoverLogs(t, db, 1, "temperature")
	if len(logs) != 1 || logs[0].Trigger != TriggerStaleData || logs[0].Reason != ReasonAuto {
		t.Fatalf("stale failover log mismatch: %+v", logs)
	}
}

// ScanStale: LastSuccess 为 nil 视为超期。
func TestScanStaleNilLastSuccessIsStale(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	a := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 1})
	if err := db.Model(&models.DataSource{}).Where("id = ?", a.ID).UpdateColumn("updated_at", base.Add(-10*time.Minute)).Error; err != nil {
		t.Fatal(err)
	}

	switched, err := svc.ScanStale(base)
	if err != nil {
		t.Fatalf("ScanStale: %v", err)
	}
	if switched != 0 {
		t.Fatalf("switched=%d want 0 (no candidate)", switched)
	}
	ra := reloadDS(t, db, a.ID)
	if ra.FailCount != 1 || ra.Status != StatusError {
		t.Fatalf("nil LastSuccess not treated stale: fail=%d status=%s", ra.FailCount, ra.Status)
	}
}

// 校验错误与哨兵错误。
func TestValidationErrors(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	t.Run("required_fields", func(t *testing.T) {
		for _, in := range []CreateInput{
			{Category: "temperature", EdgeDeviceID: 1},
			{DeviceID: 1, EdgeDeviceID: 1},
			{DeviceID: 1, Category: "temperature"},
		} {
			if _, err := svc.Create(in); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Create(%+v) err=%v want ErrInvalidRequest", in, err)
			}
		}
	})
	t.Run("source_type", func(t *testing.T) {
		if _, err := svc.Create(CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, SourceType: "mqtt"}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err=%v want ErrInvalidRequest", err)
		}
	})
	t.Run("max_fail_count", func(t *testing.T) {
		if _, err := svc.Create(CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 99}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err=%v want ErrInvalidRequest", err)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		ds, err := svc.Create(CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 4})
		if err != nil {
			t.Fatal(err)
		}
		if ds.MaxFailCount != defaultMaxFailCount {
			t.Fatalf("max_fail_count=%d want %d", ds.MaxFailCount, defaultMaxFailCount)
		}
		if ds.Name != "edge-device-4@temperature" {
			t.Fatalf("name=%s", ds.Name)
		}
		if ds.SourceType != SourceTypeEdgeDevice {
			t.Fatalf("source_type=%s", ds.SourceType)
		}
	})
	t.Run("duplicate", func(t *testing.T) {
		_, _ = svc.Create(CreateInput{DeviceID: 2, Category: "humidity", EdgeDeviceID: 9})
		if _, err := svc.Create(CreateInput{DeviceID: 2, Category: "humidity", EdgeDeviceID: 9}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err=%v want ErrConflict", err)
		}
	})
	t.Run("not_found", func(t *testing.T) {
		if _, err := svc.Get(9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get err=%v", err)
		}
		if _, err := svc.Activate(9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Activate err=%v", err)
		}
		if _, err := svc.Deactivate(9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Deactivate err=%v", err)
		}
		if _, err := svc.Reset(9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Reset err=%v", err)
		}
	})
	t.Run("activate_disabled_reenables", func(t *testing.T) {
		a := mustCreate(t, svc, CreateInput{DeviceID: 3, Category: "voltage", EdgeDeviceID: 1})
		b := mustCreate(t, svc, CreateInput{DeviceID: 3, Category: "voltage", EdgeDeviceID: 2})
		if _, err := svc.Deactivate(b.ID); err != nil {
			t.Fatal(err)
		}
		if rb := reloadDS(t, db, b.ID); rb.Status != StatusDisabled {
			t.Fatalf("setup b=%s want disabled", rb.Status)
		}
		got, err := svc.Activate(b.ID)
		if err != nil {
			t.Fatalf("activate disabled err=%v want nil", err)
		}
		if got.ID != b.ID || got.Status != StatusActive {
			t.Fatalf("activate disabled returned %+v", got)
		}
		if ra := reloadDS(t, db, a.ID); ra.Status != StatusStandby {
			t.Fatalf("previous active=%s want standby", ra.Status)
		}
	})
}

// List / Health / FailoverLogs 分页与排序。
func TestListHealthAndFailoverLogs(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	for i, prio := range []int{1, 5, 3} {
		mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: uint(i + 1), Priority: prio})
	}
	mustCreate(t, svc, CreateInput{DeviceID: 2, Category: "humidity", EdgeDeviceID: 1})

	items, total, err := svc.List(ListFilter{Page: 0, PageSize: 0})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if total != 4 || len(items) != 4 {
		t.Fatalf("List total=%d len=%d want 4/4", total, len(items))
	}
	if items[0].Priority != 5 || items[1].Priority != 3 || items[2].Priority != 1 {
		t.Fatalf("List ordering wrong: %+v", items)
	}
	items, total, err = svc.List(ListFilter{DeviceID: 2, Page: 1, PageSize: 100})
	if err != nil || total != 1 || len(items) != 1 {
		t.Fatalf("List filter total=%d len=%d err=%v", total, len(items), err)
	}

	for i := 0; i < 3; i++ {
		if err := db.Create(&models.DataSourceHealth{SourceID: 1, DeviceID: 1, Category: "temperature", Status: healthStatusFailure, Message: "x"}).Error; err != nil {
			t.Fatal(err)
		}
	}
	hs, err := svc.Health(1, 0)
	if err != nil || len(hs) != 3 {
		t.Fatalf("Health len=%d err=%v", len(hs), err)
	}
	if hs[0].ID <= hs[1].ID {
		t.Fatalf("Health not id DESC: %d,%d", hs[0].ID, hs[1].ID)
	}
	if hs, err = svc.Health(1, 999); err != nil || len(hs) != 3 {
		t.Fatalf("Health clamp len=%d err=%v", len(hs), err)
	}

	for i := 0; i < 3; i++ {
		if err := db.Create(&models.FailoverLog{DeviceID: 1, Category: "temperature", Reason: ReasonAuto, Trigger: TriggerDeviceOffline}).Error; err != nil {
			t.Fatal(err)
		}
	}
	fs, err := svc.FailoverLogs(1, "temperature", 0)
	if err != nil || len(fs) != 3 {
		t.Fatalf("FailoverLogs len=%d err=%v", len(fs), err)
	}
	if fs[0].ID <= fs[1].ID {
		t.Fatalf("FailoverLogs not id DESC: %d,%d", fs[0].ID, fs[1].ID)
	}
	if fs, err = svc.FailoverLogs(1, "humidity", 0); err != nil || len(fs) != 0 {
		t.Fatalf("FailoverLogs category filter len=%d err=%v", len(fs), err)
	}
}

// 组不变量属性测试：随机 200 次操作后每组至多一条 active。
// 预置"同组多来源、edge_device_id 互不相同"的形态 (每组 3 条)，使 MarkFailure
// 能命中组内 standby 来源；每次 MarkFailure 前推进 now 越过 cooldown，避免冷却
// 掩盖"组内已有 active 却仍提升候选"的缺陷 (删除该守卫会在此用例下产生两条 active)。
func TestGroupInvariantRandomOperations(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)
	rng := rand.New(rand.NewSource(20260912))
	cats := []string{"temperature", "humidity", "voltage"}
	devices := []uint{1, 2, 3}

	// 预置多来源组：每组 3 条来源，edge_device_id 各不相同，优先级降序；
	// MaxFailCount=1 让一次 MarkFailure 即可熔断，暴露"只对 active 触发切换"的语义。
	seededGroups := []struct {
		device uint
		cat    string
	}{
		{1, "temperature"}, {1, "humidity"}, {2, "temperature"},
		{2, "voltage"}, {3, "humidity"}, {3, "temperature"},
	}
	var nextEdge uint = 1
	for _, g := range seededGroups {
		for k := 0; k < 3; k++ {
			mustCreate(t, svc, CreateInput{
				DeviceID:     g.device,
				Category:     g.cat,
				EdgeDeviceID: nextEdge,
				Priority:     10 - k,
				MaxFailCount: 1,
			})
			nextEdge++
		}
	}

	for i := 0; i < 200; i++ {
		switch rng.Intn(6) {
		case 0:
			// Create 的 ErrConflict 属预期。
			_, _ = svc.Create(CreateInput{
				DeviceID:     devices[rng.Intn(len(devices))],
				Category:     cats[rng.Intn(len(cats))],
				EdgeDeviceID: nextEdge + uint(rng.Intn(20)),
				MaxFailCount: 1 + rng.Intn(3),
			})
		case 1:
			if id := randomSourceID(t, db, rng); id != 0 {
				if _, err := svc.Activate(id); err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
					t.Fatalf("op%d Activate: %v", i, err)
				}
			}
		case 2:
			if id := randomSourceID(t, db, rng); id != 0 {
				if _, err := svc.Deactivate(id); err != nil && !errors.Is(err, ErrConflict) && !errors.Is(err, ErrNotFound) {
					t.Fatalf("op%d Deactivate: %v", i, err)
				}
			}
		case 3:
			if id := randomSourceID(t, db, rng); id != 0 {
				if _, err := svc.Reset(id); err != nil && !errors.Is(err, ErrInvalidRequest) && !errors.Is(err, ErrNotFound) {
					t.Fatalf("op%d Reset: %v", i, err)
				}
			}
		case 4:
			// 随机命中一条真实存在来源的 edge_device_id，可能落在组内 standby 上。
			now = now.Add(6 * time.Minute) // 越过 cooldown，使自动切换路径可达
			if edge := randomEdgeDeviceID(t, db, rng); edge != 0 {
				svc.MarkFailure(edge, TriggerDeviceOffline)
			}
		case 5:
			now = now.Add(time.Second)
			if edge := randomEdgeDeviceID(t, db, rng); edge != 0 {
				svc.MarkSuccess(edge, []string{cats[rng.Intn(len(cats))]}, now)
			}
		}
		assertGroupInvariant(t, db)
	}
	assertGroupInvariant(t, db)
}

func randomSourceID(t *testing.T, db *gorm.DB, rng *rand.Rand) uint {
	t.Helper()
	var ids []uint
	if err := db.Model(&models.DataSource{}).Pluck("id", &ids).Error; err != nil {
		t.Fatalf("pluck ids: %v", err)
	}
	if len(ids) == 0 {
		return 0
	}
	return ids[rng.Intn(len(ids))]
}

// randomEdgeDeviceID 从库中已存在的来源里取一个 edge_device_id，使 MarkFailure /
// MarkSuccess 能真正命中组内某条来源（含 standby）。
func randomEdgeDeviceID(t *testing.T, db *gorm.DB, rng *rand.Rand) uint {
	t.Helper()
	var ids []uint
	if err := db.Model(&models.DataSource{}).Distinct("edge_device_id").Pluck("edge_device_id", &ids).Error; err != nil {
		t.Fatalf("pluck edge_device_ids: %v", err)
	}
	if len(ids) == 0 {
		return 0
	}
	return ids[rng.Intn(len(ids))]
}

func assertGroupInvariant(t *testing.T, db *gorm.DB) {
	t.Helper()
	type groupRow struct {
		DeviceID uint
		Category string
		C        int64
	}
	var rows []groupRow
	if err := db.Model(&models.DataSource{}).
		Select("device_id, category, COUNT(*) AS c").
		Where("status = ?", StatusActive).
		Group("device_id, category").
		Having("COUNT(*) > 1").
		Find(&rows).Error; err != nil {
		t.Fatalf("invariant query: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("group invariant violated: %+v", rows)
	}
}

// Start 在 ctx 取消后退出。
func TestStartExitsOnContextCancel(t *testing.T) {
	now := testBase()
	svc, _, _ := newTestService(t, &now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		svc.Start(ctx)
		close(done)
	}()
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Start did not exit on context cancel")
	}
}
