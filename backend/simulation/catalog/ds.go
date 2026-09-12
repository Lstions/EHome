//go:build simulation

// 场景目录 · SIM-DS 数据源主备与故障转移（设计 §9 SIM-DS-001..006）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）；
// 领域依据：docs/设计/数据源主备与故障转移.md（组键 = (逻辑设备, 数据类别)、
// 不变量"每组至多一条 active"、R1~R11 状态机）。
//
// 断言口径（照真实实现写，不照想当然）：
//   - 组内首条来源自动 active，其余 standby（datasource.Service.Create）；
//   - 自动切换只有两条真实信号：设备离线钩子（device_offline）与停滞扫描（stale_data）。
//     本域用前者：它由 offlinedetector 在边缘设备超过 60s 无数据时触发，
//     是最快且最真实的路径（停滞扫描还要先满足 R11 的 2 分钟最小驻留）；
//   - MarkSuccess 只清 fail_count 且 error→standby，**不抢占 active**，
//     所以"回切"必然是管理员显式操作（R1，设计 §3"不自动回切"）。
//
// 命名纪律（设计 §4.1）：本文件的包级标识符一律以域短名 ds 开头。
package catalog

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"ehome/backend/simulation/harness"
)

// 域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const dsDomain Domain = "DS"

func init() {
	Register(Scenario{
		ID:     "SIM-DS-001",
		Title:  "管理员给同一个设备的同一项数据配了主备两个来源，由主来源负责取值",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-001；docs/设计/数据源主备与故障转移.md §2/§3",
		Run:    dsRun001,
	})
	Register(Scenario{
		ID:     "SIM-DS-002",
		Title:  "同一项数据不会同时有两个权威来源，重复添加会被明确拒绝",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-002；docs/设计/数据源主备与故障转移.md §2/§7",
		Run:    dsRun002,
	})
	Register(Scenario{
		ID:     "SIM-DS-003",
		Title:  "主来源所在的设备掉线后，系统自动把取值切到备用来源",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-003；docs/设计/数据源主备与故障转移.md §3 R2/R3、§4",
		Run:    dsRun003,
	})
	Register(Scenario{
		ID:     "SIM-DS-004",
		Title:  "主来源恢复后，管理员可以手动把取值切回主来源，切换过程有据可查",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-004；docs/设计/数据源主备与故障转移.md §3 R1/R7",
		Run:    dsRun004,
	})
	Register(Scenario{
		ID:     "SIM-DS-005",
		Title:  "每一次来源切换都能在故障日志里查到是从谁切到谁、为什么切",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-005；docs/设计/数据源主备与故障转移.md §5 FailoverLog",
		Run:    dsRun005,
	})
	Register(Scenario{
		ID:     "SIM-DS-006",
		Title:  "删除来源之后，同一项数据仍然不会出现两个权威来源，也不会没人取值",
		Domain: dsDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DS-006；docs/设计/数据源主备与故障转移.md §7（Delete 语义）",
		Run:    dsRun006,
	})
}

// ---------------------------------------------------------------------------
// 共用夹具
// ---------------------------------------------------------------------------

// dsSourceRow 是 models.DataSource 的关键字段。
type dsSourceRow struct {
	ID           int64  `json:"id"`
	DeviceID     int64  `json:"device_id"`
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

// dsFailoverLogRow 是 models.FailoverLog。
type dsFailoverLogRow struct {
	ID           int64  `json:"id"`
	DeviceID     int64  `json:"device_id"`
	Category     string `json:"category"`
	FromSourceID int64  `json:"from_source_id"`
	ToSourceID   int64  `json:"to_source_id"`
	Reason       string `json:"reason"`
	Trigger      string `json:"trigger"`
}

// dsGroup 是"一个来源组 + 两台真实边缘设备"的夹具。
//
// 组键 device_id 必须指向 LogicalDevice.ID，而"同一逻辑身份同时只允许一个
// 存活实例"（边缘设备数据生命周期 §3.3-3）使两台存活设备无法共享逻辑身份。
// 因此夹具用两台各自持有逻辑身份的真实设备，并把组键统一取主机设备的逻辑身份：
// 这是公开 API 下唯一能让两个来源都保持"存活、能上报"的搭建方式
// （SIM-DS-004 的回切需要主机恢复上报）。该取舍见交付报告的偏差清单。
type dsGroup struct {
	LogicalDeviceID int64
	Category        string
	Primary         *harness.Fixture
	Standby         *harness.Fixture
	PrimarySource   int64
	StandbySource   int64
}

// dsProvisionGroup 用 harness 共用夹具搭两台"数据真正落库"的仿真设备。
func dsProvisionGroup(e *harness.Env, scenarioID string) *dsGroup {
	t := e.T
	t.Helper()
	primary, err := e.ProvisionSimpleDevice(scenarioID, "a")
	if err != nil {
		t.Fatalf("搭建设备 A（主来源宿主）失败: %v", err)
	}
	standby, err := e.ProvisionSimpleDevice(scenarioID, "b")
	if err != nil {
		t.Fatalf("搭建设备 B（备来源宿主）失败: %v", err)
	}
	t.Cleanup(func() {
		primary.Cleanup()
		standby.Cleanup()
		// 夹具只回收设备/通道/配置，节点由 Hello 隐式创建，这里补删，
		// 避免给后续场景留下游离节点。
		edgeCleanupOK(t, "节点A", e.Admin.Delete("/api/v1/nodes/"+primary.NodeID))
		edgeCleanupOK(t, "节点B", e.Admin.Delete("/api/v1/nodes/"+standby.NodeID))
	})
	// 备用来源必须是一台"健康在线"的设备：让 B 持续上报。
	//
	// 为什么必须这样：PROVISION 阶段每台设备都各做过一次自检上报，因此两台
	// 设备都进了离线检测器的活跃缓存。若两台都不再上报，它们会在同一个 5s
	// 扫描周期内被同时判离线（实测日志：两条 "[EdgeDevice Offline]" 相隔 1ms），
	// 于是"备用来源也在同一瞬间计一次失败"，本场景就不再是"主来源故障、
	// 备用来源健康"这一前提了。持续上报让 B 保持在线，正是真实部署里
	// 备用边缘设备的形态。
	stopStandbyReporting := standby.StartReporting(30 * time.Second)
	t.Cleanup(stopStandbyReporting)

	return &dsGroup{
		LogicalDeviceID: dsLogicalDeviceID(e, int64(primary.EdgeDeviceID)),
		Category:        harness.FixtureTempCategory,
		Primary:         primary,
		Standby:         standby,
	}
}

// dsLogicalDeviceID 读取边缘设备当前挂载的逻辑身份（每台设备必有）。
func dsLogicalDeviceID(e *harness.Env, edgeDeviceID int64) int64 {
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d", edgeDeviceID)).Expect(http.StatusOK)
	id := resp.DataInt("logical_device_id")
	if id <= 0 {
		e.T.Fatalf("边缘设备 #%d 没有逻辑身份：%s", edgeDeviceID, resp.BodyString())
	}
	return id
}

// dsCreateSource 建一个来源（真实 API）。extra 用于附加/注入请求字段。
func dsCreateSource(e *harness.Env, scenarioID, name string, body map[string]any, extra map[string]any) *harness.Response {
	payload := map[string]any{
		"device_id":      body["device_id"],
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

// dsEstablish 建立"主 active + 备 standby"的来源组，并注册自清理。
func dsEstablish(e *harness.Env, scenarioID string, g *dsGroup, primaryMaxFail int) {
	t := e.T
	t.Helper()
	first := dsCreateSource(e, scenarioID, "主来源", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       g.Category,
		"edge_device_id": int64(g.Primary.EdgeDeviceID),
		"description":    "在役主机",
		"priority":       10,
		"is_primary":     true,
		"max_fail_count": primaryMaxFail,
	}, nil).Expect(http.StatusCreated)
	g.PrimarySource = first.DataInt("id")
	t.Cleanup(func() {
		edgeCleanupOK(t, "主数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.PrimarySource)))
	})

	second := dsCreateSource(e, scenarioID, "备来源", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       g.Category,
		"edge_device_id": int64(g.Standby.EdgeDeviceID),
		"description":    "备用来源",
		"priority":       5,
		"is_primary":     false,
		"max_fail_count": 3,
	}, nil).Expect(http.StatusCreated)
	g.StandbySource = second.DataInt("id")
	t.Cleanup(func() {
		edgeCleanupOK(t, "备数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.StandbySource)))
	})
}

// dsList 读回一个来源组（走真实列表端点）。
func dsList(e *harness.Env, deviceID int64, category string) []dsSourceRow {
	path := fmt.Sprintf("/api/v1/data-sources?device_id=%d&category=%s&page_size=50", deviceID, category)
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

// dsActiveCount 断言并返回组内 active 条数（"至多一条 active"是硬不变量）。
func dsActiveCount(t *testing.T, rows []dsSourceRow) int {
	t.Helper()
	active := 0
	for _, row := range rows {
		if row.Status == "active" {
			active++
		}
	}
	if active > 1 {
		t.Fatalf("组内出现 %d 条 active，违反「每组至多一条 active」不变量：%+v", active, rows)
	}
	return active
}

// dsRowByID 在列表里按 id 找一行。
func dsRowByID(rows []dsSourceRow, id int64) (dsSourceRow, bool) {
	for _, row := range rows {
		if row.ID == id {
			return row, true
		}
	}
	return dsSourceRow{}, false
}

// dsFailoverLogs 读取某逻辑设备（组键）的切换日志，可按类别过滤。
func dsFailoverLogs(e *harness.Env, deviceID int64, category string) []dsFailoverLogRow {
	path := fmt.Sprintf("/api/v1/devices/%d/failover-logs", deviceID)
	if category != "" {
		path += "?category=" + category
	}
	resp := e.Admin.Get(path).Expect(http.StatusOK)
	var logs []dsFailoverLogRow
	resp.Decode(&logs)
	return logs
}

// dsReportAndAwaitSuccess 让主机上报一次数据，并等待"成功事件"落到来源上。
//
// 这一步同时做两件事：把 offlinedetector 的活跃缓存刷新（它只在真实数据到达时
// 登记设备），以及证明解析成功 → MarkSuccess 这条健康信号确实是通的。
func dsReportAndAwaitSuccess(e *harness.Env, g *dsGroup) {
	t := e.T
	t.Helper()
	if err := g.Primary.Report(harness.FixtureTempCategory, 21.5); err != nil {
		t.Fatalf("主机上报数据失败: %v", err)
	}
	e.Eventually(30*time.Second, func() error {
		row := dsGet(e, g.PrimarySource)
		if row.LastSuccess == "" {
			return fmt.Errorf("主来源尚未收到成功事件（last_success 为空）")
		}
		if row.Status != "active" {
			return fmt.Errorf("主来源状态 = %q，期望 active", row.Status)
		}
		return nil
	})
	e.Evidence("SIM-DS.health_success", dsGet(e, g.PrimarySource))
}

// ---------------------------------------------------------------------------
// SIM-DS-001 为同一逻辑设备+类别建立主备两个数据源
// ---------------------------------------------------------------------------

func dsRun001(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-001")

	// 首条来源自动成为权威（组内无 active 时可被采纳），第二条是待命候选。
	dsEstablish(e, "SIM-DS-001", g, 3)

	rows := dsList(e, g.LogicalDeviceID, g.Category)
	if len(rows) != 2 {
		t.Fatalf("来源组应有 2 条来源，实际 %d：%+v", len(rows), rows)
	}
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("来源组应有且仅有 1 条 active，实际 %d：%+v", active, rows)
	}
	primary, ok := dsRowByID(rows, g.PrimarySource)
	if !ok {
		t.Fatalf("列表里缺少主来源 #%d：%+v", g.PrimarySource, rows)
	}
	standby, ok := dsRowByID(rows, g.StandbySource)
	if !ok {
		t.Fatalf("列表里缺少备来源 #%d：%+v", g.StandbySource, rows)
	}
	if primary.Status != "active" {
		t.Fatalf("主来源状态 = %q，期望 active（组内首条来源）", primary.Status)
	}
	if standby.Status != "standby" {
		t.Fatalf("备来源状态 = %q，期望 standby（同组已有权威来源）", standby.Status)
	}
	// 列表按 priority DESC 给出候选顺序（用户看到的"主/备"顺序）。
	if rows[0].ID != g.PrimarySource {
		t.Fatalf("列表首条应为高优先级的主来源 #%d，实际 #%d", g.PrimarySource, rows[0].ID)
	}
	if primary.Priority != 10 || standby.Priority != 5 {
		t.Fatalf("优先级没有原样保存：primary=%d standby=%d", primary.Priority, standby.Priority)
	}
	if !primary.IsPrimary || standby.IsPrimary {
		t.Fatalf("is_primary 声明意图没有原样保存：primary=%v standby=%v", primary.IsPrimary, standby.IsPrimary)
	}
	// 两个来源必须指向同一组键的不同物理来源（组键与血缘都对）。
	if primary.DeviceID != g.LogicalDeviceID || standby.DeviceID != g.LogicalDeviceID {
		t.Fatalf("来源的 device_id 与组键不一致：primary=%d standby=%d 期望 %d",
			primary.DeviceID, standby.DeviceID, g.LogicalDeviceID)
	}
	if primary.EdgeDeviceID == standby.EdgeDeviceID {
		t.Fatalf("主备来源指向同一台边缘设备 #%d，无法构成主备", primary.EdgeDeviceID)
	}
	if primary.SourceType != "edge_device" {
		t.Fatalf("来源类型 = %q，期望 edge_device", primary.SourceType)
	}
	if primary.MaxFailCount != 3 {
		t.Fatalf("max_fail_count = %d，期望 3", primary.MaxFailCount)
	}
	e.Evidence("SIM-DS-001.group", rows)

	// 熔断阈值的合法区间是 [1,20]（领域层校验）：越界必须 400，不能被静默接受。
	bad := dsCreateSource(e, "SIM-DS-001", "越界阈值", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       "rainfall",
		"edge_device_id": int64(g.Primary.EdgeDeviceID),
		"max_fail_count": 21,
	}, nil).Expect(http.StatusBadRequest)
	e.Evidence("SIM-DS-001.max_fail_count_rejected", bad.Message)
}

// ---------------------------------------------------------------------------
// SIM-DS-002 同分组内不允许出现第二个活动数据源（返回冲突）
// ---------------------------------------------------------------------------

func dsRun002(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-002")

	first := dsCreateSource(e, "SIM-DS-002", "主来源", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       g.Category,
		"edge_device_id": int64(g.Primary.EdgeDeviceID),
		"priority":       10,
	}, nil).Expect(http.StatusCreated)
	g.PrimarySource = first.DataInt("id")
	t.Cleanup(func() {
		edgeCleanupOK(t, "主数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.PrimarySource)))
	})

	// 客户端不能自己"指定"权威来源：请求体里的 status 不是创建 DTO 的字段，
	// 必须被忽略，落库后仍然是 standby（否则同组会出现两条 active）。
	second := dsCreateSource(e, "SIM-DS-002", "备来源", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       g.Category,
		"edge_device_id": int64(g.Standby.EdgeDeviceID),
		"priority":       5,
	}, map[string]any{"status": "active"}).Expect(http.StatusCreated)
	g.StandbySource = second.DataInt("id")
	t.Cleanup(func() {
		edgeCleanupOK(t, "备数据源", e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.StandbySource)))
	})
	if got := second.DataString("status"); got != "standby" {
		t.Fatalf("客户端注入 status=active 后落库状态 = %q，期望 standby（status 由领域层拥有）", got)
	}

	// 同组同 edge_device 的第二条来源：必须 409（唯一键冲突），而不是静默新建。
	dup := dsCreateSource(e, "SIM-DS-002", "重复来源", map[string]any{
		"device_id":      g.LogicalDeviceID,
		"category":       g.Category,
		"edge_device_id": int64(g.Standby.EdgeDeviceID),
		"priority":       1,
	}, nil).Expect(http.StatusConflict)
	e.Evidence("SIM-DS-002.duplicate_conflict", dup.Message)

	rows := dsList(e, g.LogicalDeviceID, g.Category)
	if len(rows) != 2 {
		t.Fatalf("被拒的重复创建留下了残留：%+v", rows)
	}
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("组内 active 条数 = %d，期望 1：%+v", active, rows)
	}

	// 显式切换：目标变 active、原权威降为 standby，active 仍然只有一条。
	e.Admin.Post(fmt.Sprintf("/api/v1/data-sources/%d/activate", g.StandbySource), nil).
		Expect(http.StatusOK)
	rows = dsList(e, g.LogicalDeviceID, g.Category)
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("手动切换后组内 active 条数 = %d，期望 1：%+v", active, rows)
	}
	standbyRow, _ := dsRowByID(rows, g.StandbySource)
	primaryRow, _ := dsRowByID(rows, g.PrimarySource)
	if standbyRow.Status != "active" || primaryRow.Status != "standby" {
		t.Fatalf("手动切换结果不正确：目标=%q 原权威=%q", standbyRow.Status, primaryRow.Status)
	}
	e.Evidence("SIM-DS-002.after_activate", rows)

	// 目标已是 active 时 activate 幂等：不报错、不产生第二条 active。
	e.Admin.Post(fmt.Sprintf("/api/v1/data-sources/%d/activate", g.StandbySource), nil).
		Expect(http.StatusOK)
	rows = dsList(e, g.LogicalDeviceID, g.Category)
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("重复 activate 后组内 active 条数 = %d，期望 1：%+v", active, rows)
	}
}

// ---------------------------------------------------------------------------
// SIM-DS-003 主数据源健康恶化时自动切换到备用数据源
// ---------------------------------------------------------------------------

func dsRun003(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-003")
	// max_fail_count=1：设备离线一次即熔断，让自动切换在一次真实信号内发生。
	dsEstablish(e, "SIM-DS-003", g, 1)

	// 起点：主机必须真的上报过（离线检测器只对"见过数据的设备"判离线），
	// 且成功事件已把 last_success 写在主来源上。
	dsReportAndAwaitSuccess(e, g)

	// 主机停止上报。offlinedetector 每 5s 扫描一次，last_data_at 超过 60s
	// 即判定边缘设备离线 → 触发 datasource 的 device_offline 失败信号 →
	// fail_count 达到阈值 → 自动切换。超时给足 150s。
	e.Eventually(150*time.Second, func() error {
		primary := dsGet(e, g.PrimarySource)
		rows := dsList(e, g.LogicalDeviceID, g.Category)
		if primary.Status != "error" {
			return fmt.Errorf("主来源状态 = %q（fail_count=%d/%d），尚未熔断；当前组=%+v",
				primary.Status, primary.FailCount, primary.MaxFailCount, rows)
		}
		active := 0
		for _, row := range rows {
			if row.Status == "active" {
				active++
			}
		}
		if active != 1 {
			return fmt.Errorf("自动切换后组内 active 条数 = %d，期望 1；当前组=%+v 切换日志=%+v",
				active, rows, dsFailoverLogs(e, g.LogicalDeviceID, g.Category))
		}
		return nil
	})

	rows := dsList(e, g.LogicalDeviceID, g.Category)
	primary, _ := dsRowByID(rows, g.PrimarySource)
	standby, _ := dsRowByID(rows, g.StandbySource)
	if primary.Status != "error" {
		t.Fatalf("健康恶化的主来源状态 = %q，期望 error（熔断）", primary.Status)
	}
	if standby.Status != "active" {
		t.Fatalf("自动切换后备用来源状态 = %q，期望 active", standby.Status)
	}
	if primary.FailCount < 1 {
		t.Fatalf("主来源 fail_count = %d，期望 ≥1（离线信号未计入失败）", primary.FailCount)
	}
	if primary.LastFailure == "" {
		t.Fatalf("熔断后 last_failure 为空，失败时刻没有留痕：%+v", primary)
	}
	e.Evidence("SIM-DS-003.after_failover", rows)

	// 自动切换必须留痕：要能说明"因为设备离线，从 A 切到 B"。
	logs := dsFailoverLogs(e, g.LogicalDeviceID, g.Category)
	var autoLog *dsFailoverLogRow
	for i := range logs {
		if logs[i].Reason == "auto" {
			autoLog = &logs[i]
			break
		}
	}
	if autoLog == nil {
		t.Fatalf("没有自动切换日志：%+v", logs)
	}
	if autoLog.FromSourceID != g.PrimarySource || autoLog.ToSourceID != g.StandbySource {
		t.Fatalf("自动切换日志方向错误：from=%d to=%d，期望 from=%d to=%d",
			autoLog.FromSourceID, autoLog.ToSourceID, g.PrimarySource, g.StandbySource)
	}
	if autoLog.Trigger != "device_offline" {
		t.Fatalf("自动切换的 trigger = %q，期望 device_offline", autoLog.Trigger)
	}
	e.Evidence("SIM-DS-003.failover_log", autoLog)
}

// ---------------------------------------------------------------------------
// SIM-DS-004 主数据源恢复后管理员可重新激活为主（回切），并留下切换记录
// ---------------------------------------------------------------------------

func dsRun004(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-004")
	dsEstablish(e, "SIM-DS-004", g, 1)
	dsReportAndAwaitSuccess(e, g)

	// 制造一次真实的自动切换（与 SIM-DS-003 同一条路径）。
	e.Eventually(150*time.Second, func() error {
		if row := dsGet(e, g.PrimarySource); row.Status != "error" {
			return fmt.Errorf("主来源状态 = %q，尚未熔断", row.Status)
		}
		if row := dsGet(e, g.StandbySource); row.Status != "active" {
			return fmt.Errorf("备用来源状态 = %q，尚未接替", row.Status)
		}
		return nil
	})

	// 主机恢复上报：成功事件只清失败计数并把 error 降回 standby，
	// **不会**自动抢回权威（R1）——这正是"回切必须人工"的证据。
	if err := g.Primary.Report(harness.FixtureTempCategory, 22.5); err != nil {
		t.Fatalf("主机恢复上报失败: %v", err)
	}
	e.Eventually(30*time.Second, func() error {
		row := dsGet(e, g.PrimarySource)
		if row.Status != "standby" {
			return fmt.Errorf("恢复后的主来源状态 = %q（fail_count=%d），期望 standby", row.Status, row.FailCount)
		}
		if row.FailCount != 0 {
			return fmt.Errorf("恢复后 fail_count = %d，期望 0", row.FailCount)
		}
		return nil
	})
	recovered := dsGet(e, g.PrimarySource)
	if recovered.LastFailure == "" {
		t.Fatalf("恢复后 last_failure 被清空，失去熔断证据：%+v", recovered)
	}
	if standby := dsGet(e, g.StandbySource); standby.Status != "active" {
		t.Fatalf("恢复事件不应抢占权威来源，备用来源状态 = %q，期望仍为 active", standby.Status)
	}
	e.Evidence("SIM-DS-004.recovered_not_taking_over", recovered)

	// 管理员显式回切。
	e.Admin.Post(fmt.Sprintf("/api/v1/data-sources/%d/activate", g.PrimarySource), nil).
		Expect(http.StatusOK)
	rows := dsList(e, g.LogicalDeviceID, g.Category)
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("回切后组内 active 条数 = %d，期望 1：%+v", active, rows)
	}
	primary, _ := dsRowByID(rows, g.PrimarySource)
	standby, _ := dsRowByID(rows, g.StandbySource)
	if primary.Status != "active" {
		t.Fatalf("回切后主来源状态 = %q，期望 active", primary.Status)
	}
	if standby.Status != "standby" {
		t.Fatalf("回切后原权威来源状态 = %q，期望 standby", standby.Status)
	}
	e.Evidence("SIM-DS-004.after_manual_failback", rows)

	// 回切同样必须留痕（reason=manual，自动触发信号为空）。
	logs := dsFailoverLogs(e, g.LogicalDeviceID, g.Category)
	var manualLog *dsFailoverLogRow
	for i := range logs {
		if logs[i].Reason == "manual" {
			manualLog = &logs[i]
			break
		}
	}
	if manualLog == nil {
		t.Fatalf("回切没有留下切换记录：%+v", logs)
	}
	if manualLog.FromSourceID != g.StandbySource || manualLog.ToSourceID != g.PrimarySource {
		t.Fatalf("回切记录方向错误：from=%d to=%d，期望 from=%d to=%d",
			manualLog.FromSourceID, manualLog.ToSourceID, g.StandbySource, g.PrimarySource)
	}
	if manualLog.Trigger != "" {
		t.Fatalf("人工切换不应带自动触发信号，实际 trigger=%q", manualLog.Trigger)
	}
	e.Evidence("SIM-DS-004.manual_failover_log", manualLog)
}

// ---------------------------------------------------------------------------
// SIM-DS-005 故障转移全过程写入故障日志并可查询
// ---------------------------------------------------------------------------

func dsRun005(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-005")
	dsEstablish(e, "SIM-DS-005", g, 3)

	// 起点：没有任何切换记录（干净状态是断言可读性的前提）。
	if logs := dsFailoverLogs(e, g.LogicalDeviceID, g.Category); len(logs) != 0 {
		t.Fatalf("新来源组不应已有切换日志：%+v", logs)
	}

	// "点名"切换：管理员选择备用来源作为新的权威来源。
	e.Admin.Post(fmt.Sprintf("/api/v1/data-sources/%d/activate", g.StandbySource), nil).
		Expect(http.StatusOK)
	activated := dsGet(e, g.StandbySource)
	if activated.Status != "active" {
		t.Fatalf("activate 之后目标来源状态 = %q，期望 active", activated.Status)
	}

	logs := dsFailoverLogs(e, g.LogicalDeviceID, g.Category)
	if len(logs) != 1 {
		t.Fatalf("应恰好留下 1 条切换日志，实际 %d：%+v", len(logs), logs)
	}
	log := logs[0]
	if log.Category != g.Category {
		t.Fatalf("切换日志 category = %q，期望 %q", log.Category, g.Category)
	}
	if log.FromSourceID != g.PrimarySource || log.ToSourceID != g.StandbySource {
		t.Fatalf("切换日志方向错误：from=%d to=%d，期望 from=%d to=%d",
			log.FromSourceID, log.ToSourceID, g.PrimarySource, g.StandbySource)
	}
	if log.Reason != "manual" {
		t.Fatalf("手动切换的 reason = %q，期望 manual", log.Reason)
	}
	if log.DeviceID != g.LogicalDeviceID {
		t.Fatalf("切换日志 device_id = %d，期望组键 %d", log.DeviceID, g.LogicalDeviceID)
	}
	e.Evidence("SIM-DS-005.failover_log", logs)

	// 类别过滤必须精确：别的类别查不到这条记录（否则日志页会串行）。
	if other := dsFailoverLogs(e, g.LogicalDeviceID, "rainfall"); len(other) != 0 {
		t.Fatalf("按不相关类别过滤仍返回了记录：%+v", other)
	}

	// 健康事件同样可查：切换会写一条 transition 事件（来源页的健康时间线）。
	health := e.Admin.Get(fmt.Sprintf("/api/v1/data-sources/%d/health", g.StandbySource)).
		Expect(http.StatusOK)
	var events []struct {
		SourceID uint   `json:"source_id"`
		DeviceID uint   `json:"device_id"`
		Category string `json:"category"`
		Status   string `json:"status"`
		Message  string `json:"message"`
	}
	health.Decode(&events)
	if len(events) == 0 {
		t.Fatalf("切换没有写入健康事件：%s", health.BodyString())
	}
	if events[0].Status != "transition" {
		t.Fatalf("切换后的健康事件 status = %q，期望 transition", events[0].Status)
	}
	if events[0].Category != g.Category {
		t.Fatalf("健康事件 category = %q，期望 %q", events[0].Category, g.Category)
	}
	e.Evidence("SIM-DS-005.health_events", events)

	// 不存在的来源必须是 404，而不是 500 或静默成功。
	e.Admin.Delete("/api/v1/data-sources/999999999").Expect(http.StatusNotFound)
}

// ---------------------------------------------------------------------------
// SIM-DS-006 删除数据源后分组"至多一个活动"不变式仍成立
// ---------------------------------------------------------------------------

func dsRun006(e *harness.Env) {
	t := e.T
	g := dsProvisionGroup(e, "SIM-DS-006")
	dsEstablish(e, "SIM-DS-006", g, 3)

	// 删掉当前权威来源：组内还有待命候选时，候选必须接替（不能留下"没人取值"）。
	e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.PrimarySource)).
		Expect(http.StatusOK)
	rows := dsList(e, g.LogicalDeviceID, g.Category)
	if len(rows) != 1 {
		t.Fatalf("删除权威来源后应剩 1 条来源，实际 %d：%+v", len(rows), rows)
	}
	if active := dsActiveCount(t, rows); active != 1 {
		t.Fatalf("删除权威来源后组内 active 条数 = %d，期望 1（候选接替）：%+v", active, rows)
	}
	if rows[0].ID != g.StandbySource || rows[0].Status != "active" {
		t.Fatalf("接替者应为备来源 #%d 且 status=active，实际 %+v", g.StandbySource, rows[0])
	}
	e.Evidence("SIM-DS-006.after_delete_active", rows)

	// 接替也是一次切换：必须写日志。
	logs := dsFailoverLogs(e, g.LogicalDeviceID, g.Category)
	if len(logs) == 0 {
		t.Fatalf("删除权威来源导致的接替没有写切换日志")
	}
	if logs[0].ToSourceID != g.StandbySource || logs[0].FromSourceID != g.PrimarySource {
		t.Fatalf("接替日志方向错误：%+v", logs[0])
	}
	e.Evidence("SIM-DS-006.takeover_log", logs[0])

	// 删掉最后一条来源：组内 0 条 active，"至多一条"仍成立（不是"必须有一条"）。
	e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.StandbySource)).
		Expect(http.StatusOK)
	rows = dsList(e, g.LogicalDeviceID, g.Category)
	if len(rows) != 0 {
		t.Fatalf("删除全部来源后组应为空，实际 %+v", rows)
	}
	if active := dsActiveCount(t, rows); active != 0 {
		t.Fatalf("空组的 active 条数 = %d，期望 0", active)
	}
	e.Evidence("SIM-DS-006.after_delete_all", rows)

	// 重复删除同一条来源必须 404（幂等语义由调用方负责，不能假装成功）。
	e.Admin.Delete(fmt.Sprintf("/api/v1/data-sources/%d", g.StandbySource)).
		Expect(http.StatusNotFound)
}
