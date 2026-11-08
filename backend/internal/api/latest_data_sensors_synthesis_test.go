package api

// task-19 验收锁: device_data 停写 sensors 后, /latest-data 必须在**读时合成**回
// 与改造前一致的形状 —— 否则前端 useDeviceData.ts:150 / DataPanel.vue:488
// 会拿到 undefined 并静默全空（不是报错，是最难发现的那种坏）。
//
// 背景（写侧改动见 internal/databus/consumers_heavy.go:583-615）:
//   · 改前 device_data.data_json = {"raw_hex":..,"sensors":[{Name,Value,Unit,StringValue}..],..}
//   · sensors 占该列 83%（1,251 B/行里的 ~1,038 B），与 unified_data 完全重复
//   · 改后写 {"raw_hex":..,"channel_id":..,"timestamp":..}，读时按「每 sensor 取最新」合成
//
// ⚠ 本文件刻意**不复用** s3_read_shape_probe_test.go 的 fixture：
// 那个 fixture 直接往库里写"带 sensors 的老行"，恰好绕开"库里没有 sensors"这条新常态 —
// 用它来测本改造会**恒绿**（假绿）。这里的 fixture 必须写**只有 raw_hex 的新行**。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// 2026-11-08 硬切换后，以下两条用例的被测机制已删除：
//
//	TestLatestDataSynthesizesSensorsFromStopWriteRow    断言 /latest-data 从 unified_data
//	                                                     合成 3 个传感器
//	TestEdgeDeviceDataListSynthesizesSensorsAsOfRowTime 断言列表行按 as-of 时刻合成
//
// 它们用 setupTestRouter（SQLite），而 SQLite 测试库没有 telemetry_frame 表 =>
// 帧表路径恒 ok=false => 硬切换删掉 unified_data 回落后，两条路径都恒为空。
//
// 保留为 t.Skip 而不是删除：它们记录了 sensors 曾经如何被合成这个历史，
// 且同样的断言在 PG 侧有等价覆盖（帧表版本）：
//
//	TestLatestValuesAndLatestData_ShareFrameSource          latest-values 与 latest-data 同源
//	TestDeviceDataSensors_FromFramesProducesExpectedFields  列表行 sensors 合成
//	TestLatestValuesFromFrames_SurvivesUnifiedRetirement    预热来源
//
// 若有人想在 SQLite 上恢复这两条，先回答：SQLite 没有帧表，合成从哪来？
func TestLatestDataSynthesizesSensorsFromStopWriteRow(t *testing.T) {
	t.Skip("被测机制已删除：unified_data 合成回落（2026-11-08 硬切换）。PG 侧由 TestDeviceDataSensors_FromFramesProducesExpectedFields 覆盖")
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19", Type: "bms_jbd", NodeID: "T19N", HardwareID: "0x01"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	now := time.Now()
	// 三个物理量分三个时刻入库 —— 这正是 L1 收窄后的真实形态：
	// 每一帧只写"变化"的字段，所以同一时刻只有一两个物理量。
	rows := []models.UnifiedData{
		{DeviceID: dev.ID, SensorName: "total_voltage", Value: 54.12, Unit: "V", Timestamp: now.Add(-30 * time.Second)},
		{DeviceID: dev.ID, SensorName: "soc", Value: 87, Unit: "%", Timestamp: now.Add(-20 * time.Second)},
		{DeviceID: dev.ID, SensorName: "current", Value: -12.5, Unit: "A", Timestamp: now.Add(-10 * time.Second)},
		// 同一 sensor 的旧值 —— 必须被新值覆盖，否则合成出重复/陈旧读数。
		{DeviceID: dev.ID, SensorName: "total_voltage", Value: 53.00, Unit: "V", Timestamp: now.Add(-60 * time.Second)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}

	// 停写 sensors 后的真实行形状。
	newRow := models.DeviceData{
		DeviceID:  dev.ID,
		NodeID:    "T19N",
		DataJSON:  `{"raw_hex":"dd05","channel_id":1,"timestamp":` + strconv.FormatInt(now.UnixMilli(), 10) + `}`,
		Timestamp: now,
	}
	if err := db.Create(&newRow).Error; err != nil {
		t.Fatal(err)
	}

	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
			DeviceID uint   `json:"device_id"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("latest-data = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, w.Body.String())
	}
	if env.Data.DeviceID != dev.ID {
		t.Fatalf("device_id = %d, want %d", env.Data.DeviceID, dev.ID)
	}

	var dj map[string]interface{}
	if err := json.Unmarshal([]byte(env.Data.DataJSON), &dj); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (raw=%q)", err, env.Data.DataJSON)
	}
	// raw_hex 必须原样保留（前端 DataPanel 的「原始数据」列）。
	// 这是砍 sensors 时**唯一不能动**的东西 —— 它是 device_data 独有的。
	if got, _ := dj["raw_hex"].(string); got != "dd05" {
		t.Fatalf("raw_hex = %q, want \"dd05\"; data_json=%s", got, env.Data.DataJSON)
	}
	raw, ok := dj["sensors"].([]interface{})
	if !ok {
		t.Fatalf("sensors 缺失或类型不对 (前端 useDeviceData.ts:150 依赖 data_json.sensors): %s", env.Data.DataJSON)
	}
	if len(raw) != 3 {
		t.Fatalf("sensors 数量 = %d, want 3 (3 个不同 sensor_name 各取最新): %s", len(raw), env.Data.DataJSON)
	}

	got := map[string]float64{}
	for _, item := range raw {
		m, ok := item.(map[string]interface{})
		if !ok {
			t.Fatalf("sensors 元素不是对象: %v", item)
		}
		// 键名必须是**大驼峰**：历史形状是 json.Marshal([]parser.Field)，parser.Field
		// 没有 json tag ⇒ 键就是 Go 字段名。前端按 s.Name / s.Value 取值，
		// 改成 name/value 会让面板静默全空。
		name, _ := m["Name"].(string)
		if name == "" {
			t.Fatalf("sensors 元素缺 Name（键名大小写错？必须是 parser.Field 的 Go 字段名）: %v", m)
		}
		v, _ := m["Value"].(float64)
		got[name] = v
		// StringValue 必须存在（历史形状里它恒在，只是可能为空串）。
		if _, has := m["StringValue"]; !has {
			t.Fatalf("sensors 元素缺 StringValue 键: %v", m)
		}
	}
	want := map[string]float64{"total_voltage": 54.12, "soc": 87, "current": -12.5}
	for name, wantV := range want {
		if got[name] != wantV {
			t.Fatalf("%s = %v, want %v（必须是该 sensor 的**最新**值，全量=%v）", name, got[name], wantV, got)
		}
	}
	// 反向对照：旧值不得出现（若写成"全取再拼"会同时出现 54.12 与 53.00）。
	if got["total_voltage"] == 53.00 {
		t.Fatalf("取到了旧值 53.00 —— 合成没有按「最新」口径")
	}
}

// TestLatestDataKeepsLegacySensorsRowUntouched 是**存量兼容**的反向对照。
//
// 老行的 sensors 是**那一帧的完整物理量**，比"各取最新"更准。
// 若读路径覆盖它，历史数据会从"那一帧的真值"退化成"今天的值" —— 静默篡改。
func TestLatestDataKeepsLegacySensorsRowUntouched(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19legacy", Type: "bms_jbd", NodeID: "T19L", HardwareID: "0x02"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	// 库里另有一个"今天的"值，若发生覆盖就会露馅。
	if err := db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "soc", Value: 99, Unit: "%", Timestamp: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	const legacy = `{"raw_hex":"aa","channel_id":1,"sensors":[{"Name":"soc","Value":11,"Unit":"%","StringValue":""}],"timestamp":1}`
	if err := db.Create(&models.DeviceData{DeviceID: dev.ID, NodeID: "T19L", DataJSON: legacy}).Error; err != nil {
		t.Fatal(err)
	}

	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if env.Data.DataJSON != legacy {
		t.Fatalf("存量行的 data_json 被改写了: got=%s want=%s", env.Data.DataJSON, legacy)
	}
}

// TestLatestDataSynthesisCacheMissYieldsEmptySensors 锁「缓存 miss 且帧表不可用 => 空」。
//
// ============ 这条用例的语义在 2026-11-08 变了（改名是刻意的）============
//
// 原名 TestLatestDataSynthesisFallsBackToDBOnCacheMiss，断言「缓存 miss 时回落
// unified_data 的 GROUP BY 查询，拿到最新值 56」。
//
// 硬切换删掉那条回落之后：
//
//	缓存 miss + 帧表不可用（SQLite 测试库没有 telemetry_frame）
//	=> 没有任何数据源 => sensors 是**空数组**
//
// **保留原名会是「名字说测 X，实际测 Y」** —— 今天已抓过 3 次这个形态，所以改名。
//
// ⚠ sensors 是 [] 而不是缺键：deviceDataJSONWithSensors 会显式写出空数组
// （「缺键 => 前端走解析失败分支；空数组 => 库里确实还没有数值」，两者语义不同）。
// 而帧表**可用但 ok=false** 的那条列表路径才是「缺键」，见 latest_data_no_fallback_test.go。
func TestLatestDataSynthesisCacheMissYieldsEmptySensors(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19miss", Type: "bms_jbd", NodeID: "T19M", HardwareID: "0x03"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	// 缓存清空 => miss；SQLite 测试库没有 telemetry_frame => 帧表也拿不到。
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	// 故意写入 unified_data 行：它们在**旧实现**下会被回落查询读到（值 56），
	// 新实现必须**完全无视**它们 —— 这正是硬切换的证明。
	now := time.Now()
	rows := []models.UnifiedData{
		{DeviceID: dev.ID, SensorName: "soc", Value: 55, Unit: "%", Timestamp: now.Add(-2 * time.Minute)},
		{DeviceID: dev.ID, SensorName: "soc", Value: 56, Unit: "%", Timestamp: now.Add(-1 * time.Minute)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DeviceData{DeviceID: dev.ID, NodeID: "T19M", DataJSON: `{"raw_hex":"bb","channel_id":1,"timestamp":1}`}).Error; err != nil {
		t.Fatal(err)
	}

	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}

	var dj struct {
		Sensors []struct {
			Name  string
			Value float64
		}
	}
	if err := json.Unmarshal([]byte(env.Data.DataJSON), &dj); err != nil {
		t.Fatalf("data_json 解析失败: %v", err)
	}
	if len(dj.Sensors) != 0 {
		t.Fatalf("缓存 miss + 帧表不可用时 sensors = %d 项 —— "+
			"unified_data 回落还在（硬切换没生效）。data_json=%s", len(dj.Sensors), env.Data.DataJSON)
	}
	// 形状：必须显式写出空数组，不是缺键（缺键会让前端走「解析失败」分支）。
	if !strings.Contains(env.Data.DataJSON, `"sensors":[]`) {
		t.Fatalf("应有显式的 \"sensors\":[]：%s", env.Data.DataJSON)
	}
	t.Logf("缓存 miss + 帧表不可用 => sensors 为空数组（unified_data 的行被完全无视）")
}

// TestLatestDataSynthesisKeepsStringValues 锁 StringValue 不丢。
//
// ⚠ 这是本改造**唯一真正有数据丢失风险**的地方：unified_data 与 telemetry_frame
// 都没有 StringValue 列，device_data 是 hardware_version("V19") 的唯一落库位置。
// 只删 sensors 不保留 str_values ⇒ BMS 硬件版本永久消失且无副本可恢复。
func TestLatestDataSynthesisKeepsStringValues(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19sv", Type: "bms_jbd", NodeID: "T19S", HardwareID: "0x04"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	// hardware_version 在 unified_data 里 Value 恒为 0（驱动就是这样产出的）。
	if err := db.Create(&models.UnifiedData{DeviceID: dev.ID, SensorName: "hardware_version", Value: 0, Unit: "", Timestamp: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.DeviceData{
		DeviceID: dev.ID, NodeID: "T19S",
		DataJSON: `{"raw_hex":"cc","channel_id":1,"timestamp":1,"str_values":{"hardware_version":"V19"}}`,
	}).Error; err != nil {
		t.Fatal(err)
	}

	var env struct {
		Data struct {
			DataJSON string `json:"data_json"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/latest-data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	var dj struct {
		Sensors []struct {
			Name        string
			Value       float64
			StringValue string
		}
	}
	if err := json.Unmarshal([]byte(env.Data.DataJSON), &dj); err != nil {
		t.Fatalf("data_json 解析失败: %v", err)
	}
	if len(dj.Sensors) != 1 {
		t.Fatalf("sensors 数量 = %d, want 1: %s", len(dj.Sensors), env.Data.DataJSON)
	}
	if dj.Sensors[0].StringValue != "V19" {
		t.Fatalf("StringValue = %q, want \"V19\" —— 字符串读数丢失会让前端把硬件版本显示成 0: %s",
			dj.Sensors[0].StringValue, env.Data.DataJSON)
	}
}

// ==================== 分页列表 /edge-devices/:id/data ====================

// TestEdgeDeviceDataListSynthesizesSensorsAsOfRowTime 锁列表的 as-of 口径。
//
// 为什么单独测：列表与 /latest-data 的合成公式**不同**（as-of(T=行时刻) vs 取当前最新）。
// 若有人把两处"统一"成同一个函数，这条会红 —— 那正是应该红的时候：
// 给历史行配当前读数是把历史伪造成现状。
func TestEdgeDeviceDataListSynthesizesSensorsAsOfRowTime(t *testing.T) {
	t.Skip("被测机制已删除：as-of 基线合成（2026-11-08 硬切换）。PG 侧由 TestDeviceDataSensors_FromFramesProducesExpectedFields 覆盖")
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19list", Type: "bms_jbd", NodeID: "T19L2", HardwareID: "0x05"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	resetLatestValueCacheForTest()
	t.Cleanup(resetLatestValueCacheForTest)

	base := time.Now().Add(-1 * time.Hour)
	// T1 时刻: soc=10；T2 时刻: soc=20 且 power=100（两个字段）；
	// T3 时刻: 只有 soc=30（模拟 L1 收窄 —— 该帧只有 soc 变化）。
	t1, t2, t3 := base, base.Add(10*time.Minute), base.Add(20*time.Minute)
	rows := []models.UnifiedData{
		{DeviceID: dev.ID, SensorName: "soc", Value: 10, Unit: "%", Timestamp: t1},
		{DeviceID: dev.ID, SensorName: "soc", Value: 20, Unit: "%", Timestamp: t2},
		{DeviceID: dev.ID, SensorName: "power", Value: 100, Unit: "W", Timestamp: t2},
		{DeviceID: dev.ID, SensorName: "soc", Value: 30, Unit: "%", Timestamp: t3},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	// 三行 device_data，都是停写 sensors 后的新形状。
	for _, ts := range []time.Time{t1, t2, t3} {
		if err := db.Create(&models.DeviceData{
			DeviceID: dev.ID, NodeID: "T19L2",
			DataJSON:  `{"raw_hex":"ab","channel_id":1,"timestamp":` + strconv.FormatInt(ts.UnixMilli(), 10) + `}`,
			Timestamp: ts,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}

	var list struct {
		Data struct {
			Items []struct {
				DataJSON  string    `json:"data_json"`
				Timestamp time.Time `json:"timestamp"`
			} `json:"items"`
			Total int64 `json:"total"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/data?page=1&page_size=10", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET data = %d: %s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应解析失败: %v (%s)", err, w.Body.String())
	}
	if list.Data.Total != 3 {
		t.Fatalf("total = %d, want 3（fixture 没喂对，后面的断言会假通过）", list.Data.Total)
	}

	type snap map[string]float64
	got := map[time.Time]snap{}
	for _, it := range list.Data.Items {
		var dj struct {
			Sensors []struct {
				Name  string
				Value float64
			}
		}
		if err := json.Unmarshal([]byte(it.DataJSON), &dj); err != nil {
			t.Fatalf("data_json 解析失败: %v (%s)", err, it.DataJSON)
		}
		if len(dj.Sensors) == 0 {
			t.Fatalf("时刻 %v 的行没有合成出 sensors: %s", it.Timestamp, it.DataJSON)
		}
		s := snap{}
		for _, sv := range dj.Sensors {
			s[sv.Name] = sv.Value
		}
		got[it.Timestamp.UTC()] = s
	}

	// T1: 只有 soc=10（power 还没出现过 —— as-of 不得凭空引入未来的值）
	if s := got[t1.UTC()]; s["soc"] != 10 || len(s) != 1 {
		t.Fatalf("T1 快照 = %v, want {soc:10}（不得包含 T2 才出现的 power）", s)
	}
	// T2: soc=20 + power=100
	if s := got[t2.UTC()]; s["soc"] != 20 || s["power"] != 100 {
		t.Fatalf("T2 快照 = %v, want {soc:20, power:100}", s)
	}
	// T3: 该帧只有 soc 变化，但 as-of 必须仍带出 power=100（否则面板字段"消失"）
	if s := got[t3.UTC()]; s["soc"] != 30 || s["power"] != 100 {
		t.Fatalf("T3 快照 = %v, want {soc:30, power:100}（as-of 必须继承上一帧的 power）", s)
	}
	// 反向对照：T1 的行绝不能显示 T3 的 soc=30（那是"取当前最新"的错误口径）
	if s := got[t1.UTC()]; s["soc"] == 30 {
		t.Fatalf("T1 行显示了 soc=30 —— 用成了「取当前最新」口径，把历史伪造成现状")
	}
}

// TestEdgeDeviceDataListKeepsLegacyRowsUntouched 是列表路径的存量兼容反向对照。
func TestEdgeDeviceDataListKeepsLegacyRowsUntouched(t *testing.T) {
	r, db := setupTestRouter(t)
	dev := models.EdgeDevice{Name: "t19ll", Type: "bms_jbd", NodeID: "T19LL", HardwareID: "0x06"}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatal(err)
	}
	const legacy = `{"raw_hex":"aa","channel_id":1,"sensors":[{"Name":"soc","Value":7,"Unit":"%","StringValue":""}],"timestamp":1}`
	if err := db.Create(&models.DeviceData{DeviceID: dev.ID, NodeID: "T19LL", DataJSON: legacy, Timestamp: time.Now()}).Error; err != nil {
		t.Fatal(err)
	}
	var list struct {
		Data struct {
			Items []struct {
				DataJSON string `json:"data_json"`
			} `json:"items"`
		} `json:"data"`
	}
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/"+strconv.FormatUint(uint64(dev.ID), 10)+"/data", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("响应解析失败: %v", err)
	}
	if len(list.Data.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(list.Data.Items))
	}
	if list.Data.Items[0].DataJSON != legacy {
		t.Fatalf("存量行被改写了:\n got=%s\nwant=%s", list.Data.Items[0].DataJSON, legacy)
	}
}

// 原 TestEdgeDeviceDataListBaselineLookbackCoversAllSensors 已删除（2026-11-08）。
//
// 它锁的是「as-of 基线 2h 回看窗口能覆盖 90 分钟才写一次的物理量」——
// 而那个机制（latestSensorViewsAt + 2h listBaselineLookback）已在硬切换中整段删除。
// 被测对象不存在了，用例不能保留：它只会变成一条「因为走不到那段代码而恒绿」的假绿。
//
// 这条保证在新实现里的对应物：帧表的 frameSnapshot 是**累积 as-of 状态**
// （见 device_data_sensors_frames.go 文件头），每个快照都带该时刻的全部已知字段，
// 因此不存在「窗口太短漏掉慢字段」这个问题 —— 它不需要经验窗口。
// 该性质由 device_data_sensors_frames_test.go 的用例覆盖。
