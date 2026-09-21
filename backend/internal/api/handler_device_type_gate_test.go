package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// ==================== 未知驱动型号的门禁 (2026-09-21 G7) ====================
//
// 缺口 (对抗审计实测, 非推演): 三步全走公开 HTTP 就能把 device address 门禁
// 整体关掉 ——
//
//	1) POST /api/v1/device-configs {device_type: no_such_driver_xyz}   -> 201
//	2) PUT  /api/v1/nodes/<serial>/config {edge_devices:[{id:1, device_config_id:<它>}]}
//	   -> 200, edge_devices.type 变成那个未知型号
//	3) PUT  /api/v1/nodes/<serial>/config {edge_devices:[{id:1, hardware_id:"UART1"}]}
//	   -> 200, DB 落库 hardware_id="UART1"
//
// 根因是 validateEdgeDeviceAddress 里 "if !cataloged || !requiresAddress { return nil }":
// 未注册型号与"不需要地址的型号"走了同一个放行分支, 于是"注册表不认识"等于
// "随便写"。
//
// 本轮两处一起收紧:
//   Y1 POST/PUT /device-configs 的 device_type 必须是已注册驱动 (入口治理);
//   Y2 地址门禁对 !cataloged 不再静默放行 (纵深防御, 历史行同样受控)。
//
// 兼容性契约 (必须同时成立):
//   - 历史行里已有未注册 device_type 的 DeviceConfig 仍可 GET/列表/改名/禁用;
//   - 只有"改 type"或"新建"才要求已注册;
//   - 携带未注册型号的历史边缘设备仍可改名/改周期 (地址门禁只在写地址时触发)。

type httpResult struct {
	Code int
	Body string
}

// doJSON 发一个 JSON 请求 (方法/路径/可选 body), 返回状态码与响应体。
func doJSON(t *testing.T, r *gin.Engine, method, path string, body interface{}) *httpResult {
	t.Helper()
	var payload []byte
	if body != nil {
		payload, _ = json.Marshal(body)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return &httpResult{Code: w.Code, Body: w.Body.String()}
}

// Y1 正例 (审计实测的第一步): 未注册型号必须 400, 不得再落库。
func TestDeviceConfigCreate_RejectsUnregisteredDeviceType(t *testing.T) {
	r, db := setupDeviceTest(t)

	resp := doJSON(t, r, http.MethodPost, "/api/v1/device-configs", map[string]interface{}{
		"name": "x", "device_type": "no_such_driver_xyz", "hardware_type": "UART",
	})
	if resp.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unregistered device_type, got %d: %s", resp.Code, resp.Body)
	}
	if !strings.Contains(resp.Body, "not a registered driver") {
		t.Fatalf("rejection must name the reason, got: %s", resp.Body)
	}
	var count int64
	db.Model(&models.DeviceConfig{}).Where("device_type = ?", "no_such_driver_xyz").Count(&count)
	if count != 0 {
		t.Fatalf("rejected device config must not be persisted, found %d rows", count)
	}
}

// Y1 反例: 已注册驱动照旧 201 (门禁不得误伤正常型号)。
func TestDeviceConfigCreate_AcceptsRegisteredDeviceType(t *testing.T) {
	r, _ := setupDeviceTest(t)

	resp := doJSON(t, r, http.MethodPost, "/api/v1/device-configs", map[string]interface{}{
		"name": "雨量模板", "device_type": "sn3001_rain", "hardware_type": "uart",
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("registered device_type must still create, got %d: %s", resp.Code, resp.Body)
	}
}

// Y1 兼容性 (点名的契约): 历史行里已有未注册 device_type 时, **读**与**改名**
// 都不得报错。只挡新建/改 type。
func TestDeviceConfigLegacyUnregisteredTypeStaysEditable(t *testing.T) {
	r, db := setupDeviceTest(t)
	// 模拟历史数据: 门禁上线前写入的未注册型号。
	if err := db.Create(&models.DeviceConfig{
		Name: "历史模板", DeviceType: "legacy_unregistered_type", HardwareType: "uart", Status: "active",
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 1) 读: 列表 + 详情都必须 200。
	list := doJSON(t, r, http.MethodGet, "/api/v1/device-configs", nil)
	if list.Code != http.StatusOK || !strings.Contains(list.Body, "legacy_unregistered_type") {
		t.Fatalf("legacy config must stay listable, got %d: %s", list.Code, list.Body)
	}
	detail := doJSON(t, r, http.MethodGet, "/api/v1/device-configs/1", nil)
	if detail.Code != http.StatusOK {
		t.Fatalf("legacy config must stay readable, got %d: %s", detail.Code, detail.Body)
	}

	// 2) 改名 + 改描述 (type 原样回传) 必须 200。
	rename := doJSON(t, r, http.MethodPut, "/api/v1/device-configs/1", map[string]interface{}{
		"name": "历史模板-改名", "description": "仍然可编辑",
	})
	if rename.Code != http.StatusOK {
		t.Fatalf("renaming a legacy config must succeed, got %d: %s", rename.Code, rename.Body)
	}
	var after models.DeviceConfig
	db.First(&after, 1)
	if after.Name != "历史模板-改名" || after.DeviceType != "legacy_unregistered_type" {
		t.Fatalf("rename must apply and keep the legacy type, got name=%q type=%q", after.Name, after.DeviceType)
	}

	// 3) 但把 type 改到另一个**未注册**值必须 400。
	switchType := doJSON(t, r, http.MethodPut, "/api/v1/device-configs/1", map[string]interface{}{
		"name": "历史模板-改名", "device_type": "another_unregistered_type",
	})
	if switchType.Code != http.StatusBadRequest {
		t.Fatalf("changing device_type to an unregistered value must be refused, got %d: %s", switchType.Code, switchType.Body)
	}
	db.First(&after, 1)
	if after.DeviceType != "legacy_unregistered_type" {
		t.Fatalf("rejected type change must not be applied, got %q", after.DeviceType)
	}

	// 4) 改到**已注册**型号必须 200 (历史数据的修复路径)。
	repair := doJSON(t, r, http.MethodPut, "/api/v1/device-configs/1", map[string]interface{}{
		"name": "历史模板-改名", "device_type": "bmp280", "hardware_type": "i2c",
	})
	if repair.Code != http.StatusOK {
		t.Fatalf("repairing a legacy type to a registered driver must succeed, got %d: %s", repair.Code, repair.Body)
	}
	db.First(&after, 1)
	if after.DeviceType != "bmp280" {
		t.Fatalf("repair must be applied, got %q", after.DeviceType)
	}
}

// Y2 三步链的逐步复现: 第 1 步被 Y1 挡下之后, 整条链不可达。
func TestUnknownDriverBypassChain_Step1IsClosed(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "雨量计", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "1", Enabled: true})

	step1 := doJSON(t, r, http.MethodPost, "/api/v1/device-configs", map[string]interface{}{
		"name": "x", "device_type": "no_such_driver_xyz", "hardware_type": "UART",
	})
	if step1.Code != http.StatusBadRequest {
		t.Fatalf("step 1 of the bypass chain must be refused, got %d: %s", step1.Code, step1.Body)
	}
	var count int64
	db.Model(&models.DeviceConfig{}).Count(&count)
	if count != 0 {
		t.Fatalf("no config may be created by the refused step 1, found %d", count)
	}
}

// Y2 纵深防御 (最关键的一条): 即使设备**已经是**未注册型号 (历史行, 或将来某个
// 绕过入口产生的行), 写 hardware_id 也必须在门禁处被拒, 而不是静默放行。
func TestNodeConfigUpdate_UnregisteredTypeCannotBypassAddressGate(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	// 历史行: 未注册型号 + 合法地址。
	if err := db.Create(&models.EdgeDevice{
		Name: "历史设备", Type: "no_such_driver_xyz", NodeID: node.NodeID, ChannelID: 1, HardwareID: "1", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "UART1"}},
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an unregistered type must not disable the address gate, got %d: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "not registered") {
		t.Fatalf("rejection must say the type is not registered, got: %s", w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "1" {
		t.Fatalf("rejected write must leave hardware_id untouched, got %q", dev.HardwareID)
	}
}

// Y2: 正门 (PUT /edge-devices/:id) 对同一种"未注册型号 + 写地址"也必须拒绝。
// 两扇门对同一状态给同一判定, 否则又会出现"哪扇门能过"的差别。
func TestEdgeDeviceUpdate_UnregisteredTypeCannotBypassAddressGate(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true})
	db.Create(&models.EdgeDevice{Name: "历史设备", Type: "no_such_driver_xyz", NodeID: "NODE001", ChannelID: 1, HardwareID: "1", Enabled: true})

	w := putEdgeDevice(t, r, "/api/v1/edge-devices/1", map[string]interface{}{"hardware_id": "UART1"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("front door must reject an address write on an unregistered type, got %d: %s", w.Code, w.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.HardwareID != "1" {
		t.Fatalf("rejected write must leave hardware_id untouched, got %q", dev.HardwareID)
	}
}

// Y2 兼容性 (点名的契约): 未注册型号的历史行必须仍可改名/改周期。
// 若门禁改成"任何写入都拒绝", 这一条会红 —— 它就是用来钉住取舍的。
func TestNodeConfigUpdate_LegacyUnregisteredTypeStillRenamable(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{
		Name: "历史设备", Type: "no_such_driver_xyz", NodeID: node.NodeID, ChannelID: 1, HardwareID: "7", Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	rename := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "name": "历史设备-改名"}},
	})
	if rename.Code != http.StatusOK {
		t.Fatalf("renaming a legacy unregistered-type device must succeed, got %d: %s", rename.Code, rename.Body.String())
	}
	interval := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "interval_ms": 9000}},
	})
	if interval.Code != http.StatusOK {
		t.Fatalf("changing interval of a legacy unregistered-type device must succeed, got %d: %s", interval.Code, interval.Body.String())
	}
	var dev models.EdgeDevice
	db.First(&dev, 1)
	if dev.Name != "历史设备-改名" || dev.IntervalMs != 9000 || dev.HardwareID != "7" {
		t.Fatalf("legacy row must stay editable and keep its stored address, got %+v", dev)
	}
}

// Y2 反例: 已注册但**不需要地址**的型号照旧接受任意标识符
// (generic_i2c 的真实形态: 它没有寻址动作, 不得被 fail-closed 误伤)。
func TestAddressGate_RegisteredNonAddressedTypeStillAcceptsAnyIdentifier(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C", HardwareID: "I2C0", Enabled: true})

	resp := doJSON(t, r, http.MethodPost, "/api/v1/edge-devices", map[string]interface{}{
		"name": "通用I2C", "node_id": "NODE001", "channel_id": 1,
		"type": "generic_i2c", "hardware_id": "I2C0",
	})
	if resp.Code != http.StatusCreated {
		t.Fatalf("a registered non-addressed driver must keep accepting any identifier, got %d: %s", resp.Code, resp.Body)
	}
	var dev models.EdgeDevice
	db.First(&dev, "name = ?", "通用I2C")
	if dev.HardwareID != "I2C0" {
		t.Fatalf("hardware_id must be stored verbatim, got %q", dev.HardwareID)
	}
}

// 门禁对"已注册且需要地址"的型号仍按 1-254 判定 (不得把合法地址误伤)。
func TestAddressGate_RegisteredAddressedTypeStillValidatesDomain(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true})

	legal := doJSON(t, r, http.MethodPost, "/api/v1/edge-devices", map[string]interface{}{
		"name": "雨量合法", "node_id": "NODE001", "channel_id": 1,
		"type": "sn3001_rain", "hardware_id": "0x02",
	})
	if legal.Code != http.StatusCreated {
		t.Fatalf("a legal address on an addressed driver must still be accepted, got %d: %s", legal.Code, legal.Body)
	}
	illegal := doJSON(t, r, http.MethodPost, "/api/v1/edge-devices", map[string]interface{}{
		"name": "雨量非法", "node_id": "NODE001", "channel_id": 1,
		"type": "sn3001_rain", "hardware_id": "UART1",
	})
	if illegal.Code != http.StatusBadRequest {
		t.Fatalf("a bus name on an addressed driver must still be rejected, got %d: %s", illegal.Code, illegal.Body)
	}
	var count int64
	db.Model(&models.EdgeDevice{}).Where("name = ?", "雨量非法").Count(&count)
	if count != 0 {
		t.Fatalf("rejected device must not be persisted, found %d rows", count)
	}
}
