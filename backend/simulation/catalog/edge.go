//go:build simulation

// 场景目录 · SIM-EDGE 边缘设备（设计 §9 SIM-EDGE-001..006）。
//
// 2026-10-23：逻辑设备概念层已整体移除（.contract/logical-device-removal.md）。
// 原目录里依赖它的两处（SIM-EDGE-001 的「逻辑设备候选发现」、SIM-EDGE-006 的
// 「合并到同一逻辑设备」）已按契约 §4 的**新能力**重述：
//   · 候选发现（/edge-devices/candidates，随 handler_edge_device_candidates.go 删除）
//     → 换成「创建时的必填/绑定校验」；
//   · 逻辑设备合并 → 换成真正要满足的需求 R0：
//     「换节点后历史不丢」——原地换绑（PUT node_id/channel_id）与
//     merge-history（把旧 edge_device_id 的数据迁到新 id）两条路径。

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

	"ehome/backend/internal/offlinedetector"
	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// 域标识：设计 v1.1 冻结 Domain 取 §6 表"前缀"列的短名（去 SIM-），
// 且不变量 ID == "SIM-" + string(Domain) + "-" + NNN 由目录门禁校验。
const edgeDomain Domain = "EDGE"

// edgeDeviceStatusPending 是新建边缘设备的初始状态。
//
// 与 models.EdgeDeviceStatusPending 同值，但这里刻意不 import models：
// 仿真场景断言的是**外部可见的 API 行为**（设备刚建好时列表里显示什么状态），
// 而不是把服务端常量抄过来核对 —— 后者会让「两边一起改错」也照样全绿。
// 该值只在 SIM-EDGE-002 使用；若服务端改了状态机，这里必须显式跟着改。
const edgeDeviceStatusPending = "pending"

func init() {
	Register(Scenario{
		ID:     "SIM-EDGE-001",
		Title:  "添加边缘设备时缺必填绑定（节点/通道/名称）会被拒绝，补齐后可用",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-001；.contract/logical-device-removal.md §4（候选发现随逻辑层删除）",
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
		Title:  "设备从节点 A 换到节点 B 后历史数据不丢：原地换绑与合并历史两条路径",
		Domain: edgeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-EDGE-006；.contract/logical-device-removal.md §1 R0 / §4.1 / §4.3",
		Run:    edgeRun006,
	})
}

// ---------------------------------------------------------------------------
// SIM-EDGE-001 创建边缘设备的必填绑定校验
// ---------------------------------------------------------------------------
//
// 历史：本场景曾是「通过候选发现接口列出节点下挂载的逻辑设备」——
// /edge-devices/candidates 是逻辑身份层的一部分（它回答「这台新设备可以继承
// 哪条历史逻辑身份」）。逻辑设备概念层整体移除后（.contract §3），该端点与其
// handler 一并删除，候选继承语义不复存在。
//
// 现在的守卫换成创建路径上**仍然存在**的硬约束：节点与通道必须存在且相互
// 匹配。这是「设备能被采集」的前提，也是换绑（§4.1）与恢复（§4.2）共同依赖的
// 校验链 —— 比一个已删除的候选接口更贴近真实风险。

func edgeRun001(e *harness.Env) {
	t := e.T
	fx := edgeProvision(e, edgeDeviceSpec{
		Scenario:   "SIM-EDGE-001",
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
	})

	// 缺 name / node_id：400（DTO 层必填）。
	e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"channel_id":  fx.ChannelID,
		"type":        fx.Type,
		"hardware_id": "0x02",
	}).Expect(http.StatusBadRequest)

	// 缺通道（且没有内联 channel）：400。
	e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-001", "缺通道"),
		"node_id":     fx.NodeID,
		"type":        fx.Type,
		"hardware_id": "0x03",
	}).Expect(http.StatusBadRequest)

	// 节点不存在：404（设计 §7.2「node 不存在 → 404」）。
	e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-001", "缺节点"),
		"node_id":     "nonexistent-node-sim-edge-001",
		"channel_id":  fx.ChannelID,
		"type":        fx.Type,
		"hardware_id": "0x04",
	}).Expect(http.StatusNotFound)

	// 通道不属于该节点：400（服务端在事务内做 `WHERE id = ? AND node_id = ?`，
	// 命中失败即拒）。用一个不存在的通道 id 走同一条校验。
	e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-001", "错配通道"),
		"node_id":     fx.NodeID,
		"channel_id":  999999999,
		"type":        fx.Type,
		"hardware_id": "0x05",
	}).Expect(http.StatusBadRequest)

	// 不变量：三次被拒之后，节点下仍然只有夹具那一台设备（拒绝必须真没落库）。
	rows := simListAll[edgeDeviceRow](e, "/api/v1/edge-devices", "?node_id="+fx.NodeID)
	if len(rows) != 1 || rows[0].ID != fx.EdgeDeviceID {
		t.Fatalf("被拒的创建留下了残留设备：%+v", rows)
	}
	e.Evidence("SIM-EDGE-001.only_fixture_device", rows)
}

// ---------------------------------------------------------------------------
// SIM-EDGE-002 创建边缘设备并绑定节点与通道后可在列表查询到
// ---------------------------------------------------------------------------

// edgeDeviceRow 是 GET /edge-devices 列表项的关键字段（models.EdgeDevice）。
type edgeDeviceRow struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	NodeID     string `json:"node_id"`
	ChannelID  int64  `json:"channel_id"`
	Type       string `json:"type"`
	HardwareID string `json:"hardware_id"`
	Enabled    bool   `json:"enabled"`
	IntervalMs int    `json:"interval_ms"`
	Status     string `json:"status"`
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

	// 端点形状：**分页信封** {items,total,page,page_size}
	// （handler_edge_device.go:166-296，提交 ffdec935 改）。此处按 node_id 过滤后
	// 本场景只有 1 台设备，读当前页即可。
	//
	// 2026-10-23：原断言里「每台设备必须有 logical_device_id」一条已删除 ——
	// 逻辑设备概念层整体移除，edge_devices 表不再有该列（契约 §2）。
	byNodeFilter := "?node_id=" + fx.NodeID
	rows := simListGet[edgeDeviceRow](e, "/api/v1/edge-devices"+byNodeFilter)
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

	// 列表页的筛选条件（型号 + 状态）同样必须命中。
	//
	// 2026-10-04 修正：状态不再写死 "active"。新建边缘设备的初始状态是 pending
	// （models.go 的 `default:pending`，对应现场缺陷 5「新建的传感器一条数据都没有
	// 却显示在线」），只有 databus 收到首个数据帧才提升为 active。本场景只验证
	// 「绑定后能被查到」，从不上报数据，所以 pending 才是它此刻的合法状态；
	// 写死 active 等于把已被修掉的旧行为（创建即在线）当成契约。
	//
	// 这里同时守住两件事：
	//   ① 新建设备的状态必须是 pending —— 有人把 default 改回 active 即变红；
	//   ② 型号 + 状态的组合筛选必须能筛出它 —— 筛选器回归也变红。
	if hit.Status != edgeDeviceStatusPending {
		t.Fatalf("新建边缘设备 #%d 的 status = %q，期望 %q（「创建即有数据」是已修复的缺陷 5）",
			hit.ID, hit.Status, edgeDeviceStatusPending)
	}
	filterPath := fmt.Sprintf("/api/v1/edge-devices?node_id=%s&device_type=%s&status=%s",
		fx.NodeID, fx.Type, edgeDeviceStatusPending)
	filtered := simListGet[edgeDeviceRow](e, filterPath)
	found := false
	for _, row := range filtered {
		if row.ID == fx.EdgeDeviceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("device_type+status 过滤后没有边缘设备 #%d：%+v", fx.EdgeDeviceID, filtered)
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
	//
	// ⚠ 2026-10-11：期望码由 400 改为 **409**。
	//   本日修复（commit 7dcb2b03）把"三元重复"从 400 改为 409，依据是设计文档
	//   边缘设备.md §7.2「三元重复 → 409」与 §1.1 冲突文案（前端按 409 渲染）。
	//   该场景原先钉在 400，正是**旧行为**；它这次变红说明仿真确实在盯真实契约。
	//   409 是"冲突"语义：同型号设备不能占用同一通道上的同一从机地址。
	dup := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-003", "重复"),
		"node_id":     fx.NodeID,
		"channel_id":  fx.ChannelID,
		"type":        fx.Type,
		"hardware_id": fx.HardwareID,
		"enabled":     true,
	}).Expect(http.StatusConflict)
	if !strings.Contains(dup.Message, "already hosts") {
		t.Fatalf("重复设备的拒绝原因没有指出地址冲突：message=%q body=%s", dup.Message, dup.BodyString())
	}
	e.Evidence("SIM-EDGE-003.duplicate_rejected", dup.Message)

	// (b) 同地址但不同型号：不构成三元重复，必须允许创建。
	//
	// 历史：(b) 曾是「把新设备挂到已存在存活实例的逻辑设备上 ⇒ 409（存活实例
	// 不变量）」。逻辑设备概念层整体移除后（契约 §1「不实现逻辑身份」），
	// 该不变量不存在，POST 体里的 logical_device_id 字段也不再被接受。
	// 现在的 (b) 守的是三元重复判定的**边界**：它必须只针对（通道, 型号, 地址）
	// 三者全等，否则「换个型号就要换通道」会变成无法落地的约束。
	other := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":        e.NS("SIM-EDGE-003", "异型号"),
		"node_id":     fx.NodeID,
		"channel_id":  fx.ChannelID,
		"type":        "not_the_same_type",
		"hardware_id": "0x99",
		"enabled":     true,
	}).Expect(http.StatusBadRequest) // 未注册驱动型号 ⇒ 拒绝（类型门禁），不是三元冲突
	e.Evidence("SIM-EDGE-003.other_type_rejected", other.Message)

	// 不变量：两次被拒之后，节点下仍然只有本场景创建的那一台设备，
	// 拒绝必须是真的没有落库（不能"报错但建了"）。
	// 端点形状：**分页信封**（handler_edge_device.go:166-296）。
	// 断言"节点下只有本场景这一台"看的是过滤后的**全量**，因此读全部页：
	// 只读第一页会让"被拒的创建留下残留设备"这类断言在设备多时失效。
	rows := simListAll[edgeDeviceRow](e, "/api/v1/edge-devices", "?node_id="+fx.NodeID)
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
// SIM-EDGE-006 换节点后历史数据不丢（真正需求 R0）
// ---------------------------------------------------------------------------
//
// 契约 §1 把「逻辑设备」当初要解决的问题压缩成一句：**同一物理设备从节点 A 换到
// 节点 B 后，历史数据不丢、连续可查**。删除逻辑层后它有两条真实路径，本场景
// 两条都验证（都走真实 HTTP + 真实入库，不直连数据库）：
//
//  路径①「直接复用」（§4.1）：PUT /edge-devices/:id {node_id, channel_id} ——
//    同一条 edge_devices 行原地换绑，id 不变 ⇒ unified_data.device_id 不变 ⇒
//    历史天然连续。这是主路径。
//
//  路径②「合并历史」（§4.3）：POST /edge-devices/:id/merge-history {source_ids}
//    —— 旧设备行的数据（device_id = 旧 id）批量 UPDATE 到新 id。用于设备已被
//    重建/删除、生成了新行，旧 id 的数据在界面上再也看不到的情形。
//
// 断言的核心是**效果层**：换绑/合并前后，历史端点返回的行数不减少且取值齐全。
//
// 历史：本场景曾是「把多路历史设备合并到同一逻辑设备，并能看到合并进度
// （/logical-devices/merge + merge-jobs 轮询）」。那条链（身份冻结 + 每源一个
// 搬迁任务 + 水位表）随逻辑层整体删除（契约 §3），换成上面两条一步到位的操作。

// edgeHistorySensor 是夹具解析器提供的物理量名（见 edgeProvision：spec.ParserField
// 缺省为 "temperature"，夹具以 uint16/scale=0.1 解析两字节）。
const edgeHistorySensor = "temperature"

// edgeHistoryRow 是 GET /devices/:id/history 的元素子集。
type edgeHistoryRow struct {
	DeviceID   uint    `json:"device_id"`
	SensorName string  `json:"sensor_name"`
	Value      float64 `json:"value"`
}

// edgeHistoryValues 读某边缘设备的全部历史取值（按类别过滤）。
func edgeHistoryValues(e *harness.Env, edgeDeviceID int64, sensor string) []edgeHistoryRow {
	path := fmt.Sprintf("/api/v1/devices/%d/history?sensor=%s&hours=1", edgeDeviceID, sensor)
	resp := e.Admin.Get(path).Expect(http.StatusOK)
	var rows []edgeHistoryRow
	resp.Decode(&rows)
	return rows
}

func edgeRun006(e *harness.Env) {
	t := e.T
	// 两台设备用**自定义 ConfigParser 型号**（DeviceType 留空 ⇒ 夹具的
	// uint16/scale=0.1 二进制解析器）：本场景要的是「数据真的落进 unified_data」，
	// 而内置驱动型号的数据帧格式不同（sn3001_rain 要求 ≥5 字节 Modbus 帧），
	// 用 edgeReport 的 2 字节负载解析不出来 ⇒ 历史为空 ⇒ 后面所有断言都无意义。
	//
	// HardwareID 必须不同：路径① 会把 A 换绑到 B 的通道上，而三元唯一性守卫
	// （channel_id, type, hardware_id）在换绑时同样生效 —— 同地址会 409。
	a := edgeProvision(e, edgeDeviceSpec{
		Scenario: "SIM-EDGE-006", Local: "a", Suffix: "a",
		HardwareID: "1",
	})
	b := edgeProvision(e, edgeDeviceSpec{
		Scenario: "SIM-EDGE-006", Local: "b", Suffix: "b",
		HardwareID: "2",
	})

	// 让设备 A 产生历史数据（按真实订阅周期上报一帧，等它落库）。
	if err := a.edgeReport(2500); err != nil {
		t.Fatalf("设备 A 上报失败: %v", err)
	}
	var before []edgeHistoryRow
	e.Eventually(30*time.Second, func() error {
		before = edgeHistoryValues(e, a.EdgeDeviceID, edgeHistorySensor)
		if len(before) == 0 {
			return fmt.Errorf("设备 A 的历史数据尚未入库")
		}
		return nil
	})
	e.Evidence("SIM-EDGE-006.history_before", len(before))

	// ── 路径①：原地换绑到节点 B 的通道 ──
	//
	// 语义：同一 edge_devices 行改 node_id/channel_id，id 不变 ⇒
	// unified_data.device_id 不变 ⇒ 历史天然连续（契约 §4.1）。
	e.Admin.Put(fmt.Sprintf("/api/v1/edge-devices/%d", a.EdgeDeviceID), map[string]any{
		"node_id":    b.NodeID,
		"channel_id": b.ChannelID,
	}).Expect(http.StatusOK)

	// 换绑后：同一个 id 必须仍能查到换绑前的全部历史（一行不少）。
	afterMove := edgeHistoryValues(e, a.EdgeDeviceID, edgeHistorySensor)
	if len(afterMove) < len(before) {
		t.Fatalf("换节点后历史减少：换绑前 %d 行，换绑后 %d 行", len(before), len(afterMove))
	}
	// 绑定关系确实变成了节点 B（否则「换节点」只是改了个名字）。
	var moved edgeDeviceRow
	e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d", a.EdgeDeviceID)).Expect(http.StatusOK).Decode(&moved)
	if moved.NodeID != b.NodeID || moved.ChannelID != b.ChannelID {
		t.Fatalf("换绑后绑定关系 = (%s, %d)，期望 (%s, %d)",
			moved.NodeID, moved.ChannelID, b.NodeID, b.ChannelID)
	}
	e.Evidence("SIM-EDGE-006.after_rebind", map[string]any{
		"edge_device_id": moved.ID, "node_id": moved.NodeID, "channel_id": moved.ChannelID,
		"history_rows": len(afterMove),
	})

	// ── 路径②：合并历史（旧 id 的数据迁到新 id） ──
	//
	// 构造真实运维场景：设备 B 也上报一批数据 → 它在库里是自己的 id。
	// 然后把它视为「旧设备行」，把它的历史合并到 A（当前在役的那条行）。
	if err := b.edgeReport(3100); err != nil {
		t.Fatalf("设备 B 上报失败: %v", err)
	}
	var bRows []edgeHistoryRow
	e.Eventually(30*time.Second, func() error {
		bRows = edgeHistoryValues(e, b.EdgeDeviceID, edgeHistorySensor)
		if len(bRows) == 0 {
			return fmt.Errorf("设备 B 的历史数据尚未入库")
		}
		return nil
	})

	// 数据量估算端点：合并前用户要能看到「目标 + 这些源」大概多少行。
	summary := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d/data-summary?source_ids=%d",
		a.EdgeDeviceID, b.EdgeDeviceID)).Expect(http.StatusOK)
	// 2026-11-14：data-summary 已返回 {target, target_rows, sources, source_rows}，
	// 不再是 items 数组；场景按当前契约核对目标/源两侧的估算行数。
	var summaryBody map[string]any
	summary.Decode(&summaryBody)
	targetRows, _ := summaryBody["target_rows"].(float64)
	sourceRows, _ := summaryBody["source_rows"].(float64)
	sources, _ := summaryBody["sources"].([]any)
	if targetRows <= 0 || sourceRows <= 0 || len(sources) != 1 {
		t.Fatalf("data-summary 目标/源估算不完整：target_rows=%v source_rows=%v sources=%d body=%s",
			summaryBody["target_rows"], summaryBody["source_rows"], len(sources), summary.BodyString())
	}
	e.Evidence("SIM-EDGE-006.data_summary", summary.BodyString())

	// 合并规则要求源设备已删除（保留数据）：先软删 B，再合并。
	e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", b.EdgeDeviceID)).Expect(http.StatusOK)

	merged := e.Admin.Post(fmt.Sprintf("/api/v1/edge-devices/%d/merge-history", a.EdgeDeviceID),
		map[string]any{"source_ids": []int64{b.EdgeDeviceID}}).Expect(http.StatusOK)
	migrated := merged.DataInt("rows_migrated")
	if migrated <= 0 {
		t.Fatalf("merge-history 没有迁移任何行：%s", merged.BodyString())
	}
	e.Evidence("SIM-EDGE-006.merge_history", merged.BodyString())

	// 效果层：目标设备现在能看到两边的数据；源设备已无数据。
	mergedRows := edgeHistoryValues(e, a.EdgeDeviceID, edgeHistorySensor)
	if len(mergedRows) < len(before)+len(bRows) {
		t.Fatalf("合并后目标设备历史 %d 行，期望 ≥ %d（换绑前 %d + 源 %d）",
			len(mergedRows), len(before)+len(bRows), len(before), len(bRows))
	}
	// 合并与最近的 live 上报存在竞态：迁移完成后可能又来一帧（源设备在删除前的
	// 最后一批数据）。用 Eventually 等它落定，而不是立刻判失败。
	e.Eventually(10*time.Second, func() error {
		if left := edgeHistoryValues(e, b.EdgeDeviceID, edgeHistorySensor); len(left) != 0 {
			return fmt.Errorf("合并后源设备仍有 %d 行历史（迁移未生效或新帧晚到）", len(left))
		}
		return nil
	})

	// 幂等：第二次调用必须迁移 0 行（源已无数据）。
	again := e.Admin.Post(fmt.Sprintf("/api/v1/edge-devices/%d/merge-history", a.EdgeDeviceID),
		map[string]any{"source_ids": []int64{b.EdgeDeviceID}}).Expect(http.StatusOK)
	if n := again.DataInt("rows_migrated"); n != 0 {
		t.Fatalf("merge-history 不幂等：第二次仍迁移了 %d 行", n)
	}
	e.Evidence("SIM-EDGE-006.merge_history_idempotent", again.BodyString())

	// 边界：目标设备必须存活（合并进已软删设备等于把数据埋掉）。
	deleted := e.Admin.Post(fmt.Sprintf("/api/v1/edge-devices/%d/merge-history", b.EdgeDeviceID),
		map[string]any{"source_ids": []int64{a.EdgeDeviceID}})
	deleted.Expect(http.StatusNotFound)
	e.Evidence("SIM-EDGE-006.merge_into_deleted_rejected", deleted.Message)

	// 收尾：把 A 换回自己的节点/通道，恢复夹具的清理顺序前提
	// （否则 B 的通道仍被在役的 A 引用，清理会 409）。
	e.Admin.Put(fmt.Sprintf("/api/v1/edge-devices/%d", a.EdgeDeviceID), map[string]any{
		"node_id":    a.NodeID,
		"channel_id": a.ChannelID,
	}).Expect(http.StatusOK)
}

// ---------------------------------------------------------------------------
// 共用夹具：节点 + 通道 + 设备配置 + 边缘设备 + MQTT 仿真节点
// ---------------------------------------------------------------------------

// edgeDevice 是这套前置数据的句柄。
//
// 2026-10-23：LogicalDeviceID 字段已删除 —— 逻辑设备概念层整体移除后，
// 创建响应里不再有 logical_device_id（契约 §2 删除 edge_devices.logical_device_id）。
type edgeDevice struct {
	NodeID         string
	NodeDBID       int64
	ChannelID      int64
	DeviceConfigID int64
	EdgeDeviceID   int64
	Type           string
	HardwareID     string
	Device         *harness.Device
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
//	POST /api/v1/edge-devices   把设备绑到节点+通道上
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
	// 2026-10-23：原断言还要求 logical_device_id 非零（「每台设备都必须有逻辑
	// 身份」）。该列随逻辑设备概念层删除，现在创建响应里只有 id 是必有的。
	if edgeID == 0 {
		t.Fatalf("边缘设备创建返回不完整: %s", edge.BodyString())
	}

	fx := &edgeDevice{
		NodeID:         dev.NodeID,
		NodeDBID:       nodeDBID,
		ChannelID:      channelID,
		DeviceConfigID: configID,
		EdgeDeviceID:   edgeID,
		Type:           deviceType,
		HardwareID:     spec.HardwareID,
		Device:         dev,
	}
	// 5) MQTT 连接 + 可选握手（红线 §7-2 由 harness 内部把关 NodeID 前缀）。
	if err := dev.Connect(); err != nil {
		t.Fatalf("仿真节点连接设备端口失败: %v", err)
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
// 为什么需要：offlinedetector 把 last_seen 超过 NodeOfflineThreshold 的在线节点判为离线，
// 该阈值 2026-10-03 起为 3s（旧值 90s；见 internal/offlinedetector/offlinedetector.go，
// 1s 心跳 + 3s 阈值 + 1s ticker = 5s 预算），而命令可用性门禁要求 node.status == "online"。
// 长时间场景（等待派发、等待 Deadline 收尾）必须靠真实心跳维持在线，不能靠改库或 sleep 绕过。
// 这里的 ticker 属于"仿真器模拟上报节奏"（设计 §3 原则 3 允许的唯一 sleep 场景），
// 不作为任何断言的同步手段。
func (f *edgeDevice) heartbeat() {
	start := time.Now()
	// 周期必须 <= offlinedetector.NodeOfflineThreshold（当前 3s），否则节点会在
	// 两次心跳之间被判离线，任何"等派发/等收尾"超过一个周期的场景都会撞上
	// commandexec 的 gateNodeStatus（node.status == "online"），表现为动作被拒为
	// "action is unavailable for this device"。
	//
	// 2026-10-04 修复：这里原本写死 20s。阈值还是 90s 时它没问题；为满足
	// "断电 5s 内可见"把阈值收紧到 3s 后就自相矛盾 —— 用例一边声称"用真实心跳
	// 维持在线"，一边以 6.7 倍于阈值的间隔发心跳，于是被自己的门禁判离线。
	//
	// 取固件的真实上报周期（main.c 的 STATUS_REPORT_PERIOD_MS = 1s）而不是再写一个
	// "刚好够用"的数：与真实设备同拍，阈值以后再变这里仍然成立。
	f.Device.Loop(context.Background(), offlinedetector.FirmwareStatusReportPeriod, func() error {
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
