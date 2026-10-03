package api

import (
	"net/http"
	"testing"

	"ehome/backend/internal/models"
)

// =====================================================================
// 缺陷 P0（2026-10-03 实机定位）：同节点两条 UART 通道可抢同一对引脚。
//
// 实测证据（生产）：ch1 与 ch4 都是 hardware_id=UART1、bus_config 指向
// tx=4/rx=5，两条都创建成功（HTTP 201）。节点端 manifest 权威校验随后
// 以 "GPIO pin 4 conflict between channel 1 and channel 4" 拒收**整份**
// manifest ⇒ 该节点 0 条通道可用 ⇒ 所有指令 ACK 1001。
//
// 根因：validateChannelPeripheralConflicts 只比 GPIO/PWM 配置，
// **从不与同节点其它通道比对**。本文件锁住"创建/更新时就拒绝"。
//
// 这些用例凭什么会失败：把 validateChannelCrossChannelPins 的调用从
// validateChannelPeripheralConflicts 里去掉，第二个 POST 会回到 201。
// =====================================================================

func twoUARTCapabilities() string {
	return `{"buses":{"uart":[{"id":"UART0","port":0,"default_tx_pin":43,"default_rx_pin":44,"max_baud":5000000},{"id":"UART1","port":1,"default_tx_pin":4,"default_rx_pin":5,"max_baud":5000000}]}}`
}

func TestChannelCreate_SecondChannelOnSameUARTPinsRejected(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})

	first := createUARTChannel(t, r, "NODE001", "UART1", "04050000258001")
	if first.Code != http.StatusCreated {
		t.Fatalf("第一条 UART1 通道应创建成功，得到 %d %s", first.Code, first.Body.String())
	}

	second := createUARTChannel(t, r, "NODE001", "UART1", "04050000258001")
	if second.Code != http.StatusConflict {
		t.Fatalf("第二条抢同一引脚必须被拒（409），得到 %d %s", second.Code, second.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 1 {
		t.Fatalf("被拒的通道不得落库：channels=%d，期望 1", count)
	}
}

// 引脚不重叠 ⇒ 必须放行。防"加严过头把正常多串口配置也挡了"。
func TestChannelCreate_DistinctUARTPinsAllowed(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})

	for _, c := range []struct{ hw, cfg string }{
		{"UART1", "04050000258001"}, // tx=4  rx=5
		{"UART0", "2B2C0000258001"}, // tx=43 rx=44
	} {
		w := createUARTChannel(t, r, "NODE001", c.hw, c.cfg)
		if w.Code != http.StatusCreated {
			t.Fatalf("%s 引脚不重叠应放行，得到 %d %s", c.hw, w.Code, w.Body.String())
		}
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 2 {
		t.Fatalf("应有 2 条通道，得到 %d", count)
	}
}

// 只有 **部分** 引脚重叠也必须拒绝（tx 撞上对方的 rx）。
func TestChannelCreate_PartialPinOverlapRejected(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: "04050000258001", Enabled: true})

	// tx=5 rx=9：tx 撞上既有通道的 rx=5
	w := createUARTChannel(t, r, "NODE001", "", "05090000258001")
	if w.Code != http.StatusConflict {
		t.Fatalf("引脚部分重叠必须被拒（409），得到 %d %s", w.Code, w.Body.String())
	}
}

// 停用通道不参与抢占：否则"先建停用通道再启用"会变成无法完成的操作。
func TestChannelCreate_DisabledChannelDoesNotBlockPins(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})
	// 注意：models.Channel.Enabled 带 `gorm:"default:true"`，Create 会把显式 false
	// 写成 true（GORM 对零值套默认值）。POST /channels 自己也是靠建后 Update
	// 才真正落 false 的，这里用同一手法造出真正的停用行。
	disabled := models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: "04050000258001", Enabled: false}
	if err := db.Create(&disabled).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&models.Channel{}).Where("id = ?", disabled.ID).Update("enabled", false).Error; err != nil {
		t.Fatal(err)
	}

	w := createUARTChannel(t, r, "NODE001", "UART1", "04050000258001")
	if w.Code != http.StatusCreated {
		t.Fatalf("停用通道不应挡住引脚，得到 %d %s", w.Code, w.Body.String())
	}
}

// 不同节点用同一对引脚是合法的（各自独立的硬件）。
func TestChannelCreate_SamePinsOnDifferentNodesAllowed(t *testing.T) {
	r, db := setupDeviceTest(t)
	for _, n := range []string{"NODE001", "NODE002"} {
		db.Create(&models.Node{NodeID: n, Name: n, Status: "online", Capabilities: twoUARTCapabilities()})
		w := createUARTChannel(t, r, n, "UART1", "04050000258001")
		if w.Code != http.StatusCreated {
			t.Fatalf("%s 独立硬件应放行，得到 %d %s", n, w.Code, w.Body.String())
		}
	}
}

// 更新路径同样受保护：把 ch2 改指到 ch1 已占用的引脚必须 409。
func TestChannelUpdate_RepointingToOccupiedPinsRejected(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: "04050000258001", Enabled: true, IntervalMs: 5000})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART0", BusConfig: "2B2C0000258001", Enabled: true, IntervalMs: 5000})

	w := putChannel(t, r, 2, map[string]interface{}{"bus_config": "04050000258001"})
	if w.Code != http.StatusConflict {
		t.Fatalf("更新到已占用引脚必须被拒（409），得到 %d %s", w.Code, w.Body.String())
	}
}

// 更新自身引脚（同值重写）不得被自己挡住。
func TestChannelUpdate_KeepingOwnPinsAllowed(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "T", Status: "online", Capabilities: twoUARTCapabilities()})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: "04050000258001", Enabled: true, IntervalMs: 5000})

	w := putChannel(t, r, 1, map[string]interface{}{"bus_config": "04050000258001", "interval_ms": 9000})
	if w.Code != http.StatusOK {
		t.Fatalf("同值重写不应被自己挡住，得到 %d %s", w.Code, w.Body.String())
	}
}
