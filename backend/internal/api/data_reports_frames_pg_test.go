package api

// data_reports_frames_pg_test.go — /data-reports 的帧表数据源验收（2026-11-08）。
//
// ============ 为什么需要它 ============
//
// 演练 #20：藏表后 `GET /api/v1/data-reports` 返回 .data=0（基线 20）。
// 原实现只有 unified_data 一条路。本用例直接模拟**终态**：unified_data 一行
// 都没有，只有帧表 —— 这正是演练那一刻的状态。
//
// ============ 断言的对象是什么（不是「结果对」）============
//
// 这个缺陷的形态和 /nodes/:id/latest 那个不同：那里是「结果对但把整张表拉进
// 内存」，这里是「**接口整个失效**」。所以核心断言很直接：
//
//	unified_data 空 + 帧表有数据  ⇒  .data 非空
//
// 但只断言非空会漏掉三种退化，所以每条都单独钉：
//   · 只返回 1 行（解码窗口塌成一个字段）      → 断言行数 == limit
//   · 顺序错（升序 / 不稳定）                  → 断言 DESC + 两次请求同序
//   · unified_data 空时回落路径返 null          → 断言 [] 不是 null
//
// ============ 复用而不是新建 ============
//
// 解码走 latest_values_frames.go 的 LatestValuesRowsFromFrames（/latest-values
// 与启动预热的共用实现），行 ID 走 device_history_frames.go 的 stableHistoryRowID
// （/devices/:id/history 的同一套合成规则）。本任务**没有新建任何解码逻辑**。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// setupDataReportRouter 只挂本任务相关的路由，避免把整个 v1 依赖树拖进 PG 用例。
func setupDataReportRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerDataReportRoutes(v1, db)
	return r
}

// readDataReports 发请求并把 data 解成原始消息数组（保序、能区分 null 与 []）。
func readDataReports(t *testing.T, r *gin.Engine, query string) []json.RawMessage {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/data-reports"+query, nil)
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /data-reports%s = %d, want 200: %s", query, w.Code, w.Body.String())
	}
	var env struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("envelope 解析失败: %v (%s)", err, w.Body.String())
	}
	if len(env.Data) == 0 || string(env.Data) == "null" {
		t.Fatalf("data 是 null 或缺失，want 数组: %s", w.Body.String())
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(env.Data, &rows); err != nil {
		t.Fatalf("data 不是数组: %v (%s)", err, w.Body.String())
	}
	return rows
}

type dataReportRow struct {
	ID         uint      `json:"id"`
	DeviceID   uint      `json:"device_id"`
	SensorName string    `json:"sensor_name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	Timestamp  time.Time `json:"timestamp"`
	CreatedAt  time.Time `json:"created_at"`
}

func decodeDataReportRows(t *testing.T, rows []json.RawMessage) []dataReportRow {
	t.Helper()
	out := make([]dataReportRow, 0, len(rows))
	for i, raw := range rows {
		var row dataReportRow
		if err := json.Unmarshal(raw, &row); err != nil {
			t.Fatalf("第 %d 行解析失败: %v (%s)", i, err, raw)
		}
		out = append(out, row)
	}
	return out
}

// prepareDataReportEnv 把库清到「unified_data 退役后」的终态并返回帧字典。
func prepareDataReportEnv(t *testing.T, db *gorm.DB) telemetry.SlotDictionary {
	t.Helper()
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	db.Exec("DELETE FROM unified_data")
	ResetDeviceUnitCache()
	resetLatestValueCacheForTest()
	t.Cleanup(func() {
		ResetDeviceUnitCache()
		resetLatestValueCacheForTest()
		SetHistoryFrameDictionary(nil)
	})

	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	SetHistoryFrameDictionary(dict)
	return dict
}

// TestDataReports_SurvivesUnifiedRetirement 是本任务的主验收 —— 对应演练 #20。
func TestDataReports_SurvivesUnifiedRetirement(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := prepareDataReportEnv(t, db)

	const dev = 9931
	start := time.Now().UTC().Add(-10 * time.Minute).Truncate(time.Second)
	fields := []string{"current", "rsoc", "total_voltage", "temperature", "cycle_count",
		"cell_voltage_1", "cell_voltage_2", "soh"}
	// 5s 一帧、10 分钟 = 120 帧。帧数远小于 limit，所以**行数由字段数决定**：
	// 一帧 8 个字段 ⇒ 最近一条数据 = 8 行。
	seedFramesOnlyFields(t, db, dev, start, start.Add(10*time.Minute), dict, fields, 5*time.Second)

	// 前置断言：unified_data 真的空（否则测的不是「藏表后」）。
	var udCount int64
	if err := db.Model(&models.UnifiedData{}).Count(&udCount).Error; err != nil {
		t.Fatalf("统计 unified_data: %v", err)
	}
	if udCount != 0 {
		t.Fatalf("前置不成立：unified_data 有 %d 行，本用例要求 0 行", udCount)
	}

	r := setupDataReportRouter(t, db)
	rows := decodeDataReportRows(t, readDataReports(t, r, ""))

	if len(rows) == 0 {
		t.Fatal("unified_data 空之后 .data 为 0 —— **这正是演练 #20 的失败形态**，" +
			"帧表数据源没有生效")
	}
	// 行数断言：每台设备最后一帧的**全部**物理量都应出现。
	if len(rows) != len(fields) {
		names := make([]string, 0, len(rows))
		for _, row := range rows {
			names = append(names, row.SensorName)
		}
		t.Fatalf("返回 %d 行, want %d（字段集 %v，实际 %v）",
			len(rows), len(fields), fields, names)
	}

	// **这里刻意不断言排序。**
	//
	// 单设备时「最后一帧的全部物理量」共享同一个 timestamp，任何 DESC 循环
	// 在这份数据上都是**空转** —— 我第一版就是这么写的，变异验证时把排序改成
	// 升序（M4）用例依然 PASS，等于这条断言从来没验过任何东西。
	//
	// 排序在 TestDataReports_OrderIsDescendingAndStable 里用**两台设备**钉：
	// 只有时间戳真的不同，「最近的在最前」才有可错的空间。

	// 形状断言：ID / Unit / CreatedAt 都不能缺。
	seenID := map[uint]string{}
	for i, row := range rows {
		if row.ID == 0 {
			t.Fatalf("第 %d 行（%s）ID 为 0 —— 帧表没有自增主键，必须合成稳定 ID", i, row.SensorName)
		}
		if prev, dup := seenID[row.ID]; dup {
			t.Fatalf("ID 重复：%s 与 %s 都是 %d —— 前端 v-for 的 key 会撞",
				prev, row.SensorName, row.ID)
		}
		seenID[row.ID] = row.SensorName
		if row.DeviceID != dev {
			t.Fatalf("第 %d 行 device_id = %d, want %d", i, row.DeviceID, dev)
		}
		if row.CreatedAt.IsZero() {
			t.Fatalf("第 %d 行 created_at 为零值 —— 原契约里它是排序键", i)
		}
	}
}

// TestDataReports_OrderingIsStableAcrossCalls 钉「两次请求同一份数据行序相同」。
//
// 为什么单独钉：一帧含 N 个字段时它们 timestamp **完全相同**，只按时间排序时
// 同刻多行的相对顺序由排序算法决定 —— 前端列表会无缘无故闪动。
func TestDataReports_OrderingIsStableAcrossCalls(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := prepareDataReportEnv(t, db)

	const dev = 9932
	start := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	fields := []string{"a", "b", "c", "d", "e"}
	seedFramesOnlyFields(t, db, dev, start, start.Add(time.Minute), dict, fields, 5*time.Second)

	r := setupDataReportRouter(t, db)
	first := decodeDataReportRows(t, readDataReports(t, r, ""))
	second := decodeDataReportRows(t, readDataReports(t, r, ""))
	if len(first) != len(second) {
		t.Fatalf("两次请求行数不同：%d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID || first[i].SensorName != second[i].SensorName {
			t.Fatalf("第 %d 行不稳定：第一次 (%d,%s) 第二次 (%d,%s)",
				i, first[i].ID, first[i].SensorName, second[i].ID, second[i].SensorName)
		}
	}
}

// TestDataReports_RespectsLimit 钉 limit 参数。默认 20，上限 100。
func TestDataReports_RespectsLimit(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := prepareDataReportEnv(t, db)

	const dev = 9933
	start := time.Now().UTC().Add(-2 * time.Minute).Truncate(time.Second)
	fields := make([]string, 0, 8)
	for _, n := range []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8"} {
		fields = append(fields, n)
	}
	seedFramesOnlyFields(t, db, dev, start, start.Add(time.Minute), dict, fields, 5*time.Second)

	r := setupDataReportRouter(t, db)
	if got := len(readDataReports(t, r, "?limit=3")); got != 3 {
		t.Fatalf("limit=3 返回 %d 行", got)
	}
	if got := len(readDataReports(t, r, "")); got != len(fields) {
		t.Fatalf("默认 limit=20 返回 %d 行, want %d", got, len(fields))
	}
	// 非法 limit 必须**忽略**而不是 400（与改造前一致：原实现只接受 1..100）。
	if got := len(readDataReports(t, r, "?limit=999")); got != len(fields) {
		t.Fatalf("limit=999 应回落默认 20，实际 %d 行", got)
	}
	if got := len(readDataReports(t, r, "?limit=abc")); got != len(fields) {
		t.Fatalf("limit=abc 应回落默认 20，实际 %d 行", got)
	}
}

// TestDataReports_EmptyIsArrayNotNull 钉「0 行时端到端吐 [] 而不是 null」。
//
// ============ 这条用例覆盖的到底是哪条路（重要）============
//
// 我第一版写的是「帧表空 + unified_data 空 ⇒ 必须 []」，并以为它顺带验证了
// 回落分支的 nil 守卫。变异验证（删掉守卫 M5）时**用例依然 PASS** ——
// 因为实测 GORM 的 Find 在 0 行时就把切片填成非 nil 空切片，0 行这条路
// 根本走不到守卫。
//
// 所以这里只负责 ①，守卫那条路由 TestDataReportsFallback_NeverReturnsNil 单独盯：
//
//	① 0 行     ：两个数据源都空 ⇒ []（由 GORM 的 Find 语义保证，本用例）
//	② 查询失败 ：Find 报错 ⇒ []（才走到 nil 守卫，helper 单测）
//
// 只写 ① 的话，守卫是没人验的死代码；只写 ② 的话，正常空表这条没被钉。
func TestDataReports_EmptyIsArrayNotNull(t *testing.T) {
	requirePostgresAPI(t)

	t.Run("两源皆空_0行", func(t *testing.T) {
		db := testutil.OpenTestDB(t)
		prepareDataReportEnv(t, db)
		r := setupDataReportRouter(t, db)
		rows := readDataReports(t, r, "")
		if len(rows) != 0 {
			t.Fatalf("两个数据源都空时返回了 %d 行", len(rows))
		}
	})

	// 查询失败那条路**刻意不在这里测**：
	//
	// 试过在 PG 测试 schema 里造（只改名 / 改名+遮蔽表），两条都污染兄弟用例 ——
	// 前者 search_path 穿透到 public.unified_data（实测返回 20 行，条件根本没造出来），
	// 后者遮蔽表随 t.Cleanup 顺序泄漏，把兄弟用例挂成
	//\t"copy month 2026-11: column id does not exist"。
	//
	// 根因是我在 handler 层测一个 helper 的契约：要造「数据库坏掉」，就得先
	// 篡改共享的 DB 环境。改在 TestDataReportsFallback_NeverReturnsNil 里直接
	// 调 helper（纯 SQLite 空库，不碰任何 schema）—— 契约在哪一层，就在哪一层测。
}

// TestDataReports_OrderIsDescendingAndStable 是**排序**这条契约的唯一真门禁。
//
// ============ 为什么必须用两台设备 ============
//
// 单设备时「最后一帧的全部物理量」共享同一个 timestamp ⇒ 任何 DESC 循环都空转。
// 我第一版的主用例就是这么写的，把排序改成升序（M4 变异）依然 PASS ——
// 那条断言从来没验过任何东西。要验「最近的在最前」，**数据里必须存在不同的时间戳**。
//
// 两台设备 + 不同的最后一帧时刻，同时钉两件事：
//
//	① 跨设备：晚的那台的行必须排在早的那台之前；
//	② 同设备内：同一 timestamp 的多字段之间，两次请求行序相同
//	   （由副键 device_id + sensor_name 决定，不是排序算法的偶然）。
func TestDataReports_OrderIsDescendingAndStable(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	dict := prepareDataReportEnv(t, db)

	base := time.Now().UTC().Add(-20 * time.Minute).Truncate(time.Second)
	// devOld 的最后一帧比 devNew 早 10 分钟。
	const devOld, devNew = 9941, 9942
	seedFramesOnlyFields(t, db, devOld, base, base.Add(2*time.Minute), dict,
		[]string{"old_a", "old_b"}, 5*time.Second)
	seedFramesOnlyFields(t, db, devNew, base.Add(10*time.Minute), base.Add(12*time.Minute), dict,
		[]string{"new_a", "new_b"}, 5*time.Second)

	r := setupDataReportRouter(t, db)
	rows := decodeDataReportRows(t, readDataReports(t, r, ""))
	if len(rows) != 4 {
		t.Fatalf("返回 %d 行, want 4（2 设备 x 2 字段）", len(rows))
	}

	// (1) 跨设备 DESC：前两行必须是 devNew（晚的那台）。
	for i := 0; i < 2; i++ {
		if rows[i].DeviceID != devNew {
			t.Fatalf("第 %d 行 device_id=%d（%s）, want %d —— 最近的数据必须排在最前"+
				"（原契约 ORDER BY created_at DESC）；实际序列：%v",
				i, rows[i].DeviceID, rows[i].SensorName, devNew, rowSequence(rows))
		}
	}
	for i := 2; i < 4; i++ {
		if rows[i].DeviceID != devOld {
			t.Fatalf("第 %d 行 device_id=%d, want %d（较早的设备）。实际序列：%v",
				i, rows[i].DeviceID, devOld, rowSequence(rows))
		}
	}
	// 显式全序断言：任何相邻两行都不得升序。
	for i := 1; i < len(rows); i++ {
		if rows[i].Timestamp.After(rows[i-1].Timestamp) {
			t.Fatalf("第 %d/%d 行升序了，原契约是 DESC。实际序列：%v",
				i-1, i, rowSequence(rows))
		}
	}

	// (2) 同设备内稳定：再请求一次，逐行比对 (id, sensor_name)。
	again := decodeDataReportRows(t, readDataReports(t, r, ""))
	if len(again) != len(rows) {
		t.Fatalf("两次请求行数不同：%d vs %d", len(rows), len(again))
	}
	for i := range rows {
		if rows[i].ID != again[i].ID || rows[i].SensorName != again[i].SensorName {
			t.Fatalf("第 %d 行不稳定：第一次 (%d,%s) 第二次 (%d,%s)",
				i, rows[i].ID, rows[i].SensorName, again[i].ID, again[i].SensorName)
		}
	}
}

// rowSequence 只用于**失败信息**：把行序列渲染成可读串，
// 让「排序错了」这条失败能自己说明是怎么错的（而不是只报一个下标）。
func rowSequence(rows []dataReportRow) string {
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("(dev=%d,%s@%s)",
			row.DeviceID, row.SensorName, row.Timestamp.UTC().Format("15:04:05")))
	}
	return strings.Join(parts, " ")
}
