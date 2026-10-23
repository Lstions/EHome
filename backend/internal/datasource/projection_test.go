package datasource

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

// 本文件是 data_sources 自动投影的契约测试。
//
// 最重要的一条在文件末尾：**自动派生绝不覆盖人工配置**。
// 若这条被破坏，"自动管理"就变成了"自动覆盖用户的决定"。

func projDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.DataSource{}, &models.EdgeDevice{},
		&models.SensorFieldObs{}, &models.DataSourceHealth{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func projSvc(db *gorm.DB) *Service {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	return New(db, Options{Now: func() time.Time { return now }})
}

func logical(id uint) *uint { return &id }

// ---------- 基本派生：空表变有数据 ----------

// TestReconcile_CreatesRowsFromDevices 是本次改造的核心断言：
// 有设备有观测 ⇒ data_sources 不再为空。
func TestReconcile_CreatesRowsFromDevices(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)

	dev := models.EdgeDevice{ID: 14, Name: "bms", Type: "jiabaida_bms",
		NodeID: "n1", Enabled: true, LogicalDeviceID: logical(14)}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	// 观测到 3 个字段
	for _, name := range []string{"rsoc", "current", "voltage"} {
		if err := db.Create(&models.SensorFieldObs{DeviceID: 14, SensorName: name}).Error; err != nil {
			t.Fatal(err)
		}
	}

	in, err := svc.DeriveInputFrom(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	handles := source.Derive(in)
	res, err := svc.ReconcileDerived(context.Background(), handles)
	if err != nil {
		t.Fatal(err)
	}

	if res.Created != 3 {
		t.Errorf("Created = %d, want 3（三个字段三条来源）", res.Created)
	}
	var count int64
	db.Model(&models.DataSource{}).Count(&count)
	if count != 3 {
		t.Fatalf("data_sources 行数 = %d, want 3（空表应被自动填充）", count)
	}
	// 全部标记为 auto
	var autos int64
	db.Model(&models.DataSource{}).Where("managed_by = ?", models.DataSourceManagedAuto).Count(&autos)
	if autos != 3 {
		t.Errorf("auto 行 = %d, want 3", autos)
	}
	// 直连语义 ⇒ active
	var actives int64
	db.Model(&models.DataSource{}).Where("status = ?", StatusActive).Count(&actives)
	if actives != 3 {
		t.Errorf("active 行 = %d, want 3", actives)
	}
}

// TestReconcile_Idempotent 验证可重复调用（第二次不产生重复行）。
func TestReconcile_Idempotent(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(1)})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "a"})

	in, _ := svc.DeriveInputFrom(context.Background())
	handles := source.Derive(in)

	for i := 0; i < 3; i++ {
		res, err := svc.ReconcileDerived(context.Background(), handles)
		if err != nil {
			t.Fatalf("第 %d 次收敛: %v", i, err)
		}
		if i > 0 && res.Created != 0 {
			t.Errorf("第 %d 次仍创建了 %d 行（不幂等）", i, res.Created)
		}
	}
	var count int64
	db.Model(&models.DataSource{}).Count(&count)
	if count != 1 {
		t.Errorf("行数 = %d, want 1（幂等必须不重复插入）", count)
	}
}

// TestReconcile_DisablesStaleRowsInsteadOfDeleting 验证字段消失时 disable 而非删除。
//
// 为什么必须不删：删掉会丢失血缘（这台设备曾经供过这个字段），
// 且用户可能只是设备临时离线 —— 删除后设备回来又要重建，历史中断。
func TestReconcile_DisablesStaleRowsInsteadOfDeleting(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(1)})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "a"})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "b"})

	in, _ := svc.DeriveInputFrom(context.Background())
	if _, err := svc.ReconcileDerived(context.Background(), source.Derive(in)); err != nil {
		t.Fatal(err)
	}

	// 字段 b 消失
	db.Where("sensor_name = ?", "b").Delete(&models.SensorFieldObs{})
	in2, _ := svc.DeriveInputFrom(context.Background())
	res, err := svc.ReconcileDerived(context.Background(), source.Derive(in2))
	if err != nil {
		t.Fatal(err)
	}
	if res.Disabled != 1 {
		t.Errorf("Disabled = %d, want 1", res.Disabled)
	}

	var total int64
	db.Model(&models.DataSource{}).Count(&total)
	if total != 2 {
		t.Errorf("行数 = %d, want 2（必须保留历史行，只 disable）", total)
	}
	var disabled int64
	db.Model(&models.DataSource{}).Where("status = ?", StatusDisabled).Count(&disabled)
	if disabled != 1 {
		t.Errorf("disabled 行 = %d, want 1", disabled)
	}
	// 确认是 b 被 disable 而不是删掉
	var bRow models.DataSource
	if err := db.Where("category = ?", "b").First(&bRow).Error; err != nil {
		t.Fatalf("b 行被删除了（应只 disable）: %v", err)
	}
	if bRow.Status != StatusDisabled {
		t.Errorf("b 状态 = %q, want disabled", bRow.Status)
	}
}

// ---------- 最重要：绝不覆盖人工配置 ----------

// TestReconcile_NeverOverwritesManual 是**本文件最重要的测试**。
//
// 语义：用户手工创建的 manual 行，自动派生必须完全不动它（不改状态、不覆盖名字）。
// 若这条失效，"自动管理"就变成"自动覆盖用户的决定" —— 这是最不可接受的失败模式。
func TestReconcile_NeverOverwritesManual(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "rsoc"})

	// 用户手工建了一条，状态 standby、名字自定义
	manual := models.DataSource{
		DeviceID: 10, Category: "rsoc", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "我手工起的名字",
		Status: StatusStandby, ManagedBy: models.DataSourceManagedManual,
	}
	if err := db.Create(&manual).Error; err != nil {
		t.Fatal(err)
	}

	in, _ := svc.DeriveInputFrom(context.Background())
	res, err := svc.ReconcileDerived(context.Background(), source.Derive(in))
	if err != nil {
		t.Fatal(err)
	}

	if res.SkippedManual != 1 {
		t.Errorf("SkippedManual = %d, want 1", res.SkippedManual)
	}
	if res.Created != 0 {
		t.Errorf("Created = %d, want 0（人工行已存在，不得重复创建）", res.Created)
	}

	var got models.DataSource
	if err := db.First(&got, manual.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Name != "我手工起的名字" {
		t.Errorf("名字被覆盖为 %q —— 自动派生不得改人工配置", got.Name)
	}
	if got.Status != StatusStandby {
		t.Errorf("状态被改为 %q（原 standby）—— 自动派生不得改人工状态", got.Status)
	}
	if got.ManagedBy != models.DataSourceManagedManual {
		t.Errorf("managed_by 被改为 %q", got.ManagedBy)
	}
}

// TestReconcile_ManualAndAutoCoexist 验证同一 key 下 manual 存在时不会额外插 auto 行。
func TestReconcile_ManualAndAutoCoexist(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "x"})

	db.Create(&models.DataSource{DeviceID: 10, Category: "x", EdgeDeviceID: 1,
		SourceType: "edge_device", Name: "manual", Status: StatusActive,
		ManagedBy: models.DataSourceManagedManual})

	in, _ := svc.DeriveInputFrom(context.Background())
	if _, err := svc.ReconcileDerived(context.Background(), source.Derive(in)); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&models.DataSource{}).Count(&count)
	if count != 1 {
		t.Errorf("行数 = %d, want 1（同 key 下不得 manual+auto 各一条）", count)
	}
}

// ---------- 主备状态反映 ----------

// TestReconcile_PrimaryBackupMarksActiveAndStandby 验证主备组的状态映射。
func TestReconcile_PrimaryBackupMarksActiveAndStandby(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d1", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)})
	db.Create(&models.EdgeDevice{ID: 2, Name: "d2", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(10)})

	g := &sourceref.GroupSnapshot{
		ID: 7, Kind: "primary_backup", Category: "v", Enabled: true,
		// ⚠ ActiveMemberID 是**成员行 ID**（MemberSnapshot.MemberID），不是设备 ID。
		// 成员行 702 对应设备 2 ⇒ 设备 2 是权威。
		ActiveMemberID: 702,
		Members: []sourceref.MemberSnapshot{
			{MemberID: 701, EdgeDeviceID: 1, Priority: 10},
			{MemberID: 702, EdgeDeviceID: 2, Priority: 5},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{
			{ID: 1, Name: "d1", Enabled: true, LogicalDeviceID: logical(10)},
			{ID: 2, Name: "d2", Enabled: true, LogicalDeviceID: logical(10)},
		},
		Groups: []*sourceref.GroupSnapshot{g},
	})
	if _, err := svc.ReconcileDerived(context.Background(), handles); err != nil {
		t.Fatal(err)
	}

	var active, standby int64
	db.Model(&models.DataSource{}).Where("status = ?", StatusActive).Count(&active)
	db.Model(&models.DataSource{}).Where("status = ?", StatusStandby).Count(&standby)
	if active != 1 {
		t.Errorf("active = %d, want 1（主备同组至多一个 active）", active)
	}
	if standby != 1 {
		t.Errorf("standby = %d, want 1", standby)
	}
	// active 必须是设备 2
	var row models.DataSource
	db.Where("status = ?", StatusActive).First(&row)
	if row.EdgeDeviceID != 2 {
		t.Errorf("active 是设备 %d, want 2（组快照指定 2 为权威）", row.EdgeDeviceID)
	}
}

// ---------- 边界 ----------

// TestReconcile_UnboundDeviceProducesNoRows 验证未绑逻辑设备的设备不产生行。
func TestReconcile_UnboundDeviceProducesNoRows(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n", Enabled: true})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "a"})

	in, _ := svc.DeriveInputFrom(context.Background())
	res, err := svc.ReconcileDerived(context.Background(), source.Derive(in))
	if err != nil {
		t.Fatal(err)
	}
	if res.Total != 0 || res.Created != 0 {
		t.Errorf("未绑逻辑设备的设备不应产生来源，Total=%d Created=%d", res.Total, res.Created)
	}
}

// TestReconcile_EmptyHandlesDisablesAll 验证期望态为空时把所有 auto 行 disable。
func TestReconcile_EmptyHandlesDisablesAll(t *testing.T) {
	db := projDB(t)
	svc := projSvc(db)
	db.Create(&models.EdgeDevice{ID: 1, Name: "d", Type: "t", NodeID: "n",
		Enabled: true, LogicalDeviceID: logical(1)})
	db.Create(&models.SensorFieldObs{DeviceID: 1, SensorName: "a"})

	in, _ := svc.DeriveInputFrom(context.Background())
	svc.ReconcileDerived(context.Background(), source.Derive(in))

	// 设备解绑逻辑身份 ⇒ 期望态空
	db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Update("logical_device_id", nil)
	in2, _ := svc.DeriveInputFrom(context.Background())
	res, err := svc.ReconcileDerived(context.Background(), source.Derive(in2))
	if err != nil {
		t.Fatal(err)
	}
	if res.Disabled != 1 {
		t.Errorf("Disabled = %d, want 1", res.Disabled)
	}
}
