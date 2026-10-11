package api

// S3 读契约锁 (2026-10-07) —— Lead task-16 裁决采纳项 2。
//
// 背景 (量化证据见 internal/databus/s3_duplicate_device_data_probe_test.go):
// 一个可解析 DataEvent 会写 device_data **两行**, 由两个消费者各写一行,
// 形状不同:
//   db_persist    : device_id=0, data_json={"raw":<hex>,...}              (原始字节, 审计/取证)
//   sensor_parser : device_id=N, data_json={"raw_hex":...,"sensors":[...]} (解析后物理量)
//
// 两者**覆盖面不同, 都不是冗余** (error_code!=0 / 空 raw / 解析被拒 三类只有 db_persist 会写),
// 所以本轮**两个写入者都保留**。但"保留两行"必须配一条锁:
// 前端依赖的**读形状**不能被日后任何"去重"悄悄改掉。
//
// 前端依赖点: frontend-shared/src/composables/useDeviceData.ts:114 读
// `data_json.sensors` 逐项取名 (DataPanel 面板)。若 latest-data 改返回 raw 形状,
// 该循环拿不到 sensors ⇒ 面板静默全空。
//
// 因此本文件锁两件事, 且**每条正向断言都配反向对照**:
//   正向: 两个端点必须返回 parsed 形状;
//   反向: raw 形状**不得**出现在这两个端点上 (否则"两边都返回"也能骗过正向断言)。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// s3Shape 是一行 data_json 的形状分类。
type s3Shape string

const (
	s3ShapeParsed s3Shape = "parsed(sensor_parser)" // 有 sensors
	s3ShapeRaw    s3Shape = "raw(db_persist)"       // 有 raw, 无 sensors
	s3ShapeBoth   s3Shape = "both"
	s3ShapeNone   s3Shape = "neither"
)

func s3Classify(t *testing.T, dataJSON string) s3Shape {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(dataJSON), &m); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (raw=%q)", err, dataJSON)
	}
	_, hasRaw := m["raw"]
	_, hasSensors := m["sensors"]
	switch {
	case hasRaw && hasSensors:
		return s3ShapeBoth
	case hasSensors:
		return s3ShapeParsed
	case hasRaw:
		return s3ShapeRaw
	default:
		return s3ShapeNone
	}
}

// s3SeedBothShapes 造出"同一事件两行都已在库"的状态:
// 一行 raw(db_persist, device_id=0), 一行 parsed(sensor_parser, device_id=dev.ID)。
// 两行同 node_id —— 与真实写侧一致。
func s3SeedBothShapes(t *testing.T, db *gorm.DB) (models.Node, models.EdgeDevice) {
	t.Helper()
	node := models.Node{NodeID: "S3READ", Name: "s3read", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareID: "0x76", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	dev := models.EdgeDevice{Name: "s3read-dev", Type: "bms_jbd", NodeID: node.NodeID, ChannelID: ch.ID, HardwareID: "0x76"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	// 行 1: db_persist 形状 (device_id=0, 只有 raw)。
	if err := db.Create(&models.DeviceData{
		NodeID: node.NodeID, DeviceID: 0,
		DataJSON: `{"channel":1,"command_index":0,"edge_device_id":1,"error_code":0,"raw":"655ac07eed00","request_id":1,"sequence":7}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// 行 2: sensor_parser 形状 (device_id=dev.ID, 有 sensors)。
	if err := db.Create(&models.DeviceData{
		NodeID: node.NodeID, DeviceID: dev.ID,
		DataJSON: `{"channel_id":1,"raw_hex":"655ac07eed00","sensors":[{"Name":"temperature","Value":25.0,"Unit":"C","StringValue":""}],"timestamp":1791338725971}`,
	}).Error; err != nil {
		t.Fatal(err)
	}
	return node, dev
}

func s3GetJSON(t *testing.T, r *gin.Engine, path string, out interface{}) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s = %d, want 200: %s", path, w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), out); err != nil {
		t.Fatalf("GET %s 响应解析失败: %v (body=%s)", path, err, w.Body.String())
	}
}

// TestS3_ReadContract_LatestDataMustBeParsedShape 锁 latest-data 的读形状。
//
// 正向: 必须是 parsed。反向: 不得是 raw/both (若"两边都返回", 前端仍会在某些请求上崩)。
func TestS3_ReadContract_LatestDataMustBeParsedShape(t *testing.T) {
	r, db := setupTestRouter(t)
	_, dev := s3SeedBothShapes(t, db)

	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
			DeviceID uint   `json:"device_id"`
		} `json:"data"`
	}
	s3GetJSON(t, r, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", &env)

	if env.Data.DataJSON == "" {
		t.Fatal("latest-data 返回空 data_json —— 前端 useDeviceData.ts:114 依赖 data_json.sensors")
	}
	shape := s3Classify(t, env.Data.DataJSON)
	t.Logf("latest-data → shape=%s device_id=%d", shape, env.Data.DeviceID)

	// 正向断言
	if shape != s3ShapeParsed {
		t.Fatalf("latest-data 形状 = %s, 必须为 %s (前端 useDeviceData.ts:114 读 data_json.sensors)",
			shape, s3ShapeParsed)
	}
	// 反向对照: raw 形状不得出现在本端点
	if strings.Contains(env.Data.DataJSON, `"raw":`) && !strings.Contains(env.Data.DataJSON, `"sensors"`) {
		t.Fatalf("latest-data 返回了 db_persist 的 raw 形状 —— 前端会拿到空面板: %s", env.Data.DataJSON)
	}
	// 也必须带 sensors 字段 (形状契约的实质)
	if !strings.Contains(env.Data.DataJSON, `"sensors"`) {
		t.Fatalf("latest-data 的 data_json 缺 sensors 字段: %s", env.Data.DataJSON)
	}
}

// TestS3_ReadContract_EdgeDeviceDataMustBeParsedShape 锁 /edge-devices/:id/data 的读形状。
//
// 这个端点按 device scope 过滤, 理论上只会命中 parsed 行 —— 但"理论上"正是要锁的东西:
// 若日后有人把 db_persist 的 device_id 也填成真实 id (或改 scope 语义), 这里立刻变红。
func TestS3_ReadContract_EdgeDeviceDataMustBeParsedShape(t *testing.T) {
	r, db := setupTestRouter(t)
	_, dev := s3SeedBothShapes(t, db)

	var list struct {
		Data struct {
			Items []map[string]interface{} `json:"items"`
			Total int64                    `json:"total"`
		} `json:"data"`
	}
	s3GetJSON(t, r, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/data", &list)

	if list.Data.Total == 0 {
		t.Fatal("edge-devices/:id/data 返回 0 行 —— 断言会假通过, fixture 没喂对")
	}
	for _, it := range list.Data.Items {
		dj, _ := it["data_json"].(string)
		shape := s3Classify(t, dj)
		t.Logf("edge-devices/:id/data → shape=%s device_id=%v", shape, it["device_id"])
		if shape != s3ShapeParsed {
			t.Fatalf("edge-devices/:id/data 混入 %s 形状行 (device_id=%v): %s", shape, it["device_id"], dj)
		}
	}
}

// TestS3_ReadContract_ReverseControlRawShapeIsDistinguishable 是**反向对照的自身校验**。
//
// 上面两条"必须是 parsed"若写成恒真 (例如 classify 永远返回 parsed), 就会假绿。
// 本用例用同一套 classify 去判一行**已知的 raw 形状**, 必须判成 raw;
// 再判一行已知 parsed, 必须判成 parsed。这证明分类器真的在区分两种形状。
func TestS3_ReadContract_ReverseControlRawShapeIsDistinguishable(t *testing.T) {
	rawJSON := `{"channel":1,"command_index":0,"edge_device_id":1,"error_code":0,"raw":"655ac07eed00","request_id":1,"sequence":7}`
	parsedJSON := `{"channel_id":1,"raw_hex":"655ac07eed00","sensors":[{"Name":"temperature","Value":25.0}],"timestamp":1}`

	if got := s3Classify(t, rawJSON); got != s3ShapeRaw {
		t.Fatalf("分类器把 raw 形状判成 %s —— 反向对照失效, 正向断言会假绿", got)
	}
	if got := s3Classify(t, parsedJSON); got != s3ShapeParsed {
		t.Fatalf("分类器把 parsed 形状判成 %s", got)
	}
	// both: 两种键都有 —— 那是真正的"两边都返回", 必须被识别为第三态而不是 parsed。
	if got := s3Classify(t, `{"raw":"aa","sensors":[]}`); got != s3ShapeBoth {
		t.Fatalf("分类器未识别 both 形态, got %s", got)
	}
}

// TestS3_KnownLegacy_NodeDataEndpointMixesShapes 是**已知遗留的表征测试**。
//
// Lead task-16 裁决: /nodes/:id/data 本轮**故意不改** —— 它按 node_id 过滤, 两种形状
// 都会命中, 但它**没有任何生产调用者** (全仓 grep + docs/分析/功能模块与业务功能实测盘点-2026-09-23.md:163
// 「无包装、无调用者」), 为一个无人调用的端点改接口只承担风险而无收益。
//
// 本用例把"歧义仍然存在"钉成**可执行的记录**, 而不是口头结论:
//
//	· 它证明该端点确实混两种形状 (审计 S3 的核心事实);
//	· 若日后有人改了它 (改名 /raw-data、或加形状谓词、或删路由), 本用例会变红,
//	  强制其回来更新这条记录与审计条目 —— 这正是"已知遗留"应有的行为。
func TestS3_KnownLegacy_NodeDataEndpointMixesShapes(t *testing.T) {
	r, db := setupTestRouter(t)
	node, _ := s3SeedBothShapes(t, db)

	var nodeList struct {
		Data []map[string]interface{} `json:"data"`
	}
	s3GetJSON(t, r, "/api/v1/nodes/"+node.NodeID+"/data", &nodeList)

	shapes := map[s3Shape]int{}
	for _, it := range nodeList.Data {
		dj, _ := it["data_json"].(string)
		shapes[s3Classify(t, dj)]++
	}
	t.Logf("/nodes/:id/data → %d 行, 形状分布=%v", len(nodeList.Data), shapes)

	if len(shapes) < 2 {
		t.Fatalf("已知遗留已改变: /nodes/:id/data 只返回 %v 一种形状。\n"+
			"若这是有意修复 (改名/加形状谓词/删路由), 请更新本用例与审计 S3 条目;\n"+
			"若是无意回归, 请还原。", shapes)
	}
	if shapes[s3ShapeRaw] == 0 || shapes[s3ShapeParsed] == 0 {
		t.Fatalf("已知遗留的形状组合变了: %v (期望同时含 raw 与 parsed)", shapes)
	}
	t.Logf("=== 已知遗留确认: 该端点同时返回 raw 与 parsed, 无生产调用者, 本轮故意未动 ===")
}
