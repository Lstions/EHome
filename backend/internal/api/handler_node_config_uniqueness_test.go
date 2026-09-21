package api

import (
	"net/http"
	"strings"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// ==================== 侧门唯一性校验 (2026-09-21 G6) ====================
//
// 缺口 (对抗审计实测, 非推演): PUT /nodes/:id/config 有地址门禁, 但没有
// **每通道唯一性**校验。于是同一通道上两台同型号设备可以被侧门写成同一个
// 从站地址, 而完全相同的状态走正门 (PUT /edge-devices/:id) 会被 400 拒绝:
//
//	PUT /nodes/<serial>/config {edge_devices:[{id:1, hardware_id:9}]} -> 200
//	PUT /nodes/<serial>/config {edge_devices:[{id:2, hardware_id:9}]} -> 200  (应 400)
//	PUT /edge-devices/2 {hardware_id:9}                              -> 400
//
// 同一根 Modbus 总线上两个相同 unit id 不是"校验洁癖": 两台设备的寻址动作在
// 线路上不可区分, set_device_address / read_rainfall 都会打到错误的物理设备。
//
// 修复复用 checkDeviceUniqueness (创建/更新路径的同一函数、同一语义), 并且
// excludeID 必须是本行自己的 ID, 否则设备会与自己冲突。

// seedTwoRainDevicesOnOneChannel 造出"同通道两台同型号设备"这一唯一性校验的
// 判定场景, 地址分别是 1 与 2 (合法且互不相同, 是校验必须放行的基线)。
func seedTwoRainDevicesOnOneChannel(t *testing.T, db *gorm.DB) *models.Node {
	t.Helper()
	node := models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	for _, dev := range []models.EdgeDevice{
		{Name: "雨量计A", Type: "sn3001_rain", NodeID: node.NodeID, ChannelID: 1, HardwareID: "1", Enabled: true, Status: "active"},
		{Name: "雨量计B", Type: "sn3001_rain", NodeID: node.NodeID, ChannelID: 1, HardwareID: "2", Enabled: true, Status: "active"},
	} {
		if err := db.Create(&dev).Error; err != nil {
			t.Fatal(err)
		}
	}
	return &node
}

// G6 正例 (审计实测过的绕过链): 侧门把第二台设备写到第一台已占用的地址上,
// 必须 400, 且整行保持原值 (回滚, 不是"写了再改回")。
func TestNodeConfigUpdate_RejectsDuplicateAddressOnSameChannel(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "hardware_id": "1"}},
	})

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a duplicate address on one channel, got %d: %s", w.Code, w.Body.String())
	}
	// 拒绝理由必须与正门同一句话 (同一个函数), 否则两条路径又会漂移。
	if !stringsContainsAll(w.Body.String(), "channel 1", "sn3001_rain", "already hosts", "address") {
		t.Fatalf("rejection must name channel/type/address like the front door, got: %s", w.Body.String())
	}
	var second models.EdgeDevice
	if err := db.First(&second, 2).Error; err != nil {
		t.Fatal(err)
	}
	if second.HardwareID != "2" {
		t.Fatalf("rejected duplicate must leave hardware_id untouched, got %q", second.HardwareID)
	}
}

// G6 反例 (门禁不能误伤): 把设备写到**空闲**地址必须照旧通过。
func TestNodeConfigUpdate_AcceptsFreeAddressOnSameChannel(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "hardware_id": "0x09"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("moving a device onto a free address must succeed, got %d: %s", w.Code, w.Body.String())
	}
	var second models.EdgeDevice
	db.First(&second, 2)
	if second.HardwareID != "0x09" {
		t.Fatalf("free address must be stored verbatim, got %q", second.HardwareID)
	}
}

// G6 关键陷阱: excludeID 必须是本行 ID。传 0 (或别人的 ID) 时, 设备会与
// **自己已占用的地址**冲突 —— 于是"改个名字"、“把自己地址原样重发"这类
// 合法请求都会 400, 而现场只会看到"什么都改不了"。
func TestNodeConfigUpdate_DeviceDoesNotCollideWithItself(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)

	// 把自己的地址原样再发一次 + 同时改名 (同一批次)。
	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 1, "hardware_id": "1", "name": "雨量计A改名"}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("a device must not collide with its own address, got %d: %s", w.Code, w.Body.String())
	}
	var first models.EdgeDevice
	db.First(&first, 1)
	if first.HardwareID != "1" || first.Name != "雨量计A改名" {
		t.Fatalf("self-update must be applied, got hardware_id=%q name=%q", first.HardwareID, first.Name)
	}
}

// G6 边界: 唯一性口径是 (channel, type, hardware_id)。同地址换一条通道是
// 合法的多总线部署 (与 checkDeviceUniqueness 的注释一致), 不得被误伤。
func TestNodeConfigUpdate_SameAddressOnDifferentChannelIsAllowed(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART2", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "channel_id": 2}},
	})
	if w.Code != http.StatusOK {
		t.Fatalf("moving a device to another channel must stay allowed, got %d: %s", w.Code, w.Body.String())
	}
	// 移动之后它自己的地址 "2" 在新通道上是空闲的; 另一台仍在通道 1 的 "1"。
	var second models.EdgeDevice
	db.First(&second, 2)
	if second.ChannelID != 2 {
		t.Fatalf("channel move must be applied, got %d", second.ChannelID)
	}
}

// G6: 唯一性判定必须用**变更后**的通道/型号/地址三元组。把设备搬到另一台
// 设备所在的通道并同时采用其地址, 是本请求造成的真实冲突, 必须 400。
func TestNodeConfigUpdate_CandidateTripleIsCheckedAfterChanges(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)
	if err := db.Create(&models.Channel{NodeID: node.NodeID, HardwareType: "UART", BusType: "UART", HardwareID: "UART2", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	// 设备 2 先搬到通道 2 (空), 用地址 2。
	if err := db.Model(&models.EdgeDevice{}).Where("id = ?", 2).
		Updates(map[string]interface{}{"channel_id": 2, "hardware_id": "2"}).Error; err != nil {
		t.Fatal(err)
	}

	// 同一批次里把它移回通道 1 —— 通道 1 上地址 "1" 已被设备 1 占用, "2" 空闲,
	// 所以这一次搬回是合法的 (反向钉死: 不能因为"原来在通道 2"就放行任意地址)。
	ok := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "channel_id": 1}},
	})
	if ok.Code != http.StatusOK {
		t.Fatalf("moving back with a free address must succeed, got %d: %s", ok.Code, ok.Body.String())
	}

	// 再把它移到通道 1 并抢占地址 "1" —— 必须 400 (变更后的三元组冲突)。
	clash := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "channel_id": 1, "hardware_id": "1"}},
	})
	if clash.Code != http.StatusBadRequest {
		t.Fatalf("candidate triple after changes must be checked, got %d: %s", clash.Code, clash.Body.String())
	}
	var second models.EdgeDevice
	db.First(&second, 2)
	if second.HardwareID != "2" || second.ChannelID != 1 {
		t.Fatalf("rejected batch must roll back both fields, got channel=%d hardware_id=%q", second.ChannelID, second.HardwareID)
	}
}

// G6: 批量里的非法条目必须让整批回滚 (与既有事务契约一致)。
func TestNodeConfigUpdate_DuplicateAddressRollsBackWholeBatch(t *testing.T) {
	r, db := nodeConfigTestRouter(t)
	node := seedTwoRainDevicesOnOneChannel(t, db)

	w := putNodeConfig(t, r, node.ID, map[string]interface{}{
		"edge_devices": []map[string]interface{}{
			{"id": 1, "name": "先改名"},
			{"id": 2, "hardware_id": "1"},
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
}

// 正门 / 侧门同等状态对照: 正门 (PUT /edge-devices/:id) 对同一状态给出 400,
// 侧门必须给出同样的 400 —— 两条路径的门禁不得再漂移。
func TestDuplicateAddressRejectedByBothDoors(t *testing.T) {
	r, db := twoDoorTestRouter(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", HardwareID: "UART1", Enabled: true})
	if err := db.Create(&models.EdgeDevice{Name: "A", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "1", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.EdgeDevice{Name: "B", Type: "sn3001_rain", NodeID: "NODE001", ChannelID: 1, HardwareID: "2", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}

	front := putEdgeDevice(t, r, "/api/v1/edge-devices/2", map[string]interface{}{"hardware_id": "1"})
	side := putNodeConfig(t, r, "NODE001", map[string]interface{}{
		"edge_devices": []map[string]interface{}{{"id": 2, "hardware_id": "1"}},
	})
	if front.Code != http.StatusBadRequest || side.Code != http.StatusBadRequest {
		t.Fatalf("both doors must reject the same duplicate state, front=%d side=%d (front body=%s side body=%s)",
			front.Code, side.Code, front.Body.String(), side.Body.String())
	}
	if front.Body.String() != side.Body.String() {
		t.Fatalf("the two doors must give the SAME reason (same function), front=%s side=%s", front.Body.String(), side.Body.String())
	}
}

// stringsContainsAll 是本地小工具: 断言响应体同时包含若干子串。
func stringsContainsAll(body string, wants ...string) bool {
	for _, w := range wants {
		if !strings.Contains(body, w) {
			return false
		}
	}
	return true
}

// twoDoorTestRouter 同时注册正门 (/edge-devices) 与侧门 (/nodes/:id/config),
// 用于断言两条写入路径对同一状态给出**同一个**判定。
// 两条路由必须挂在同一个 engine 上, 否则测不出"两扇门是否一致"。
func twoDoorTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.AutoMigrate(
		&models.Node{}, &models.Channel{}, &models.ConfigTemplate{},
		&models.EdgeDevice{}, &models.DeviceConfig{}, &models.DeviceData{},
		&models.UnifiedData{}, &models.User{}, &models.OTATask{},
		&models.Firmware{}, &models.Vendor{}, &models.Notification{},
		&models.DeviceModel{}, &models.NodeEvent{}, &models.CalibrationCache{},
		&models.PendingWriteRecord{}, &models.LogicalDevice{},
	)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)
	mgr := nodemgr.NewManager(db, nil, nil, nil, nil, nil, registry)
	registerEdgeDeviceRoutes(v1, db, mgr, registry)
	registerNodeRoutes(v1, db, mgr, registry)
	return r, db
}
