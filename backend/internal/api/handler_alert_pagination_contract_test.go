package api

// 分页契约测试 (架构与接口评估 P1.2 裁决: items + total) — GET /alert-events。
//
// 覆盖四类边界各一条 (与 TestAutomationEventsPagination_* 同范式):
// 默认页 / 指定页 / 超范围页 / 筛选+分页组合。
//
// 为什么必须有「超范围页」一条: 本端点改前是
// `Order("id DESC").Limit(500).Find(&items)` + `Success(c, items)` ——
// **裸数组 + 硬截断**, 既没有 total 也没有任何截断提示。真分页的判据因此不只是
// 「能翻页」, 而是「total 如实反映过滤后的全量条数, 且越界页返回空集而非报错或
// 退化成第一页」; 只断言首页等于没断言 (本仓已有 6 例「伪装成正常」的教训)。

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// newAlertPaginationRouter 装配 /alert-events 所需的路由与表。
// setupTestRouter 的 AutoMigrate 链不含 AlertRule/AlertEvent, 这里补迁。
func newAlertPaginationRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}); err != nil {
		t.Fatalf("auto migrate alert: %v", err)
	}
	registerAlertRoutes(r.Group("/api/v1"), db, nil)
	return r, db
}

// seedAlertEvents 造 25 条事件: rule 1 有 20 条 (其中 resolved 12 条), rule 2 有 5 条。
// 25 > 默认 page_size(20) 且 < 旧硬上限 500, 因此若分页失效 (退回全量或截断),
// 首页条数断言会立刻变红。
func seedAlertEvents(t *testing.T, db *gorm.DB) {
	t.Helper()
	base := time.Date(2026, 8, 21, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 20; i++ {
		state := "firing"
		var resolvedAt *time.Time
		if i < 12 {
			state = "resolved"
			ts := base.Add(time.Duration(i) * time.Minute)
			resolvedAt = &ts
		}
		firedAt := base.Add(time.Duration(i) * time.Minute)
		ev := models.AlertEvent{
			RuleID: 1, State: state, Value: 4.2,
			FiredAt: &firedAt, ResolvedAt: resolvedAt,
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		firedAt := base.Add(time.Duration(i) * time.Minute)
		ev := models.AlertEvent{
			RuleID: 2, State: "firing", Value: 3.3, FiredAt: &firedAt,
		}
		if err := db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}
}

func TestAlertEventsPagination_DefaultPage(t *testing.T) {
	r, db := newAlertPaginationRouter(t)
	seedAlertEvents(t, db)

	// 无参数 → page=1, page_size=20, total=25 (全量真实条数, 不是 500 也不是 20)。
	resp := getPage(t, r, "/api/v1/alert-events")
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

func TestAlertEventsPagination_ExplicitPage(t *testing.T) {
	r, db := newAlertPaginationRouter(t)
	seedAlertEvents(t, db)

	// 第 2 页 (page_size=10) → 10 条, 且与第 1 页无重叠 (真偏移, 不是重复首页)。
	p1 := getPage(t, r, "/api/v1/alert-events?page=1&page_size=10")
	p2 := getPage(t, r, "/api/v1/alert-events?page=2&page_size=10")

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
	p3 := getPage(t, r, "/api/v1/alert-events?page=3&page_size=10")
	if got := len(p3.Data.Items); got != 5 {
		t.Fatalf("末页 items = %d, want 5 (25 取模 10)", got)
	}
}

func TestAlertEventsPagination_OutOfRangePage(t *testing.T) {
	r, db := newAlertPaginationRouter(t)
	seedAlertEvents(t, db)

	// 超范围页 → 200 + 空集 + total 仍为真实全量 (不得报错、不得退化成第一页)。
	resp := getPage(t, r, "/api/v1/alert-events?page=99&page_size=20")
	if got := len(resp.Data.Items); got != 0 {
		t.Fatalf("越界页 items = %d, want 0 (不得退化成第一页)", got)
	}
	if resp.Data.Total != 25 {
		t.Fatalf("越界页 total = %d, want 25 (total 与页码无关)", resp.Data.Total)
	}

	// page<1 归 1; page_size 越界归默认 20 —— clamp 不报错。
	clamped := getPage(t, r, "/api/v1/alert-events?page=0&page_size=100000")
	if got := len(clamped.Data.Items); got != 20 {
		t.Fatalf("clamp 后 items = %d, want 20", got)
	}
	if clamped.Data.Page != 1 || clamped.Data.PageSize != 20 {
		t.Fatalf("clamp 回显 = %d/%d, want 1/20", clamped.Data.Page, clamped.Data.PageSize)
	}

	// page_size=0: 实测 GORM 会写 LIMIT 0 (见 TestNodesPagination_InvalidPageSize 的
	// 机制说明), 若 clamp 缺失就是"静默空页"; 必须归默认 20。
	zero := getPage(t, r, "/api/v1/alert-events?page_size=0")
	if got := len(zero.Data.Items); got != 20 {
		t.Fatalf("page_size=0 items = %d, want 20 (必须 clamp 到默认页长)", got)
	}
	// page_size=-1: 负值丢弃 LIMIT 子句 → 全表 25 条, 同样必须归 20。
	neg := getPage(t, r, "/api/v1/alert-events?page_size=-1")
	if got := len(neg.Data.Items); got != 20 {
		t.Fatalf("page_size=-1 items = %d, want 20 (负值丢弃 LIMIT 子句, 必须 clamp)", got)
	}
	// page_size=99999: 未 clamp 的超大页, 同样必须归 20。
	huge := getPage(t, r, "/api/v1/alert-events?page_size=99999")
	if got := len(huge.Data.Items); got != 20 {
		t.Fatalf("page_size=99999 items = %d, want 20 (超大页必须 clamp)", got)
	}
	if zero.Data.PageSize != 20 {
		t.Fatalf("page_size=0 回显 = %d, want 20", zero.Data.PageSize)
	}
}

func TestAlertEventsPagination_FilterWithPagination(t *testing.T) {
	r, db := newAlertPaginationRouter(t)
	seedAlertEvents(t, db)

	// rule_id=1 → total=20 (过滤后的全量, 不是 25)。
	resp := getPage(t, r, "/api/v1/alert-events?rule_id=1&page=1&page_size=8")
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

	// 组合筛选 rule_id=1&state=resolved → total=12, 且分页在过滤**之后**进行。
	combo := getPage(t, r, "/api/v1/alert-events?rule_id=1&state=resolved&page=1&page_size=5")
	if combo.Data.Total != 12 {
		t.Fatalf("rule_id=1&state=resolved total = %d, want 12", combo.Data.Total)
	}
	if got := len(combo.Data.Items); got != 5 {
		t.Fatalf("组合筛选首页 items = %d, want 5", got)
	}
	for _, it := range combo.Data.Items {
		if it["state"] != "resolved" {
			t.Fatalf("组合筛选结果混入 state=%v", it["state"])
		}
		if rid, _ := it["rule_id"].(float64); uint(rid) != 1 {
			t.Fatalf("组合筛选结果混入 rule_id=%v", it["rule_id"])
		}
	}
	// 组合筛选第 3 页 → 12 条 5/页 → 剩 2 条 (证明 offset 用的是过滤后的集合)。
	last := getPage(t, r, "/api/v1/alert-events?rule_id=1&state=resolved&page=3&page_size=5")
	if got := len(last.Data.Items); got != 2 {
		t.Fatalf("组合筛选末页 items = %d, want 2", got)
	}

	// 未命中筛选 → 200 + 空集 + total=0 (不得退化成全量)。
	none := getPage(t, r, "/api/v1/alert-events?rule_id=999&page=1&page_size=5")
	if len(none.Data.Items) != 0 || none.Data.Total != 0 {
		t.Fatalf("未命中筛选 = %d 条/total %d, want 0/0", len(none.Data.Items), none.Data.Total)
	}

	// 空集必须是 [] 而非 null —— getPage 已断言 Items 非 nil, 这里再钉一次原始字节
	// (契约回退成裸数组时, 顶层 data 会是 [] 而不是 {items:[],...}, 本断言同时变红)。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/alert-events?rule_id=999", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("空集请求 = %d, want 200: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `"items":[]`) {
		t.Fatalf("空集 items 必须序列化为 [] 而非 null, got %s", w.Body.String())
	}
}
