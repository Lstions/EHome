package datasource

// 本文件测试 datasource.Service 的**台账能力**：CRUD、手动运维（Activate/
// Deactivate/Reset/Delete）、查询（List/Health/FailoverLogs）、校验错误。
//
// # 已下线的 legacy 主备状态机（M3，2026-10-23）
//
// 本文件历史上还包含 R1–R11 的**自动切换**规格测试（MarkSuccess/MarkFailure/
// ScanStale/Start）。这些方法与测试已随 legacy 状态机一起删除 —— 主备语义现在
// 只由 GroupService 承担（用户要求「只有来源组支持主备」）。
//
// # 覆盖迁移登记（删除 ≠ 丢覆盖）
//
// 每一条被删测试断言的规格，都有 GroupService 侧**更严格**的测试覆盖
// （更严格 = 除状态外还断言事件契约 / revision bump / 决策原因）：
//
// | 已删的 legacy 测试                              | Group 侧承接的测试                                        |
// |------------------------------------------------|----------------------------------------------------------|
// | TestR1_MarkSuccessUpdatesWithoutStealingActive  | TestGroupMarkSuccessDoesNotBumpRevision,                  |
// |                                                 | TestVerifyEvents_R1R2_NoEvent                             |
// | TestR2_MarkFailureIncrementsAndWritesHealth     | TestGroupMarkFailureFailover                              |
// | TestR2_StandbyFailureDoesNotCreateSecondActive  | TestGroupMarkFailureFailover（单 active 不变量）           |
// | TestR3_AutoFailoverPicksHighestPriority         | TestGroupMarkFailureFailover,                             |
// |                                                 | TestVerifyEvents_R3_FailoverExactlyOne                    |
// | TestR4_DegradedWhenNoCandidate                  | TestGroupMarkFailureDegradedEmitsNextZero,                |
// |                                                 | TestVerifyEvents_R4_DegradedNextZero                      |
// | TestR10_CooldownSuppressesAutoFailover          | TestGroupCooldownSuppressedEmitsNextZeroAndHealth,        |
// |                                                 | TestVerifyEvents_R10_CooldownSuppresses                   |
// | TestR11_MinResidencySkipsStaleAccumulation      | TestVerifyEvents_R11_MinResidency                         |
// | TestScanStaleTriggersStaleFailover              | TestGroupScanSelfHealsDegradedGroup                       |
// | TestScanStaleNilLastSuccessIsStale              | TestGroupScanSelfHealsDegradedGroup（同样覆盖零值分支）     |
// | TestStartExitsOnContextCancel                   | 无对应 —— 该测试只验"goroutine 能随 ctx 退出"，             |
// |                                                 | 属生命周期而非规格；GroupService.Start 有同样的循环结构     |
//
// 迁移时逐条核对过断言语义（不是凭测试名对应），例如：
//   · legacy R3 断言「失败者 error / 最高优先候选 active / 低优先 standby /
//     active 唯一 / 切换日志 1 条 / 过渡健康 1 条 / 通知 1 条」；
//     Group 侧 TestGroupMarkFailureFailover 断言了同样的状态迁移，**并额外**断言
//     ActiveChange 事件的 Prev/Next 成员、reason、trigger、revision +1。
//   · legacy R11 断言「驻留期内不计失败」；TestVerifyEvents_R11_MinResidency 断言同一事。

import (
	"errors"
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

// TestDelete_ActiveWithStandbyCandidate_PromotesWithoutDoubleActive 是 CI run 39871785449
// (Backend simulation gates) 暴露缺陷的回归钉。
//
// 缺陷形态：Delete() 处理「删除 active、组内还有 standby 候选」时，
// 改前是**先 Save(candidate) 升 active、再在函数末尾 Delete(ds)** ——
// 同一事务内出现两条 active，撞上 data_sources 的部分唯一索引
// ux_ds_one_active (device_id,category) WHERE status='active'
// ⇒ PG/SQLite 语句级检查直接报 duplicate key，整个删除事务回滚。
//
// 为什么原有 TestDataSourceDelete_ActivePromotesCandidate 没抓到：
// 它走 API + seedDataSource，删的 active 是**组内第一条**，
// 而本用例直接打 Service 层，覆盖「active + standby 候选共存」这条真实路径。
// （真实触发场景：仿真 SIM-DS-002/005/006 的 t.Cleanup 删主来源时组内仍有备来源。）
func TestDelete_ActiveWithStandbyCandidate_PromotesWithoutDoubleActive(t *testing.T) {
	now := time.Now().UTC()
	svc, db, notes := newTestService(t, &now)

	active := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 1, Priority: 5})
	standby := mustCreate(t, svc, CreateInput{DeviceID: 1, Category: "temperature", EdgeDeviceID: 2, Priority: 1})
	if active.Status != StatusActive || standby.Status != StatusStandby {
		t.Fatalf("前置状态不符：active=%q standby=%q", active.Status, standby.Status)
	}

	if err := svc.Delete(active.ID); err != nil {
		t.Fatalf("删除 active（组内还有 standby 候选）失败：%v", err)
	}

	var rows []models.DataSource
	db.Where("device_id = ? AND category = ?", 1, "temperature").Find(&rows)
	if len(rows) != 1 {
		t.Fatalf("期望只剩备来源 1 条，实际 %d 条：%+v", len(rows), rows)
	}
	if rows[0].ID != standby.ID {
		t.Fatalf("剩余的应是备来源 %d，实际 %d", standby.ID, rows[0].ID)
	}
	// 候选必须被接替成 active（删除权威来源不能导致该组失去权威）
	if rows[0].Status != StatusActive {
		t.Fatalf("备来源接替后状态 = %q，期望 active", rows[0].Status)
	}
	// 且必须留下切换审计（谁接替谁、原因）
	if len(*notes) == 0 {
		t.Fatal("删除权威来源后未产生「数据源切换」通知")
	}
}
