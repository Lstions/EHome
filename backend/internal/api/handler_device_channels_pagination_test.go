package api

// GET /api/v1/channels 的服务端分页契约。
//
// 缺陷（先红证据见交付报告）：本端点此前**完全不分页** —— list handler 直接
// Success(c, chs) 全量返回裸数组，page/page_size 被忽略；前端
// frontend-shared/src/views/channel/ChannelList.vue 只能对全量做本地
// filteredChannels.value.slice(...) 假装分页。本地切片的翻页与真实数据脱节：
// 后端一旦有上限/截断，页码越大越容易切到空页，且 total 不是服务端事实。
//
// 本文件钉住三件事：
//   ① data 是分页信封 {items,total,page,page_size}（不是裸数组、不是 list）；
//   ② items 是**当前页切片**，total 是**过滤后的全量**；
//   ③ 默认页长/上界与全仓既有约定一致（默认 20，上界 200，非法值 clamp）。
//
// 判据刻意走**原始 JSON map**（同 handler_device_dialect_test.go 的
// deviceConfigListData）：解到 struct 会让"data 其实是裸数组"变成静默零值。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// channelListRaw 返回 GET /channels 的原始 body 与解析后的 data 段（map）。
func channelListRaw(t *testing.T, r http.Handler, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/channels"+query, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels%s = %d, want 200: %s", query, w.Code, w.Body.String())
	}
	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("GET /api/v1/channels%s 的 data 不是分页信封对象（裸数组 = 未分页）：%v (body=%s)",
			query, err, w.Body.String())
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels%s 信封 code = %d, want 200", query, resp.Code)
	}
	return resp.Data
}

func channelListKeys(data map[string]any) []string {
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	return keys
}

func channelListItems(t *testing.T, data map[string]any) []any {
	t.Helper()
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("data.items 应是数组，实际 %T（data 键 = %v）", data["items"], channelListKeys(data))
	}
	return items
}

func channelListTotal(t *testing.T, data map[string]any) int {
	t.Helper()
	total, ok := data["total"].(float64)
	if !ok {
		t.Fatalf("data.total 应是数字，实际 %T（data 键 = %v）", data["total"], channelListKeys(data))
	}
	return int(total)
}

// seedPagedChannels 造 n 条挂在同一物理序列号下的通道（node_id 是 varchar，非自增 id）。
// 刻意不叫 seedChannels：handler_notification_channel_test.go 已占用该名字
// （签名 (t, db, count)），重名会让整包编译失败。
func seedPagedChannels(t *testing.T, db *gorm.DB, nodeID string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := db.Create(&models.Channel{
			NodeID: nodeID, HardwareType: "i2c", HardwareID: "I2C0",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestChannelListPagination_ServerSideSlice 是本缺陷的核心断言：
// page=2&page_size=10 必须返回**服务端的第 2 页切片**，total 是服务端全量。
// 改动前必红（data 是 25 条裸数组）。
func TestChannelListPagination_ServerSideSlice(t *testing.T) {
	r, db := setupDeviceTest(t)
	seedPagedChannels(t, db, "F0F5BDFFFE02", 25)

	data := channelListRaw(t, r, "?page=2&page_size=10")

	if _, ok := data["list"]; ok {
		t.Fatalf("禁止旧方言键 list（全仓契约是 items）；data 键 = %v", channelListKeys(data))
	}
	items := channelListItems(t, data)
	if len(items) != 10 {
		t.Fatalf("第 2 页 items = %d, want 10（共 25 条、每页 10）", len(items))
	}
	if got := channelListTotal(t, data); got != 25 {
		t.Fatalf("total = %d, want 25（必须是过滤后全量，不是页内条数）", got)
	}
	if got, _ := data["page"].(float64); int(got) != 2 {
		t.Fatalf("回显 page = %v, want 2", data["page"])
	}
	if got, _ := data["page_size"].(float64); int(got) != 10 {
		t.Fatalf("回显 page_size = %v, want 10", data["page_size"])
	}

	// 切片必须真的是"第 2 页"：id 升序时第一条 > 第 1 页最后一条。
	first, _ := items[0].(map[string]any)
	firstID, _ := first["id"].(float64)
	if int(firstID) != 11 {
		t.Fatalf("第 2 页首条 id = %v, want 11（id 升序分页）", first["id"])
	}
}

// TestChannelListPagination_DefaultsAndClamp 钉住默认值与上界与全仓既有约定一致：
// 无参数 → page=1/page_size=20；page_size=200 是合法闭区间上界；
// 越界/非法页长 clamp 回默认且**不得影响 total**。
func TestChannelListPagination_DefaultsAndClamp(t *testing.T) {
	r, db := setupDeviceTest(t)
	seedPagedChannels(t, db, "F0F5BDFFFE02", 25)

	// 无参数：默认页长 20，total 仍为全量 25。
	def := channelListRaw(t, r, "")
	if got := len(channelListItems(t, def)); got != 20 {
		t.Fatalf("默认页 items = %d, want 20", got)
	}
	if got := channelListTotal(t, def); got != 25 {
		t.Fatalf("默认页 total = %d, want 25", got)
	}
	if p, _ := def["page"].(float64); int(p) != 1 {
		t.Fatalf("默认 page = %v, want 1", def["page"])
	}
	if ps, _ := def["page_size"].(float64); int(ps) != 20 {
		t.Fatalf("默认 page_size = %v, want 20", def["page_size"])
	}

	// 合法上界：page_size=200 原样保留，全量不满一页。
	upper := channelListRaw(t, r, "?page_size=200")
	if ps, _ := upper["page_size"].(float64); int(ps) != 200 {
		t.Fatalf("page_size=200 回显 = %v, want 200（上界是闭区间）", upper["page_size"])
	}
	if got := len(channelListItems(t, upper)); got != 25 {
		t.Fatalf("page_size=200 items = %d, want 25（全量不满一页）", got)
	}

	// 非法页长（0 / 201 / 非数字）→ clamp 回默认 20，total 不受影响。
	for _, q := range []string{"?page_size=0", "?page_size=201", "?page_size=abc", "?page=0"} {
		got := channelListRaw(t, r, q)
		if ps, _ := got["page_size"].(float64); int(ps) != 20 {
			t.Fatalf("%s: 回显 page_size = %v, want 20（非法值必须 clamp 到默认）", q, got["page_size"])
		}
		if p, _ := got["page"].(float64); int(p) < 1 {
			t.Fatalf("%s: 回显 page = %v, want >= 1", q, got["page"])
		}
		if total := channelListTotal(t, got); total != 25 {
			t.Fatalf("%s: total = %d, want 25（clamp 不得影响 total）", q, total)
		}
	}

	// 越界页：空集 + total 仍为全量（不得退化成第一页、不得报错）。
	over := channelListRaw(t, r, "?page=99&page_size=10")
	if got := len(channelListItems(t, over)); got != 0 {
		t.Fatalf("越界页 items = %d, want 0（越界必须返回空集）", got)
	}
	if got := channelListTotal(t, over); got != 25 {
		t.Fatalf("越界页 total = %d, want 25", got)
	}
}

// TestChannelListPagination_NodeFilterIsCountedServerSide 钉住过滤与分页的组合：
// node_id 过滤必须下沉到 Count 侧，total 是**过滤后**的全量，不是库内总量。
// 若把过滤留在内存（Count 打在未过滤的 q 上），total 会虚高成两个节点之和。
func TestChannelListPagination_NodeFilterIsCountedServerSide(t *testing.T) {
	r, db := setupDeviceTest(t)
	// node_id 过滤对**非数字**取值走"先查 nodes.node_id 再按物理序列号匹配"的分支，
	// 所以必须真的建出 Node 行，否则 handler 认为该节点不存在、直接给空集。
	for _, nid := range []string{"F0F5BDFFFE02", "AABBCCDDEEFF"} {
		if err := db.Create(&models.Node{NodeID: nid, Name: "N-" + nid}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedPagedChannels(t, db, "F0F5BDFFFE02", 12)
	seedPagedChannels(t, db, "AABBCCDDEEFF", 8)

	filtered := channelListRaw(t, r, "?node_id=AABBCCDDEEFF&page=1&page_size=5")
	if got := channelListTotal(t, filtered); got != 8 {
		t.Fatalf("node_id=AABBCCDDEEFF total = %d, want 8（过滤必须在 Count 侧生效）", got)
	}
	items := channelListItems(t, filtered)
	if len(items) != 5 {
		t.Fatalf("node_id 过滤 + page_size=5 首页 items = %d, want 5", len(items))
	}
	for _, it := range items {
		row, _ := it.(map[string]any)
		if row["node_id"] != "AABBCCDDEEFF" {
			t.Fatalf("node_id 过滤混入 %v", row["node_id"])
		}
	}

	// 未知节点：空页 + total=0，且仍是分页信封（不得退回裸数组）。
	unknown := channelListRaw(t, r, "?node_id=NOPE-NOT-A-NODE&page=1&page_size=10")
	if got := len(channelListItems(t, unknown)); got != 0 {
		t.Fatalf("未知节点 items = %d, want 0", got)
	}
	if got := channelListTotal(t, unknown); got != 0 {
		t.Fatalf("未知节点 total = %d, want 0", got)
	}
}
