package databus

// 摄入边界必须校验「帧来源节点」与「边缘设备所属节点」一致（纵深防御，回归锁）。
//
// 缺口（docs/设计/场景仿真验证框架.md §4.1b 记载为「产品侧观察，不在本期范围，留待裁决」）：
//   解析设备时，`evt.EdgeDeviceID > 0` 分支**只按 id 查**，不校验该设备是否属于
//   帧来源主题里的那个节点。
//   后果：任何能向 `nodes/<任意>/up` 发布的 MQTT 客户端，只要猜中一个数字 edge_device_id，
//   就能**以别的节点的名义**写入数据 —— 落库、告警、自动化、WS 推送全部按那台设备走。
//   生产靠 broker 认证限制发布者，但那是**唯一**一道防线；文档明确称之为「纵深防御的缺口」。
//
// 文档给的修法：摄入时校验 `edge_device.node_id == evt.DeviceID`。
//
// 为什么本测试必须断言「没有落库」而不只是「有日志」：
//   这条路径的危害是**数据被写进错误的设备**，只看日志无法证明没有污染；
//   因此断言 unified_data 里**一条都不该有**。

import (
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// newNodeBindingTestDB 只迁移本文件需要的表。
func newNodeBindingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.UnifiedData{}, &models.DeviceData{}, &models.CalibrationCache{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestIngest_RejectsCrossNodeEdgeDeviceID 是本次加固的核心断言。
func TestIngest_RejectsCrossNodeEdgeDeviceID(t *testing.T) {
	db := newNodeBindingTestDB(t)

	// 两个互不相关的节点，各有一台设备。
	victimNode := models.Node{NodeID: "victim-node"}
	if err := db.Create(&victimNode).Error; err != nil {
		t.Fatal(err)
	}
	attackerNode := models.Node{NodeID: "attacker-node"}
	if err := db.Create(&attackerNode).Error; err != nil {
		t.Fatal(err)
	}
	chVictim := models.Channel{NodeID: victimNode.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&chVictim).Error; err != nil {
		t.Fatal(err)
	}
	chAttacker := models.Channel{NodeID: attackerNode.NodeID, HardwareID: "I2C0"}
	if err := db.Create(&chAttacker).Error; err != nil {
		t.Fatal(err)
	}

	// victim 的设备：攻击者想冒用它的身份写数据。
	victimDev := models.EdgeDevice{
		Name: "victim-sensor", NodeID: victimNode.NodeID, ChannelID: chVictim.ID,
		Type: "plain_test", Status: "active",
	}
	if err := db.Create(&victimDev).Error; err != nil {
		t.Fatal(err)
	}
	// 攻击者自己的设备（合法存在，但**不属于** victim 节点）。
	attackerDev := models.EdgeDevice{
		Name: "attacker-sensor", NodeID: attackerNode.NodeID, ChannelID: chAttacker.ID,
		Type: "plain_test", Status: "active",
	}
	if err := db.Create(&attackerDev).Error; err != nil {
		t.Fatal(err)
	}

	consumer := newSensorParserTestConsumer(db, passthroughReassembler{}, &plainTestDriver{})

	// 攻击：主题里的节点是 attacker-node，但携带 victim 设备的 id。
	consumer.Handle(DataEvent{
		DeviceID:     attackerNode.NodeID,  // ← 帧来源主题声明的节点
		EdgeDeviceID: uint64(victimDev.ID), // ← 冒用他人的设备 id
		RequestID:    1,
		RawData:      []byte{0x01, 0x02, 0x03, 0x04},
	})

	// 断言：victim 设备名下不得产生任何数据。
	var victimRows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", victimDev.ID).Count(&victimRows)
	if victimRows != 0 {
		t.Errorf("跨节点冒用未被拦截：victim 设备(id=%d) 名下出现 %d 条数据 —— "+
			"任何能向 nodes/<任意>/up 发布的客户端都能伪造他人设备数据（§4.1b 记载的纵深防御缺口）",
			victimDev.ID, victimRows)
	}

	// 反向对照：同样这帧若声明的是**自己**的设备，必须正常落库 ——
	// 否则说明我加的校验把所有写入都挡住了（那不是加固，是把功能改坏）。
	consumer.Handle(DataEvent{
		DeviceID:     attackerNode.NodeID,
		EdgeDeviceID: uint64(attackerDev.ID),
		RequestID:    2,
		RawData:      []byte{0x01, 0x02, 0x03, 0x04},
	})
	var ownRows int64
	db.Model(&models.UnifiedData{}).Where("device_id = ?", attackerDev.ID).Count(&ownRows)
	if ownRows == 0 {
		t.Errorf("同节点上报被误拦：attacker 自己的设备(id=%d) 一条数据都没落 —— "+
			"校验必须是「节点不匹配才拒」，不能变成「一律拒」", attackerDev.ID)
	}
}
