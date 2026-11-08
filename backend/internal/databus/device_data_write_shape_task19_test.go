package databus

// task-19 写路径锁: device_data 不再存 sensors。
//
// 背景（详见 internal/api/device_data_sensors.go 文件头）: sensors 与 unified_data
// 存的是同一份 (name,value,unit)，占 device_data 单行 83%（1,251 B 里的 ~1,038 B）。
// 改成读时合成后，写侧必须**真的**不再写它 —— 否则省的是 0 字节，还多了一层合成逻辑。
//
// 三条断言各自防一种"看起来改了其实没改"的假绿:
//   1. 无 sensors 键      → 防"只改了注释/名字"               （正向）
//   2. raw_hex 仍在       → 防"顺手把 device_data 也删了"       （反向）
//   3. str_values 仍在    → 防"连 StringValue 一起丢掉"         （反向，唯一有数据丢失风险的）

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/parser"
)

// TestTask19_ParsedDeviceDataHasNoSensors 是**正向**断言：新形状里没有 sensors。
func TestTask19_ParsedDeviceDataHasNoSensors(t *testing.T) {
	fields := []parser.Field{
		{Name: "total_voltage", Value: 54.12, Unit: "V"},
		{Name: "soc", Value: 87, Unit: "%"},
		{Name: "current", Value: -12.5, Unit: "A"},
	}
	b, err := marshalParsedDeviceData([]byte{0xdd, 0x05}, 1, time.UnixMilli(1791338725971), fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(b)
	t.Logf("device_data.data_json = %s (%d B)", got, len(b))

	if strings.Contains(got, `"sensors"`) {
		t.Fatalf("data_json 仍含 sensors —— 写路径没改到位: %s", got)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (%s)", err, got)
	}
	// 反向对照 1: raw_hex 必须还在（device_data 独有的东西，砍掉它就丢取证能力）。
	if m["raw_hex"] != "dd05" {
		t.Fatalf("raw_hex = %v, want \"dd05\"（device_data 唯一的独有内容，不得丢）", m["raw_hex"])
	}
	// 反向对照 2: 元数据必须还在。
	if m["channel_id"] != float64(1) {
		t.Fatalf("channel_id = %v, want 1", m["channel_id"])
	}
	if m["timestamp"] != float64(1791338725971) {
		t.Fatalf("timestamp = %v, want 1791338725971", m["timestamp"])
	}
	// 反向对照 3: 没有字符串读数时**不得**多出 str_values 键（否则每帧白涨 ~15 B）。
	if _, has := m["str_values"]; has {
		t.Fatalf("无非空 StringValue 却写了 str_values: %s", got)
	}
	// 量化: 改造前 3 个物理量的 sensors 约 150 B；新形状应当只有 raw_hex + 两个元数据。
	if len(b) > 120 {
		t.Fatalf("data_json 长度 = %d B，超出预期（3 个物理量的 sensors 约 150 B，新形状应远小于此）: %s", len(b), got)
	}
}

// TestTask19_StringValuesSurviveWithoutSensors 锁住**唯一有数据丢失风险**的点。
//
// StringValue 不在 unified_data（无该列）、不在 telemetry_frame（Reading 只有 Name/Value），
// 所以 device_data.sensors 是 BMS hardware_version("V19") / serial_number 的**唯一**落库位置。
// 停写 sensors 时若不做这件事，这些字符串会**永久消失且无副本可恢复**。
func TestTask19_StringValuesSurviveWithoutSensors(t *testing.T) {
	// hardware_version 的真值就在 StringValue 里，Value 恒为 0（jiabaida_parse.go:437）。
	fields := []parser.Field{
		{Name: "hardware_version", Value: 0, Unit: "", StringValue: "V19"},
	}
	b, err := marshalParsedDeviceData([]byte{0x05}, 1, time.Now(), fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (%s)", err, b)
	}
	if strings.Contains(string(b), `"sensors"`) {
		t.Fatalf("data_json 仍含 sensors: %s", b)
	}
	sv, ok := m["str_values"].(map[string]interface{})
	if !ok {
		t.Fatalf("缺 str_values —— hardware_version \"V19\" 会永久丢失: %s", b)
	}
	if sv["hardware_version"] != "V19" {
		t.Fatalf("str_values[hardware_version] = %v, want \"V19\": %s", sv["hardware_version"], b)
	}
}

// TestTask19_StringValuesEmptyMapNeverEmitted 是上一条的**反向对照**。
//
// 若实现写成"无条件写 str_values"，空 map 也会序列化成 "str_values":{} ——
// 每帧 15 字节 × 每天 19 万帧 ≈ 2.8 MB/天，白花。
// 这条用例证明: 没有字符串读数时该键**根本不出现**（不是空对象）。
func TestTask19_StringValuesEmptyMapNeverEmitted(t *testing.T) {
	fields := []parser.Field{{Name: "soc", Value: 50, Unit: "%", StringValue: ""}}
	b, err := marshalParsedDeviceData(nil, 1, time.Now(), fields)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "str_values") {
		t.Fatalf("空 StringValue 也写了 str_values 键: %s", b)
	}
	// 空 raw 也必须能序列化（值为 ""），不能因为 nil 就 panic 或丢键。
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (%s)", err, b)
	}
	if _, has := m["raw_hex"]; !has {
		t.Fatalf("raw_hex 键缺失: %s", b)
	}
}

// TestTask19_EndToEnd_RealBusRowHasNoSensors 是**端到端**验收：走真实总线，
// 不直接调 marshalParsedDeviceData —— 后者只能证明函数本身对，证明不了
// Handle 里真的用了它（有人改回去也照样绿）。
//
// 同时量化"改造前后单行字节数"，替代生产上"连续采样 10 分钟"的验收：
// 生产采样只能看到结果，这里能同时看到**同一帧在两种形状下的字节数**，
// 降幅是可复算的，不依赖那 10 分钟的运气。
func TestTask19_EndToEnd_RealBusRowHasNoSensors(t *testing.T) {
	f := newS3Fixture(t)
	bus, barrier := s3WiredBus(t, f)

	bus.Publish(s3Sample(f))
	select {
	case <-barrier.done:
	case <-time.After(2 * time.Second):
		t.Fatal("2s 内解析者未完成 Handle")
	}

	rows := s3WaitForRows(t, f.db, 2, "可解析事件应写 2 行")
	// 找出 sensor_parser 写的那行（device_id != 0）。
	var parsed *models.DeviceData
	for i := range rows {
		if rows[i].DeviceID != 0 {
			parsed = &rows[i]
		}
	}
	if parsed == nil {
		t.Fatal("没找到 sensor_parser 写的行（device_id != 0）")
	}
	t.Logf("sensor_parser 行: %s (%d B)", parsed.DataJSON, len(parsed.DataJSON))

	// 正向：总线上真的没有 sensors 了。
	if strings.Contains(parsed.DataJSON, `"sensors"`) {
		t.Fatalf("端到端仍写 sensors —— marshalParsedDeviceData 没接到 Handle 上: %s", parsed.DataJSON)
	}
	// 反向：raw_hex 还在（device_data 唯一不可替代的内容）。
	var dj map[string]interface{}
	if err := json.Unmarshal([]byte(parsed.DataJSON), &dj); err != nil {
		t.Fatalf("data_json 不是合法 JSON: %v (%s)", err, parsed.DataJSON)
	}
	if _, has := dj["raw_hex"]; !has {
		t.Fatalf("端到端丢了 raw_hex: %s", parsed.DataJSON)
	}

	// 量化：把同一批解析结果按**改造前**的形状序列化，对比字节数。
	// 解析结果不由测试构造，而是从同一行里能推断的物理量个数算 —— 这里退一步：
	// 只要设备是 bmp280（温度/气压/湿度 3 个物理量），改造前那行就必然含 3 项 sensors。
	oldShape, _ := json.Marshal(map[string]interface{}{
		"raw_hex":    dj["raw_hex"],
		"sensors":    []map[string]interface{}{{"Name": "temperature", "Value": 25.3, "Unit": "C", "StringValue": ""}, {"Name": "pressure", "Value": 101.3, "Unit": "kPa", "StringValue": ""}, {"Name": "humidity", "Value": 62.1, "Unit": "%", "StringValue": ""}},
		"channel_id": dj["channel_id"],
		"timestamp":  dj["timestamp"],
	})
	t.Logf("同一帧改造前形状: %d B；改造后: %d B（降 %.0f%%）",
		len(oldShape), len(parsed.DataJSON),
		100*float64(len(oldShape)-len(parsed.DataJSON))/float64(len(oldShape)))
	if len(parsed.DataJSON) >= len(oldShape) {
		t.Fatalf("改造后并不更小: 新 %d B, 旧 %d B", len(parsed.DataJSON), len(oldShape))
	}
}

// TestTask19_SustainedWriteNoSensors 是生产「连续采样 10 分钟」验收的**确定性替代**。
//
// 生产上那条验收是"停写后连续采样 10 分钟确认 JSON 里不再有 sensors 且写入量降 ≥70%"。
// 这里不能连生产库（任务明确禁止），所以做一个**同参数**的等价测量：
// 按 5s 一帧（jiabaida read_basic_info 的默认 interval_ms=5000）灌 120 帧 = 600 秒，
// 全部走真实总线（不直接调 marshalParsedDeviceData —— 直接调证明不了 Handle 用了它）。
//
// 这比在生产上"看 10 分钟"更强的地方：这里能同时算出**同一批帧在改造前的字节数**，
// 所以降幅是算出来的、可复算的，而不是靠那 10 分钟恰好没遇到字符串帧。
func TestTask19_SustainedWriteNoSensors(t *testing.T) {
	f := newS3Fixture(t)
	bus, _ := s3WiredBus(t, f)

	// ⚠ 必须**逐帧等收敛**，不能一口气 publish 120 条：
	// DataEventBus.Publish 是有界队列 + **满即丢**（bus.go:85-104，满时 recordDrop 并
	// 丢弃旧事件）。一口气灌 120 条会丢一大半 —— 那不是被测代码的问题，是总线
	// 反压设计的正常表现（生产上 5s 一帧永远不会触发）。实测一口气 publish 只落下 130 行，
	// 而期望 240 行 —— 这条用例的第一版就是这么"失败"的，属于测试自身的错。
	const frames = 120
	var rows []models.DeviceData
	for i := 0; i < frames; i++ {
		bus.Publish(s3Sample(f))
		want := (i + 1) * 2
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			rows = deviceDataRows(t, f.db)
			if len(rows) >= want {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if len(rows) != want {
			t.Fatalf("第 %d 帧后应有 %d 行，实际 %d 行（10s 未收敛）", i+1, want, len(rows))
		}
	}

	// 停写后没有任何一行含 sensors（含 db_persist 行 —— 它本来就没有）。
	var parsedRows []models.DeviceData
	for _, r := range rows {
		if r.DeviceID != 0 {
			parsedRows = append(parsedRows, r)
		}
		if strings.Contains(r.DataJSON, `"sensors"`) {
			t.Fatalf("行 id=%d 仍含 sensors: %s", r.ID, r.DataJSON)
		}
	}
	if len(parsedRows) != frames {
		t.Fatalf("sensor_parser 行数 = %d, want %d", len(parsedRows), frames)
	}

	newBytes := 0
	for _, r := range parsedRows {
		newBytes += len(r.DataJSON)
	}
	// 改造前同一批帧的字节数：用同一行里的 raw_hex/channel_id/timestamp 重建旧形状，
	// sensors 用 bmp280 的真实 3 个物理量（温度/气压/湿度）。
	oldBytes := 0
	for _, r := range parsedRows {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(r.DataJSON), &m); err != nil {
			t.Fatalf("data_json 非法: %v (%s)", err, r.DataJSON)
		}
		old, _ := json.Marshal(map[string]interface{}{
			"raw_hex":    m["raw_hex"],
			"sensors":    []map[string]interface{}{{"Name": "temperature", "Value": 25.3, "Unit": "C", "StringValue": ""}, {"Name": "pressure", "Value": 101.3, "Unit": "kPa", "StringValue": ""}, {"Name": "humidity", "Value": 62.1, "Unit": "%", "StringValue": ""}},
			"channel_id": m["channel_id"],
			"timestamp":  m["timestamp"],
		})
		oldBytes += len(old)
	}
	drop := 100 * float64(oldBytes-newBytes) / float64(oldBytes)
	t.Logf("=== 连续 %d 帧（600s @5s）测量 ===", frames)
	t.Logf("  改造前: %d B (%.1f B/帧)", oldBytes, float64(oldBytes)/frames)
	t.Logf("  改造后: %d B (%.1f B/帧)", newBytes, float64(newBytes)/frames)
	t.Logf("  降幅  : %.1f%%   (验收线 ≥70%%)", drop)

	// 验收线（Lead 定的 ≥70%）。bmp280 只有 3 个物理量，是**最不利**的情形：
	// 字段越多降幅越大（29 字段实测 93.6%）。所以 3 字段过线 ⇒ 其余帧型都过线。
	if drop < 70 {
		t.Fatalf("降幅 %.1f%% < 70%%（3 字段帧是最不利情形，其余帧型只会更高）", drop)
	}
}
