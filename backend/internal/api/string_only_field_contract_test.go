package api

// string_only_field_contract_test.go — 把「纯字符串字段靠 str_values 补齐」从注释里的共识
// 变成用例里的断言（2026-11-08，Lead 裁决：能从注释变成断言的就别留到下一轮）。
//
// ============ 这条约束管什么 ============
//
// 硬切换后数值字段有两个来源，都能对拍、都能区分：
//
//	· 帧表 telemetry_frame       （长期存储，L2 全字段）
//	· latest 值缓存              （SensorParserConsumer 每帧更新）
//
// 而**纯字符串字段**（hardware_version="V19"、serial_number=...）只有一个来源：
// device_data.data_json.str_values —— 因为它们的 Value 恒为 0，unified_data 与
// telemetry_frame 都**没有 StringValue 列**，device_data 是唯一落库位置。
//
// 所以一旦某处代码把 sensors 与 str_values 看成「两套各管各的」，纯字符串字段就会
// 静默消失，且**没有任何症状**：sensors 数组里少一项，前端显示「—」，没人会报 bug。
//
// ============ 为什么用「契约」而不是「实现细节」的写法 ============
//
// 断言的是**输入输出关系**，不是某条分支：
//
//	given  str_values 里有一个数值源给不出的字段 F
//	when   任意路径（单行 / 列表、缓存命中 / miss、帧表可用 / 不可用）合成 sensors
//	then   sensors 里必须有一项 {Name: F, Value: 0, StringValue: <str_values[F]>}
//
// 这样它不依赖「现在有几条路径」，将来加路径也自动受约束。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

type stringOnlySensor struct {
	Name        string
	Value       float64
	StringValue string
}

// extractFields 把 data_json 里的 sensors 解成「名字 -> (Value, StringValue)」。
func extractFields(t *testing.T, dataJSON string) map[string]stringOnlySensor {
	t.Helper()
	var dj struct {
		Sensors []stringOnlySensor
	}
	if err := json.Unmarshal([]byte(dataJSON), &dj); err != nil {
		t.Fatalf("data_json 解析失败: %v (%s)", err, dataJSON)
	}
	out := make(map[string]stringOnlySensor, len(dj.Sensors))
	for _, s := range dj.Sensors {
		out[s.Name] = s
	}
	return out
}

// assertStringOnlyFieldSurvives 是这条契约的唯一断言体。
func assertStringOnlyFieldSurvives(t *testing.T, dataJSON, field, want string) {
	t.Helper()
	fields := extractFields(t, dataJSON)
	got, ok := fields[field]
	if !ok {
		t.Fatalf("纯字符串字段 %q 从 sensors 里消失了 —— \n"+
			"str_values 里有它，但数值源（帧表 / 缓存）给不出它（Value 恒为 0），\n"+
			"所以必须由 mergeStringValues 补齐；只「更新已存在项」会把它整个丢掉。\n"+
			"这是**永久性数据丢失**：device_data 是该字段唯一落库位置。\n"+
			"data_json=%s", field, dataJSON)
	}
	if got.StringValue != want {
		t.Fatalf("字段 %q 的 StringValue = %q, want %q（前端读 StringValue 优先，读 Value 会显示成 0）: %s",
			field, got.StringValue, want, dataJSON)
	}
	if got.Value != 0 {
		t.Fatalf("字段 %q 的 Value = %v, want 0（该字段的真值只在 StringValue 里）: %s",
			field, got.Value, dataJSON)
	}
}

// TestStringOnlyField_SurvivesWhenValueSourceEmpty 锁「数值源为空」这个最坏情形。
//
// 这正是删掉 unified_data 回落之后暴露出来的那个 bug 的形状：
// 缓存 miss + 帧表不可用时，数值源给出 0 个字段，纯字符串字段必须靠 str_values 自己活下来。
func TestStringOnlyField_SurvivesWhenValueSourceEmpty(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "stronly", Type: "bms_jbd", NodeID: "STR1", HardwareID: "0x01"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	// 缓存清空（miss）+ SQLite 测试库没有 telemetry_frame（帧表不可用）=> 数值源为空。
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	if err := db.Create(&models.DeviceData{
		DeviceID: dev.ID, NodeID: "STR1",
		DataJSON: `{"raw_hex":"ee","channel_id":1,"timestamp":1,` +
			`"str_values":{"hardware_version":"V19","serial_number":"SN-0042"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("latest-data = %d: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, w.Body.String())
	}
	assertStringOnlyFieldSurvives(t, env.Data.DataJSON, "hardware_version", "V19")
	assertStringOnlyFieldSurvives(t, env.Data.DataJSON, "serial_number", "SN-0042")
}

// TestStringOnlyField_SurvivesInListPath 锁列表路径（applySensorViews）上的同一契约。
//
// 为什么单行通过还不够：两者走**不同的写出函数**
//
//	· 单行  deviceDataJSONWithSensors -> mergeStringValues
//	· 列表  applySensorViews          -> mergeStringValues
//
// 我最初只修了单行那条（TestLatestDataSynthesisKeepsStringValues 抓到的），
// 列表那条的原地「更新已存在项」是同一形状的第二个实例。写这条用例是为了让
// 「两条路径必须同构」这件事**由用例保证**，而不是由我记得。
func TestStringOnlyField_SurvivesInListPath(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "strlist", Type: "bms_jbd", NodeID: "STR2", HardwareID: "0x02"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	now := time.Now()
	if err := db.Create(&models.DeviceData{
		DeviceID: dev.ID, NodeID: "STR2",
		DataJSON: `{"raw_hex":"ef","channel_id":1,"timestamp":1,` +
			`"str_values":{"hardware_version":"V20"}}`,
		Timestamp: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var list struct {
		Data struct {
			Items []struct {
				DataJSON string `json:"data_json"`
			} `json:"items"`
		} `json:"data"`
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet,
		"/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/data", nil))
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, w.Body.String())
	}
	if len(list.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Data.Items))
	}
	assertStringOnlyFieldSurvives(t, list.Data.Items[0].DataJSON, "hardware_version", "V20")
}
