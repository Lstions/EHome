package api

// GET /api/v1/channels 的 hardware_type 服务端过滤契约。
//
// 缺陷背景（主控已实测）：生产库 SELECT DISTINCT hardware_type FROM channels
// 只返回大写 'UART'，而前端硬件类型下拉框提交的是小写 'uart'/'i2c'/'spi'/'adc'。
// 若按大小写敏感的精确匹配（WHERE hardware_type = 'uart'）实现，该筛选器在真实
// 数据下恒返回 0 行 —— 用户看到"永远筛不出东西"的静默缺陷，这正是本次要修的。
//
// 冻结契约（不得自行改动）：
//   (1) 参数名 hardware_type；空/缺省 = 不过滤；
//   (2) 大小写不敏感，实现为 UPPER(hardware_type) = ? 并传入 strings.ToUpper(值)
//       （SQLite 与 PostgreSQL 都支持 UPPER）；
//   (3) 与既有 node_id 过滤按 AND 叠加；
//   (4) 过滤必须在 Count 与 Find 两侧同时生效，total = **过滤后**全量，
//       否则分页器会算出多余页数（纪律见 handler_node.go:70-71）；
//   (5) 未知/非法值 => 空页 + 分页信封（不得退回裸数组、不得 500）。
//
// 判据刻意走原始 JSON map（同 handler_device_channels_pagination_test.go）：
// 解到 struct 会让"data 其实是裸数组"变成静默零值。
//
// 本文件自带 helper（前缀 chFilter），不复用其它测试文件的函数名，
// 避免与并行改动的测试文件发生重名编译冲突。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// chFilterRaw 返回 GET /channels 原始 body 解析后的 data 段（map）。
// data 不是对象（例如裸数组）会直接判失败 —— 分页信封是本端点契约的一部分。
func chFilterRaw(t *testing.T, r http.Handler, query string) map[string]any {
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
		t.Fatalf("GET /api/v1/channels%s 的 data 不是分页信封对象（裸数组 = 契约破坏）：%v (body=%s)",
			query, err, w.Body.String())
	}
	if resp.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/channels%s 信封 code = %d, want 200", query, resp.Code)
	}
	return resp.Data
}

func chFilterItems(t *testing.T, data map[string]any) []any {
	t.Helper()
	items, ok := data["items"].([]any)
	if !ok {
		t.Fatalf("data.items 应是数组（空集也必须是 []），实际 %T（data=%v）", data["items"], data)
	}
	return items
}

func chFilterTotal(t *testing.T, data map[string]any) int {
	t.Helper()
	total, ok := data["total"].(float64)
	if !ok {
		t.Fatalf("data.total 应是数字，实际 %T（data=%v）", data["total"], data)
	}
	return int(total)
}

// chFilterEnvelopeShape 断言分页信封四个键齐全（存在性 + 类型）。
func chFilterEnvelopeShape(t *testing.T, data map[string]any) {
	t.Helper()
	if _, ok := data["items"]; !ok {
		t.Fatalf("分页信封缺 items：%v", data)
	}
	if _, ok := data["total"]; !ok {
		t.Fatalf("分页信封缺 total：%v", data)
	}
	if _, ok := data["page"]; !ok {
		t.Fatalf("分页信封缺 page：%v", data)
	}
	if _, ok := data["page_size"]; !ok {
		t.Fatalf("分页信封缺 page_size：%v", data)
	}
}

// seedHardwareTypeChannels 造 n 条指定 hardware_type 的通道。
// hardware_type 按"存储即原样"写入：生产库存的是大写 UART，
// 所以测试也必须写大写，才能验证请求侧小写能命中。
func seedHardwareTypeChannels(t *testing.T, db *gorm.DB, nodeID, hardwareType string, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := db.Create(&models.Channel{
			NodeID: nodeID, HardwareType: hardwareType, HardwareID: hardwareType + "0",
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
}

// TestChannelListHardwareTypeFilter_CaseInsensitive 是本次缺陷的核心断言：
// 存储为 'UART'（生产库真实取值）的行，必须能被请求参数 hardware_type=小写 uart 命中。
// 大小写敏感实现（WHERE hardware_type = 'uart'）下 total=0 —— 即"筛选器恒空"缺陷；
// 完全忽略参数时 total=2（未过滤全量）。
func TestChannelListHardwareTypeFilter_CaseInsensitive(t *testing.T) {
	r, db := setupDeviceTest(t)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "UART", 1)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "I2C", 1)

	for _, value := range []string{"uart", "UART", "UaRt"} {
		data := chFilterRaw(t, r, "?hardware_type="+value)
		if got := chFilterTotal(t, data); got != 1 {
			t.Fatalf("hardware_type=%s total = %d, want 1（存储为 UART，过滤必须大小写不敏感："+
				"2 = 参数被忽略/未过滤，0 = 大小写敏感精确匹配）", value, got)
		}
		items := chFilterItems(t, data)
		if len(items) != 1 {
			t.Fatalf("hardware_type=%s items = %d, want 1", value, len(items))
		}
		row, _ := items[0].(map[string]any)
		if row["hardware_type"] != "UART" {
			t.Fatalf("hardware_type=%s 命中的行 hardware_type = %v, want UART", value, row["hardware_type"])
		}
	}
}

// TestChannelListHardwareTypeFilter_AndNodeID 钉住 AND 语义与 Count 侧过滤：
// total 必须是"两个条件同时生效"后的全量。
// 取并集或只取其一都会得到 3/5/6 而非 2。
func TestChannelListHardwareTypeFilter_AndNodeID(t *testing.T) {
	r, db := setupDeviceTest(t)
	// node_id 非数字取值走"先查 nodes 再按物理序列号匹配"分支，必须真的建出 Node 行。
	for _, nid := range []string{"F0F5BDFFFE02", "AABBCCDDEEFF"} {
		if err := db.Create(&models.Node{NodeID: nid, Name: "N-" + nid}).Error; err != nil {
			t.Fatal(err)
		}
	}
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "UART", 2)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "I2C", 1)
	seedHardwareTypeChannels(t, db, "AABBCCDDEEFF", "UART", 3)

	// 单独 hardware_type（小写请求命中大写存储）：过滤后全量 5，不是库内 6。
	only := chFilterRaw(t, r, "?hardware_type=uart")
	if got := chFilterTotal(t, only); got != 5 {
		t.Fatalf("hardware_type=uart total = %d, want 5（Count 侧必须带过滤，不能是未过滤全量 6）", got)
	}

	// AND 叠加：node_id=F0F5BDFFFE02 且 hardware_type=uart => 2。
	both := chFilterRaw(t, r, "?hardware_type=uart&node_id=F0F5BDFFFE02&page=1&page_size=10")
	if got := chFilterTotal(t, both); got != 2 {
		t.Fatalf("hardware_type=uart + node_id=F0F5BDFFFE02 total = %d, want 2（两个过滤必须 AND 叠加）", got)
	}
	for _, it := range chFilterItems(t, both) {
		row, _ := it.(map[string]any)
		if row["node_id"] != "F0F5BDFFFE02" || row["hardware_type"] != "UART" {
			t.Fatalf("AND 过滤混入 node_id=%v hardware_type=%v", row["node_id"], row["hardware_type"])
		}
	}

	// 组合后无交集 => 0 行，且仍是分页信封。
	none := chFilterRaw(t, r, "?hardware_type=i2c&node_id=AABBCCDDEEFF")
	chFilterEnvelopeShape(t, none)
	if got := chFilterTotal(t, none); got != 0 {
		t.Fatalf("hardware_type=i2c + node_id=AABBCCDDEEFF total = %d, want 0", got)
	}
}

// TestChannelListHardwareTypeFilter_AbsentOrEmptyIsNoFilter 回归：
// 缺省 / 空串 hardware_type 必须与改动前完全一致（不过滤）。
func TestChannelListHardwareTypeFilter_AbsentOrEmptyIsNoFilter(t *testing.T) {
	r, db := setupDeviceTest(t)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "UART", 1)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "I2C", 1)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "SPI", 1)

	for _, q := range []string{"", "?hardware_type=", "?page=1&page_size=20"} {
		data := chFilterRaw(t, r, q)
		chFilterEnvelopeShape(t, data)
		if got := chFilterTotal(t, data); got != 3 {
			t.Fatalf("GET /channels%s total = %d, want 3（缺省/空 hardware_type = 不过滤，行为不得变）", q, got)
		}
		if got := len(chFilterItems(t, data)); got != 3 {
			t.Fatalf("GET /channels%s items = %d, want 3", q, got)
		}
	}
}

// TestChannelListHardwareTypeFilter_UnknownValueIsEmptyEnvelope 钉住非法值语义：
// 库里不存在的取值 => 200 + 空页 + 完整分页信封，不得退回裸数组、不得 500。
func TestChannelListHardwareTypeFilter_UnknownValueIsEmptyEnvelope(t *testing.T) {
	r, db := setupDeviceTest(t)
	seedHardwareTypeChannels(t, db, "F0F5BDFFFE02", "UART", 2)

	for _, q := range []string{
		"?hardware_type=no-such-bus",
		"?hardware_type=SPI", // 合法枚举但库内无此类行
	} {
		data := chFilterRaw(t, r, q)
		chFilterEnvelopeShape(t, data)
		if got := len(chFilterItems(t, data)); got != 0 {
			t.Fatalf("GET /channels%s items = %d, want 0（未知值必须空页）", q, got)
		}
		if got := chFilterTotal(t, data); got != 0 {
			t.Fatalf("GET /channels%s total = %d, want 0（未知值不是未过滤全量 2）", q, got)
		}
		if p, _ := data["page"].(float64); int(p) != 1 {
			t.Fatalf("GET /channels%s 回显 page = %v, want 1（空页也要回显分页参数）", q, data["page"])
		}
		if ps, _ := data["page_size"].(float64); int(ps) != 20 {
			t.Fatalf("GET /channels%s 回显 page_size = %v, want 20", q, data["page_size"])
		}
	}
}
