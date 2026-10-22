//go:build e2e

package e2e

// 来源组全链路端到端验证（Lead 侧贯通验证，方案 v0.3 §8.3 E1）。
//
// 与 source_group_e2e_test.go 的分工：
//   - 那份由 edge-analyst 写，覆盖纯函数层契约与逐条 E 用例；
//   - 本文件是**一条贯通验证**：真实数据面（databus + SensorParserConsumer）
//     → 真实 GroupService（快照发布）→ 真实 sourceref.Registry
//     → 真实 automation.Evaluator（按 kind 分流 + 复合整组求值）→ 真实事件落库。
//     中间不 mock 任何一环。
//
// 为什么必须有：单测各自绿但接线错（sink 只喂一个引擎、快照忘了发布、resolver
// 注入晚于求值）时，单测全绿而线上功能不可用。这条用例的作用就是「接线错了必红」。

import (
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/api"
	"ehome/backend/internal/automation"
	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/databus"
	"ehome/backend/internal/datasource"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/parser"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// sgFullChain 是一条最小但完整的光照链路：两台 prs3001（跨逻辑设备）+ 一个来源组。
type sgFullChain struct {
	db       *gorm.DB
	node     models.Node
	channel  models.Channel
	logicalA models.LogicalDevice
	logicalB models.LogicalDevice
	edgeA    models.EdgeDevice
	edgeB    models.EdgeDevice
	gsvc     *datasource.GroupService
	reg      *sourceref.Registry
	eval     *automation.Evaluator
	bus      *databus.DataEventBus
	done     *signalingConsumer
}

// newSGChain 建两台光照设备 + 数据面 + 引擎，全部走真实构造。
func newSGFullChain(t *testing.T, rules ...*models.AutomationRule) *sgFullChain {
	t.Helper()
	db := testutil.OpenTestDB(t)
	c := &sgFullChain{db: db}

	// 驱动自检：prs3001 必须真的声明并产出 illuminance。
	// 防「声明≠产出」假绿：若只声明不解析，后面所有断言都在空值上空转。
	drv := &drivers.PRS3001Driver{}
	var hasIllum bool
	for _, d := range drv.GetSensorDefinitions() {
		if d.Name == "illuminance" {
			hasIllum = true
		}
	}
	if !hasIllum {
		t.Fatal("prs3001 未声明 illuminance：光照场景的夹具前提不成立")
	}
	sample, err := drv.ParseData(prs3001Frame4B(0, 600))
	if err != nil {
		t.Fatalf("prs3001 ParseData 失败（夹具帧不合法）: %v", err)
	}
	var gotIllum float64 = -1
	for _, s := range sample {
		if s.Name == "illuminance" {
			gotIllum = s.Value
		}
	}
	if gotIllum != 600 {
		t.Fatalf("prs3001 4 字节帧解析 illuminance = %v, want 600：声明与产出不一致", gotIllum)
	}

	reported := time.Now().UTC()
	c.node = models.Node{
		NodeID: "sg-e2e", Model: "ESP32S3", FirmwareVersion: "1.0.0", Status: "online",
		ConfigVersion: "manifest-sg", ConfigStatus: "applied", ConfigSyncState: "in_sync",
		BootID: "boot-sg", ResourceReportedAt: &reported, CommandEngineRevision: 1,
		CommandEngineCapabilities: E2ECapabilitiesJSON,
	}
	if err := db.Create(&c.node).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	c.channel = models.Channel{NodeID: c.node.NodeID, HardwareID: "1", IntervalMs: 1000, Enabled: true}
	if err := db.Create(&c.channel).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	hw := fmt.Sprintf("{\"channels\":[{\"id\":%d,\"enabled\":true}]}", c.channel.ID)
	if err := db.Model(&models.Node{}).Where("id = ?", c.node.ID).Update("hardware_info", hw).Error; err != nil {
		t.Fatalf("set hardware_info: %v", err)
	}

	// 两台**跨逻辑设备**的光照传感器：来源组的意义就是把不同逻辑设备上的
	// 同类传感器合成一个来源（同逻辑设备装两台会被 v3.3 单存活实例约束挡住）。
	c.logicalA = models.LogicalDevice{IdentityKey: "prs3001:sg-e2e:A:1", Name: "光照A", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&c.logicalA).Error; err != nil {
		t.Fatalf("create logical A: %v", err)
	}
	c.logicalB = models.LogicalDevice{IdentityKey: "prs3001:sg-e2e:B:1", Name: "光照B", DeviceType: "prs3001", RetentionDays: 90}
	if err := db.Create(&c.logicalB).Error; err != nil {
		t.Fatalf("create logical B: %v", err)
	}
	c.edgeA = models.EdgeDevice{Name: "光照A", NodeID: c.node.NodeID, ChannelID: c.channel.ID,
		Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &c.logicalA.ID, IntervalMs: 1000}
	if err := db.Create(&c.edgeA).Error; err != nil {
		t.Fatalf("create edge A: %v", err)
	}
	c.edgeB = models.EdgeDevice{Name: "光照B", NodeID: c.node.NodeID, ChannelID: c.channel.ID,
		Type: "prs3001", Enabled: true, Status: "active", LogicalDeviceID: &c.logicalB.ID, IntervalMs: 1000}
	if err := db.Create(&c.edgeB).Error; err != nil {
		t.Fatalf("create edge B: %v", err)
	}

	// 字段解析层（enabled=true，selector 走编译产物）
	sreg := sensorfield.New(sensorfield.Config{Enabled: true})
	drvReg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(drvReg)
	if d, e := drvReg.Get("prs3001"); e == nil {
		var decls []sensorfield.Field
		for _, sd := range d.GetSensorDefinitions() {
			decls = append(decls, sensorfield.Field{Name: sd.Name, Unit: sd.Unit})
		}
		sreg.RegisterDeclarations("prs3001", decls)
	}
	sreg.SetDeviceType(c.edgeA.ID, "prs3001")
	sreg.SetDeviceType(c.edgeB.ID, "prs3001")

	// 来源组服务 + 注册表（快照唯一发布者）
	c.reg = sourceref.NewRegistry()
	c.gsvc = datasource.NewGroupService(db, datasource.Options{})
	c.gsvc.SetGroupRegistry(c.reg)

	for _, rule := range rules {
		if err := db.Create(rule).Error; err != nil {
			t.Fatalf("create rule: %v", err)
		}
	}
	actions := deviceaction.NewBuiltInRegistry(drvReg)
	cmdSvc := commandexec.NewService(db, actions)
	cmdSvc.SetDispatchEnabled(true)
	planner := automation.NewPlanner(db, cmdSvc, func(string, any) {}, 0)
	planner.SetLatestValueBySensorFn(api.LatestValueBySensor)
	planner.SetLatestValuesFn(api.LatestValues)
	planner.SetSourceResolver(c.reg)
	c.eval = automation.NewEvaluator(db, planner)
	c.eval.SetSourceResolver(c.reg)
	c.eval.SetLatestValueBySensorFn(api.LatestValueBySensor)
	c.eval.SetDeviceIntervalFn(func(uint) time.Duration { return time.Second })

	// 数据面：真实 consumer + 真实 sink
	inner := databus.NewSensorParserConsumerWithRegistry(db, nil, passthroughReassembler{}, drvReg, func(uint) {})
	inner.SetLatestSink(func(rec models.UnifiedData) {
		api.SetLatestValue(rec)
		sreg.Observe(rec.DeviceID, rec.SensorName, rec.Unit, rec.Timestamp)
	})
	inner.SetAutomationSink(func(edgeDeviceID uint, fields []parser.Field, at time.Time) {
		c.eval.Evaluate(edgeDeviceID, fields, at)
	})
	// 数据源健康：两个引擎都要喂（漏一边那个引擎永不切换）
	inner.SetSourceHealthSink(func(edgeDeviceID uint, names []string, at time.Time) {
		c.gsvc.MarkSuccess(edgeDeviceID, names, at)
	})
	c.done = &signalingConsumer{inner: inner}
	c.bus = databus.NewDataEventBus()
	c.bus.Register(c.done)
	t.Cleanup(c.bus.Stop)
	return c
}

// publish 以指定边缘设备身份上报一帧，并等消费完成（同步化，不睡猜）。
func (c *sgFullChain) publish(t *testing.T, dev models.EdgeDevice, frame []byte, requestID uint64) {
	t.Helper()
	before := c.done.done.Load()
	c.bus.Publish(databus.DataEvent{
		DeviceID: c.node.NodeID, ChannelID: uint64(c.channel.ID),
		EdgeDeviceID: uint64(dev.ID), RequestID: requestID,
		RawData: frame, ReceivedAt: time.Now(),
	})
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if c.done.done.Load() > before {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("数据面未在 5s 内处理完该帧 (request_id=%d)", requestID)
}

// events 读该规则的全部自动化事件（按 id 升序）。
func (c *sgFullChain) events(t *testing.T, ruleID uint) []models.AutomationEvent {
	t.Helper()
	var out []models.AutomationEvent
	if err := c.db.Where("rule_id = ?", ruleID).Order("id").Find(&out).Error; err != nil {
		t.Fatalf("query automation_events: %v", err)
	}
	return out
}
