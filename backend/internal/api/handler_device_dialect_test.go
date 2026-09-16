package api

// P1「分页方言收敛」：`GET /device-configs` 是**最后一个** `{list,total}` 旧方言端点。
//
// 实测（2026-09-15）：全仓分页端点的 data 段形状已收敛到 `{items,total,page,page_size}`，
// 12 个里有 11 个用 `items`，**只有 `/device-configs` 用 `list`**：
//     grep -rn '"list":'  internal/api/*.go | grep -v _test   ⇒ 1（就是它）
//     grep -rn '"items":' internal/api/*.go | grep -v _test  ⇒ 11
// 前端代码里也明写了这件事（api/automation.ts:164）：
//     「用 `items` 而非 `list` —— `/device-configs` 的 `{list,...}` 是待收敛的旧方言」。
//
// 为什么这个漂移能藏这么久：**既有测试只断言 `total`，从不断言“数组挂在哪个键上”**
// （见 handler_device_crud_test.go 的三个 List 用例）。于是键名换了、测试照绿。
// 这属于「注释/计划声称已收敛、实现没有」的同族问题。
//
// 本文件钉两件事：
//   ① 该端点必须用 `items`，且**不得**再出现 `list`（避免两套并存继续漂移）；
//   ② 该端点的分页语义（total 是全量、items 是当前页切片）与其它端点一致。
//
// See docs/分析/后续工作计划与方案-2026-09-15.md（分页方言收敛）。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

// deviceConfigListData 取 GET /device-configs 的 data 段为**原始 map**。
// 刻意用 map 而不是 struct：struct 的字段零值会掩盖「键名不存在」——
// 那正是本缺陷此前不被发现的原因。
func deviceConfigListData(t *testing.T, r http.Handler, query string) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/device-configs"+query, nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int            `json:"code"`
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("envelope code: expected 200, got %d", resp.Code)
	}
	return resp.Data
}

// TestDeviceConfig_List_UsesItemsDialect 是本次收敛的核心断言。
func TestDeviceConfig_List_UsesItemsDialect(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.DeviceConfig{Name: "Temp1", DeviceType: "temperature", HardwareType: "uart", Status: "active"})

	data := deviceConfigListData(t, r, "")
	keys := make([]string, 0, len(data))
	for k := range data {
		keys = append(keys, k)
	}
	if _, ok := data["items"]; !ok {
		t.Fatalf("GET /device-configs 的 data 缺 `items`（全仓其余 11 个分页端点都用它）；实际键=%v。"+
			"旧方言 `list` 必须收敛，否则前端要同时兼容两套形状", keys)
	}
	if _, ok := data["list"]; ok {
		t.Fatalf("GET /device-configs 仍返回旧方言键 `list`；两套并存会继续漂移。实际键=%v", keys)
	}
	if _, ok := data["total"]; !ok {
		t.Fatalf("GET /device-configs 缺 `total`；实际键=%v", keys)
	}
}

// TestDeviceConfig_List_ItemsAreCurrentPage 钉住分页语义：
// items 是**当前页切片**，total 是**过滤后的全量** —— 与其它端点一致。
// 两者若被弄反（例如 items 返回全量），前端分页器会静默错位。
func TestDeviceConfig_List_ItemsAreCurrentPage(t *testing.T) {
	r, db := setupDeviceTest(t)
	for i := 0; i < 25; i++ {
		db.Create(&models.DeviceConfig{
			Name: "Config" + string(rune('A'+i)), DeviceType: "temperature", HardwareType: "uart", Status: "active",
		})
	}

	data := deviceConfigListData(t, r, "?page=2&page_size=10")
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("items 应是数组，实际 %T", data["items"])
	}
	if len(items) != 10 {
		t.Fatalf("第 2 页应有 10 条（共 25 条、每页 10），实际 %d", len(items))
	}
	total, ok := data["total"].(float64)
	if !ok {
		t.Fatalf("total 应是数字，实际 %T", data["total"])
	}
	if int(total) != 25 {
		t.Fatalf("total 应是过滤后全量 25，实际 %v（若返回页内条数则分页器会错位）", total)
	}
}
