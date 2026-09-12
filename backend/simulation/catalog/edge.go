//go:build simulation

// 场景目录 · SIM-EDGE 边缘设备与逻辑设备（设计 §9 SIM-EDGE-001..006）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
//
// 本文件同时承载 SIM-EDGE / SIM-DS / SIM-CMD 三个域共用的
// 「节点 + 通道 + 设备配置 + 边缘设备 + MQTT 仿真节点」夹具：
// 三个文件同属 package catalog，共用一套前置数据搭建，避免三份重复实现。
// 夹具一律走真实 HTTP 端点（§3 原则 2：不为造数直连数据库），
// 只有 MQTT 上行经 harness.Device（§5.3 冻结的仿真器）。
//
// 命名纪律（设计 §4.1）：本文件的包级标识符一律以域短名 edge 开头，
// 避免与同包其它域文件撞名。
package catalog

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// 域标识：设计 v1.1 冻结 Domain 取 §6 表"前缀"列的短名（去 SIM-），
// 且不变量 ID == "SIM-" + string(Domain) + "-" + NNN 由目录门禁校验。
const edgeDomain Domain = "EDGE"

func init() {
	Register(Scenario{
		ID:     "SIM-EDGE-001",
		Title:  "管理员添加新设备时，可以在候选列表里看到这台设备以前用过的同型号记录",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-001；docs/设计/边缘设备数据生命周期.md §1.3",
		Run:    edgeRun001,
	})
	Register(Scenario{
		ID:     "SIM-EDGE-002",
		Title:  "把新传感器绑定到节点和通道后，管理员能在设备列表里查到它",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-002；docs/设计/边缘设备.md",
		Run:    edgeRun002,
	})
	Register(Scenario{
		ID:     "SIM-EDGE-003",
		Title:  "在同一节点同一地址重复添加同一型号设备会被拒绝，并说明冲突原因",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-003；docs/设计/边缘设备.md",
		Run:    edgeRun003,
	})
	Register(Scenario{
		ID:     "SIM-EDGE-004",
		Title:  "停用某台边缘设备后，节点不再采集它",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-004；docs/设计/同步机制.md",
		Run:    edgeRun004,
	})
	Register(Scenario{
		ID:     "SIM-EDGE-005",
		Title:  "重新启用被停用的边缘设备后，节点恢复采集它",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-005；docs/设计/同步机制.md",
		Run:    edgeRun005,
	})
	Register(Scenario{
		ID:     "SIM-EDGE-006",
		Title:  "管理员把两路历史设备合并到同一个逻辑设备下，并能看到合并进度",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-006；docs/设计/边缘设备数据生命周期.md §3.4",
		Run:    edgeRun006,
	})
}

// ---------------------------------------------------------------------------
// SIM-EDGE-001 通过候选发现接口列出节点下挂载的边缘设备
// ---------------------------------------------------------------------------

// edgeDeviceCandidateRow 是 GET /edge-devices/candidates 的响应行
// （api/handler_edge_device_candidates.go 的 logicalDeviceCandidate）。
type edgeDeviceCandidateRow struct {
	ID            uint   `json:"id"`
	Name          string `json:"name"`
	DeviceType    string `json:"device_type"`
	InstanceCount int64  `json:"instance_count"`
	MatchWeight   int    `json:"match_weight"`
}

func edgeRun001(e *harness.Env) {
	t := e.T
	fx := edgeProvision(e, edgeDeviceSpec{
		Scenario:   "SIM-EDGE-001",
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
	})

	// 型号是候选发现的必填维度：没有它就无从判断"哪条历史记录可以继承"。
	e.Admin.Get("/api/v1/edge-devices/candidates").Expect(http.StatusBadRequest)

	path := fmt.Sprintf("/api/v1/edge-devices/candidates?type=%s&node_id=%s&channel_id=%d&hardware_id=%s",
		fx.Type, fx.NodeID, fx.ChannelID, fx.HardwareID)
	resp := e.Admin.Get(path).Expect(http.StatusOK)
	var rows []edgeDeviceCandidateRow
	resp.Decode(&rows)
	e.Evidence("SIM-EDGE-001.candidates", rows)

	// 不变量：同型号设备的逻辑身份必须出现在候选里，且"同节点 + 同通道 +
	// 同地址 + 同型号"是最强匹配档位（权重 100，即"原位置重建"）。
	var hit *edgeDeviceCandidateRow
	for i := range rows {
		if int64(rows[i].ID) == fx.LogicalDeviceID {
			hit = &rows[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("候选列表里没有本场景创建的逻辑设备 #%d（type=%s node=%s channel=%d hw=%s）：%+v",
			fx.LogicalDeviceID, fx.Type, fx.NodeID, fx.ChannelID, fx.HardwareID, rows)
	}
	if hit.DeviceType != fx.Type {
		t.Fatalf("候选 #%d 的 device_type = %q，期望 %q", hit.ID, hit.DeviceType, fx.Type)
	}
	if hit.MatchWeight != 100 {
		t.Fatalf("候选 #%d 的 match_weight = %d，期望 100（同节点+同通道+同地址+同型号）", hit.ID, hit.MatchWeight)
	}
	if hit.InstanceCount < 1 {
		t.Fatalf("候选 #%d 的 instance_count = %d，期望 ≥1（刚创建的实例）", hit.ID, hit.InstanceCount)
	}
}

// ---------------------------------------------------------------------------
// SIM-EDGE-002 创建边缘设备并绑定节点与通道后可在列表查询到
// ---------------------------------------------------------------------------

// edgeDeviceRow 是 GET /edge-devices 列表项的关键字段（models.EdgeDevice）。
type edgeDeviceRow struct {
	ID              int64  `json:"id"`
	Name            string `json:"name"`
	NodeID          string `json:"node_id"`
	ChannelID       int64  `json:"channel_id"`
	Type            string `json:"type"`
	HardwareID      string `json:"hardware_id"`
	Enabled         bool   `json:"enabled"`
	IntervalMs      int    `json:"interval_ms"`
	LogicalDeviceID *int64 `json:"logical_device_id"`
}

func edgeRun002(e *harness.Env) {
	t := e.T
	fx := edgeProvision(e, edgeDeviceSpec{
		Scenario:   "SIM-EDGE-002",
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
		IntervalMs: 7000,
	})

	byNode := e.Admin.Get("/api/v1/edge-devices?node_id=" + fx.NodeID).Expect(http.StatusOK)
	var rows []edgeDeviceRow
	byNode.Decode(&rows)
	e.Evidence("SIM-EDGE-002.list_by_node", rows)

	var hit *edgeDeviceRow
	for i := range rows {
		if rows[i].ID == fx.EdgeDeviceID {
			hit = &rows[i]
			break
		}
	}
	if hit == nil {
		t.Fatalf("按 node_id=%s 过滤后没有本场景创建的边缘设备 #%d：%+v", fx.NodeID, fx.EdgeDeviceID, rows)
	}
	// 不变量：绑定关系（节点 + 通道 + 型号 + 地址）必须原样可查回，
	// 否则用户在列表里看到的就是一台"没接在总线上"的设备。
	if hit.ChannelID != fx.ChannelID {
		t.Fatalf("边缘设备 #%d 的 channel_id = %d，期望 %d", hit.ID, hit.ChannelID, fx.ChannelID)
	}
	if hit.NodeID != fx.NodeID {
		t.Fatalf("边缘设备 #%d 的 node_id = %q，期望 %q", hit.ID, hit.NodeID, fx.NodeID)
	}
	if hit.Type != fx.Type || hit.HardwareID != fx.HardwareID {
		t.Fatalf("边缘设备 #%d 的 type/hardware_id = %q/%q，期望 %q/%q",
			hit.ID, hit.Type, hit.HardwareID, fx.Type, fx.HardwareID)
	}
	if hit.IntervalMs != 7000 {
		t.Fatalf("边缘设备 #%d 的 interval_ms = %d，期望 7000", hit.ID, hit.IntervalMs)
	}
	if hit.LogicalDeviceID == nil || *hit.LogicalDeviceID != fx.LogicalDeviceID {
		t.Fatalf("边缘设备 #%d 的 logical_device_id = %v，期望 %d（每台设备都必须有逻辑身份）",
			hit.ID, hit.LogicalDeviceID, fx.LogicalDeviceID)
	}

	// 列表页的筛选条件（型号 + 状态）同样必须命中。
	filtered := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices?node_id=%s&device_type=%s&status=active",
		fx.NodeID, fx.Type)).Expect(http.StatusOK)
	var active []edgeDeviceRow
	filtered.Decode(&active)
	found := false
	for _, row := range active {
		if row.ID == fx.EdgeDeviceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("device_type+status 过滤后没有边缘设备 #%d：%+v", fx.EdgeDeviceID, active)
	}

	// 详情页与列表必须一致。
	detail := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d", fx.EdgeDeviceID)).Expect(http.StatusOK)
	var one edgeDeviceRow
	detail.Decode(&one)
	if one.ID != fx.EdgeDeviceID || one.ChannelID != fx.ChannelID {
		t.Fatalf("详情与列表不一致：detail=%+v list=%+v", one, *hit)
	}
}

// ---------------------------------------------------------------------------
// SIM-EDGE-003 重复创建同节点同地址的边缘设备返回冲突
// ---------------------------------------------------------------------------

func edgeRun003(e *harness.Env) {
	t := e.T
	fx := edgeProvision(e, edgeDeviceSpec{
		Scenario:   "SIM-EDGE-003",
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
	})

	// (a) 同一通道上同型号同地址：必须被拒绝，且原因必须指向地址冲突
	// （不能只说"参数错误"——用户要知道是哪个地址被占了）。
	dup := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-003", "重复"),
		"node_id":     fx.NodeID,
		"channel_id":  fx.ChannelID,
		"type":        fx.Type,
		"hardware_id": fx.HardwareID,
		"enabled":     true,
	}).Expect(http.StatusBadRequest)
	if !strings.Contains(dup.Message, "already hosts") {
		t.Fatalf("重复设备的拒绝原因没有指出地址冲突：message=%q body=%s", dup.Message, dup.BodyString())
	}
	e.Evidence("SIM-EDGE-003.duplicate_rejected", dup.Message)

	// (b) 真正的"冲突"语义（409）：把新设备挂到一台已存在存活实例的逻辑设备上
	// —— 同一逻辑身份不允许同时有两台存活设备。
	inherit := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":              e.NS("SIM-EDGE-003", "继承"),
		"node_id":           fx.NodeID,
		"channel_id":        fx.ChannelID,
		"type":              fx.Type,
		"hardware_id":       "0x99",
		"enabled":           true,
		"logical_device_id": fx.LogicalDeviceID,
	}).Expect(http.StatusConflict)
	if !strings.Contains(inherit.Message, "存活实例") {
		t.Fatalf("继承冲突的说明不完整：message=%q body=%s", inherit.Message, inherit.BodyString())
	}
	e.Evidence("SIM-EDGE-003.inherit_conflict", inherit.Message)

	// 不变量：两次被拒之后，节点下仍然只有本场景创建的那一台设备，
	// 拒绝必须是真的没有落库（不能"报错但建了"）。
	list := e.Admin.Get("/api/v1/edge-devices?node_id=" + fx.NodeID).Expect(http.StatusOK)
	var rows []edgeDeviceRow
	list.Decode(&rows)
	if len(rows) != 1 || rows[0].ID != fx.EdgeDeviceID {
		t.Fatalf("被拒的两次创建留下了残留设备：%+v", rows)
	}
}

// ---------------------------------------------------------------------------
// SIM-EDGE-004 / SIM-EDGE-005 停用 → 节点不再采集；重新启用 → 恢复采集
// ---------------------------------------------------------------------------

func edgeRun004(e *harness.Env) { edgeRunEnableToggle(e, "SIM-EDGE-004", false) }
func edgeRun005(e *harness.Env) { edgeRunEnableToggle(e, "SIM-EDGE-005", true) }

// edgeRunEnableToggle 是 004/005 的公共实现。
//
// 为什么这样断言：停用一台边缘设备对"数据是否还入库"没有影响（databus 的
// 解析链路不读 edge_devices.enabled，见交付报告的偏差清单），用户能观察到的
// 真实效果是**节点侧不再采集它**。配置清单是这件事的唯一权威来源：
// nodemgr/sender_snapshot.go 的 loadManifestSnapshot 只加载 enabled=true 的
// 边缘设备，因此停用后该设备的整组采集指令会从清单里消失，节点自然不再轮询它。
//
// 于是本场景断言两层：
//  1. 管理面：PUT /edge-devices/:id 的 enabled 被持久化并可查回；
//  2. 设备面：强制下发一次配置清单，节点在 control 主题收到的清单帧里，
//     该设备的采集指令"在启用时必须存在且开启 / 在停用时必须整组消失"。
func edgeRunEnableToggle(e *harness.Env, scenarioID string, reenable bool) {
	t := e.T
	fx := edgeProvision(e, edgeDeviceSpec{
		Scenario:   scenarioID,
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
		Handshake:  true, // 配置清单的 v2 通道段要求节点协议版本 ≥ 2.3（Hello 才会写入）
	})

	// 起点：设备必须是启用状态，否则"停用后不再采集"无从对比。
	if !edgeEnabled(e, fx.EdgeDeviceID) {
		t.Fatalf("前置条件不成立：边缘设备 #%d 初始 enabled=false", fx.EdgeDeviceID)
	}
	edgeAssertManifestSampling(e, fx, true, "启用状态")

	// —— 停用 ——
	e.Admin.Put(fmt.Sprintf("/api/v1/edge-devices/%d", fx.EdgeDeviceID),
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	if edgeEnabled(e, fx.EdgeDeviceID) {
		t.Fatalf("停用后管理面仍显示 enabled=true（边缘设备 #%d）", fx.EdgeDeviceID)
	}
	edgeAssertManifestSampling(e, fx, false, "停用状态")
	e.Evidence(scenarioID+".disabled_edge_device_id", fx.EdgeDeviceID)

	if !reenable {
		return
	}

	// —— 重新启用（SIM-EDGE-005）——
	e.Admin.Put(fmt.Sprintf("/api/v1/edge-devices/%d", fx.EdgeDeviceID),
		map[string]any{"enabled": true}).Expect(http.StatusOK)
	if !edgeEnabled(e, fx.EdgeDeviceID) {
		t.Fatalf("重新启用后管理面仍显示 enabled=false（边缘设备 #%d）", fx.EdgeDeviceID)
	}
	edgeAssertManifestSampling(e, fx, true, "重新启用状态")
	e.Evidence(scenarioID+".reenabled_edge_device_id", fx.EdgeDeviceID)
}

// edgeEnabled 读取边缘设备当前的启用开关（管理面）。
func edgeEnabled(e *harness.Env, edgeDeviceID int64) bool {
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d", edgeDeviceID)).Expect(http.StatusOK)
	var row edgeDeviceRow
	resp.Decode(&row)
	if row.ID != edgeDeviceID {
		e.T.Fatalf("读取边缘设备 #%d 得到 id=%d", edgeDeviceID, row.ID)
	}
	return row.Enabled
}

// edgeAssertManifestSampling 强制下发一次配置清单，并断言节点真实收到的
// 清单帧里该边缘设备的采集指令状态等于 want。
func edgeAssertManifestSampling(e *harness.Env, fx *edgeDevice, want bool, label string) {
	t := e.T
	t.Helper()
	after := fx.Device.FrameSeq()
	e.Admin.Post(fmt.Sprintf("/api/v1/nodes/%s/config/sync", fx.NodeID), nil).Expect(http.StatusOK)
	raw, err := fx.Device.AwaitRawAfter(frame.MsgConfigMfst, after, 20*time.Second)
	if err != nil {
		t.Fatalf("等待配置清单帧失败（%s）: %v", label, err)
	}
	enabled, found, err := edgeManifestSampling(raw, fx.EdgeDeviceID)
	if err != nil {
		t.Fatalf("解析配置清单帧失败（%s）: %v", label, err)
	}
	if want {
		if !found {
			t.Fatalf("配置清单里没有边缘设备 #%d 的采集指令（%s）——节点不会被调度去采集它",
				fx.EdgeDeviceID, label)
		}
		if !enabled {
			t.Fatalf("配置清单里边缘设备 #%d 的采集开关为关闭（%s）", fx.EdgeDeviceID, label)
		}
	} else if found {
		t.Fatalf("停用后配置清单里仍然带着边缘设备 #%d 的采集指令（enabled=%v，%s）——节点还在采集它",
			fx.EdgeDeviceID, enabled, label)
	}
	e.Evidence("SIM-EDGE.manifest_"+label, map[string]any{
		"edge_device_id": fx.EdgeDeviceID, "found": found, "enabled": enabled,
	})
}

// ---------------------------------------------------------------------------
// SIM-EDGE-006 创建逻辑设备并把多路边缘设备合并到一个逻辑设备
// ---------------------------------------------------------------------------

// edgeLogicalDeviceRow 是 GET /logical-devices 列表项（api/handler_logical_device.go）。
type edgeLogicalDeviceRow struct {
	ID            uint    `json:"id"`
	Name          string  `json:"name"`
	DeviceType    string  `json:"device_type"`
	MergedInto    *uint   `json:"merged_into"`
	MergeStatus   *string `json:"merge_status"`
	InstanceCount int64   `json:"instance_count"`
}

func edgeRun006(e *harness.Env) {
	t := e.T
	a := edgeProvision(e, edgeDeviceSpec{
		Scenario: "SIM-EDGE-006", Local: "a", Suffix: "a",
		DeviceType: "sn3001_rain", HardwareID: "1",
	})
	b := edgeProvision(e, edgeDeviceSpec{
		Scenario: "SIM-EDGE-006", Local: "b", Suffix: "b",
		DeviceType: "sn3001_rain", HardwareID: "1",
	})
	if a.LogicalDeviceID == b.LogicalDeviceID {
		t.Fatalf("两台独立设备不应共用逻辑身份：a=%d b=%d", a.LogicalDeviceID, b.LogicalDeviceID)
	}

	// 合并的前提是源身份下没有存活实例（设备已被替换/退役），
	// 因此先按真实生命周期把两台设备删除（软删，历史数据保留）。
	e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", a.EdgeDeviceID)).Expect(http.StatusOK)
	e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", b.EdgeDeviceID)).Expect(http.StatusOK)

	// 合并预览：用户点"合并"之前必须先看到各来源的时间范围与预估数据量。
	preview := e.Admin.Post("/api/v1/logical-devices/merge/preview", map[string]any{
		"source_ids": []int64{a.LogicalDeviceID, b.LogicalDeviceID},
	}).Expect(http.StatusOK)
	var pv struct {
		Sources []struct {
			ID         uint   `json:"id"`
			Name       string `json:"name"`
			DeviceType string `json:"device_type"`
		} `json:"sources"`
		TargetRetentionDays int `json:"target_retention_days"`
	}
	preview.Decode(&pv)
	if len(pv.Sources) != 2 {
		t.Fatalf("合并预演应返回 2 个来源，实际 %d：%+v", len(pv.Sources), pv)
	}
	e.Evidence("SIM-EDGE-006.preview", pv)

	// 真正合并：单事务冻结目标身份 + 每源一个搬迁任务。
	targetName := e.NS("SIM-EDGE-006", "合并目标")
	merged := e.Admin.Post("/api/v1/logical-devices/merge", map[string]any{
		"target_name": targetName,
		"source_ids":  []int64{a.LogicalDeviceID, b.LogicalDeviceID},
	}).Expect(http.StatusCreated)
	targetID := merged.DataInt("target_id")
	jobIDs := merged.DataSlice("job_ids")
	if targetID <= 0 {
		t.Fatalf("合并未返回 target_id：%s", merged.BodyString())
	}
	if len(jobIDs) != 2 {
		t.Fatalf("合并应为每个来源创建一个搬迁任务，实际 %d：%s", len(jobIDs), merged.BodyString())
	}
	e.Evidence("SIM-EDGE-006.merge", merged.BodyString())

	// 不变量：管理列表里目标存在，两个来源都挂在目标之下（可追溯）。
	// 注意 /logical-devices 没有单资源 GET，只能经列表读取。
	list := e.Admin.Get("/api/v1/logical-devices").Expect(http.StatusOK)
	var devices struct {
		Items []edgeLogicalDeviceRow `json:"items"`
		Total int                    `json:"total"`
	}
	list.Decode(&devices)

	var target, sourceA, sourceB *edgeLogicalDeviceRow
	for i := range devices.Items {
		item := &devices.Items[i]
		switch {
		case item.ID == uint(targetID):
			target = item
		case int64(item.ID) == a.LogicalDeviceID:
			sourceA = item
		case int64(item.ID) == b.LogicalDeviceID:
			sourceB = item
		}
	}
	if target == nil {
		t.Fatalf("逻辑设备列表里没有合并目标 #%d：%+v", targetID, devices.Items)
	}
	if target.Name != targetName {
		t.Fatalf("合并目标名称 = %q，期望 %q", target.Name, targetName)
	}
	for _, probe := range []struct {
		label string
		row   *edgeLogicalDeviceRow
		rawID int64
	}{{"来源 a", sourceA, a.LogicalDeviceID}, {"来源 b", sourceB, b.LogicalDeviceID}} {
		if probe.row == nil {
			t.Fatalf("逻辑设备列表里缺少%s（#%d）：%+v", probe.label, probe.rawID, devices.Items)
		}
		if probe.row.MergedInto == nil || uint(targetID) != *probe.row.MergedInto {
			t.Fatalf("%s (#%d) 的 merged_into = %v，期望 %d", probe.label, probe.row.ID, probe.row.MergedInto, targetID)
		}
	}
	e.Evidence("SIM-EDGE-006.logical_devices", devices.Items)

	// 合并进度必须可查（后台搬迁任务不是"发出去就没了"）。
	for i, raw := range jobIDs {
		jobID := int64(raw.(float64))
		job := e.Admin.Get(fmt.Sprintf("/api/v1/logical-devices/merge-jobs/%d", jobID)).Expect(http.StatusOK)
		status := job.DataString("status")
		if status == "" {
			t.Fatalf("第 %d 个搬迁任务 #%d 没有状态：%s", i+1, jobID, job.BodyString())
		}
		e.Evidence(fmt.Sprintf("SIM-EDGE-006.job_%d", jobID), job.BodyString())
	}

	// 目标可改名（用户在合并后重新命名这个"合并后的设备"）。
	renamed := e.NS("SIM-EDGE-006", "合并后")
	updated := e.Admin.Put(fmt.Sprintf("/api/v1/logical-devices/%d", targetID),
		map[string]any{"name": renamed}).Expect(http.StatusOK)
	if got := updated.DataString("name"); got != renamed {
		t.Fatalf("重命名后目标名称 = %q，期望 %q", got, renamed)
	}
}

// ---------------------------------------------------------------------------
// 共用夹具：节点 + 通道 + 设备配置 + 边缘设备 + MQTT 仿真节点
// ---------------------------------------------------------------------------

// edgeDevice 是这套前置数据的句柄。
type edgeDevice struct {
	NodeID          string
	NodeDBID        int64
	ChannelID       int64
	DeviceConfigID  int64
	EdgeDeviceID    int64
	LogicalDeviceID int64
	Type            string
	HardwareID      string
	Device          *harness.Device
}

// edgeDeviceSpec 描述一台要被搭出来的仿真边缘设备。
type edgeDeviceSpec struct {
	Scenario string // 场景 ID，用于命名空间
	Local    string // 节点局部名后缀（完整局部名 = 域-编号-该后缀，必须 ≤14 字符）
	Suffix   string // 实体名后缀，进 e.NS
	// DeviceType 非空时用驱动注册表里的型号（从而有指令模板/动作目录）；
	// 为空时创建一个带 binary ConfigParser 的自定义型号，让
	// SensorParserConsumer 走 DeviceConfig.Parser 分支（不依赖校准缓存）。
	DeviceType  string
	ParserField string // DeviceType 为空时的解析字段名，默认 temperature
	HardwareID  string
	BusType     string
	IntervalMs  int
	Handshake   bool // 是否执行 MQTT Hello 握手（需要节点在线/协议 ≥ 2.3 时为 true）
}

// edgeLocal 生成节点局部名："SIM-EDGE-001" + "a" → "edge-001-a"。
//
// 为什么不用 e.NS：harness.Env.Device 会在局部名之前再拼 "<runid>-"
// （缺省 runid 17 字符），而 nodes.node_id 是 varchar(32)，
// e.NS 的 "sim-edge-001-a" 形式会溢出上限（harness 会直接判失败）。
// 短横线形式既满足长度约束，又天然场景内唯一。
func edgeLocal(scenarioID, discriminator string) string {
	base := strings.ToLower(strings.TrimPrefix(strings.ToUpper(scenarioID), "SIM-"))
	if discriminator == "" {
		return base
	}
	return base + "-" + discriminator
}

// edgeParserDeviceType 由场景 ID 派生一个"未注册驱动"的自定义型号。
func edgeParserDeviceType(scenarioID string) string {
	return strings.ToLower(strings.ReplaceAll(scenarioID, "-", "_")) + "_sensor"
}

// edgeProvision 用真实产品端点搭出边缘设备全链路的前置数据：
//
//	POST /api/v1/nodes          建节点，node_id 与 MQTT 仿真节点同名
//	POST /api/v1/channels       建一条已启用的总线通道（bus_config 是必需的：
//	                            校验器按 bus_config 判断引脚是否与 GPIO/PWM 冲突）
//	POST /api/v1/device-configs （仅自定义型号）建带 binary 解析规则的设备配置
//	POST /api/v1/edge-devices   把设备绑到节点+通道上（自动附带逻辑身份）
//
// 所有资源都在 t.Cleanup 里按"边缘设备 → 通道 → 设备配置 → 节点"的顺序清理，
// 保证后续场景从干净状态出发（设计 §5.6）。
func edgeProvision(e *harness.Env, spec edgeDeviceSpec) *edgeDevice {
	t := e.T
	t.Helper()

	if spec.BusType == "" {
		spec.BusType = "UART"
	}
	if spec.IntervalMs == 0 {
		spec.IntervalMs = 3000
	}
	if spec.HardwareID == "" {
		spec.HardwareID = "1"
	}
	if spec.ParserField == "" {
		spec.ParserField = "temperature"
	}

	label := e.NS(spec.Scenario, spec.Suffix)
	dev := e.Device(edgeLocal(spec.Scenario, spec.Local))

	// 1) 节点
	node := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": dev.NodeID,
		"name":    label + "-节点",
	}).Expect(http.StatusCreated)
	nodeDBID := node.DataInt("id")

	// 2) 通道
	channel := e.Admin.Post("/api/v1/channels", map[string]any{
		"node_id":       dev.NodeID,
		"hardware_type": spec.BusType,
		"bus_type":      spec.BusType,
		"hardware_id":   spec.HardwareID,
		"bus_config":    "0102", // UART/I2C 至少 2 字节路由；不与其他外设抢引脚
		"interval_ms":   spec.IntervalMs,
		"enabled":       true,
	}).Expect(http.StatusCreated)
	channelID := channel.DataInt("id")

	// 3) 设备配置（仅自定义型号）
	deviceType := spec.DeviceType
	var configID int64
	if deviceType == "" {
		deviceType = edgeParserDeviceType(spec.Scenario)
		cfg := e.Admin.Post("/api/v1/device-configs", map[string]any{
			"name":          label + "-配置",
			"device_type":   deviceType,
			"hardware_type": strings.ToLower(spec.BusType),
			"status":        "active",
			"parser": map[string]any{
				"data_format": "binary",
				"fields": []map[string]any{
					{"name": spec.ParserField, "type": "uint16", "scale": 0.1, "offset": 0, "length": 2, "unit": "°C"},
				},
			},
		}).Expect(http.StatusCreated)
		configID = cfg.DataInt("id")
	}

	// 4) 边缘设备（绑定节点 + 通道）
	body := map[string]any{
		"name":        label + "-设备",
		"node_id":     dev.NodeID,
		"channel_id":  channelID,
		"hardware_id": spec.HardwareID,
		"interval_ms": spec.IntervalMs,
		"enabled":     true,
	}
	if configID > 0 {
		body["device_config_id"] = configID
	} else {
		body["type"] = deviceType
	}
	edge := e.Admin.Post("/api/v1/edge-devices", body).Expect(http.StatusCreated)
	edgeID := edge.DataInt("id")
	logicalID := edge.DataInt("logical_device_id")
	if edgeID == 0 || logicalID == 0 {
		t.Fatalf("边缘设备创建返回不完整: %s", edge.BodyString())
	}

	fx := &edgeDevice{
		NodeID:          dev.NodeID,
		NodeDBID:        nodeDBID,
		ChannelID:       channelID,
		DeviceConfigID:  configID,
		EdgeDeviceID:    edgeID,
		LogicalDeviceID: logicalID,
		Type:            deviceType,
		HardwareID:      spec.HardwareID,
		Device:          dev,
	}
	// 5) MQTT 连接 + 可选握手（红线 §7-2 由 harness 内部把关 NodeID 前缀）。
	if err := dev.Connect(); err != nil {
		t.Fatalf("仿真节点连接 MQTT 失败: %v", err)
	}
	if spec.Handshake {
		dev.Hello("sim-1.0.0", "SIM-EDGE", 1)
	}
	// 消费者按 edge_device_id 反查设备（databus 的调度采样语义），
	// 仿真器必须带上它，否则帧会被判为无关联透传数据而丢弃。
	dev.EdgeDeviceID = uint32(edgeID)
	dev.ChannelID = uint32(channelID)

	t.Cleanup(func() {
		edgeCleanupOK(t, "边缘设备", e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", edgeID)))
		edgeCleanupOK(t, "通道", e.Admin.Delete(fmt.Sprintf("/api/v1/channels/%d", channelID)))
		if configID > 0 {
			edgeCleanupOK(t, "设备配置", e.Admin.Delete(fmt.Sprintf("/api/v1/device-configs/%d", configID)))
		}
		edgeCleanupOK(t, "节点", e.Admin.Delete(fmt.Sprintf("/api/v1/nodes/%d", nodeDBID)))
	})
	return fx
}

// edgeReport 让仿真设备上报一个 16 位原值（按夹具的 uint16/scale=0.1 规则解析）。
func (f *edgeDevice) edgeReport(raw uint16) error {
	return f.Device.DataReport(uint32(f.ChannelID), uint64(time.Now().UnixMilli()),
		[]byte{byte(raw >> 8), byte(raw)})
}

// heartbeat 以固定节奏上报自身状态，保住 nodes.last_seen。
//
// 为什么需要：offlinedetector 把 last_seen 超过 90s 的在线节点判为离线，
// 而命令可用性门禁要求 node.status == "online"。长时间场景（等待派发、
// 等待 Deadline 收尾）必须靠真实心跳维持在线，不能靠改库或 sleep 绕过。
// 这里的 ticker 属于"仿真器模拟上报节奏"（设计 §3 原则 3 允许的唯一 sleep 场景），
// 不作为任何断言的同步手段。
func (f *edgeDevice) heartbeat() {
	start := time.Now()
	f.Device.Loop(context.Background(), 20*time.Second, func() error {
		return f.Device.StatusReport(uint64(time.Since(start).Seconds()), "online", 0)
	})
}

// edgeCleanupOK 是清理专用断言：清理阶段容忍"资源已不存在"（404/BadRequest），
// 其余状态码一律记为错误——静默失败会让脏数据流进后续场景。
func edgeCleanupOK(t *testing.T, what string, resp *harness.Response) {
	t.Helper()
	switch resp.Status {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound, http.StatusBadRequest:
		return
	default:
		t.Errorf("清理%s失败: %s", what, resp.BodyString())
	}
}

// edgeManifestSampling 在配置清单帧（MsgConfigMfst）里查找指定边缘设备的
// 采集开关。清单结构见 nodemgr/sender_snapshot.go:encodeConfigManifest：
//
//	顶层 field 4  = 通道子消息（repeated）
//	通道 field 9  = 边缘设备组（repeated）
//	  组 field 1  = edge_devices.id
//	  组 field 3  = 指令子消息（repeated）；子消息 field 3 = enabled(bool)
//
// 返回 (enabled, found, err)。found=false 表示该设备在清单里没有任何指令，
// 即节点根本不会调度它——这正是"停用"的可观测后果。
func edgeManifestSampling(raw []byte, edgeDeviceID int64) (bool, bool, error) {
	top, err := frame.NewDecoder(raw)
	if err != nil {
		return false, false, fmt.Errorf("解码配置清单: %w", err)
	}
	if top.MsgType() != frame.MsgConfigMfst {
		return false, false, fmt.Errorf("不是配置清单帧: 0x%02X", top.MsgType())
	}
	for {
		field, err := top.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return false, false, err
		}
		if field.FieldNum != 4 {
			continue
		}
		channel, err := frame.NewSubDecoder(frame.GetBytes(field))
		if err != nil {
			return false, false, err
		}
		for {
			channelField, err := channel.NextField()
			if errors.Is(err, frame.ErrEndOfFrame) {
				break
			}
			if err != nil {
				return false, false, err
			}
			if channelField.FieldNum != 9 {
				continue
			}
			group, err := frame.NewSubDecoder(frame.GetBytes(channelField))
			if err != nil {
				return false, false, err
			}
			groupEdgeID := int64(-1)
			enabled := false
			sawCommand := false
			for {
				groupField, err := group.NextField()
				if errors.Is(err, frame.ErrEndOfFrame) {
					break
				}
				if err != nil {
					return false, false, err
				}
				switch groupField.FieldNum {
				case 1:
					groupEdgeID = int64(frame.GetUint64(groupField))
				case 3:
					command, err := frame.NewSubDecoder(frame.GetBytes(groupField))
					if err != nil {
						return false, false, err
					}
					for {
						commandField, err := command.NextField()
						if errors.Is(err, frame.ErrEndOfFrame) {
							break
						}
						if err != nil {
							return false, false, err
						}
						if commandField.FieldNum == 3 {
							sawCommand = true
							if frame.GetBool(commandField) {
								enabled = true
							}
						}
					}
				}
			}
			if groupEdgeID == edgeDeviceID {
				return enabled, sawCommand, nil
			}
		}
	}
	return false, false, nil
}
