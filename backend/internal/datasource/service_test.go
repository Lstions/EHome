package datasource

// 本文件测试 datasource.Service 的**台账能力**：Create/Update/Get 的校验与默认值、
// 组键口径、以及只读查询（List/Health/FailoverLogs）。
//
// 2026-10-24（遗留处置方案 A）：手动运维四个方法（Activate/Deactivate/Reset/Delete）
// 及其测试**已删除** —— 组键二元化后它们的「候选接替」分支物理不可达，在生产上
// 表现为恒 409 / 恒 400 / 删了被投影器重建，测试 PASS 只会伪装功能正常。
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
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

func testBase() time.Time {
	return time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
}

// newTestService 用 SQLite 内存库构造 Service。
//
// 返回值里的 notes 仅为**兼容既有调用点的签名**而保留：Service 自 2026-10-24 起
// 不再是通知生产者（SetNotifier / 四个手工运维方法已删，通知链迁到 GroupService），
// 因此它恒为空切片。保留而不是改签名，是为了让未被本次处置触及的用例零改动。
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

// activeCount 按**组键** (edge_device_id, category) 统计 active 行。
//
// 2026-10-23: 逻辑设备层移除后组键不再含逻辑 id（.contract/logical-device-removal.md §6）。
func activeCount(t *testing.T, db *gorm.DB, edgeDeviceID uint, category string) int64 {
	t.Helper()
	return countRows(t, db, &models.DataSource{}, "edge_device_id = ? AND category = ? AND status = ?", edgeDeviceID, category, StatusActive)
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

// ── 组键口径与只读查询 ──
//
// # 已执行的处置（2026-10-24，方案 A）
//
// 组键从 (逻辑设备, 类别) 变成 **(edge_device_id, 类别)**，唯一索引 uniq_ds_source
// 正好建在这两列上 ⇒ **一个组恒有且只有一行**。由此产生两个后果，均已落地处理：
//
//  1. 【删除】Service 的 Delete / Activate / Deactivate / Reset 四个手工运维方法
//     及其通知链（recordHealth / writeFailoverLog / newNotification / emitAll /
//     emit / SetNotifier / SetDispatchNotifier）**已全部删除**。
//     理由：这些方法的所有「候选接替」分支在本组键下物理不可达（同组第二行插不进去），
//     在生产上表现为「恒 409 / 恒 400 / 删了被投影器以新 id 重建」，是死代码 + 陷阱。
//     相关测试（旧 TestR6/R7/R8/R9/TestDelete_ActiveWithoutCandidate*）随之删除 ——
//     **它们断言的是生产不可达的分支，PASS 只会伪装功能正常**。
//  2. 【保留】真正可用的手工干预能力全在 **GroupService**（source_groups）：
//     ActivateGroupMember / DeactivateGroupMember / 组 CRUD / 健康与切换日志，
//     且已真接入 UI（SourceGroupList.vue 调 activateGroup）。
//
// 本文件现在只测**仍然存在**的能力：组键口径（同键第二行必被拒绝，这是唯一索引
// 不被悄悄放宽的守卫）、Create/Update/Get 的校验与默认值、以及只读查询。

// TestCreate_GroupKeyIsEdgeDeviceAndCategory 钉住组键口径本身。
//
// 三件事：
//  1. 同 (edge_device, category) 第二次创建 ⇒ ErrConflict（组恒一行，前提成立）；
//  2. 不同 edge_device + 同 category ⇒ **两条独立来源**，各自 active
//     （它们不是同一个组，因此不适用「每组至多一条 active」）；
//  3. 同 edge_device + 不同 category ⇒ 两条独立来源。
func TestCreate_GroupKeyIsEdgeDeviceAndCategory(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	first := mustCreate(t, svc, CreateInput{Category: "temperature", EdgeDeviceID: 1})
	if first.Status != StatusActive {
		t.Fatalf("新键首行 status=%s want active", first.Status)
	}
	if _, err := svc.Create(CreateInput{Category: "temperature", EdgeDeviceID: 1}); !errors.Is(err, ErrConflict) {
		t.Fatalf("同 (edge_device_id, category) 第二次创建 err=%v want ErrConflict", err)
	}

	otherDevice := mustCreate(t, svc, CreateInput{Category: "temperature", EdgeDeviceID: 2})
	otherCategory := mustCreate(t, svc, CreateInput{Category: "humidity", EdgeDeviceID: 1})
	if otherDevice.ID == first.ID || otherCategory.ID == first.ID {
		t.Fatal("不同 edge_device 或不同 category 必须产生独立来源")
	}
	var total int64
	if err := db.Model(&models.DataSource{}).Count(&total).Error; err != nil {
		t.Fatal(err)
	}
	if total != 3 {
		t.Fatalf("行数 = %d, want 3", total)
	}
	// 三条都是各自组的首行 ⇒ 全部 active；每键至多一条 active 的不变量依然成立。
	if got := countRows(t, db, &models.DataSource{}, "status = ?", StatusActive); got != 3 {
		t.Fatalf("active 行 = %d, want 3", got)
	}
	assertGroupInvariant(t, db)
}

// 校验错误与哨兵错误。
func TestValidationErrors(t *testing.T) {
	base := testBase()
	now := base
	svc, _, _ := newTestService(t, &now)

	t.Run("required_fields", func(t *testing.T) {
		// 组键 = (edge_device_id, category)：两列都必填。
		// 历史：这里曾有第三个用例 {DeviceID: 1, Category: ...}（缺 edge_device_id）。
		// 逻辑设备层移除后 device_id 列已不存在，必填项只剩这两列；
		// 留一个"两列都缺"的用例补上前 DeviceID 用例的覆盖位。
		for _, in := range []CreateInput{
			{EdgeDeviceID: 1},         // 缺 category
			{Category: "temperature"}, // 缺 edge_device_id
			{},                        // 两者都缺
		} {
			if _, err := svc.Create(in); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("Create(%+v) err=%v want ErrInvalidRequest", in, err)
			}
		}
	})
	t.Run("source_type", func(t *testing.T) {
		if _, err := svc.Create(CreateInput{Category: "temperature", EdgeDeviceID: 1, SourceType: "mqtt"}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err=%v want ErrInvalidRequest", err)
		}
	})
	t.Run("max_fail_count", func(t *testing.T) {
		if _, err := svc.Create(CreateInput{Category: "temperature", EdgeDeviceID: 1, MaxFailCount: 99}); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("err=%v want ErrInvalidRequest", err)
		}
	})
	t.Run("defaults", func(t *testing.T) {
		ds, err := svc.Create(CreateInput{Category: "temperature", EdgeDeviceID: 4})
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
		_, _ = svc.Create(CreateInput{Category: "humidity", EdgeDeviceID: 9})
		if _, err := svc.Create(CreateInput{Category: "humidity", EdgeDeviceID: 9}); !errors.Is(err, ErrConflict) {
			t.Fatalf("err=%v want ErrConflict", err)
		}
	})
	t.Run("not_found", func(t *testing.T) {
		// Activate/Deactivate/Reset 已于 2026-10-24 删除（方案 A），只剩 Get。
		if _, err := svc.Get(9999); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Get err=%v", err)
		}
		if _, err := svc.Update(9999, UpdateInput{}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Update err=%v", err)
		}
	})
}

// List / Health / FailoverLogs 分页与排序。
func TestListHealthAndFailoverLogs(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	for i, prio := range []int{1, 5, 3} {
		mustCreate(t, svc, CreateInput{Category: "temperature", EdgeDeviceID: uint(i + 1), Priority: prio})
	}
	mustCreate(t, svc, CreateInput{Category: "humidity", EdgeDeviceID: 1})

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
	items, total, err = svc.List(ListFilter{EdgeDeviceID: 2, Page: 1, PageSize: 100})
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
		EdgeDeviceID uint
		Category     string
		C            int64
	}
	var rows []groupRow
	if err := db.Model(&models.DataSource{}).
		Select("edge_device_id, category, COUNT(*) AS c").
		Where("status = ?", StatusActive).
		Group("edge_device_id, category").
		Having("COUNT(*) > 1").
		Find(&rows).Error; err != nil {
		t.Fatalf("invariant query: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("group invariant violated: %+v", rows)
	}
}

// TestIsPrimaryIsDerivedFromStatus 钉住 is_primary 不变量：**恒等于 (status == active)**。
//
// 为什么需要这条门禁：is_primary 曾是「人工声明的主来源」标记，但主备状态机 M3 下线后
// 零个读者（所有「谁在供数」判定看 status），而状态迁移只 Update("status") 不同步它
// ⇒ 2026-10-24 审核实测可以出现 active + is_primary=false 的漂移（POST 创建的来源
// 缺省 is_primary=false 但 status=active）。
//
// 现在它由 status 派生，不接收入参。本用例锁两件事：
//  1. Create 时忽略入参 IsPrimary，按 status 派生；
//  2. Update 也不能把 active 行改成 is_primary=false（否则漂移会从 API 面重新进来）。
func TestIsPrimaryIsDerivedFromStatus(t *testing.T) {
	base := testBase()
	now := base
	svc, db, _ := newTestService(t, &now)

	// 1) Create 忽略入参。
	//
	// 组键 = (edge_device_id, category) + 唯一索引 ⇒ 同组只能有一行，
	// 「组内第二条是 standby」这个形态**构造不出来**。能反证派生的只有：
	// 首条必为 active，而传 IsPrimary=false —— 若实现照抄入参就会得到 false。
	first := mustCreate(t, svc, CreateInput{
		Category: "humidity", EdgeDeviceID: 1,
		IsPrimary: false,
	})
	if first.Status != StatusActive {
		t.Fatalf("前置：首条应为 active，实际 %s", first.Status)
	}
	if !first.IsPrimary {
		t.Fatalf("Create: active 行 is_primary 必须为 true（入参 false 应被忽略），实际 false")
	}

	// 2) Update 不能破坏派生。
	if _, err := svc.Update(first.ID, UpdateInput{IsPrimary: boolPtr(false)}); err != nil {
		t.Fatalf("Update: %v", err)
	}
	var got models.DataSource
	if err := db.First(&got, first.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusActive || !got.IsPrimary {
		t.Fatalf("Update 后 status=%s is_primary=%v，不变量要求 active ⇒ is_primary=true",
			got.Status, got.IsPrimary)
	}

	// 组内唯一一行不变式顺带校验（唯一索引没被放宽）。
	assertGroupInvariant(t, db)
}

func boolPtr(b bool) *bool { return &b }
