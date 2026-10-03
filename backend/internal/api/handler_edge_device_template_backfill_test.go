package api

import (
	"net/http"
	"strconv"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 现场缺陷修复回归（2026-10-03，edge_devices.id=4 "逆变器"）
// ========================================================
//
// 现象：UART1 上的泰琪丰逆变器从未收到任何指令（用户观察到 TTL↔RS232 转接板
// 的 Tx/Rx 灯从未亮过，证明固件根本没在 UART1 上发过字节）。根因链见
// drivers/inverter_techfine.go 的 GetCommandTemplates 注释与
// nodemgr/inverter_polling_regression_test.go。
//
// 本条测试守的是**修复路径**：ConfigTemplates 原先只在 POST /edge-devices
// 创建时生成。于是"设备先建好、驱动之后才获得可调度模板"的存量设备永远拿不到
// 模板 —— 现场逆变器正是这种状态（channels.template_ids=''，config_templates
// 一条都没有）。PUT 路径必须能补齐，否则用户唯一的补救手段是删掉设备重建，
// 而这会丢失历史数据与设备 id。

func seedInverterWithoutTemplates(t *testing.T) (*gin.Engine, *gorm.DB, models.EdgeDevice) {
	t.Helper()
	r, db := setupEdgeDeviceTest(t)
	if err := db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	if err := db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", Enabled: true}).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	// 模拟存量逆变器：设备已存在、无归属模板、channel.template_ids 为空，
	// 与现场 channels.id=4 / edge_devices.id=4 完全一致。
	dev := models.EdgeDevice{NodeID: "NODE001", Name: "逆变器", Type: "techfine_inverter",
		ChannelID: 1, Enabled: true}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create inverter device: %v", err)
	}
	return r, db, dev
}

func TestEdgeDevice_Update_BackfillsMissingConfigTemplates(t *testing.T) {
	r, db, dev := seedInverterWithoutTemplates(t)

	var before int64
	db.Model(&models.ConfigTemplate{}).Where("edge_device_id = ?", dev.ID).Count(&before)
	if before != 0 {
		t.Fatalf("fixture must start with 0 owned templates, got %d", before)
	}

	// 一次普通的 PUT（仅改名）就应当顺带补齐模板。
	w := putEdgeDevice(t, r, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10),
		map[string]interface{}{"name": "逆变器-改名"})
	if w.Code != http.StatusOK {
		t.Fatalf("PUT should succeed, got %d: %s", w.Code, w.Body.String())
	}

	var after int64
	db.Model(&models.ConfigTemplate{}).Where("edge_device_id = ?", dev.ID).Count(&after)
	if after == 0 {
		t.Fatal("PUT did not backfill ConfigTemplates — a pre-existing device whose driver " +
			"later gained schedulable templates can never be polled (this is the 2026-10-03 " +
			"inverter field defect: UART1 never transmits)")
	}

	// channel.template_ids 必须同步填充：manifest 编码器解析模板走的是
	// channel.template_ids，而不是直接查 config_templates 表。
	var ch models.Channel
	if err := db.First(&ch, dev.ChannelID).Error; err != nil {
		t.Fatalf("reload channel: %v", err)
	}
	if ch.TemplateIDs == "" {
		t.Fatal("channel.template_ids still empty after backfill — the manifest encoder " +
			"resolves templates through channel.template_ids, so 0 commands would be encoded")
	}
}

func TestEdgeDevice_Update_DoesNotDuplicateTemplates(t *testing.T) {
	r, db, dev := seedInverterWithoutTemplates(t)
	path := "/api/v1/edge-devices/" + strconv.FormatUint(uint64(dev.ID), 10)

	// 第一次 PUT 补齐模板。
	if w := putEdgeDevice(t, r, path, map[string]interface{}{"name": "逆变器-A"}); w.Code != http.StatusOK {
		t.Fatalf("first PUT failed: %d %s", w.Code, w.Body.String())
	}
	var first int64
	db.Model(&models.ConfigTemplate{}).Where("edge_device_id = ?", dev.ID).Count(&first)
	if first == 0 {
		t.Fatal("first PUT created no templates")
	}

	// 后续多次 PUT 不得重复插入：重复会把 template_ids 撑长，而编码器上限
	// 是 16 条 —— 用户多点几次"保存"就可能让整包 manifest 被拒
	// （2026-09-17 生产事故形态）。
	for i := 0; i < 3; i++ {
		if w := putEdgeDevice(t, r, path,
			map[string]interface{}{"name": "逆变器-B" + strconv.Itoa(i)}); w.Code != http.StatusOK {
			t.Fatalf("repeat PUT #%d failed: %d %s", i+1, w.Code, w.Body.String())
		}
	}
	var after int64
	db.Model(&models.ConfigTemplate{}).Where("edge_device_id = ?", dev.ID).Count(&after)
	if after != first {
		t.Fatalf("templates duplicated by repeated PUT: %d -> %d (idempotency broken, "+
			"and the encoder limit is 16)", first, after)
	}
}
