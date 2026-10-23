package datasource

// 投影器维护 data_sources.last_success 的测试（M3 补齐的缺口）。
//
// # 为什么需要它
//
// M3 删除 datasource.Service.MarkSuccess 后，data_sources.last_success 无人写入，
// 但前端来源总览读的正是这一列 ⇒ 页面"最后成功时间"永远为空。
// 修法：投影器从 source_group_members.last_success 取事实并回填。
//
// 本测试钉住三件事：
//  1. 有组 + 成员成功过 ⇒ 台账 last_success 被填；
//  2. **状态未变时也要更新**（最常见的路径，曾漏掉会导致时间越来越旧）；
//  3. 无组（直连）时留空（诚实：没有成员健康概念）。

import (
	"context"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/source"
	"ehome/backend/internal/sourceref"
)

func lastSuccessDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DataSource{}, &models.EdgeDevice{},
		&models.SensorFieldObs{}, &models.SourceGroup{}, &models.SourceGroupMember{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestReconcile_BackfillsLastSuccessFromMember 验证从组成员表回填 last_success。
func TestReconcile_BackfillsLastSuccessFromMember(t *testing.T) {
	db := lastSuccessDB(t)
	svc := projSvc(db)

	// 一台设备 + 一个启用组 + 该设备是成员，成员成功过。
	dev := models.EdgeDevice{ID: 1, Name: "d1", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	g := models.SourceGroup{ID: 7, Name: "grp", Kind: "primary_backup",
		Category: "rsoc", Enabled: true}
	if err := db.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	succeededAt := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	m := models.SourceGroupMember{GroupID: 7, EdgeDeviceID: 1, Priority: 10,
		Status: models.SourceMemberStatusActive, LastSuccess: &succeededAt}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}

	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev},
		Groups: []*sourceref.GroupSnapshot{{
			ID: 7, Kind: "primary_backup", Category: "rsoc", Enabled: true,
			ActiveMemberID: m.ID,
			Members:        []sourceref.MemberSnapshot{{MemberID: m.ID, EdgeDeviceID: 1}},
		}},
	})
	if _, err := svc.ReconcileDerived(context.Background(), handles); err != nil {
		t.Fatal(err)
	}

	var row models.DataSource
	if err := db.Where("device_id = ? AND category = ? AND edge_device_id = ?", 10, "rsoc", 1).
		First(&row).Error; err != nil {
		t.Fatalf("derived row not found: %v", err)
	}
	if row.LastSuccess == nil {
		t.Fatal("last_success 未被回填 —— 前端「最后成功时间」会永远为空")
	}
	if !row.LastSuccess.Equal(succeededAt) {
		t.Errorf("last_success = %v, want %v", *row.LastSuccess, succeededAt)
	}
}

// TestReconcile_UpdatesLastSuccessWhenStatusUnchanged 验证**状态未变路径**也更新。
//
// 这是最容易漏的地方：投影器原实现只在 status 变化时写，
// 而"设备一直正常"恰恰是 status 不变的常态 ⇒ last_success 会停在第一次，
// 页面上显示越来越旧的时间，看起来像掉线。
func TestReconcile_UpdatesLastSuccessWhenStatusUnchanged(t *testing.T) {
	db := lastSuccessDB(t)
	svc := projSvc(db)

	dev := models.EdgeDevice{ID: 1, Name: "d1", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SourceGroup{ID: 7, Name: "grp", Kind: "primary_backup",
		Category: "rsoc", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	first := time.Date(2026, 10, 23, 5, 0, 0, 0, time.UTC)
	m := models.SourceGroupMember{GroupID: 7, EdgeDeviceID: 1, Priority: 10,
		Status: models.SourceMemberStatusActive, LastSuccess: &first}
	if err := db.Create(&m).Error; err != nil {
		t.Fatal(err)
	}
	snap := &sourceref.GroupSnapshot{
		ID: 7, Kind: "primary_backup", Category: "rsoc", Enabled: true,
		ActiveMemberID: m.ID,
		Members:        []sourceref.MemberSnapshot{{MemberID: m.ID, EdgeDeviceID: 1}},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev},
		Groups:  []*sourceref.GroupSnapshot{snap},
	})
	if _, err := svc.ReconcileDerived(context.Background(), handles); err != nil {
		t.Fatal(err)
	}

	// 第二次：成员又成功了一次（status 不变，仍是 active）。
	second := first.Add(10 * time.Minute)
	if err := db.Model(&models.SourceGroupMember{}).Where("id = ?", m.ID).
		Update("last_success", second).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ReconcileDerived(context.Background(), handles); err != nil {
		t.Fatal(err)
	}

	var row models.DataSource
	if err := db.Where("device_id = ? AND category = ?", 10, "rsoc").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastSuccess == nil {
		t.Fatal("last_success 为空")
	}
	if !row.LastSuccess.Equal(second) {
		t.Errorf("last_success 停在 %v，未随第二次成功更新（want %v）—— 状态未变路径漏了写入",
			*row.LastSuccess, second)
	}
}

// TestReconcile_DirectSourceLastSuccessStaysEmpty 验证直连来源留空（诚实语义）。
func TestReconcile_DirectSourceLastSuccessStaysEmpty(t *testing.T) {
	db := lastSuccessDB(t)
	svc := projSvc(db)

	dev := models.EdgeDevice{ID: 1, Name: "d1", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "rsoc"}).Error; err != nil {
		t.Fatal(err)
	}

	in, err := svc.DeriveInputFrom(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handles := source.Derive(in)
	if _, err := svc.ReconcileDerived(context.Background(), handles); err != nil {
		t.Fatal(err)
	}

	var row models.DataSource
	if err := db.Where("device_id = ? AND category = ?", 10, "rsoc").First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.LastSuccess != nil {
		t.Errorf("直连来源（无组）不应有 last_success，得到 %v —— 无成员健康概念时应留空",
			*row.LastSuccess)
	}
}
