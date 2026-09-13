package api

// 分页契约测试 (架构与接口评估及优化方案 P1.2 裁决: items + total)。
//
// 覆盖 automation-events 与 logical-devices 两个端点, 四类边界各一条:
// 默认页 / 指定页 / 超范围页 / 筛选+分页组合。
//
// 为什么必须有「超范围页」一条: 本任务的根因是 automation-events 曾用
// Limit(500) **静默截断** —— 用户看到 500 条却以为看到了全部。真分页的判据因此
// 不只是「能翻页」, 而是「total 如实反映过滤后的全量条数, 且越界页返回空集而非
// 报错或退化成第一页」; 只断言首页等于没断言。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newPaginationRouter 装配 automation-events + logical-devices 两组路由。
// 复用 setupTestRouter 的 db (已迁移 LogicalDevice), 再补迁 automation 两张表。
func newPaginationRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{}); err != nil {
		t.Fatalf("auto migrate automation: %v", err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, nil, nil, nil, nil)
	return r, db
}

// pageResp 是分页端点的统一响应形状 (items + total + page/page_size 回显)。
type pageResp struct {
	Code int `json:"code"`
	Data struct {
		Items    []map[string]interface{} `json:"items"`
		Total    int64                    `json:"total"`
		Page     int                      `json:"page"`
		PageSize int                      `json:"page_size"`
	} `json:"data"`
}

func getPage(t *testing.T, r *gin.Engine, path string) pageResp {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, w.Code, w.Body.String())
	}
	var resp pageResp
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET %s 解析响应失败: %v (body=%s)", path, err, w.Body.String())
	}
	// items 必须是非 nil 切片: 空集序列化为 [] 而非 null。
	if resp.Data.Items == nil {
		t.Fatalf("GET %s items 为 null, 应为 []", path)
	}
	return resp
}

// seedAutomationEvents 造 25 条事件: rule 1 有 20 条 (其中 expired 12 条), rule 2 有 5 条。
// 25 > 默认 page_size(20) 且 < 旧硬上限 500, 因此若分页失效 (退回全量或截断),
// 首页条数断言会立刻变红。
func seedAutomationEvents(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		result := models.AutomationResultExecuted
		if i < 12 {
			result = models.AutomationResultExpired
		}
		ev := models.AutomationEvent{
			RuleID: 1, TriggeredAt: base.Add(time.Duration(i) * time.Minute),
			TriggerSource: models.AutomationTriggerSourceAuto, Result: result,
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		ev := models.AutomationEvent{
			RuleID: 2, TriggeredAt: base.Add(time.Duration(i) * time.Minute),
			TriggerSource: models.AutomationTriggerSourceAuto, Result: models.AutomationResultNotification,
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAutomationEventsPagination_DefaultPage(t *testing.T) {
	r, db := newPaginationRouter(t)
	seedAutomationEvents(t, db)

	// 无参数 → page=1, page_size=20, total=25 (全量真实条数, 不是 500 也不是 20)。
	resp := getPage(t, r, "/api/v1/automation-events")
	if got := len(resp.Data.Items); got != 20 {
		t.Fatalf("默认页 items = %d, want 20", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("默认页 total = %d, want 25", resp.Data.Total)
	}
	if resp.Data.Page != 1 || resp.Data.PageSize != 20 {
		t.Fatalf("默认页回显 page/page_size = %d/%d, want 1/20", resp.Data.Page, resp.Data.PageSize)
	}

	// 排序契约保持 id DESC: 首页第一条必须是 id 最大的那条事件。
	var maxID uint
	if err := db.Model(&models.AutomationEvent{}).Select("MAX(id)").Scan(&maxID).Error; err != nil {
		t.Fatal(err)
	}
	first, _ := resp.Data.Items[0]["id"].(float64)
	if uint(first) != maxID {
		t.Fatalf("首页首条 id = %v, want %d (id DESC 契约)", first, maxID)
	}
}

func TestAutomationEventsPagination_ExplicitPage(t *testing.T) {
	r, db := newPaginationRouter(t)
	seedAutomationEvents(t, db)

	// 第 2 页 (page_size=10) → 10 条, 且与第 1 页无重叠 (真偏移, 不是重复首页)。
	p1 := getPage(t, r, "/api/v1/automation-events?page=1&page_size=10")
	p2 := getPage(t, r, "/api/v1/automation-events?page=2&page_size=10")

	if len(p1.Data.Items) != 10 || len(p2.Data.Items) != 10 {
		t.Fatalf("page1/page2 items = %d/%d, want 10/10", len(p1.Data.Items), len(p2.Data.Items))
	}
	if p1.Data.Total != 25 || p2.Data.Total != 25 {
		t.Fatalf("total 应恒为 25, got %d/%d", p1.Data.Total, p2.Data.Total)
	}
	seen := map[float64]bool{}
	for _, it := range p1.Data.Items {
		seen[it["id"].(float64)] = true
	}
	for _, it := range p2.Data.Items {
		if seen[it["id"].(float64)] {
			t.Fatalf("第 2 页与第 1 页重叠于 id=%v (Offset 未生效)", it["id"])
		}
	}

	// 最后一页 (25 条, page_size=10) → 只剩 5 条。
	p3 := getPage(t, r, "/api/v1/automation-events?page=3&page_size=10")
	if got := len(p3.Data.Items); got != 5 {
		t.Fatalf("末页 items = %d, want 5 (25 取模 10)", got)
	}
}

func TestAutomationEventsPagination_OutOfRangePage(t *testing.T) {
	r, db := newPaginationRouter(t)
	seedAutomationEvents(t, db)

	// 超范围页 → 200 + 空集 + total 仍为真实全量 (不得报错、不得退化成第一页)。
	resp := getPage(t, r, "/api/v1/automation-events?page=99&page_size=20")
	if got := len(resp.Data.Items); got != 0 {
		t.Fatalf("越界页 items = %d, want 0", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("越界页 total = %d, want 25 (total 是过滤后全量, 与页码无关)", resp.Data.Total)
	}

	// page<1 归 1; page_size 越界归默认 20 —— clamp 不报错。
	clamped := getPage(t, r, "/api/v1/automation-events?page=0&page_size=100000")
	if got := len(clamped.Data.Items); got != 20 {
		t.Fatalf("clamp 后 items = %d, want 20", got)
	}
	if clamped.Data.Page != 1 || clamped.Data.PageSize != 20 {
		t.Fatalf("clamp 回显 = %d/%d, want 1/20", clamped.Data.Page, clamped.Data.PageSize)
	}
}

func TestAutomationEventsPagination_FilterWithPagination(t *testing.T) {
	r, db := newPaginationRouter(t)
	seedAutomationEvents(t, db)

	// rule_id=1 → total=20 (过滤后的全量, 不是 25)。
	resp := getPage(t, r, "/api/v1/automation-events?rule_id=1&page=1&page_size=8")
	if resp.Data.Total != 20 {
		t.Fatalf("rule_id=1 total = %d, want 20", resp.Data.Total)
	}
	if got := len(resp.Data.Items); got != 8 {
		t.Fatalf("rule_id=1 首页 items = %d, want 8", got)
	}
	for _, it := range resp.Data.Items {
		if rid, _ := it["rule_id"].(float64); uint(rid) != 1 {
			t.Fatalf("rule_id=1 结果混入 rule_id=%v", it["rule_id"])
		}
	}

	// 组合筛选 rule_id=1&result=expired → total=12, 且分页在过滤**之后**进行。
	combo := getPage(t, r, "/api/v1/automation-events?rule_id=1&result=expired&page=1&page_size=5")
	if combo.Data.Total != 12 {
		t.Fatalf("rule_id=1&result=expired total = %d, want 12", combo.Data.Total)
	}
	if got := len(combo.Data.Items); got != 5 {
		t.Fatalf("组合筛选首页 items = %d, want 5", got)
	}
	for _, it := range combo.Data.Items {
		if it["result"] != models.AutomationResultExpired {
			t.Fatalf("组合筛选结果混入 result=%v", it["result"])
		}
		if rid, _ := it["rule_id"].(float64); uint(rid) != 1 {
			t.Fatalf("组合筛选结果混入 rule_id=%v", it["rule_id"])
		}
	}
	// 组合筛选第 3 页 → 12 条 5/页 → 剩 2 条 (证明 offset 用的是过滤后的 total)。
	last := getPage(t, r, "/api/v1/automation-events?rule_id=1&result=expired&page=3&page_size=5")
	if got := len(last.Data.Items); got != 2 {
		t.Fatalf("组合筛选末页 items = %d, want 2", got)
	}

	// 未命中筛选 → 200 + 空集 + total=0 (不得退化成全量)。
	none := getPage(t, r, "/api/v1/automation-events?rule_id=999&page=1&page_size=5")
	if len(none.Data.Items) != 0 || none.Data.Total != 0 {
		t.Fatalf("未命中筛选 = %d 条/total %d, want 0/0", len(none.Data.Items), none.Data.Total)
	}
}

func TestLogicalDevicesPagination(t *testing.T) {
	r, db := setupTestRouter(t)
	for i := 0; i < 25; i++ {
		ld := models.LogicalDevice{
			IdentityKey: "pag:" + strconv.Itoa(i), Name: "LD-" + strconv.Itoa(i),
			DeviceType: "bms", RetentionDays: 365,
		}
		if err := db.Create(&ld).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 默认页: page_size=20, total 为全量 25。
	def := getPage(t, r, "/api/v1/logical-devices")
	if got := len(def.Data.Items); got != 20 {
		t.Fatalf("默认页 items = %d, want 20", got)
	}
	if def.Data.Total != 25 {
		t.Fatalf("默认页 total = %d, want 25 (total 必须是全量, 不是当前页)", def.Data.Total)
	}

	// 第 2 页 → 5 条, 与第 1 页不重叠。
	p2 := getPage(t, r, "/api/v1/logical-devices?page=2&page_size=20")
	if got := len(p2.Data.Items); got != 5 {
		t.Fatalf("第 2 页 items = %d, want 5", got)
	}
	seen := map[float64]bool{}
	for _, it := range def.Data.Items {
		seen[it["id"].(float64)] = true
	}
	for _, it := range p2.Data.Items {
		if seen[it["id"].(float64)] {
			t.Fatalf("第 2 页与第 1 页重叠于 id=%v (Offset 未生效)", it["id"])
		}
	}

	// 超范围页 → 200 + 空集 + total 不变。
	out := getPage(t, r, "/api/v1/logical-devices?page=50&page_size=20")
	if got := len(out.Data.Items); got != 0 {
		t.Fatalf("越界页 items = %d, want 0", got)
	}
	if out.Data.Total != 25 {
		t.Fatalf("越界页 total = %d, want 25", out.Data.Total)
	}

	// clamp: page=0 → 1, page_size 越界 → 默认 20。
	clamped := getPage(t, r, "/api/v1/logical-devices?page=0&page_size=0")
	if got := len(clamped.Data.Items); got != 20 {
		t.Fatalf("clamp 后 items = %d, want 20", got)
	}

	// 聚合字段仍在当前页生效 (分页不得吞掉 instance_count/last_data_at)。
	if _, ok := def.Data.Items[0]["instance_count"]; !ok {
		t.Fatalf("分页后条目缺 instance_count 聚合字段: %v", def.Data.Items[0])
	}

	// ── 筛选 + 分页组合 (§3.3.5: 服务端分页时检索必须覆盖全库, 不是当前页本地过滤) ──
	// device_type 过滤: 25 条全为 bms, 造 3 条 sn3001 后过滤应得 3 条 (而非 28)。
	for i := 0; i < 3; i++ {
		ld := models.LogicalDevice{
			IdentityKey: "other:" + strconv.Itoa(i), Name: "Other-" + strconv.Itoa(i),
			DeviceType: "sn3001", RetentionDays: 365,
		}
		if err := db.Create(&ld).Error; err != nil {
			t.Fatal(err)
		}
	}
	byType := getPage(t, r, "/api/v1/logical-devices?device_type=sn3001&page=1&page_size=20")
	if byType.Data.Total != 3 {
		t.Fatalf("device_type=sn3001 total = %d, want 3 (过滤须在 Count 侧生效)", byType.Data.Total)
	}
	if got := len(byType.Data.Items); got != 3 {
		t.Fatalf("device_type=sn3001 items = %d, want 3", got)
	}
	for _, it := range byType.Data.Items {
		if it["device_type"] != "sn3001" {
			t.Fatalf("device_type 过滤混入 %v", it["device_type"])
		}
	}

	// search 是全库检索: "LD-1" 命中的 LD-1x 分散在**第 1 页之外**, 若 search 被当成
	// 当前页本地过滤就会漏掉 —— 这正是分页前的伪装全局检索缺陷。
	// 25 条 LD-0..LD-24 中名称含 "ld-1" 的有 LD-1、LD-10..LD-19 共 11 条。
	bySearch := getPage(t, r, "/api/v1/logical-devices?search=ld-1&page=1&page_size=20")
	if bySearch.Data.Total != 11 {
		t.Fatalf("search=ld-1 total = %d, want 11 (检索须覆盖全库)", bySearch.Data.Total)
	}
	for _, it := range bySearch.Data.Items {
		name, _ := it["name"].(string)
		if !strings.Contains(strings.ToLower(name), "ld-1") {
			t.Fatalf("search 结果混入不匹配项 %q", name)
		}
	}

	// 筛选 + 分页组合: 11 条按 5/页 → 第 3 页剩 1 条 (offset 基于过滤后的集合)。
	combo := getPage(t, r, "/api/v1/logical-devices?search=ld-1&page=3&page_size=5")
	if got := len(combo.Data.Items); got != 1 {
		t.Fatalf("search+page 组合末页 items = %d, want 1", got)
	}
	if combo.Data.Total != 11 {
		t.Fatalf("search+page 组合 total = %d, want 11", combo.Data.Total)
	}
}

// =====================================================================
// 负债 I-11「假分页」— GET /nodes 与 GET /edge-devices 的真分页契约
// =====================================================================
//
// 为什么必须有这组用例 (与 automation-events 同源的根因):
// 两个端点改前都是 `Find` + `Success(c, 裸数组)`, **静默丢弃** page/page_size ——
// 而前端 NodeList.vue / EdgeDeviceList.vue 一直在发这两个参数, :data 绑定的却是
// 本地全量数组, el-pagination 纯装饰。用户看到分页器、以为在翻页, 实际是本地切片。
//
// 因此判据同样不只是「能翻页」, 而是三条:
//   1. total 如实反映**过滤后**的全量条数 (不是当前页条数、不是整表条数);
//   2. 越界页返回**空集 + 正确 total**, 不报错、不退化成第一页;
//   3. 筛选在服务端**全库**生效 (§3.3.5: 不得把当前页本地筛选伪装成全局检索) ——
//      命中项刻意放在第 1 页之外, 只断言首页等于没断言。
//
// edge-devices 另有一条本端点独有的不变式: latest-data 富化只对**当前页**的
// deviceIDs 执行。若富化仍走全表, 「分页」了但仍在全表捞数据, 首屏开销与改前一样。

// newListNodeRouter 装配 /nodes 列表所需的依赖。
// setupTestRouter 已注册 registerNodeRoutes + registerEdgeDeviceRoutes,
// 这里只需补迁 NodeEvent (registerNodeRoutes 注册的 status-history 路由会碰它)。
func newListNodeRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	return r, db
}

// seedNodes 造 25 个节点: 20 个 model=ESP32 (其中 12 个 status=online),
// 5 个 model=RPi4 (全部 offline)。
// 25 > 默认 page_size(20): 若分页失效 (退回全量), 首页条数断言立刻变红。
func seedNodes(t *testing.T, db *gorm.DB) {
	t.Helper()
	for i := 0; i < 20; i++ {
		status := "offline"
		if i < 12 {
			status = "online"
		}
		n := models.Node{
			NodeID: "NODE-" + strconv.Itoa(i), Name: "Node-" + strconv.Itoa(i),
			Model: "ESP32", Status: status,
		}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		n := models.Node{
			NodeID: "RPI-" + strconv.Itoa(i), Name: "RPi-" + strconv.Itoa(i),
			Model: "RPi4", Status: "offline",
		}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestNodesPagination_DefaultPage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedNodes(t, db)

	// 无参数 → page=1, page_size=20, total=25 (全量真实条数, 不是 20)。
	resp := getPage(t, r, "/api/v1/nodes")
	if got := len(resp.Data.Items); got != 20 {
		t.Fatalf("默认页 items = %d, want 20", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("默认页 total = %d, want 25 (total 是全量, 不是当前页)", resp.Data.Total)
	}
	if resp.Data.Page != 1 || resp.Data.PageSize != 20 {
		t.Fatalf("默认页回显 page/page_size = %d/%d, want 1/20", resp.Data.Page, resp.Data.PageSize)
	}
}

func TestNodesPagination_ExplicitPage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedNodes(t, db)

	// 第 2 页 (page_size=10) → 10 条, 且与第 1 页无重叠 (真偏移, 不是重复首页)。
	p1 := getPage(t, r, "/api/v1/nodes?page=1&page_size=10")
	p2 := getPage(t, r, "/api/v1/nodes?page=2&page_size=10")

	if len(p1.Data.Items) != 10 || len(p2.Data.Items) != 10 {
		t.Fatalf("page1/page2 items = %d/%d, want 10/10", len(p1.Data.Items), len(p2.Data.Items))
	}
	if p1.Data.Total != 25 || p2.Data.Total != 25 {
		t.Fatalf("total 应恒为 25, got %d/%d", p1.Data.Total, p2.Data.Total)
	}
	seen := map[float64]bool{}
	for _, it := range p1.Data.Items {
		seen[it["id"].(float64)] = true
	}
	for _, it := range p2.Data.Items {
		if seen[it["id"].(float64)] {
			t.Fatalf("第 2 页与第 1 页重叠于 id=%v (Offset 未生效)", it["id"])
		}
	}

	// 末页 (25 条, page_size=10) → 只剩 5 条。
	p3 := getPage(t, r, "/api/v1/nodes?page=3&page_size=10")
	if got := len(p3.Data.Items); got != 5 {
		t.Fatalf("末页 items = %d, want 5 (25 取模 10)", got)
	}
}

func TestNodesPagination_OutOfRangePage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedNodes(t, db)

	// 超范围页 → 200 + 空集 + total 仍为真实全量 (不得报错、不得退化成第一页)。
	resp := getPage(t, r, "/api/v1/nodes?page=99&page_size=20")
	if got := len(resp.Data.Items); got != 0 {
		t.Fatalf("越界页 items = %d, want 0 (不得退化成第一页)", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("越界页 total = %d, want 25 (total 与页码无关)", resp.Data.Total)
	}

	// page<1 归 1; page_size 越界归默认 20 —— clamp 不报错。
	clamped := getPage(t, r, "/api/v1/nodes?page=0&page_size=100000")
	if got := len(clamped.Data.Items); got != 20 {
		t.Fatalf("clamp 后 items = %d, want 20", got)
	}
	if clamped.Data.Page != 1 || clamped.Data.PageSize != 20 {
		t.Fatalf("clamp 回显 = %d/%d, want 1/20", clamped.Data.Page, clamped.Data.PageSize)
	}
}

func TestNodesPagination_FilterWithPagination(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedNodes(t, db)

	// status=online → total=12 (过滤后的全量, 不是 25)。
	byStatus := getPage(t, r, "/api/v1/nodes?status=online&page=1&page_size=5")
	if byStatus.Data.Total != 12 {
		t.Fatalf("status=online total = %d, want 12 (过滤须在 Count 侧生效)", byStatus.Data.Total)
	}
	if got := len(byStatus.Data.Items); got != 5 {
		t.Fatalf("status=online 首页 items = %d, want 5", got)
	}
	for _, it := range byStatus.Data.Items {
		if it["status"] != "online" {
			t.Fatalf("status 过滤混入 %v", it["status"])
		}
	}

	// 组合筛选 status=online&model=ESP32 → 仍是 12; 再加一个不存在的 model → 0。
	combo := getPage(t, r, "/api/v1/nodes?status=online&model=ESP32&page=2&page_size=5")
	if combo.Data.Total != 12 {
		t.Fatalf("status+model total = %d, want 12", combo.Data.Total)
	}
	if got := len(combo.Data.Items); got != 5 {
		t.Fatalf("status+model 第 2 页 items = %d, want 5", got)
	}
	// 12 条按 5/页 → 第 3 页剩 2 条 (证明 offset 用的是过滤后的 total)。
	last := getPage(t, r, "/api/v1/nodes?status=online&page=3&page_size=5")
	if got := len(last.Data.Items); got != 2 {
		t.Fatalf("status=online 末页 items = %d, want 2", got)
	}

	// 未命中筛选 → 200 + 空集 + total=0 (不得退化成全量)。
	none := getPage(t, r, "/api/v1/nodes?status=online&model=NOSUCH&page=1&page_size=5")
	if len(none.Data.Items) != 0 || none.Data.Total != 0 {
		t.Fatalf("未命中筛选 = %d 条/total %d, want 0/0", len(none.Data.Items), none.Data.Total)
	}

	// search 是全库检索: "Node-1" 命中的 Node-1、Node-10..Node-19 共 11 个,
	// **全部在第 1 页之外** (page_size=5 时)。若 search 被当成当前页本地过滤,
	// 这里只会命中当前页里的那几个 —— 这正是分页前的伪装全局检索缺陷。
	bySearch := getPage(t, r, "/api/v1/nodes?search=node-1&page=1&page_size=20")
	if bySearch.Data.Total != 11 {
		t.Fatalf("search=node-1 total = %d, want 11 (检索须覆盖全库)", bySearch.Data.Total)
	}
	for _, it := range bySearch.Data.Items {
		name, _ := it["name"].(string)
		model, _ := it["model"].(string)
		if !strings.Contains(strings.ToLower(name), "node-1") &&
			!strings.Contains(strings.ToLower(model), "node-1") {
			t.Fatalf("search 结果混入不匹配项 name=%q model=%q", name, model)
		}
	}

	// model 命中 search: "esp32" 应命中全部 20 个 ESP32 节点 (model 参与检索)。
	byModel := getPage(t, r, "/api/v1/nodes?search=esp32&page=1&page_size=20")
	if byModel.Data.Total != 20 {
		t.Fatalf("search=esp32 total = %d, want 20 (model 须参与服务端检索)", byModel.Data.Total)
	}

	// 筛选 + 分页组合: 11 条按 5/页 → 第 3 页剩 1 条 (offset 基于过滤后的集合)。
	searchPage := getPage(t, r, "/api/v1/nodes?search=node-1&page=3&page_size=5")
	if got := len(searchPage.Data.Items); got != 1 {
		t.Fatalf("search+page 组合末页 items = %d, want 1", got)
	}
	if searchPage.Data.Total != 11 {
		t.Fatalf("search+page 组合 total = %d, want 11", searchPage.Data.Total)
	}
}

// seedEdgeDevices 造 25 台设备挂在同一个 node/channel 上:
// 20 台 type=temp_humidity (12 台 status=active), 5 台 type=wind_speed。
func seedEdgeDevices(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := db.Create(&models.Node{NodeID: "N-ED", Name: "ed-node", Status: "online"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Channel{NodeID: "N-ED", HardwareType: "UART", BusType: "UART", HardwareID: "UART0", Enabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		status := "offline"
		if i < 12 {
			status = "active"
		}
		d := models.EdgeDevice{
			Name: "Device-" + strconv.Itoa(i), Type: "temp_humidity",
			NodeID: "N-ED", ChannelID: 1, HardwareID: "0x" + strconv.Itoa(i), Status: status,
		}
		if err := db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		d := models.EdgeDevice{
			Name: "Wind-" + strconv.Itoa(i), Type: "wind_speed",
			NodeID: "N-ED", ChannelID: 1, HardwareID: "0x" + strconv.Itoa(100+i), Status: "offline",
		}
		if err := db.Create(&d).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestEdgeDevicesPagination_DefaultPage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedEdgeDevices(t, db)

	resp := getPage(t, r, "/api/v1/edge-devices")
	if got := len(resp.Data.Items); got != 20 {
		t.Fatalf("默认页 items = %d, want 20 (分页失效会退回全量 25)", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("默认页 total = %d, want 25", resp.Data.Total)
	}
	if resp.Data.Page != 1 || resp.Data.PageSize != 20 {
		t.Fatalf("默认页回显 = %d/%d, want 1/20", resp.Data.Page, resp.Data.PageSize)
	}
}

func TestEdgeDevicesPagination_ExplicitPage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedEdgeDevices(t, db)

	p1 := getPage(t, r, "/api/v1/edge-devices?page=1&page_size=10")
	p2 := getPage(t, r, "/api/v1/edge-devices?page=2&page_size=10")
	if len(p1.Data.Items) != 10 || len(p2.Data.Items) != 10 {
		t.Fatalf("page1/page2 items = %d/%d, want 10/10", len(p1.Data.Items), len(p2.Data.Items))
	}
	if p1.Data.Total != 25 || p2.Data.Total != 25 {
		t.Fatalf("total 应恒为 25, got %d/%d", p1.Data.Total, p2.Data.Total)
	}
	seen := map[float64]bool{}
	for _, it := range p1.Data.Items {
		seen[it["id"].(float64)] = true
	}
	for _, it := range p2.Data.Items {
		if seen[it["id"].(float64)] {
			t.Fatalf("第 2 页与第 1 页重叠于 id=%v (Offset 未生效)", it["id"])
		}
	}

	// 末页 → 5 条; Preload 链必须仍在当前页生效 (Channel 关联不可被分页吞掉)。
	p3 := getPage(t, r, "/api/v1/edge-devices?page=3&page_size=10")
	if got := len(p3.Data.Items); got != 5 {
		t.Fatalf("末页 items = %d, want 5", got)
	}
	if _, ok := p3.Data.Items[0]["channel"]; !ok {
		t.Fatalf("分页后条目缺 Preload 关联 channel: %v", p3.Data.Items[0])
	}
}

func TestEdgeDevicesPagination_OutOfRangePage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedEdgeDevices(t, db)

	resp := getPage(t, r, "/api/v1/edge-devices?page=99&page_size=20")
	if got := len(resp.Data.Items); got != 0 {
		t.Fatalf("越界页 items = %d, want 0 (不得退化成第一页)", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("越界页 total = %d, want 25", resp.Data.Total)
	}

	clamped := getPage(t, r, "/api/v1/edge-devices?page=-3&page_size=0")
	if got := len(clamped.Data.Items); got != 20 {
		t.Fatalf("clamp 后 items = %d, want 20", got)
	}
	if clamped.Data.Page != 1 || clamped.Data.PageSize != 20 {
		t.Fatalf("clamp 回显 = %d/%d, want 1/20", clamped.Data.Page, clamped.Data.PageSize)
	}
}

func TestEdgeDevicesPagination_FilterWithPagination(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedEdgeDevices(t, db)

	// status=active → total=12 (过滤后的全量, 不是 25)。
	byStatus := getPage(t, r, "/api/v1/edge-devices?status=active&page=1&page_size=5")
	if byStatus.Data.Total != 12 {
		t.Fatalf("status=active total = %d, want 12 (过滤须在 Count 侧生效)", byStatus.Data.Total)
	}
	for _, it := range byStatus.Data.Items {
		if it["status"] != "active" {
			t.Fatalf("status 过滤混入 %v", it["status"])
		}
	}

	// device_type 过滤。
	byType := getPage(t, r, "/api/v1/edge-devices?device_type=wind_speed&page=1&page_size=20")
	if byType.Data.Total != 5 {
		t.Fatalf("device_type=wind_speed total = %d, want 5", byType.Data.Total)
	}

	// hardware_type 过滤下沉服务端: 真源在 channels.hardware_type (大写 UART),
	// 前端筛选值是小写 uart → 必须大小写归一, 否则永远 0 条。
	// 这条同时钉死"EXISTS 子查询不得让行重复": 25 台设备同挂一个 channel,
	// 若误用 JOIN 会变成 25 行且 total 虚高。
	byHw := getPage(t, r, "/api/v1/edge-devices?hardware_type=uart&page=1&page_size=10")
	if byHw.Data.Total != 25 {
		t.Fatalf("hardware_type=uart total = %d, want 25 (大小写须归一, 且不得因 JOIN 重复计数)", byHw.Data.Total)
	}
	if got := len(byHw.Data.Items); got != 10 {
		t.Fatalf("hardware_type=uart 首页 items = %d, want 10", got)
	}
	noneHw := getPage(t, r, "/api/v1/edge-devices?hardware_type=spi&page=1&page_size=10")
	if noneHw.Data.Total != 0 || len(noneHw.Data.Items) != 0 {
		t.Fatalf("hardware_type=spi = %d 条/total %d, want 0/0", len(noneHw.Data.Items), noneHw.Data.Total)
	}

	// search 全库检索: "Device-1" 命中 Device-1、Device-10..Device-19 共 11 个,
	// page_size=5 时全部落在第 1 页之外 —— 本地过滤会漏掉它们。
	bySearch := getPage(t, r, "/api/v1/edge-devices?search=device-1&page=1&page_size=20")
	if bySearch.Data.Total != 11 {
		t.Fatalf("search=device-1 total = %d, want 11 (检索须覆盖全库)", bySearch.Data.Total)
	}
	// search 同时命中 type: "wind" 应命中 5 台。
	byTypeSearch := getPage(t, r, "/api/v1/edge-devices?search=wind&page=1&page_size=20")
	if byTypeSearch.Data.Total != 5 {
		t.Fatalf("search=wind total = %d, want 5 (type 须参与服务端检索)", byTypeSearch.Data.Total)
	}

	// 组合筛选: status=active&device_type=temp_humidity → 12; 按 5/页 → 第 3 页 2 条。
	combo := getPage(t, r, "/api/v1/edge-devices?status=active&device_type=temp_humidity&page=3&page_size=5")
	if combo.Data.Total != 12 {
		t.Fatalf("组合筛选 total = %d, want 12", combo.Data.Total)
	}
	if got := len(combo.Data.Items); got != 2 {
		t.Fatalf("组合筛选末页 items = %d, want 2", got)
	}
	for _, it := range combo.Data.Items {
		if it["status"] != "active" || it["type"] != "temp_humidity" {
			t.Fatalf("组合筛选混入 status=%v type=%v", it["status"], it["type"])
		}
	}

	// 未命中 → 空集 + total=0。
	none := getPage(t, r, "/api/v1/edge-devices?status=active&device_type=wind_speed&page=1&page_size=5")
	if len(none.Data.Items) != 0 || none.Data.Total != 0 {
		t.Fatalf("未命中筛选 = %d 条/total %d, want 0/0", len(none.Data.Items), none.Data.Total)
	}
}

// TestEdgeDevicesPagination_EnrichmentScopedToCurrentPage 钉死本端点独有的不变式:
// latest-data 富化只对**当前页**的 deviceIDs 执行。
//
// 观测方式: 给全部 25 台设备都写入 latest-data (第 1 页是 10 台)。
// 分页正确时, 只有当前页那 10 台带 last_data; 若富化仍按全表跑,
// 拿到的 deviceIDs 会是 25 个, 但只有当前页 10 台会被赋回 —— 单看响应区分不出来。
// 因此这里改从**查询范围**取证: latest-data 的回落 SQL 只应见到当前页的 id。
// 用 GORM callback 记录实际出现在 SQL 里的 device_id 参数。
func TestEdgeDevicesPagination_EnrichmentScopedToCurrentPage(t *testing.T) {
	r, db := newListNodeRouter(t)
	seedEdgeDevices(t, db)

	// 造 latest-data: 每台设备一条 unified_data。
	var devices []models.EdgeDevice
	if err := db.Order("id").Find(&devices).Error; err != nil {
		t.Fatal(err)
	}
	for _, d := range devices {
		if err := db.Create(&models.UnifiedData{
			DeviceID: d.ID, SensorName: "temperature", Value: 20, Unit: "C",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 用 SQL 记分板抓取富化查询里出现的 device_id。
	rec := &sqlParamRecorder{}
	if err := db.Callback().Query().After("gorm:query").Register("test:capture_unified_data_ids", rec.afterQuery); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Callback().Query().Remove("test:capture_unified_data_ids") }()

	// 第 1 页 10 台。
	resp := getPage(t, r, "/api/v1/edge-devices?page=1&page_size=10")
	if got := len(resp.Data.Items); got != 10 {
		t.Fatalf("首页 items = %d, want 10", got)
	}

	pageIDs := map[uint]bool{}
	for _, it := range resp.Data.Items {
		pageIDs[uint(it["id"].(float64))] = true
	}

	// 富化查询必须只触及当前页的 id: 抓到的 unified_data device_id 集合
	// 必须是 pageIDs 的子集, 且非空 (否则富化根本没跑, 断言会假通过)。
	enriched := rec.unifiedDataDeviceIDs()
	if len(enriched) == 0 {
		t.Fatalf("未捕获到 unified_data 富化查询 (富化可能未执行, 该断言会假通过)")
	}
	for id := range enriched {
		if !pageIDs[id] {
			t.Fatalf("富化查询触及了非当前页设备 device_id=%d (当前页 %v) —— 仍在全表捞数据", id, pageIDs)
		}
	}

	// 反向对照: 第 2 页富化的必须是另一批 id, 与第 1 页不重叠。
	// (若富化无视分页, 两页会抓到同一批全表 id, 这里立刻变红。)
	rec.reset()
	resp2 := getPage(t, r, "/api/v1/edge-devices?page=2&page_size=10")
	page2IDs := map[uint]bool{}
	for _, it := range resp2.Data.Items {
		page2IDs[uint(it["id"].(float64))] = true
		if pageIDs[uint(it["id"].(float64))] {
			t.Fatalf("第 2 页与第 1 页重叠于 id=%v", it["id"])
		}
	}
	enriched2 := rec.unifiedDataDeviceIDs()
	if len(enriched2) == 0 {
		t.Fatalf("第 2 页未捕获到 unified_data 富化查询")
	}
	for id := range enriched2 {
		if !page2IDs[id] {
			t.Fatalf("第 2 页富化触及了非本页设备 device_id=%d (本页 %v)", id, page2IDs)
		}
	}
}

// sqlParamRecorder 记录 GORM 查询里出现的 unified_data.device_id 参数值。
// 通过检查 SQL 文本 + 变量参数识别富化查询, 不改动被测代码。
type sqlParamRecorder struct {
	mu  sync.Mutex
	ids map[uint]bool
}

func (r *sqlParamRecorder) reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.ids = nil
}

func (r *sqlParamRecorder) afterQuery(tx *gorm.DB) {
	if tx.Statement == nil || !strings.Contains(tx.Statement.SQL.String(), "unified_data") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.ids == nil {
		r.ids = map[uint]bool{}
	}
	for _, v := range tx.Statement.Vars {
		switch id := v.(type) {
		case uint:
			r.ids[id] = true
		case int:
			r.ids[uint(id)] = true
		case int64:
			r.ids[uint(id)] = true
		case []uint:
			for _, e := range id {
				r.ids[e] = true
			}
		case []int:
			for _, e := range id {
				r.ids[uint(e)] = true
			}
		case []int64:
			for _, e := range id {
				r.ids[uint(e)] = true
			}
		case []interface{}:
			for _, e := range id {
				switch n := e.(type) {
				case uint:
					r.ids[n] = true
				case int:
					r.ids[uint(n)] = true
				case int64:
					r.ids[uint(n)] = true
				}
			}
		}
	}
}

func (r *sqlParamRecorder) unifiedDataDeviceIDs() map[uint]bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[uint]bool, len(r.ids))
	for k := range r.ids {
		out[k] = true
	}
	return out
}
