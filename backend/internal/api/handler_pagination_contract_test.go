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
