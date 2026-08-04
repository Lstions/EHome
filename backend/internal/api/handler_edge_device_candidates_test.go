package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// T5 P2 后端单测 — candidates API 与创建继承 (方案 v3.3 §1.3 §3.3)。

// TestCandidates_WeightOrdering 验证五档权重排序 (100/80/60/40/20):
// 同 type 的候选按与创建目标的匹配度降序, 权重内按 last_data_at 降序。
func TestCandidates_WeightOrdering(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "节点一", Status: "online"})
	db.Create(&models.Node{NodeID: "NODE002", Name: "节点二", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", HardwareID: "I2C0", Enabled: true}) // ch 1
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", HardwareID: "I2C1", Enabled: true}) // ch 2

	// 候选 A: 权重 100 (同 node+channel+hw+type)
	ldA := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "候选A", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ldA)
	db.Create(&models.EdgeDevice{Name: "instA", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ldA.ID})
	// 候选 B: 权重 80 (同 node+hw 0x76, 不同 channel)
	ldB := models.LogicalDevice{IdentityKey: "bmp280:0x76#9", Name: "候选B", DeviceType: "bmp280", RetentionDays: 30}
	db.Create(&ldB)
	db.Create(&models.EdgeDevice{Name: "instB", Type: "bmp280", NodeID: "NODE001", ChannelID: 2, HardwareID: "0x76", LogicalDeviceID: &ldB.ID})
	// 候选 C: 权重 60 (同 node+type)
	ldC := models.LogicalDevice{IdentityKey: "bmp280:0x78", Name: "候选C", DeviceType: "bmp280", RetentionDays: 90}
	db.Create(&ldC)
	db.Create(&models.EdgeDevice{Name: "instC", Type: "bmp280", NodeID: "NODE001", ChannelID: 2, HardwareID: "0x78", LogicalDeviceID: &ldC.ID})
	// 候选 D: 权重 40 (不同 node+同 hw)
	ldD := models.LogicalDevice{IdentityKey: "bmp280:0x76#2", Name: "候选D", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ldD)
	db.Create(&models.EdgeDevice{Name: "instD", Type: "bmp280", NodeID: "NODE002", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ldD.ID})
	// 候选 E: 权重 20 (不同 node+type, 无 hw 匹配)
	ldE := models.LogicalDevice{IdentityKey: "bmp280:0x99", Name: "候选E", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ldE)
	db.Create(&models.EdgeDevice{Name: "instE", Type: "bmp280", NodeID: "NODE002", ChannelID: 1, HardwareID: "0x99", LogicalDeviceID: &ldE.ID})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates?type=bmp280&node_id=NODE001&channel_id=1&hardware_id=0x76", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []logicalDeviceCandidate `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Data) != 5 {
		t.Fatalf("expected 5 candidates, got %d: %s", len(resp.Data), w.Body.String())
	}
	wantWeights := []int{100, 80, 60, 40, 20}
	for i, want := range wantWeights {
		if resp.Data[i].MatchWeight != want {
			t.Errorf("position %d: expected weight %d, got %d (name=%s)", i, want, resp.Data[i].MatchWeight, resp.Data[i].Name)
		}
	}
	// retention_days 必须显式返回 (v3.1-Q6)
	if resp.Data[0].RetentionDays != 365 || resp.Data[1].RetentionDays != 30 {
		t.Errorf("retention_days missing/wrong: %d, %d", resp.Data[0].RetentionDays, resp.Data[1].RetentionDays)
	}
	// instance_count 全部为 1
	for _, c := range resp.Data {
		if c.InstanceCount != 1 {
			t.Errorf("candidate %s instance_count = %d, want 1", c.Name, c.InstanceCount)
		}
	}
}

// TestCandidates_IncludesSoftDeletedInstances: 候选聚合必须 Unscoped —
// 逻辑设备只挂软删实例时仍列出 (§1.3 v1-H1)。
func TestCandidates_IncludesSoftDeletedInstances(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	ld := models.LogicalDevice{IdentityKey: "sn3001_rain:0x01", Name: "已删实例的逻辑设备", DeviceType: "sn3001_rain", RetentionDays: 365}
	db.Create(&ld)
	inst := models.EdgeDevice{Name: "soft-deleted", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x01", LogicalDeviceID: &ld.ID}
	db.Create(&inst)
	db.Delete(&inst) // 软删

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates?type=sn3001_rain&node_id=NODE001", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []logicalDeviceCandidate `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 candidate with soft-deleted instance, got %d: %s", len(resp.Data), w.Body.String())
	}
	if resp.Data[0].InstanceCount != 1 {
		t.Errorf("instance_count should count soft-deleted instances, got %d", resp.Data[0].InstanceCount)
	}
}

// TestCandidates_ExcludesMergedAndWrongType: merged_into 非空与 type 不匹配
// 的候选不出现。
func TestCandidates_ExcludesMergedAndWrongType(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})

	ldTarget := models.LogicalDevice{IdentityKey: "bmp280:t1", Name: "目标", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ldTarget)
	db.Create(&models.EdgeDevice{Name: "i1", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, LogicalDeviceID: &ldTarget.ID})

	merged := models.LogicalDevice{IdentityKey: "bmp280:t2", Name: "已合并", DeviceType: "bmp280", RetentionDays: 365, MergedInto: &ldTarget.ID}
	db.Create(&merged)
	db.Create(&models.EdgeDevice{Name: "i2", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, LogicalDeviceID: &merged.ID})

	otherType := models.LogicalDevice{IdentityKey: "lk_th01:t3", Name: "异型", DeviceType: "lk_th01", RetentionDays: 365}
	db.Create(&otherType)
	db.Create(&models.EdgeDevice{Name: "i3", Type: "lk_th01", NodeID: "NODE001", ChannelID: 1, LogicalDeviceID: &otherType.ID})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates?type=bmp280&node_id=NODE001", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	var resp struct {
		Data []logicalDeviceCandidate `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 || resp.Data[0].Name != "目标" {
		t.Fatalf("expected only 目标 candidate, got %+v", resp.Data)
	}
}

// TestCandidates_LastDataAtFromBothScopes: last_data_at 走 dataScopeCondition
// 同构条件 — logical_device_id 命中行 与 NULL-logical 但 device_id 命中行
// 都计入 (v3.2-F7, backfill 过渡期不失真)。
func TestCandidates_LastDataAtFromBothScopes(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "候选", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ld)
	inst := models.EdgeDevice{Name: "i", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	db.Create(&inst)

	now := time.Now().UTC().Truncate(time.Second)
	// NULL-logical 行 (backfill 前旧行), 时间戳更新
	db.Create(&models.UnifiedData{DeviceID: inst.ID, SensorName: "t", Value: 1, Timestamp: now})
	// logical 行, 时间戳更旧
	db.Create(&models.UnifiedData{DeviceID: inst.ID, SensorName: "t", Value: 2, Timestamp: now.Add(-time.Hour), LogicalDeviceID: &ld.ID})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates?type=bmp280&node_id=NODE001", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	var resp struct {
		Data []logicalDeviceCandidate `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Data))
	}
	if resp.Data[0].LastDataAt == nil {
		t.Fatalf("last_data_at should be set from OR-scoped rows")
	}
	if got := resp.Data[0].LastDataAt.UTC(); !got.Equal(now) {
		t.Errorf("last_data_at = %v, want %v (NULL-logical 行时间戳应计入)", got, now)
	}
}

// TestCandidates_RequiresType: 缺 type 参数 → 400。
func TestCandidates_RequiresType(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 without type, got %d", w.Code)
	}
}

// TestCandidates_EstimateDegrades: 估算失败 (数据表缺失场景用 ctx 过期
// 模拟) 时 row_estimate 省略, 端点仍 200 (§1.3 降级)。
// SQLite :memory: 有表, 用已过期 ctx 无法从 handler 注入, 改验正常路径:
// 有数据行时 row_estimate 出现且 >0。
func TestCandidates_RowEstimatePresent(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "候选", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ld)
	inst := models.EdgeDevice{Name: "i", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	db.Create(&inst)
	db.Create(&models.UnifiedData{DeviceID: inst.ID, SensorName: "t", Value: 1, LogicalDeviceID: &ld.ID})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/v1/edge-devices/candidates?type=bmp280&node_id=NODE001", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	var resp struct {
		Data []logicalDeviceCandidate `json:"data"`
	}
	json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.Data) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(resp.Data))
	}
	if resp.Data[0].RowEstimate == nil {
		t.Errorf("row_estimate should be present on SQLite truncated-count path: %s", w.Body.String())
	} else if *resp.Data[0].RowEstimate < 1 {
		t.Errorf("row_estimate should count the seeded row, got %d", *resp.Data[0].RowEstimate)
	}
}

// ==================== 创建继承测试 (§3.3) ====================

func createInheritBody(t *testing.T, extra map[string]interface{}) []byte {
	t.Helper()
	body := map[string]interface{}{
		"name":        "新实例",
		"node_id":     "NODE001",
		"channel_id":  1,
		"type":        "bmp280",
		"hardware_id": "0x76",
		"enabled":     true,
	}
	for k, v := range extra {
		body[k] = v
	}
	b, _ := json.Marshal(body)
	return b
}

// TestCreate_InheritSuccess: 目标合法且无存活实例 → 201, 新实例挂到目标
// logical_device。
func TestCreate_InheritSuccess(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "旧身份", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ld)
	old := models.EdgeDevice{Name: "旧实例", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	db.Create(&old)
	db.Delete(&old) // 软删旧实例 → 无存活实例, 可继承

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": ld.ID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "新实例")
	if dev.LogicalDeviceID == nil || *dev.LogicalDeviceID != ld.ID {
		t.Fatalf("new instance should attach to logical device %d, got %v", ld.ID, dev.LogicalDeviceID)
	}
}

// TestCreate_Inherit_TargetMissing: 目标不存在 → 409。
func TestCreate_Inherit_TargetMissing(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": 9999})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for missing target, got %d: %s", w.Code, w.Body.String())
	}
}

// TestCreate_Inherit_TypeMismatch: device_type 不匹配 → 409。
func TestCreate_Inherit_TypeMismatch(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "lk_th01:0x76", Name: "温湿度计", DeviceType: "lk_th01", RetentionDays: 365}
	db.Create(&ld)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": ld.ID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for type mismatch, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "不匹配") {
		t.Errorf("409 body should mention type mismatch: %s", w.Body.String())
	}
}

// TestCreate_Inherit_MergedTarget: merged_into 非空 → 409 并指向合并目标。
func TestCreate_Inherit_MergedTarget(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	target := models.LogicalDevice{IdentityKey: "bmp280:final", Name: "合并目标", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&target)
	src := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "已合并源", DeviceType: "bmp280", RetentionDays: 365, MergedInto: &target.ID}
	db.Create(&src)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": src.ID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for merged target, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), fmt.Sprintf("#%d", target.ID)) {
		t.Errorf("409 body should point to merge target #%d: %s", target.ID, w.Body.String())
	}
}

// TestCreate_Inherit_PurgeRequested: v3.3-N1 — 目标数据已标记删除 → 409
// 文案"该逻辑设备的数据已标记删除，无法继承"。
func TestCreate_Inherit_PurgeRequested(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "待清数据", DeviceType: "bmp280", RetentionDays: 365, PurgeRequested: true}
	db.Create(&ld)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": ld.ID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for purge_requested target, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "该逻辑设备的数据已标记删除，无法继承") {
		t.Errorf("409 body must carry the v3.3-N1 message: %s", w.Body.String())
	}
}

// TestCreate_Inherit_LivingInstanceConflict: §3.3-3 存活实例唯一性 —
// 目标已有存活实例 → 409, 文案携带实例 ID + 节点名。
func TestCreate_Inherit_LivingInstanceConflict(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "生产节点", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true}) // ch 2 供新设备
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "电池A", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ld)
	living := models.EdgeDevice{Name: "存活实例", Type: "bmp280", NodeID: "NODE001", ChannelID: 2, HardwareID: "0x77", LogicalDeviceID: &ld.ID}
	db.Create(&living)

	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{"logical_device_id": ld.ID})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("expected 409 for living-instance conflict, got %d: %s", w.Code, w.Body.String())
	}
	body := w.Body.String()
	if !strings.Contains(body, fmt.Sprintf("实例 ID %d", living.ID)) {
		t.Errorf("409 body should carry instance ID %d: %s", living.ID, body)
	}
	if !strings.Contains(body, "生产节点") {
		t.Errorf("409 body should carry node name: %s", body)
	}
	if !strings.Contains(body, "作为新设备创建") {
		t.Errorf("409 body should guide toward 作为新设备创建: %s", body)
	}
	// 冲突拒绝必须不落库
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "新实例").Count(&count)
	if count != 0 {
		t.Errorf("rejected create must not persist, found %d rows", count)
	}
}

// TestCreate_NoInherit_NewLogicalNeverReuses: §3.3-4/§2.3.1 — 未指定
// logical_device_id 时新建逻辑身份, 即使 identity_key 与既有逻辑设备撞车
// 也永不复用 (追加序号)。
func TestCreate_NoInherit_NewLogicalNeverReuses(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	// 既有逻辑设备 key = bmp280:0x76 (挂一个软删实例, 模拟"退役设备")
	existing := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "既有身份", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&existing)
	old := models.EdgeDevice{Name: "旧", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &existing.ID}
	db.Create(&old)
	db.Delete(&old)

	// 新建同 type+hardware_id 设备 (不同 channel 避开唯一性约束), 不传 logical_device_id
	body := createInheritBody(t, map[string]interface{}{"channel_id": 2, "hardware_id": "0x76"})
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "新实例")
	if dev.LogicalDeviceID == nil {
		t.Fatal("new device must have a logical device (§3.3-4)")
	}
	if *dev.LogicalDeviceID == existing.ID {
		t.Fatal("作为新设备创建路径永不复用既有 key")
	}
	var newLD models.LogicalDevice
	db.First(&newLD, *dev.LogicalDeviceID)
	if newLD.IdentityKey != "bmp280:0x76#2" {
		t.Errorf("collision should append sequence suffix, got %q", newLD.IdentityKey)
	}
}

// TestCreate_Inherit_CalibrationCopiedOnlyForInPlace: §2.5 — 原位置重建
// (同 channel+hw+type, 权重 100 档) 复制校准行; 换 channel 不复制。
func TestCreate_Inherit_CalibrationCopiedOnlyForInPlace(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", Enabled: true})
	ld := models.LogicalDevice{IdentityKey: "bmp280:0x76", Name: "身份", DeviceType: "bmp280", RetentionDays: 365}
	db.Create(&ld)
	old := models.EdgeDevice{Name: "旧实例", Type: "bmp280", NodeID: "NODE001", ChannelID: 1, HardwareID: "0x76", LogicalDeviceID: &ld.ID}
	db.Create(&old)
	db.Create(&models.CalibrationCache{NodeID: "NODE001", EdgeDeviceID: old.ID, DeviceType: "bmp280", Data: `{"offset":1.5}`})
	db.Delete(&old)

	// 场景 1: 原位置重建 (同 channel 1 + 0x76) → 复制校准
	w := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{
		"name": "原位重建", "logical_device_id": ld.ID,
	})))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var rebuild models.EdgeDevice
	db.First(&rebuild, "name = ?", "原位重建")
	var cals []models.CalibrationCache
	db.Where("edge_device_id = ?", rebuild.ID).Find(&cals)
	if len(cals) != 1 || cals[0].Data != `{"offset":1.5}` {
		t.Fatalf("in-place rebuild should copy calibration rows, got %d rows", len(cals))
	}

	// 场景 2: 换位置 (channel 2) → 不复制校准。先软删场景 1 实例腾出存活位
	db.Delete(&rebuild)
	w2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("POST", "/api/v1/edge-devices", bytes.NewReader(createInheritBody(t, map[string]interface{}{
		"name": "换位置重建", "logical_device_id": ld.ID, "channel_id": 2,
	})))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w2, req2)
	if w2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w2.Code, w2.Body.String())
	}
	var moved models.EdgeDevice
	db.First(&moved, "name = ?", "换位置重建")
	var movedCals []models.CalibrationCache
	db.Where("edge_device_id = ?", moved.ID).Find(&movedCals)
	if len(movedCals) != 0 {
		t.Fatalf("non-in-place rebuild must NOT copy calibration, got %d rows", len(movedCals))
	}
}
