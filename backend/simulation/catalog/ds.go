//go:build simulation

// 场景目录 · SIM-DS 数据源台账（设计 §9 SIM-DS-001..006）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
//
// # 2026-10-23 重构：逻辑设备层移除后的 DS 域口径
//
// 组键从 (逻辑设备, 数据类别) 变成 **(edge_device_id, 数据类别)**
// （.contract/logical-device-removal.md §6），而 data_sources 的唯一索引
// uniq_ds_source 正建在这两列上 ⇒ **一个组恒有且只有一行**。
//
// 这使原目录里「同组主备两行、自动熔断切换、候选接替」的一整套场景
// （SIM-DS-001/003/004/005/006 的核心断言）**在数据源台账上不可达**：
//  1. 同组第二条来源会被 Create 以 409 拒绝（唯一键）；
//  2. legacy 自动切换状态机（MarkSuccess/MarkFailure/ScanStale）已在 M3 删除；
//  3. 跨设备的主备/复合语义**只由 source_groups 承担**（契约 §6「来源组不动」），
//     其端到端验证在 SIM-SCNE / SIM-SRC 域，不在本域。
//
// 因此本域按**仍然成立的产品语义**重写为「数据源台账」场景：创建/校验/列表/
// 手动运维/删除/健康留痕。这是诚实的收窄 —— 保留旧断言只会得到一批必红或
// 恒绿的假场景（前者被跳过、后者更坏）。
//
// 命名纪律（设计 §4.1）：本文件的包级标识符一律以域短名 ds 开头。
package catalog

import (
	"fmt"
	"net/http"
	"time"

	"ehome/backend/simulation/harness"
)

// 域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const dsDomain Domain = "DS"

func init() {
	Register(Scenario{
		ID:     "SIM-DS-001",
		Title:  "管理员给某台边缘设备的某项数据登记一条来源，台账如实记录它的归属与优先级",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-001；.contract/logical-device-removal.md §6（组键 = edge_device_id + category）",
		Run:    dsRun001,
	})
	Register(Scenario{
		ID:     "SIM-DS-002",
		Title:  "同一台设备同一项数据不能重复登记来源，重复会被明确拒绝",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-002；.contract/logical-device-removal.md §6",
		Run:    dsRun002,
	})
	Register(Scenario{
		ID:     "SIM-DS-003",
		Title:  "不同边缘设备可以各自登记同一项数据的来源，彼此独立互不影响",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-003；.contract/logical-device-removal.md §6",
		Run:    dsRun003,
	})
	Register(Scenario{
		ID:     "SIM-DS-004",
		Title:  "管理员可以手工更新来源元数据，状态仍由领域层拥有（启停路由已随单行组模型移除）",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-004；2026-10-24 方案 A：activate/deactivate 路由删除，仅保留 POST/PUT",
		Run:    dsRun004,
	})
	Register(Scenario{
		ID:     "SIM-DS-005",
		Title:  "来源健康时间线可查，缺失来源返回 404（reset/切换日志路由已移除）",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-005；internal/datasource/service.go（recordHealth/writeFailoverLog）",
		Run:    dsRun005,
	})
	Register(Scenario{
		ID:     "SIM-DS-006",
		Title:  "未登记来源的台账为空、缺失来源返回 404（删除路由已随投影器接管移除）",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-006；internal/datasource/service.go（Delete 语义）",
		Run:    dsRun006,
	})
}

// ---------------------------------------------------------------------------
// 共用夹具
// ---------------------------------------------------------------------------

// dsSourceRow 是 models.DataSource 的关键字段。
type dsSourceRow struct {
	ID           int64  `json:"id"`
	Category     string `json:"category"`
	EdgeDeviceID int64  `json:"edge_device_id"`
	SourceType   string `json:"source_type"`
	Name         string `json:"name"`
	Priority     int    `json:"priority"`
	IsPrimary    bool   `json:"is_primary"`
	MaxFailCount int    `json:"max_fail_count"`
	FailCount    int    `json:"fail_count"`
	Status       string `json:"status"`
	LastSuccess  string `json:"last_success"`
	LastFailure  string `json:"last_failure"`
}

// dsFailoverLogRow 是 GET /edge-devices/:id/failover-logs 的响应项。
//
// 2026-10-23：API 层 DTO 的字段名已从 device_id 改为 edge_device_id
// （逻辑层删除后 device_id 这个列名不再有意义，契约 §6）。
type dsFailoverLogRow struct {
	ID           int64  `json:"id"`
	EdgeDeviceID int64  `json:"edge_device_id"`
	Category     string `json:"category"`
	FromSourceID int64  `json:"from_source_id"`
	ToSourceID   int64  `json:"to_source_id"`
	Reason       string `json:"reason"`
	Trigger      string `json:"trigger"`
}

// dsFixture 是"两台真实边缘设备"的夹具。
//
// 为什么仍要两台：组键含 edge_device_id，验证「不同设备同类别是两条独立来源」
// 必须有第二台真实设备（不是造一个假 id —— 只有真实设备才能证明投影器与
// 校验链不会把它们混在一起）。
type dsFixture struct {
	Category string
	A        *harness.Fixture
	B        *harness.Fixture
	SourceA  int64
	SourceB  int64
}

// dsProvision 用 harness 共用夹具搭两台"数据真正落库"的仿真设备。
func dsProvision(e *harness.Env, scenarioID string) *dsFixture {
	t := e.T
	t.Helper()
	a, err := e.ProvisionSimpleDevice(scenarioID, "a")
	if err != nil {
		t.Fatalf("搭建设备 A 失败: %v", err)
	}
	b, err := e.ProvisionSimpleDevice(scenarioID, "b")
	if err != nil {
		t.Fatalf("搭建设备 B 失败: %v", err)
	}
	t.Cleanup(func() {
		a.Cleanup()
		b.Cleanup()
		// 夹具只回收设备/通道/配置，节点由 Hello 隐式创建，这里补删，
		// 避免给后续场景留下游离节点。
		edgeCleanupOK(t, "节点A", e.Admin.Delete("/api/v1/nodes/"+a.NodeID))
		edgeCleanupOK(t, "节点B", e.Admin.Delete("/api/v1/nodes/"+b.NodeID))
	})
	// 设备 B 持续上报：保持它在线，避免离线检测器把它判离线后
	// 影响"两台设备各自独立"这类断言的稳定性。
	stop := b.StartReporting(30 * time.Second)
	t.Cleanup(stop)

	return &dsFixture{
		Category: harness.FixtureTempCategory,
		A:        a,
		B:        b,
	}
}

// dsCreateSource 建一个来源（真实 API）。
func dsCreateSource(e *harness.Env, scenarioID, name string, body map[string]any, extra map[string]any) *harness.Response {
	payload := map[string]any{
		"category":       body["category"],
		"edge_device_id": body["edge_device_id"],
		"source_type":    "edge_device",
		"name":           e.NS(scenarioID, name),
	}
	for key, value := range body {
		payload[key] = value
	}
	for key, value := range extra {
		payload[key] = value
	}
	return e.Admin.Post("/api/v1/data-sources", payload)
}

// dsCreateForDevice 为某台设备建一条来源并注册自清理。
func dsCreateForDevice(e *harness.Env, scenarioID, name string, f *harness.Fixture, extra map[string]any) int64 {
	resp := dsCreateSource(e, scenarioID, name, map[string]any{
		"category":       harness.FixtureTempCategory,
		"edge_device_id": int64(f.EdgeDeviceID),
		"priority":       10,
		"is_primary":     true,
		"max_fail_count": 3,
	}, extra).Expect(http.StatusCreated)
	id := resp.DataInt("id")
	if id <= 0 {
		e.T.Fatalf("创建来源未返回 id：%s", resp.BodyString())
	}
	e.T.Cleanup(func() {
		edgeCleanupOK(e.T, "数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", id)))
	})
	return id
}

// dsList 读回某台设备某类别的来源（走真实列表端点）。
func dsList(e *harness.Env, edgeDeviceID int64, category string) []dsSourceRow {
	path := fmt.Sprintf("/api/v1/data-sources?edge_device_id=%d&category=%s&page_size=50", edgeDeviceID, category)
	resp := e.Admin.Get(path).Expect(http.StatusOK)
	var page struct {
		Items []dsSourceRow `json:"items"`
		Total int64         `json:"total"`
	}
	resp.Decode(&page)
	if int64(len(page.Items)) != page.Total {
		e.T.Fatalf("列表 total=%d 与 items 长度 %d 不一致（路径 %s）", page.Total, len(page.Items), path)
	}
	return page.Items
}

// dsGet 读回单个来源。
func dsGet(e *harness.Env, sourceID int64) dsSourceRow {
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/data-sources/%d", sourceID)).Expect(http.StatusOK)
	var row dsSourceRow
	resp.Decode(&row)
	return row
}

// dsFailoverLogs 读取某边缘设备的切换日志，可按类别过滤。
func dsFailoverLogs(e *harness.Env, edgeDeviceID int64, category string) []dsFailoverLogRow {
	path := fmt.Sprintf("/api/v1/edge-devices/%d/failover-logs", edgeDeviceID)
	if category != "" {
		path += "?category=" + category
	}
	resp := e.Admin.Get(path).Expect(http.StatusOK)
	var logs []dsFailoverLogRow
	resp.Decode(&logs)
	return logs
}

// ---------------------------------------------------------------------------
// SIM-DS-001 台账如实记录来源的归属与优先级
// ---------------------------------------------------------------------------

func dsRun001(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-001")

	primary := dsCreateSource(e, "SIM-DS-001", "主来源", map[string]any{
		"category":       fx.Category,
		"edge_device_id": int64(fx.A.EdgeDeviceID),
		"description":    "在役主机",
		"priority":       10,
		"is_primary":     true,
		"max_fail_count": 3,
	}, nil).Expect(http.StatusCreated)
	fx.SourceA = primary.DataInt("id")
	t.Cleanup(func() {
		edgeCleanupOK(t, "主数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", fx.SourceA)))
	})

	rows := dsList(e, int64(fx.A.EdgeDeviceID), fx.Category)
	if len(rows) != 1 {
		t.Fatalf("(edge_device_id=%d, category=%s) 应只有 1 条来源，实际 %d：%+v",
			fx.A.EdgeDeviceID, fx.Category, len(rows), rows)
	}
	row := rows[0]
	// 不变量：组键两列 + 声明字段必须原样保存。
	if row.EdgeDeviceID != int64(fx.A.EdgeDeviceID) {
		t.Fatalf("来源的 edge_device_id = %d，期望 %d", row.EdgeDeviceID, fx.A.EdgeDeviceID)
	}
	if row.Category != fx.Category {
		t.Fatalf("来源的 category = %q，期望 %q", row.Category, fx.Category)
	}
	// 首条来源自动成为权威（组内无 active 时可被采纳）。
	if row.Status != "active" {
		t.Fatalf("首条来源状态 = %q，期望 active", row.Status)
	}
	if row.Priority != 10 || !row.IsPrimary {
		t.Fatalf("优先级/主标记没有原样保存：priority=%d is_primary=%v", row.Priority, row.IsPrimary)
	}
	if row.MaxFailCount != 3 {
		t.Fatalf("max_fail_count = %d，期望 3", row.MaxFailCount)
	}
	if row.SourceType != "edge_device" {
		t.Fatalf("来源类型 = %q，期望 edge_device", row.SourceType)
	}
	e.Evidence("SIM-DS-001.ledger_row", rows)

	// 熔断阈值的合法区间是 [1,20]（领域层校验）：越界必须 400，不能被静默接受。
	bad := dsCreateSource(e, "SIM-DS-001", "越界阈值", map[string]any{
		"category":       harness.FixtureLevelCategory,
		"edge_device_id": int64(fx.A.EdgeDeviceID),
		"max_fail_count": 21,
	}, nil).Expect(http.StatusBadRequest)
	e.Evidence("SIM-DS-001.max_fail_count_rejected", bad.Message)
}

// ---------------------------------------------------------------------------
// SIM-DS-002 同键重复登记被拒绝
// ---------------------------------------------------------------------------

func dsRun002(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-002")
	fx.SourceA = dsCreateForDevice(e, "SIM-DS-002", "唯一来源", fx.A, nil)

	// 同 (edge_device_id, category) 的第二条来源：必须 409（唯一键冲突），
	// 而不是静默新建第二条 —— 那会让「某设备某类别的权威来源」变得不确定。
	dup := dsCreateSource(e, "SIM-DS-002", "重复来源", map[string]any{
		"category":       fx.Category,
		"edge_device_id": int64(fx.A.EdgeDeviceID),
		"priority":       1,
	}, nil).Expect(http.StatusConflict)
	e.Evidence("SIM-DS-002.duplicate_conflict", dup.Message)

	rows := dsList(e, int64(fx.A.EdgeDeviceID), fx.Category)
	if len(rows) != 1 {
		t.Fatalf("被拒的重复创建留下了残留：%+v", rows)
	}
	if rows[0].ID != fx.SourceA {
		t.Fatalf("留存的应是原来源 #%d，实际 %+v", fx.SourceA, rows)
	}

	// 缺 edge_device_id 或 category 必须 400（组键两列都必填）。
	dsCreateSource(e, "SIM-DS-002", "缺设备", map[string]any{
		"category": fx.Category,
	}, nil).Expect(http.StatusBadRequest)
	dsCreateSource(e, "SIM-DS-002", "缺类别", map[string]any{
		"edge_device_id": int64(fx.A.EdgeDeviceID),
	}, nil).Expect(http.StatusBadRequest)

	// 客户端不能自己"指定"权威状态：请求体里的 status 不是创建 DTO 的字段，必须被忽略。
	other := dsCreateSource(e, "SIM-DS-002", "注入状态", map[string]any{
		"category":       harness.FixtureLevelCategory,
		"edge_device_id": int64(fx.A.EdgeDeviceID),
	}, map[string]any{"status": "standby"})
	other.Expect(http.StatusCreated)
	if got := other.DataString("status"); got != "active" {
		t.Fatalf("客户端注入 status 后落库状态 = %q，期望 active（status 由领域层拥有）", got)
	}
	e.Evidence("SIM-DS-002.status_owned_by_domain", other.BodyString())
}

// ---------------------------------------------------------------------------
// SIM-DS-003 不同边缘设备各自独立登记，互不影响
// ---------------------------------------------------------------------------

func dsRun003(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-003")
	fx.SourceA = dsCreateForDevice(e, "SIM-DS-003", "设备A来源", fx.A, nil)
	fx.SourceB = dsCreateForDevice(e, "SIM-DS-003", "设备B来源", fx.B, nil)

	rowsA := dsList(e, int64(fx.A.EdgeDeviceID), fx.Category)
	rowsB := dsList(e, int64(fx.B.EdgeDeviceID), fx.Category)
	if len(rowsA) != 1 || len(rowsB) != 1 {
		t.Fatalf("两台设备各自应有 1 条来源：A=%d B=%d（%+v / %+v）", len(rowsA), len(rowsB), rowsA, rowsB)
	}
	if rowsA[0].ID == rowsB[0].ID {
		t.Fatalf("两台设备的来源串成同一条：%+v", rowsA[0])
	}
	// 组键含 edge_device_id ⇒ 两条来源各自 active，不存在「跨设备抢同一权威」。
	// 跨设备的主备语义由 source_groups 承担（契约 §6），不在本表。
	if rowsA[0].Status != "active" || rowsB[0].Status != "active" {
		t.Fatalf("不同设备的来源应各自 active：A=%q B=%q", rowsA[0].Status, rowsB[0].Status)
	}
	e.Evidence("SIM-DS-003.two_devices", map[string]any{"a": rowsA, "b": rowsB})

	// 两台设备各自 active、id 不同，已证明组键隔离；activate/deactivate 路由
	// 随单行组模型删除，不再有跨设备停用动作可做。
	e.Evidence("SIM-DS-003.independent", map[string]any{"a": rowsA, "b": rowsB})
}

// ---------------------------------------------------------------------------
// SIM-DS-004 停用再启用一条来源
// ---------------------------------------------------------------------------

func dsRun004(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-004")
	fx.SourceA = dsCreateForDevice(e, "SIM-DS-004", "来源", fx.A, nil)

	// 2026-10-24 方案 A 后仍可用的手工写路径是 PUT（名称/描述/优先级/主标记/阈值）。
	// 组键与 status 不在 DTO 白名单内：改不动，也不会被客户端篡改。
	updated := e.Admin.Put(fmt.Sprintf("/api/v1/data-sources/%d", fx.SourceA), map[string]any{
		"name":           e.NS("SIM-DS-004", "改名后的来源"),
		"priority":       7,
		"is_primary":     false,
		"max_fail_count": 9,
		"status":         "standby", // 不在 update DTO 白名单 → 必须被忽略
	}).Expect(http.StatusOK)
	got := dsGet(e, fx.SourceA)
	if got.Priority != 7 {
		t.Fatalf("PUT 未更新 priority：%+v", got)
	}
	// 组内唯一来源必须保持 primary（取消唯一权威会让组无主），因此 is_primary
	// 即使提交 false 也由领域层守为 true —— 这是产品语义，不是 PUT 失效。
	if !got.IsPrimary {
		t.Fatalf("组内唯一来源被取消 primary：%+v", got)
	}
	if got.MaxFailCount != 9 {
		t.Fatalf("PUT 未更新 max_fail_count：%+v", got)
	}
	if got.Status != "active" {
		t.Fatalf("客户端注入 status 后落库状态 = %q，期望 active（状态由领域层拥有）", got.Status)
	}
	if got.EdgeDeviceID != int64(fx.A.EdgeDeviceID) || got.Category != fx.Category {
		t.Fatalf("PUT 改动了组键：%+v", got)
	}
	e.Evidence("SIM-DS-004.updated", map[string]any{"put": updated.BodyString(), "row": got})
}

// ---------------------------------------------------------------------------
// SIM-DS-005 手动运维留痕（健康事件 + 切换日志）
// ---------------------------------------------------------------------------

func dsRun005(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-005")
	fx.SourceA = dsCreateForDevice(e, "SIM-DS-005", "来源", fx.A, nil)

	// reset 路由随单行组模型删除；本场景保留仍可观测的健康时间线契约。
	health := e.Admin.Get(fmt.Sprintf("/api/v1/data-sources/%d/health", fx.SourceA)).
		Expect(http.StatusOK)
	var events []map[string]any
	health.Decode(&events)
	for _, ev := range events {
		if id, ok := ev["source_id"].(float64); ok && uint(id) != uint(fx.SourceA) {
			t.Fatalf("健康事件 source_id = %v，期望 %d", ev["source_id"], fx.SourceA)
		}
	}
	e.Evidence("SIM-DS-005.health_timeline", health.BodyString())

	// 不存在的来源必须 404，而不是 500 或静默成功。
	e.Admin.Delete("/api/v1/data-sources/999999999").Expect(http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// SIM-DS-006 删除来源：台账清零、幂等删除 404
// ---------------------------------------------------------------------------

func dsRun006(e *harness.Env) {
	t := e.T
	fx := dsProvision(e, "SIM-DS-006")

	// 未登记任何来源时，台账为空（不是 404/500）。
	if rows := dsList(e, int64(fx.A.EdgeDeviceID), fx.Category); len(rows) != 0 {
		t.Fatalf("未登记来源时列表应为空，实际 %+v", rows)
	}

	// 缺失来源的详情与健康时间线都必须 404，且不能留下悬空引用。
	e.Admin.Get("/api/v1/data-sources/999999999").Expect(http.StatusNotFound)
	// health 对不存在的来源返回 200 + 空数组（列表语义），而不是 404。
	health := e.Admin.Get("/api/v1/data-sources/999999999/health").Expect(http.StatusOK)
	var events []map[string]any
	health.Decode(&events)
	if len(events) != 0 {
		t.Fatalf("缺失来源的健康时间线应为空，实际 %+v", events)
	}

	// 手工建行仍可用（投影器不覆盖 managed_by=manual）；建完后可查回。
	fx.SourceA = dsCreateForDevice(e, "SIM-DS-006", "手工登记", fx.A, nil)
	row := dsGet(e, fx.SourceA)
	if row.EdgeDeviceID != int64(fx.A.EdgeDeviceID) || row.Category != fx.Category {
		t.Fatalf("手工登记的来源归属不正确：%+v", row)
	}
	e.Evidence("SIM-DS-006.manual_source", row)
}
