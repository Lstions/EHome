package telemetry

// observed_frames_test.go — DeviceObservedField 的判据验收（2026-11-08 硬切换）。
//
// ============ 守什么 ============
//
// 停写之后 category_not_observed 的两条实现（api 与 datasource）都不能再读
// unified_data，否则「60s 内刚上报的新字段」会被判未上报 => 用户创建数据源/来源组
// 被 400 拒绝 => 他以为自己填错了字段名。
//
// 四条断言：
//  1. 帧里有的字段 => true（主路径）
//  2. 帧里没有的字段 => false（不能一律放行，否则校验失去意义）
//  3. 表不存在 => false（不 panic、不返回 true）
//  4. 超出回看窗口的帧 => false（窗口语义生效）
//
// 变异验证：把 DeviceObservedField 改成恒返回 true => 用例 2 红。

import (
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"ehome/backend/internal/models"
)

func openObservedTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.TelemetryFrame{}, &models.TelemetrySlot{}); err != nil {
		t.Fatalf("automigrate frame tables: %v", err)
	}
	return db
}

// seedObservedSlot 造一个「字段集合 = fields」的 slot，并让设备在 ts 时刻有一帧。
//
// ⚠ 每次调用分配**新的 slot_id**（用 signature 去重）：同一个用例里造两批
// 不同字段的帧时，不能复用 slot_id（主键冲突）。这也更贴近真实 ——
// 不同帧类型本来就对应不同 slot。
func seedObservedSlot(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time, fields ...string) {
	t.Helper()
	sig := "sig-" + strings.Join(fields, "|")
	slot := Slot{ID: 0, Fields: make([]SlotField, 0, len(fields))}
	for _, f := range fields {
		slot.Fields = append(slot.Fields, SlotField{Name: f, Min: -100, Step: 0.1, Bits: 16})
	}
	raw, err := slot.MarshalFields()
	if err != nil {
		t.Fatalf("marshal fields: %v", err)
	}
	var slotID int16
	if err := db.Raw("SELECT coalesce(max(slot_id), 0) + 1 FROM telemetry_slot").Scan(&slotID).Error; err != nil {
		t.Fatalf("next slot id: %v", err)
	}
	if err := db.Create(&models.TelemetrySlot{
		SlotID: slotID, Signature: sig, SpecVersion: 1,
		FieldCount: int16(len(fields)), FieldsJSON: raw,
	}).Error; err != nil {
		t.Fatalf("create slot: %v", err)
	}
	if err := db.Create(&models.TelemetryFrame{
		DeviceID: deviceID, TS: ts, Seq: 0,
		FrameKind: models.TelemetryFrameKindKey, SlotID: slotID, Payload: []byte{0x00},
	}).Error; err != nil {
		t.Fatalf("create frame: %v", err)
	}
}

// TestDeviceObservedField_FindsFieldInFrames 主路径：帧里有的字段判 true。
func TestDeviceObservedField_FindsFieldInFrames(t *testing.T) {
	db := openObservedTestDB(t)
	const dev = 9001
	seedObservedSlot(t, db, dev, time.Now().UTC().Add(-time.Hour), "voltage", "current")

	for _, f := range []string{"voltage", "current"} {
		if !DeviceObservedField(db, dev, f) {
			t.Errorf("字段 %q 在帧里出现过，必须判 true（否则停写后 V-5 校验会误拒）", f)
		}
	}
}

// TestDeviceObservedField_RejectsUnknownField 负向对照：帧里没有的字段必须判 false。
//
// **这条是本文件最重要的断言**：如果 DeviceObservedField 恒返回 true，
// 校验就失去了意义 —— 用户填任意字段名都能通过，错误被推迟到运行期。
func TestDeviceObservedField_RejectsUnknownField(t *testing.T) {
	db := openObservedTestDB(t)
	const dev = 9002
	// 故意用**前缀相同但不等**的名字：挡住 strings.Contains 式的错误实现。
	seedObservedSlot(t, db, dev, time.Now().UTC().Add(-time.Hour), "voltage", "current")

	for _, f := range []string{"volt", "voltage_raw", "nonexistent"} {
		if DeviceObservedField(db, dev, f) {
			t.Errorf("字段 %q 从未出现在帧里，必须判 false（挡不住错字段名 = 校验失效）", f)
		}
	}
}

// TestDeviceObservedField_MissingTablesIsFalse 表不存在时判 false，不 panic。
//
// 精简部署 / 迁移未跑时表可能不存在。这时**不能放行** ——
// 放行等于「校验没做」，而调用方（api/datasource）会把它当成「已上报」。
func TestDeviceObservedField_MissingTablesIsFalse(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// 刻意不建表。
	if DeviceObservedField(db, 9003, "voltage") {
		t.Error("帧表不存在时必须判 false（放行 = 校验没做）")
	}
}

// TestDeviceObservedField_RespectsLookbackWindow 窗口**两侧**都要钉住。
//
// 只钉「超出窗口 => false」是半个判据：一个恒返回 false 的实现也能过。
// 所以同时钉「窗口内边界附近 => true」—— 两条一起才证明窗口真的在起作用，
// 而不是碰巧某个方向成立。
//
// 边界取 1 天：离窗口边界（30 天）足够远，不会被「今天/UTC 偏移」这类
// 时间抖动影响，但明确落在窗口内。
func TestDeviceObservedField_RespectsLookbackWindow(t *testing.T) {
	db := openObservedTestDB(t)
	const dev = 9004

	// ① 窗口内（1 天前）：必须 true。
	withinWindow := time.Now().UTC().AddDate(0, 0, -1)
	seedObservedSlot(t, db, dev, withinWindow, "fresh_field")
	if !DeviceObservedField(db, dev, "fresh_field") {
		t.Error("帧在 1 天前（窗口内），必须判 true —— 否则新建来源组会被误拒")
	}

	// ② 窗口外（ObservedLookbackDays+1 天前）：必须 false。
	tooOld := time.Now().UTC().AddDate(0, 0, -(ObservedLookbackDays + 1))
	seedObservedSlot(t, db, dev, tooOld, "stale_field")
	if DeviceObservedField(db, dev, "stale_field") {
		t.Errorf("帧在 %d 天前（超出 %d 天回看窗），必须判 false", ObservedLookbackDays+1, ObservedLookbackDays)
	}
}

// TestObservedLookbackCoversProjectorLag 窗口必须**远大于** projector 的 flush 间隔。
//
// ============ 为什么这条断言存在而不是靠注释 ============
//
// 这个窗口的核心职责之一是「覆盖投影表还没 flush 的那 60 秒」。
// 如果有人为了省扫描量把它改成 1 分钟甚至几秒，**所有用例仍然会绿**
// （因为测试里造帧是瞬间完成的，不经过 projector）——
// 而生产上就会退化成「刚上报的字段被判未上报」。
//
// 所以这里用「窗口天数 × 24h ≫ 60s」做结构断言：不锁具体数值，只锁量级关系。
// 它不会因为「30 天改成 7 天」而红（那是合理的调整），
// 但会因为「改成 1 分钟」而红 —— 后者是语义错误，不是调参。
func TestObservedLookbackCoversProjectorLag(t *testing.T) {
	const projectorFlushInterval = 60 * time.Second // config.DefaultFieldResolverObserveInterval
	window := time.Duration(ObservedLookbackDays) * 24 * time.Hour
	const wantRatio = 100
	if window < wantRatio*projectorFlushInterval {
		t.Errorf("回看窗口 %v 不到 projector flush 间隔（%v）的 %d 倍；"+
			"窗口若接近 flush 间隔，刚上报的字段会被判未上报 => 创建被误拒",
			window, projectorFlushInterval, wantRatio)
	}
}

// TestObservedFrameTableNamesAligned 表名常量必须与 models 的 TableName 逐字一致。
//
// 写错了只会让 HasTable 返回 false => 判据恒 false => 校验永远拒绝。
// 症状是「配什么都提示未上报」，极容易被误判成业务逻辑问题。
func TestObservedFrameTableNamesAligned(t *testing.T) {
	if got, want := observedFrameTable, (models.TelemetryFrame{}).TableName(); got != want {
		t.Errorf("observedFrameTable = %q, want %q", got, want)
	}
	if got, want := observedSlotTable, (models.TelemetrySlot{}).TableName(); got != want {
		t.Errorf("observedSlotTable = %q, want %q", got, want)
	}
}
