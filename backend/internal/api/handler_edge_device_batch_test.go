package api

// handler_edge_device_batch_test.go — 批量管理（启用/停用/采集间隔）+ 历史设备
// 「彻底删除」（批量入清理队列）的测试。
//
// 产品设计：docs/分析/边缘设备批量管理与历史设备-2026-10-25.md
// 不变量：
//  1. 批量接口逐条执行、逐条返回结果（一条失败不影响其它）；
//  2. 被来源组规则引用的设备「停用」必须失败并给出原因（与单台守卫同一判定）；
//  3. 已删除/不存在的 id 不得被误改，也不得让整批失败；
//  4. 历史设备彻底删除 = 只接受已软删设备 + 置 data_purge_requested + 幂等；
//  5. deleted 列表 search 支持名称/型号模糊（大小写不敏感）。

import (
	"net/http"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

func batchData(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("response has no data object: %v", env)
	}
	return data
}

func batchResults(t *testing.T, data map[string]any) []map[string]any {
	t.Helper()
	raw, ok := data["results"].([]any)
	if !ok {
		t.Fatalf("response has no results array: %v", data)
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok {
			t.Fatalf("result item is not object: %v", item)
		}
		out = append(out, m)
	}
	return out
}

func resultByID(results []map[string]any, id uint) map[string]any {
	for _, r := range results {
		if got, ok := r["id"].(float64); ok && uint(got) == id {
			return r
		}
	}
	return nil
}

// TestEdgeDevice_BatchUpdate_EnableDisableInterval — 启用/停用/间隔都落库；
// 请求校验（无字段、越界 interval、超量 ids）返回 400。
func TestEdgeDevice_BatchUpdate_EnableDisableInterval(t *testing.T) {
	r, db, _ := newSourceGroupGuardAPI(t)
	d1 := models.EdgeDevice{Name: "d1", NodeID: "node-1", ChannelID: 1, Enabled: true, IntervalMs: 5000}
	d2 := models.EdgeDevice{Name: "d2", NodeID: "node-1", ChannelID: 1, Enabled: true, IntervalMs: 5000}
	if err := db.Create(&d1).Error; err != nil {
		t.Fatalf("create d1: %v", err)
	}
	if err := db.Create(&d2).Error; err != nil {
		t.Fatalf("create d2: %v", err)
	}

	status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-update",
		map[string]any{"ids": []uint{d1.ID, d2.ID, d1.ID}, "enabled": false})
	if status != http.StatusOK {
		t.Fatalf("batch disable: %d %v", status, env)
	}
	data := batchData(t, env)
	if data["total"].(float64) != 2 || data["succeeded"].(float64) != 2 || data["failed"].(float64) != 0 {
		t.Fatalf("batch disable counts = %v, want total/succeeded/failed = 2/2/0（重复 id 必须去重）", data)
	}
	for _, id := range []uint{d1.ID, d2.ID} {
		var got models.EdgeDevice
		if err := db.First(&got, id).Error; err != nil {
			t.Fatalf("reload %d: %v", id, err)
		}
		if got.Enabled {
			t.Errorf("device %d enabled = true, want false", id)
		}
	}

	status, env = doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-update",
		map[string]any{"ids": []uint{d1.ID}, "interval_ms": 1234})
	if status != http.StatusOK {
		t.Fatalf("batch interval: %d %v", status, env)
	}
	var got models.EdgeDevice
	if err := db.First(&got, d1.ID).Error; err != nil {
		t.Fatalf("reload d1: %v", err)
	}
	if got.IntervalMs != 1234 {
		t.Errorf("interval_ms = %d, want 1234", got.IntervalMs)
	}

	// 请求级校验
	for name, body := range map[string]map[string]any{
		"no fields":        {"ids": []uint{d1.ID}},
		"negative":         {"ids": []uint{d1.ID}, "interval_ms": -1},
		"interval too big": {"ids": []uint{d1.ID}, "interval_ms": 3600001},
		"empty ids":        {"ids": []uint{}, "enabled": true},
	} {
		status, _ := doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-update", body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", name, status)
		}
	}
	bigIDs := make([]uint, 0, 101)
	for i := 1; i <= 101; i++ {
		bigIDs = append(bigIDs, uint(i))
	}
	status, _ = doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-update",
		map[string]any{"ids": bigIDs, "enabled": true})
	if status != http.StatusBadRequest {
		t.Errorf("ids > 100: status = %d, want 400", status)
	}
}

// TestEdgeDevice_BatchUpdate_PerItemFailures — 被规则引用的组内设备停用失败、
// 未删除/不存在的 id 失败，其余设备照常成功。
func TestEdgeDevice_BatchUpdate_PerItemFailures(t *testing.T) {
	r, db, gsvc := newSourceGroupGuardAPI(t)
	blocked := createGuardEdgeDevice(t, db, "blocked")
	group := createGuardGroup(t, gsvc, "组-批量停用守卫", blocked)
	kind := string(models.SourceKindSourceGroup)
	rule := models.AutomationRule{
		Name: "batch-ref-rule", Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold, TriggerSensorName: "illuminance",
		TriggerComparator: models.AlertComparatorGT, TriggerThreshold: 500,
		TriggerEdgeDeviceID: blocked.ID, CooldownSec: 300,
		SourceKind: &kind, SourceGroupID: &group.ID,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatalf("create rule: %v", err)
	}
	free := createGuardEdgeDevice(t, db, "free")
	deleted := models.EdgeDevice{Name: "deleted", NodeID: "node-1", ChannelID: 1, Enabled: true}
	if err := db.Create(&deleted).Error; err != nil {
		t.Fatalf("create deleted: %v", err)
	}
	if err := db.Delete(&deleted).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/batch-update",
		map[string]any{"ids": []uint{blocked.ID, free.ID, deleted.ID, 999999}, "enabled": false})
	if status != http.StatusOK {
		t.Fatalf("batch update: %d %v", status, env)
	}
	data := batchData(t, env)
	if data["succeeded"].(float64) != 1 || data["failed"].(float64) != 3 {
		t.Fatalf("counts = %v, want succeeded 1 / failed 3", data)
	}
	results := batchResults(t, data)
	if r := resultByID(results, free.ID); r == nil || r["success"] != true {
		t.Errorf("free device result = %v, want success", r)
	}
	if r := resultByID(results, blocked.ID); r == nil || r["success"] == true ||
		!strings.Contains(r["error"].(string), group.Name) {
		t.Errorf("blocked device result = %v, want failure containing group name %q", r, group.Name)
	}
	if r := resultByID(results, deleted.ID); r == nil || r["success"] == true {
		t.Errorf("deleted device result = %v, want failure", r)
	}
	if r := resultByID(results, 999999); r == nil || r["success"] == true {
		t.Errorf("nonexistent device result = %v, want failure", r)
	}

	var blockedReloaded models.EdgeDevice
	if err := db.First(&blockedReloaded, blocked.ID).Error; err != nil {
		t.Fatalf("reload blocked: %v", err)
	}
	if !blockedReloaded.Enabled {
		t.Errorf("blocked device enabled = false, want true（守卫失败不得改状态）")
	}
	var freeReloaded models.EdgeDevice
	if err := db.First(&freeReloaded, free.ID).Error; err != nil {
		t.Fatalf("reload free: %v", err)
	}
	if freeReloaded.Enabled {
		t.Errorf("free device enabled = true, want false")
	}
}

// TestEdgeDevice_DeletedList_Search — 历史设备列表按名称/型号模糊搜索（大小写不敏感）。
func TestEdgeDevice_DeletedList_Search(t *testing.T) {
	r, db, _ := newSourceGroupGuardAPI(t)
	mk := func(name, typ string) models.EdgeDevice {
		d := models.EdgeDevice{Name: name, Type: typ, NodeID: "node-1", ChannelID: 1}
		if err := db.Create(&d).Error; err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := db.Delete(&d).Error; err != nil {
			t.Fatalf("delete %s: %v", name, err)
		}
		return d
	}
	mk("Old BMS", "jiabaida_bms")
	mk("旧逆变器", "techfine_inverter")
	mk("雨量计", "optical_rain")

	cases := []struct {
		search string
		want   int
	}{
		{"old", 1},      // 名称（大小写不敏感）
		{"JIABAIDA", 1}, // 型号（大小写不敏感）
		{"逆变", 1},       // 中文名称
		{"不存在的名字", 0},
	}
	for _, tc := range cases {
		status, env := doGuardJSON(t, r, http.MethodGet,
			"/api/v1/edge-devices/deleted?search="+tc.search, nil)
		if status != http.StatusOK {
			t.Fatalf("search %q: %d %v", tc.search, status, env)
		}
		data := batchData(t, env)
		if got := int(data["total"].(float64)); got != tc.want {
			t.Errorf("search %q total = %d, want %d", tc.search, got, tc.want)
		}
		items, _ := data["items"].([]any)
		if len(items) != tc.want {
			t.Errorf("search %q items = %d, want %d", tc.search, len(items), tc.want)
		}
	}
}

// TestEdgeDevice_DeletedBatchDelete_QueuesPurge — 历史设备彻底删除只接受已软删
// 设备：置 data_purge_requested、幂等；在役/不存在 id 逐条失败且不改状态。
func TestEdgeDevice_DeletedBatchDelete_QueuesPurge(t *testing.T) {
	r, db, _ := newSourceGroupGuardAPI(t)
	old := models.EdgeDevice{Name: "old", Type: "bms_jbd", NodeID: "node-1", ChannelID: 1}
	if err := db.Create(&old).Error; err != nil {
		t.Fatalf("create old: %v", err)
	}
	if err := db.Create(&models.UnifiedData{DeviceID: old.ID, SensorName: "voltage", Value: 1}).Error; err != nil {
		t.Fatalf("seed data: %v", err)
	}
	if err := db.Delete(&old).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	live := models.EdgeDevice{Name: "live", NodeID: "node-1", ChannelID: 1}
	if err := db.Create(&live).Error; err != nil {
		t.Fatalf("create live: %v", err)
	}

	status, env := doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/deleted/batch-delete",
		map[string]any{"ids": []uint{old.ID, live.ID, 999999}})
	if status != http.StatusOK {
		t.Fatalf("history batch delete: %d %v", status, env)
	}
	data := batchData(t, env)
	if data["succeeded"].(float64) != 1 || data["failed"].(float64) != 2 {
		t.Fatalf("counts = %v, want succeeded 1 / failed 2", data)
	}
	results := batchResults(t, data)
	if r := resultByID(results, old.ID); r == nil || r["success"] != true || r["queued"] != true {
		t.Errorf("old device result = %v, want success+queued", r)
	}
	if r := resultByID(results, live.ID); r == nil || r["success"] == true {
		t.Errorf("live device result = %v, want failure（在役设备不能走彻底删除）", r)
	}

	var reloaded models.EdgeDevice
	if err := db.Unscoped().First(&reloaded, old.ID).Error; err != nil {
		t.Fatalf("reload old: %v", err)
	}
	if !reloaded.DataPurgeRequested {
		t.Errorf("data_purge_requested = false, want true（提交清理队列）")
	}
	// 数据必须还在：真正删除由后台 purger 异步执行，接口不阻塞大设备。
	var rows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", old.ID).Count(&rows)
	if rows != 1 {
		t.Errorf("unified rows = %d, want 1（接口只入队，不阻塞删数据）", rows)
	}

	// 幂等：重复提交仍是成功。
	status, env = doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/deleted/batch-delete",
		map[string]any{"ids": []uint{old.ID}})
	if status != http.StatusOK {
		t.Fatalf("repeat: %d %v", status, env)
	}
	if batchData(t, env)["succeeded"].(float64) != 1 {
		t.Errorf("repeat should be idempotent success")
	}

	// 请求校验
	status, _ = doGuardJSON(t, r, http.MethodPost, "/api/v1/edge-devices/deleted/batch-delete",
		map[string]any{"ids": []uint{}})
	if status != http.StatusBadRequest {
		t.Errorf("empty ids: status = %d, want 400", status)
	}
}
