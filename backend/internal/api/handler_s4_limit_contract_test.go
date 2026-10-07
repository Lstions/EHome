package api

// S4 回归契约: 未校验的 limit 参数不得让查询退化成"无界"。
//
// 缺陷本体不是"返回值个数不对", 而是"生成的 SQL 里没有/不该有那个 LIMIT"。
// 所以在空表上数条数是测不出来的 (0 条既可能是 LIMIT 生效, 也可能是全表为空),
// 本文件一律**取证到 SQL 文本**: 用 GORM query 回调抓每条实际执行的 SQL,
// 断言针对目标表的取行查询里出现了钳制后的 LIMIT 值。
//
// 三种非法输入必须**分别**构造 —— 它们都是缺陷, 但错法各不相同:
//   limit = -1      → GORM 丢弃整条 LIMIT 子句 → 返回**全表** (无界)   ← S4 本体
//   limit = 0/"abc" → GORM 写 LIMIT 0          → **静默空页**
//   limit = 99999   → 未钳制的超大值           → 单次响应体不受控
// 合并成一条断言就分不清"修好了哪一种", 因此下面按三种输入各起一个子测试。

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// sqlCapture 记录 GORM 实际执行的 SQL 文本 (与 handler_pagination_contract_test.go
// 的 sqlParamRecorder 同款取证手法: 只在测试侧挂回调, 不改被测代码)。
type sqlCapture struct {
	mu  sync.Mutex
	sql []string
}

func newSQLCapture(t *testing.T, db *gorm.DB) *sqlCapture {
	t.Helper()
	c := &sqlCapture{}
	if err := db.Callback().Query().After("gorm:query").Register("test:s4_capture", c.after); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:s4_capture") })
	return c
}

func (c *sqlCapture) after(tx *gorm.DB) {
	if tx.Statement == nil {
		return
	}
	q := tx.Statement.SQL.String()
	if q == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sql = append(c.sql, q)
}

func (c *sqlCapture) reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sql = nil
}

// rowsQueryFor 返回本次请求里针对 table 的**取行**查询 (排除 Count)。
// Count 查询天然没有 LIMIT, 把它算进来会让断言假红。
func (c *sqlCapture) rowsQueryFor(table string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []string
	for _, q := range c.sql {
		if !strings.Contains(q, table) {
			continue
		}
		if strings.Contains(strings.ToLower(q), "count(") {
			continue
		}
		out = append(out, q)
	}
	return out
}

// s4Router 装配本任务涉及的全部端点, 并回传 SQL 取证器。
func s4Router(t *testing.T) (*gin.Engine, *gorm.DB, *sqlCapture) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.DeviceModel{}, &models.NodeEvent{}); err != nil {
		t.Fatalf("auto migrate S4 依赖表: %v", err)
	}
	v1 := r.Group("/api/v1")
	registerNotificationRoutes(v1, db)
	registerVendorRoutes(v1, db)
	return r, db, newSQLCapture(t, db)
}

// s4Seed 造出每条端点至少 3 行数据: 少于此数, "全表返回"与"钳制生效"在
// 条数层面无法区分 (虽然本文件只认 SQL, 有数据才让"查询真的跑到了目标表"成立)。
func s4Seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	node := models.Node{NodeID: "S4-NODE", Name: "S4 Node", Model: "ESP32", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: "S4-NODE", HardwareID: "0x76", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{Name: "S4 Device", Type: "bms_jbd", NodeID: "S4-NODE", ChannelID: ch.ID, HardwareID: "0x76"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if err := db.Create(&models.DeviceData{
			DeviceID: dev.ID, NodeID: "S4-NODE", DataJSON: fmt.Sprintf("{\"value\":%d}", i),
		}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.Notification{Title: fmt.Sprintf("n%d", i), Type: "info", Message: "m"}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.Vendor{Name: fmt.Sprintf("v%d", i)}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&models.DeviceModel{Name: fmt.Sprintf("m%d", i), Type: "bms", VendorID: 1}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// s4Site 描述一个"能被查询参数打成无界"的端点。
// clampsTo 是修复后**应当出现在 SQL 里的 LIMIT 值** (即该端点的默认页长)。
type s4Site struct {
	name     string
	table    string // SQL 里必须出现的表名
	clampsTo int    // 修复后 SQL 里应当出现的 LIMIT 值
	build    func(limitParam string) string
	nodeAuth bool // /edge-devices/:id/data 在 setupTestRouter 下无需 token, 保留以备分组
}

// s4Sites 是本任务实测确认的**全部**活站点 (Lead 原始清单只列了第 1 条)。
func s4Sites() []s4Site {
	return []s4Site{
		{
			name:     "GET /nodes/:id/data (limit)",
			table:    "device_data",
			clampsTo: 100,
			build: func(v string) string {
				return "/api/v1/nodes/S4-NODE/data?limit=" + v
			},
		},
		{
			name:     "GET /notifications (limit)",
			table:    "notifications",
			clampsTo: 20,
			build: func(v string) string {
				return "/api/v1/notifications?limit=" + v
			},
		},
		{
			name:     "GET /vendors (page_size)",
			table:    "vendors",
			clampsTo: 20,
			build: func(v string) string {
				return "/api/v1/vendors?page_size=" + v
			},
		},
		{
			name:     "GET /device-models (page_size)",
			table:    "device_models",
			clampsTo: 20,
			build: func(v string) string {
				return "/api/v1/device-models?page_size=" + v
			},
		},
		{
			name:     "GET /edge-devices/:id/data (page_size)",
			table:    "device_data",
			clampsTo: 50,
			build: func(v string) string {
				return "/api/v1/edge-devices/1/data?page_size=" + v
			},
		},
	}
}

// assertRowsQueryClamped 是本题的**唯一判据**:
//  1. 必须真的抓到针对目标表的取行查询 —— 否则说明查询根本没跑,
//     断言会**假通过** (这正是"空表数条数"测法的漏洞);
//  2. 每条这样的 SQL 都必须含钳制后的 LIMIT 值。
//
// 第 2 条同时覆盖三种非法输入:
//
//	-1 修复前 SQL 里**没有** LIMIT → 不含 "LIMIT 100" → 红;
//	0/"abc" 修复前是 LIMIT 0        → 不含 "LIMIT 100" → 红;
//	99999 修复前是 LIMIT 99999      → 不含 "LIMIT 100" → 红。
func assertRowsQueryClamped(t *testing.T, cap *sqlCapture, site s4Site, ctx string) {
	t.Helper()
	rows := cap.rowsQueryFor(site.table)
	if len(rows) == 0 {
		t.Fatalf("%s [%s]: 未捕获到针对 %s 的取行查询 —— 该断言会假通过, 请检查 fixture", ctx, site.name, site.table)
	}
	want := fmt.Sprintf("LIMIT %d", site.clampsTo)
	for _, q := range rows {
		if !strings.Contains(q, want) {
			t.Fatalf("%s [%s]: SQL 缺少 %q —— 该端点仍可被打成无界/静默空页/未钳制。\nSQL: %s",
				ctx, site.name, want, q)
		}
	}
}

// TestS4_LimitNegative_KeepsLimitClause 覆盖 S4 本体的第一种错法。
//
// limit=-1 时 GORM (v1.31.2 clause/limit.go) 因 `*limit.Limit >= 0` 不成立而
// **整条丢弃 LIMIT 子句** ⇒ 查询变成无界全表扫描。修复前本用例必然变红,
// 因为抓到的 SQL 里根本没有 LIMIT。
func TestS4_LimitNegative_KeepsLimitClause(t *testing.T) {
	r, db, cap := s4Router(t)
	s4Seed(t, db)

	for _, site := range s4Sites() {
		t.Run(site.name, func(t *testing.T) {
			cap.reset()
			getOK(t, r, site.build("-1"))
			assertRowsQueryClamped(t, cap, site, "limit=-1")
		})
	}
}

// TestS4_LimitZeroOrGarbage_NotSilentlyEmpty 覆盖第二种错法。
//
// limit=0 与 limit=abc 都令 Atoi 得到 0, GORM 于是**真的写 LIMIT 0**
// (不是"当作无限制返回全表" —— 任务书早期版本的这条机制说法是错的)。
// 用户拿到的是**静默空页**: 200 + 空数组, 没有任何错误提示。
// 注意 "abc" 这一档: 只判 limit > 0 的修法会漏掉它 (0 不 > 0, 但 0 也不是合法页长)。
func TestS4_LimitZeroOrGarbage_NotSilentlyEmpty(t *testing.T) {
	r, db, cap := s4Router(t)
	s4Seed(t, db)

	for _, site := range s4Sites() {
		for _, bad := range []string{"0", "abc"} {
			t.Run(site.name+" limit="+bad, func(t *testing.T) {
				cap.reset()
				getOK(t, r, site.build(bad))
				assertRowsQueryClamped(t, cap, site, "limit="+bad)
			})
		}
	}
}

// TestS4_LimitOversized_ClampedToCeiling 覆盖第三种错法。
//
// limit=99999: 数值合法但远超任何合理页长, 未钳制时单次响应体不受控
// (资源耗尽面)。修复后必须回落到该端点默认值。
func TestS4_LimitOversized_ClampedToCeiling(t *testing.T) {
	r, db, cap := s4Router(t)
	s4Seed(t, db)

	for _, site := range s4Sites() {
		t.Run(site.name, func(t *testing.T) {
			cap.reset()
			getOK(t, r, site.build("99999"))
			assertRowsQueryClamped(t, cap, site, "limit=99999")
		})
	}
}

// TestS4_LegalValuesPreserved 是反向对照: 合法页长必须**原样保留**。
//
// 没有这条, 把钳制写成"无条件归默认值"也能骗过上面三个用例 ——
// 那会静默废掉调用方显式指定页长的能力。
func TestS4_LegalValuesPreserved(t *testing.T) {
	r, db, cap := s4Router(t)
	s4Seed(t, db)

	for _, tc := range []struct {
		table string
		path  string
		want  int
	}{
		{"device_data", "/api/v1/nodes/S4-NODE/data?limit=37", 37},
		{"notifications", "/api/v1/notifications?limit=7", 7},
		{"vendors", "/api/v1/vendors?page_size=3", 3},
		{"device_models", "/api/v1/device-models?page_size=4", 4},
		{"device_data", "/api/v1/edge-devices/1/data?page_size=9", 9},
	} {
		t.Run(tc.path, func(t *testing.T) {
			cap.reset()
			getOK(t, r, tc.path)
			rows := cap.rowsQueryFor(tc.table)
			if len(rows) == 0 {
				t.Fatalf("未捕获到针对 %s 的取行查询 (断言会假通过)", tc.table)
			}
			want := fmt.Sprintf("LIMIT %d", tc.want)
			for _, q := range rows {
				if !strings.Contains(q, want) {
					t.Fatalf("合法页长被改写: SQL 缺少 %q\nSQL: %s", want, q)
				}
			}
		})
	}
}

// getOK 发一次 GET 并要求 200。非 200 说明 fixture 没把端点喂起来,
// 后续 SQL 断言就没有意义了。
func getOK(t *testing.T, r *gin.Engine, path string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, w.Code, w.Body.String())
	}
	return w.Body.String()
}
