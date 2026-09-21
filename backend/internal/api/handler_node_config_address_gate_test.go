package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// ==================== PUT /nodes/:id/config 地址侧门 + 类型抹除 (2026-09-21 G1/G2) ====================
//
// 背景 (对抗审计实测, 非推演): R2 只修了 /edge-devices 的创建/更新两道口子,
// PUT /nodes/:id/config 是**第三条**写 edge_devices.hardware_id 的路径, 且它
// 完全没有地址校验。审计代理把两条请求打到**未修改的生产镜像**上:
//
//   G1  PUT {edge_devices:[{id:1, hardware_id:"UART1"}]}     -> 200, DB 落库 UART1
//   G2  PUT {edge_devices:[{id:1}]}(只给 id)                  -> 200, type 被抹成空串
//
// G2 还是 G1 的放大器: type 被抹空后, driverRequiresTargetAddress 对
// cataloged=false 直接放行, 于是后续任何 hardware_id 写入都不再有门禁。
//
// 本组测试把这两条都钉死在**写入时**: 非法地址 400 且不落库, 未指定
// device_config_id 时 type 保持原值。

// nodeConfigTestRouter 装配 node 配置路由 + 真实的 driver registry,
// 复刻生产组合根 (routes.go: registerNodeRoutes(v1, db, nodeMgr, driverRegistry))。
func nodeConfigTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	db := setupTestDB(t)
	r := setupRouter()
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil, registry)
	registerNodeRoutes(v1, db, mgr, registry)
	return r, db
}

// putNodeConfig 发一个 PUT /api/v1/nodes/:id/config 请求。
func putNodeConfig(t *testing.T, r *gin.Engine, nodeID interface{}, payload map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(payload)
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, fmt.Sprintf("/api/v1/nodes/%v/config", nodeID), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

// seedAddressedRainDevice 造出事故形态: 通道 hardware_id 是总线名 "UART1",
// 设备是地址化驱动 sn3001_rain, 当前地址合法 ("1")。
func seedAddressedRainDevice(t *testing.T, db *gorm.DB) *models.Node {
	t.Helper()
	node := models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{
		Name: "审计雨量计", Type: "sn3001_rain", NodeID: node.NodeID, ChannelID: 1,
		HardwareID: "1", IntervalMs: 1000, Enabled: true, Status: "active",
	}).Error; err != nil {
		t.Fatal(err)
	}
	return &node
}

// G1 正例: 审计代理实测过的原始请求。未修改的镜像返回 200 并把 "UART1" 写进
// hardware_id; 修复后必须 400, 且 DB 不落库 (回滚, 不是"写了再改回")。
func TestNodeConfigUpdate_RejectsBusNameAsDeviceAddress(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "UART1"}},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a bus name as device address on the config side door, got %d: %s", w.Code, w.Body.String())
	}
	// 拒绝理由必须可读且带合法域 (与 /edge-devices 的门禁同一句话, 因为它复用同一函数)。
	if !strings.Contains(w.Body.String(), "1-254") {
		t.Fatalf("rejection must state the legal address domain, got: %s", w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, 1).Error; err != nil {
		t.Fatal(err)
	}
	if dev.HardwareID != "1" {
		t.Fatalf("rejected config update must leave hardware_id untouched, got %q", dev.HardwareID)
	}
}

// G1 关键陷阱: type 与 hardware_id 在**同一请求**内一起变。
// 若校验用变更前的 type (或在校验后才解析 type), bmp280 -> sn3001_rain 这一次
// 切换会用 bmp280 的口径判定 (该驱动不需要地址), "UART1" 就会被放行。
func TestNodeConfigUpdate_TypeSwitchValidatesAgainstFinalType(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	// 起始设备: 不寻址的驱动 + 一个总线名 (历史行, 门禁上线前写入)。
	if err := db.Create(&models.DeviceConfig{Name: "Rain", DeviceType: "sn3001_rain", HardwareType: "uart", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"type": "bmp280", "hardware_id": "UART1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 1, "hardware_id": "UART1"}},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("switching onto an addressed driver must validate with the FINAL type, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Type != "bmp280" || dev.HardwareID != "UART1" {
		t.Fatalf("rejected batch must roll back both fields, got type=%q hardware_id=%q", dev.Type, dev.HardwareID)
	}
}

// G1 反例 (门禁不能误伤): 同一个端点上合法的地址修复必须照旧通过。
func TestNodeConfigUpdate_AcceptsLegalAddress(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Update("hardware_id", "UART1").Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "0x02"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("repairing a legacy row through this endpoint must keep working, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "0x02" {
		t.Fatalf("legal address must be stored verbatim, got %q", dev.HardwareID)
	}
}

// G1 反例: 不寻址的驱动仍可接受任意标识符 (门禁只对 RequiresTargetAddress 的类型生效)。
func TestNodeConfigUpdate_AllowsBusNameForNonAddressedDriver(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"type": "bmp280", "hardware_id": "0x76",
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "I2C0"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("non-addressed driver must keep accepting any identifier, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "I2C0" {
		t.Fatalf("hardware_id must be stored verbatim, got %q", dev.HardwareID)
	}
}

// G1: 未修改地址的请求不能被历史坏值反噬。审计报告指出"改个 interval 也要能过"
// 是 R2 已确立的契约 (handler_edge_device.go:789 只在确实写入地址时校验),
// 本端点必须同口径, 否则存量坏行连改采样周期都做不到。
func TestNodeConfigUpdate_UnrelatedFieldKeepsLegacyAddressUntouched(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Update("hardware_id", "UART1").Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "interval_ms": 7000}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("changing interval_ms on a row with a legacy address must stay allowed, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "UART1" {
		t.Fatalf("untouched legacy address must be preserved, got %q", dev.HardwareID)
	}
	if dev.IntervalMs != 7000 {
		t.Fatalf("interval_ms must be applied, got %d", dev.IntervalMs)
	}
}

// G2 正例: 审计代理实测过的第二条请求 (只给 id)。未修改的镜像把 type 抹成 "",
// 修复后 type 必须保持原值。
func TestNodeConfigUpdate_OnlyChangedFieldsAreWritten(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, 1).Error; err != nil {
		t.Fatal(err)
	}
	if dev.Type != "sn3001_rain" {
		t.Fatalf("type must survive an update that does not select a device_config_id, got %q", dev.Type)
	}
	if dev.DeviceConfigID != 0 {
		t.Fatalf("device_config_id must stay 0, got %d", dev.DeviceConfigID)
	}
	if dev.HardwareID != "1" {
		t.Fatalf("hardware_id must stay untouched, got %q", dev.HardwareID)
	}
}

// G2 核心用例 (契约点名的): 只改 interval_ms -> type 保持原值。
func TestNodeConfigUpdate_IntervalOnlyKeepsType(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "interval_ms": 2500}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	if err := db.First(&dev, 1).Error; err != nil {
		t.Fatal(err)
	}
	if dev.Type != "sn3001_rain" {
		t.Fatalf("interval-only update must keep type=sn3001_rain, got %q", dev.Type)
	}
	if dev.IntervalMs != 2500 {
		t.Fatalf("interval_ms must be updated, got %d", dev.IntervalMs)
	}
}

// G2 连带效应 (审计报告里最危险的一环): type 被抹空 => 后续 hardware_id 写入
// 全部放行。这里把两步放在一起来钉: 任何一步都不允许把 type 变成空串。
func TestNodeConfigUpdate_CannotEraseTypeThenWriteBusName(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)

	// 第一步: 抹 type 的尝试 (审计实测 200 + type="")。
	first := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "name": "改名"}},
	})
	if first.Code != http.StatusOK {
		t.Fatalf("rename must succeed, got %d: %s", first.Code, first.Body.String())
	}
	var afterFirst models.EdgeDevice
	db.First(&afterFirst, 1)
	if afterFirst.Type == "" {
		t.Fatal("type was erased: every later address write would bypass the gate")
	}

	// 第二步: 通过侧门写总线名 (审计实测 200 + 落库 UART1)。
	second := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "UART1"}},
	})
	if second.Code != http.StatusBadRequest {
		t.Fatalf("address side door must stay closed after an unrelated update, got %d: %s", second.Code, second.Body.String())
	}
	var afterSecond models.EdgeDevice
	db.First(&afterSecond, 1)
	if afterSecond.HardwareID != "1" {
		t.Fatalf("bus name must never be persisted, got hardware_id=%q", afterSecond.HardwareID)
	}
	if afterSecond.Type != "sn3001_rain" {
		t.Fatalf("type must stay sn3001_rain, got %q", afterSecond.Type)
	}
}

// G2: 显式切换 device_config_id 时必须照旧推导 type (原有契约不能回退)。
func TestNodeConfigUpdate_DeviceConfigSelectionStillDerivesType(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Create(&models.DeviceConfig{Name: "BMP", DeviceType: "bmp280", HardwareType: "uart", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 1, "hardware_id": "0x76"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.DeviceConfigID != 1 || dev.Type != "bmp280" {
		t.Fatalf("selecting a device_config_id must still bind id and type, got config=%d type=%q", dev.DeviceConfigID, dev.Type)
	}
}

// G2: 显式解绑 (device_config_id = 0) 是合法部分更新, 但不得抹掉 type ——
// 与 /edge-devices 更新路径 (handler_edge_device.go configID==0 分支) 同语义。
func TestNodeConfigUpdate_ExplicitDetachKeepsType(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Create(&models.DeviceConfig{Name: "Rain", DeviceType: "sn3001_rain", HardwareType: "uart", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Update("device_config_id", 1).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 0}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.DeviceConfigID != 0 {
		t.Fatalf("explicit detach must clear device_config_id, got %d", dev.DeviceConfigID)
	}
	if dev.Type != "sn3001_rain" {
		t.Fatalf("explicit detach must NOT erase the driver type, got %q", dev.Type)
	}
}

// 门禁必须在**写入前**生效且整批回滚: 前面合法的条目不能因为后面的非法条目
// 而被提交 (与既有事务契约一致)。
func TestNodeConfigUpdate_RejectionRollsBackWholeBatch(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Create(&models.EdgeDevice{Name: "Second", Type: "sn3001_rain", NodeID: node.NodeID, ChannelID: 1, HardwareID: "2", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{
			{"id": 1, "name": "先改名"},
			{"id": 2, "hardware_id": "UART1"},
		},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
	}
	var first models.EdgeDevice
	db.First(&first, 1)
	if first.Name == "先改名" {
		t.Fatal("a rejected batch must not commit the earlier valid item")
	}
	var second models.EdgeDevice
	db.First(&second, 2)
	if second.HardwareID != "2" {
		t.Fatalf("second device must be untouched, got %q", second.HardwareID)
	}
}

// 反向钉死 (G1 陷阱的另一半): 请求**不带** hardware_id, 只把 type 切到地址化驱动,
// 存量非法地址必须在这一刻被重新校验并拒绝。
// 若门禁只在 "请求给了 hardware_id" 时触发, 这一步会静默放行, 设备从此带着
// 总线名停在地址化驱动上 —— 正是事故的形态。
func TestNodeConfigUpdate_TypeSwitchAloneRevalidatesStoredAddress(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedAddressedRainDevice(t, db)
	if err := db.Create(&models.DeviceConfig{Name: "Rain", DeviceType: "sn3001_rain", HardwareType: "uart", Status: "active"}).Error; err != nil {
		t.Fatal(err)
	}
	// 历史行: 不寻址的驱动 + 总线名。
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"type": "bmp280", "hardware_id": "UART1",
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "device_config_id": 1}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("switching onto an addressed driver must re-validate the stored address, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Type != "bmp280" {
		t.Fatalf("rejected type switch must not be applied, got type=%q", dev.Type)
	}
}
