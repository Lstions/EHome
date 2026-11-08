package api

// handler_node_latest_cache_test.go — /nodes/:id/latest 缓存路径的验收（task-24 步骤 1）。
//
// 两条互补的证明（Lead 2026-11-07 要求）：
//
//  1. **单元级 + SQL 计数器**：热缓存路径下，打到 unified_data 的 SQL 条数必须 == 0。
//     端到端「删掉 unified_data 仍能返回」也有鉴别力，但如果因为别的原因（比如缓存
//     刚好失效又恰好回落成功）也能通过，它就说明不了问题。计数器是直接证据。
//  2. **冷缓存必须回落**：任一设备冷 => ok=false，调用方走原 SQL。
//     不能返回"只有热设备"的部分结果 —— 那会让某台设备的数据凭空消失。

import (
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// countUnifiedHits 统计打到 unified_data 的 SQL 条数（注册 GORM Query 回调）。
func countUnifiedHits(t *testing.T, db *gorm.DB) *int64 {
	t.Helper()
	var hits int64
	name := "test:count_unified_" + t.Name()
	err := db.Callback().Query().After("gorm:query").Register(name, func(tx *gorm.DB) {
		if tx.Statement != nil && strings.Contains(tx.Statement.SQL.String(), "unified_data") {
			atomic.AddInt64(&hits, 1)
		}
	})
	if err != nil {
		t.Fatalf("register callback: %v", err)
	}
	return &hits
}

// seedNodeWithDevices 造 1 个节点 + 1 个通道 + n 台设备，返回 channelID。
func seedNodeWithDevices(t *testing.T, db *gorm.DB, nodeID string, n int) (uint, []uint) {
	t.Helper()
	db.Exec("DELETE FROM edge_devices")
	db.Exec("DELETE FROM channels WHERE node_id = ?", nodeID)
	ch := models.Channel{NodeID: nodeID, HardwareType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	ids := make([]uint, 0, n)
	for i := 0; i < n; i++ {
		dev := models.EdgeDevice{
			Type: "test", Name: "dev-" + nodeID, NodeID: nodeID,
			ChannelID: ch.ID, Enabled: true,
		}
		if err := db.Create(&dev).Error; err != nil {
			t.Fatalf("create device: %v", err)
		}
		ids = append(ids, dev.ID)
	}
	return ch.ID, ids
}

// TestNodeLatestFromCache_DoesNotTouchUnifiedData 是核心断言：热缓存下零 SQL 命中。
func TestNodeLatestFromCache_DoesNotTouchUnifiedData(t *testing.T) {
	db := testutil.OpenTestDB(t)
	resetLatestValueCacheForTest()
	defer resetLatestValueCacheForTest()

	const nodeID = "node-cache-1"
	chID, devIDs := seedNodeWithDevices(t, db, nodeID, 2)

	// 灌缓存：每设备 3 个字段
	for _, id := range devIDs {
		for _, name := range []string{"current", "rsoc", "total_voltage"} {
			SetLatestValue(models.UnifiedData{
				DeviceID: id, SensorName: name, Value: 42.5, Unit: "A",
				Timestamp: time.Now().UTC(),
			})
		}
	}

	hits := countUnifiedHits(t, db)
	values, ok := nodeLatestValuesFromCache(db, []uint{chID})
	if !ok {
		t.Fatal("热缓存下应返回 ok=true")
	}
	if got := atomic.LoadInt64(hits); got != 0 {
		t.Fatalf("热缓存路径打了 %d 条 unified_data SQL —— 必须为 0（步骤 1 的目的就是不再依赖它）", got)
	}
	if want := len(devIDs) * 3; len(values) != want {
		t.Fatalf("values = %d 项, want %d", len(values), want)
	}
}

// TestNodeLatestFromCache_ColdDeviceFallsBack 守冷缓存回落。
func TestNodeLatestFromCache_ColdDeviceFallsBack(t *testing.T) {
	db := testutil.OpenTestDB(t)
	resetLatestValueCacheForTest()
	defer resetLatestValueCacheForTest()

	const nodeID = "node-cache-2"
	chID, devIDs := seedNodeWithDevices(t, db, nodeID, 2)

	// 只灌第一台设备 => 第二台冷 => 必须整节点回落。
	for _, name := range []string{"current", "rsoc"} {
		SetLatestValue(models.UnifiedData{
			DeviceID: devIDs[0], SensorName: name, Value: 1, Unit: "A", Timestamp: time.Now().UTC(),
		})
	}

	if _, ok := nodeLatestValuesFromCache(db, []uint{chID}); ok {
		t.Fatal("有一台设备冷缓存却返回 ok=true —— " +
			"只返回热设备会让另一台设备的数据凭空消失，必须整节点回落原 SQL")
	}
}

// TestNodeLatestFromCache_ExcludesSoftDeletedDevices 钉住**刻意的行为变更**。
//
// 原 SQL 是裸 SQL，不经过软删 scope => 软删设备仍出现；本实现排除软删，
// 与 overview / 设备列表 一致。演练清单里有对应的人工核对项。
func TestNodeLatestFromCache_ExcludesSoftDeletedDevices(t *testing.T) {
	db := testutil.OpenTestDB(t)
	resetLatestValueCacheForTest()
	defer resetLatestValueCacheForTest()

	const nodeID = "node-cache-3"
	chID, devIDs := seedNodeWithDevices(t, db, nodeID, 2)
	for _, id := range devIDs {
		SetLatestValue(models.UnifiedData{
			DeviceID: id, SensorName: "current", Value: 7, Unit: "A", Timestamp: time.Now().UTC(),
		})
	}
	// 软删第二台
	if err := db.Delete(&models.EdgeDevice{}, devIDs[1]).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	values, ok := nodeLatestValuesFromCache(db, []uint{chID})
	if !ok {
		t.Fatal("应返回 ok=true")
	}
	for _, v := range values {
		if v.Value == 7 && len(values) == 1 {
			continue
		}
	}
	if len(values) != 1 {
		t.Fatalf("values = %d 项, want 1（软删设备应被排除）", len(values))
	}
}
