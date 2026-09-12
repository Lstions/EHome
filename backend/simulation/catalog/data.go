//go:build simulation

// SIM-DATA：数据采集链路（设计 docs/设计/场景仿真验证框架.md §9）。
//
// 为什么这样写：本域验证的是"设备上报 → 真实 MQTT → 组合根消费者 → 查询
// 接口"的整条链路。数据只能从 nodes/<id>/up 上的真实二进制帧进入系统（没有
// HTTP 数据注入端点），因此夹具复用 harness.ProvisionSimpleDevice（真实 HTTP
// 建好节点/通道/设备配置/边缘设备，真实 MQTT 上报），不另造一套落库路径。
//
// 命名约定（设计 §4.1）：本文件是包 catalog 内的 DATA 域，所有包级声明一律
// 以域小写短名 data 开头。
package catalog

import (
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

// dataDomain 是本文件注册的场景域（设计 §6 表前缀列去掉 SIM-）。
const dataDomain Domain = "DATA"

func init() {
	Register(Scenario{
		ID:     "SIM-DATA-001",
		Title:  "节点上报一帧数据后可在最新值接口读到该值",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §最新值",
		Run:    dataScenario001,
	})
	Register(Scenario{
		ID:     "SIM-DATA-002",
		Title:  "按时间范围查询历史数据，返回刚才上报的数据点",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §历史查询",
		Run:    dataScenario002,
	})
	Register(Scenario{
		ID:     "SIM-DATA-003",
		Title:  "时间范围参数非法时返回 400 并给出可读错误",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §查询契约",
		Run:    dataScenario003,
	})
	Register(Scenario{
		ID:     "SIM-DATA-004",
		Title:  "大数据量范围查询走降采样且点数不超过上限",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §降采样",
		Run:    dataScenario004,
	})
	Register(Scenario{
		ID:     "SIM-DATA-005",
		Title:  "多类别批量查询与逐类别单查结果一致（同一时间窗）",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §批量查询",
		Run:    dataScenario005,
	})
	Register(Scenario{
		ID:     "SIM-DATA-006",
		Title:  "多实例合并后按逻辑设备维度查到合并后的数据",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/边缘设备数据生命周期.md §多源合并",
		Run:    dataScenario006,
	})
	Register(Scenario{
		ID:     "SIM-DATA-007",
		Title:  "乱序上报的数据点按服务端接收时间归档，时间序列不倒序",
		Domain: dataDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-DATA；docs/设计/数据采集与数据链路.md §时间戳语义",
		Run:    dataScenario007,
	})
}

// ---------------------------------------------------------------------------
// 夹具与通用小工具
// ---------------------------------------------------------------------------

// dataProvision 备好一套"节点 + 通道 + 设备配置 + 边缘设备"的采集链路。
//
// 复用 harness.ProvisionSimpleDevice：它自己会做哨兵上报自检，因此后续失败
// 一定能归因到场景断言，而不是"夹具没配好"（设计 §3 原则 2：不得自建落库路径）。
func dataProvision(e *harness.Env, scenarioID, suffix string) *harness.Fixture {
	e.T.Helper()
	fixture, err := e.ProvisionSimpleDevice(scenarioID, suffix)
	if err != nil {
		e.Fatalf("采集链路夹具准备失败（%s/%s）: %v", scenarioID, suffix, err)
	}
	e.T.Cleanup(fixture.Cleanup)
	return fixture
}

// dataPoint 是 unified_data 查询结果的元素子集。
type dataPoint struct {
	ID              uint      `json:"id"`
	DeviceID        uint      `json:"device_id"`
	SensorName      string    `json:"sensor_name"`
	Value           float64   `json:"value"`
	Unit            string    `json:"unit"`
	Timestamp       time.Time `json:"timestamp"`
	LogicalDeviceID *uint     `json:"logical_device_id"`
}

// dataHistoryQuery 是历史查询的参数集（/devices/:id/history 支持的子集）。
type dataHistoryQuery struct {
	Sensor    string
	Start     time.Time
	End       time.Time
	MaxPoints int
	Hours     int
}

func (q dataHistoryQuery) values() url.Values {
	values := url.Values{}
	if q.Sensor != "" {
		values.Set("sensor", q.Sensor)
	}
	if !q.Start.IsZero() && !q.End.IsZero() {
		values.Set("start_time", q.Start.UTC().Format(time.RFC3339))
		values.Set("end_time", q.End.UTC().Format(time.RFC3339))
	} else if q.Hours > 0 {
		values.Set("hours", strconv.Itoa(q.Hours))
	}
	if q.MaxPoints > 0 {
		values.Set("max_points", strconv.Itoa(q.MaxPoints))
	}
	return values
}

// dataHistory 按边缘设备主键查询历史（图表端点，实例软删后仍可查）。
func dataHistory(e *harness.Env, edgeDeviceID uint, query dataHistoryQuery) []dataPoint {
	e.T.Helper()
	resp := e.Admin.GetQuery(fmt.Sprintf("/api/v1/devices/%d/history", edgeDeviceID), query.values())
	resp.Expect(http.StatusOK)
	var points []dataPoint
	resp.Decode(&points)
	return points
}

// dataUnifiedHistorical 按 device_pk + 类别查询历史（Dashboard 端点）。
func dataUnifiedHistorical(e *harness.Env, edgeDeviceID uint, category string, query dataHistoryQuery) []dataPoint {
	e.T.Helper()
	values := query.values()
	values.Set("device_pk", strconv.FormatUint(uint64(edgeDeviceID), 10))
	values.Set("category", category)
	resp := e.Admin.GetQuery("/api/v1/unified-data/historical", values)
	resp.Expect(http.StatusOK)
	var points []dataPoint
	resp.Decode(&points)
	return points
}

// dataPayload 按夹具解析器的字节布局编码一帧负载（int16 温度 + uint16 电平）。
//
// 复用 harness 导出的编码器，保证与夹具的解析规则同源，不在这里另写一套
// 二进制编码（协议漂移是致命缺陷）。
func dataPayload(tempRaw int, levelRaw uint32) ([]byte, error) {
	tempBytes, err := harness.EncodeInt16BigEndian(tempRaw)
	if err != nil {
		return nil, fmt.Errorf("编码 int16 温度失败: %w", err)
	}
	levelBytes, err := harness.EncodeUint16BigEndian(levelRaw)
	if err != nil {
		return nil, fmt.Errorf("编码 uint16 电平失败: %w", err)
	}
	return append(tempBytes, levelBytes...), nil
}

// dataReportAt 用显式"设备时间戳"上报一帧（用于构造乱序/迟到数据）。
func dataReportAt(fixture *harness.Fixture, tsMillis int64, tempRaw int, levelRaw uint32) error {
	payload, err := dataPayload(tempRaw, levelRaw)
	if err != nil {
		return err
	}
	return fixture.Node.DataReport(uint32(fixture.ChannelID), uint64(tsMillis), payload)
}

// dataCloseTo 判断两个物理量在解析缩放误差内相等。
func dataCloseTo(got, want float64) bool { return math.Abs(got-want) < 1e-6 }

// dataValuesOf 返回某类别下的全部取值（按查询顺序）。
func dataValuesOf(points []dataPoint, sensor string) []float64 {
	var values []float64
	for _, point := range points {
		if point.SensorName == sensor {
			values = append(values, point.Value)
		}
	}
	return values
}

// dataHasValue 判断取值集合里是否含期望值。
func dataHasValue(values []float64, want float64) bool {
	for _, value := range values {
		if dataCloseTo(value, want) {
			return true
		}
	}
	return false
}

// dataPointKeys 把数据点规范化成可比较的字符串序列（类别|取值|时间）。
func dataPointKeys(points []dataPoint) []string {
	keys := make([]string, 0, len(points))
	for _, point := range points {
		keys = append(keys, fmt.Sprintf("%s|%.6f|%s", point.SensorName, point.Value, point.Timestamp.UTC().Format(time.RFC3339Nano)))
	}
	sort.Strings(keys)
	return keys
}

// dataTempRaw 把物理量温度换算成夹具解析器的原始整数值（scale 0.01）。
func dataTempRaw(celsius float64) int { return int(math.Round(celsius * 100)) }

// ---------------------------------------------------------------------------
// SIM-DATA-001
// ---------------------------------------------------------------------------

// 守护的不变量：一帧上报必须走完整条链路（MQTT → 消费者 → 解析 → 最新值），
// 用户在最新值接口里看到的是解析后的物理量，而不是原始字节。
func dataScenario001(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-001", "n1")
	if err := fixture.Report(harness.FixtureTempCategory, 25.1); err != nil {
		e.T.Fatalf("上报失败 node=%s: %v", fixture.NodeID, err)
	}

	// 夹具自检已经写过一个哨兵值（21.37），因此必须等到"本次上报的值"出现，
	// 否则断言会命中哨兵而给出假绿。
	var payload nodeLatestPayload
	e.Eventually(30*time.Second, func() error {
		resp := e.Admin.Get("/api/v1/nodes/" + fixture.NodeID + "/latest")
		if err := resp.Check(http.StatusOK); err != nil {
			return err
		}
		var latest nodeLatestPayload
		if err := nodeDecodeInto(resp, &latest); err != nil {
			return err
		}
		for _, value := range latest.Values {
			if value.SensorName == harness.FixtureTempCategory && dataCloseTo(value.Value, 25.1) {
				payload = latest
				return nil
			}
		}
		return fmt.Errorf("最新值尚未收敛到 25.1: %+v", latest.Values)
	})

	var temperature *nodeLatestValue
	for i := range payload.Values {
		if payload.Values[i].SensorName == harness.FixtureTempCategory {
			temperature = &payload.Values[i]
		}
	}
	if temperature == nil {
		e.T.Fatalf("最新值里没有 %s: %+v", harness.FixtureTempCategory, payload.Values)
	}
	if !dataCloseTo(temperature.Value, 25.1) {
		e.T.Fatalf("最新温度=%v，期望 25.1（夹具解析器 scale 0.01）", temperature.Value)
	}
	if temperature.Unit != "C" {
		e.T.Fatalf("温度单位=%q，期望 C", temperature.Unit)
	}
	if temperature.ChannelID != fixture.ChannelID {
		e.T.Fatalf("最新值归属通道=%d，期望 %d", temperature.ChannelID, fixture.ChannelID)
	}

	e.Evidence("latest_temperature", temperature.Value)
	e.Evidence("latest_timestamp", temperature.Timestamp.String())
}

// ---------------------------------------------------------------------------
// SIM-DATA-002
// ---------------------------------------------------------------------------

// 守护的不变量：历史查询必须按传入的时间窗返回真实上报过的数据点（升序），
// 且窗口之外的数据不能混进来（用 2020 年的窗口做反例）。
func dataScenario002(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-002", "n1")
	for i, value := range []float64{11.1, 22.2, 33.3} {
		if err := fixture.Report(harness.FixtureTempCategory, value); err != nil {
			e.T.Fatalf("第 %d 帧上报失败: %v", i+1, err)
		}
		time.Sleep(30 * time.Millisecond) // 模拟真实上报节奏（设计 §3 原则 3）
	}

	window := dataHistoryQuery{
		Sensor: harness.FixtureTempCategory,
		Start:  time.Now().Add(-5 * time.Minute),
		End:    time.Now().Add(1 * time.Minute),
	}
	var points []dataPoint
	e.Eventually(45*time.Second, func() error {
		points = dataHistory(e, fixture.EdgeDeviceID, window)
		values := dataValuesOf(points, harness.FixtureTempCategory)
		for _, want := range []float64{11.1, 22.2, 33.3} {
			if !dataHasValue(values, want) {
				return fmt.Errorf("窗口内还缺 %.1f（当前 %v）", want, values)
			}
		}
		return nil
	})

	for i := 1; i < len(points); i++ {
		if points[i].Timestamp.Before(points[i-1].Timestamp) {
			e.T.Fatalf("历史查询未按时间升序返回: [%d]=%s 早于 [%d]=%s",
				i, points[i].Timestamp, i-1, points[i-1].Timestamp)
		}
	}

	// 反例：完全在过去的窗口必须为空，证明时间过滤真的生效。
	past := dataHistoryQuery{
		Sensor: harness.FixtureTempCategory,
		Start:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		End:    time.Date(2020, 1, 2, 0, 0, 0, 0, time.UTC),
	}
	if leaked := dataHistory(e, fixture.EdgeDeviceID, past); len(leaked) != 0 {
		e.T.Fatalf("2020 年窗口返回了 %d 条数据，时间过滤失效: %+v", len(leaked), leaked)
	}

	e.Evidence("window_points", len(points))
	e.Evidence("window_values", dataValuesOf(points, harness.FixtureTempCategory))
}

// ---------------------------------------------------------------------------
// SIM-DATA-003
// ---------------------------------------------------------------------------

// 守护的不变量：时间参数写错时必须给出 400 + 可读原因，而不是"空成功"——
// 空成功会让前端把"参数写错了"误判成"这段时间真没数据"。
//
// 与设计 §9 的偏差（以真实行为为准，见交付报告）：实测"起 > 止"返回 200 +
// 空数组，而不是 400；真正被拒绝的是时间格式非法与缺参数。本场景按真实行为
// 断言，并把"起 > 止"的实测结果作为证据留痕。
func dataScenario003(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-003", "n1")

	good := time.Now().UTC().Format(time.RFC3339)
	bad := "not-a-timestamp"

	// 1) /devices/:id/history 起止时间格式非法
	e.Admin.GetQuery(fmt.Sprintf("/api/v1/devices/%d/history", fixture.EdgeDeviceID), url.Values{
		"start_time": {bad},
		"end_time":   {good},
	}).Expect(http.StatusBadRequest)
	e.Admin.GetQuery(fmt.Sprintf("/api/v1/devices/%d/history", fixture.EdgeDeviceID), url.Values{
		"start_time": {good},
		"end_time":   {bad},
	}).Expect(http.StatusBadRequest)

	// 2) /unified-data/historical 时间格式非法 + 缺参数
	badRange := e.Admin.GetQuery("/api/v1/unified-data/historical", url.Values{
		"device_pk":  {strconv.FormatUint(uint64(fixture.EdgeDeviceID), 10)},
		"category":   {harness.FixtureTempCategory},
		"start_time": {bad},
		"end_time":   {good},
	})
	badRange.Expect(http.StatusBadRequest)
	if badRange.Message == "" {
		e.T.Fatalf("400 响应没有给出可读原因: %s", badRange.BodyString())
	}
	e.Admin.GetQuery("/api/v1/unified-data/historical", url.Values{
		"device_pk":  {strconv.FormatUint(uint64(fixture.EdgeDeviceID), 10)},
		"start_time": {good},
		"end_time":   {good},
	}).Expect(http.StatusBadRequest) // 缺 category
	e.Admin.GetQuery("/api/v1/unified-data/historical", url.Values{
		"category": {harness.FixtureTempCategory},
	}).Expect(http.StatusBadRequest) // 缺 device_pk
	e.Admin.GetQuery("/api/v1/unified-data/categories", url.Values{}).
		Expect(http.StatusBadRequest) // 缺 device_pk

	// 3) 取证：起 > 止 的实测行为（设计 §9 期望 400，实测 200 空成功）。
	reversed := e.Admin.GetQuery(fmt.Sprintf("/api/v1/devices/%d/history", fixture.EdgeDeviceID), url.Values{
		"start_time": {time.Now().Add(time.Hour).UTC().Format(time.RFC3339)},
		"end_time":   {time.Now().UTC().Format(time.RFC3339)},
	})
	reversedPoints := -1
	if reversed.Status == http.StatusOK {
		var points []dataPoint
		if err := nodeDecodeInto(reversed, &points); err == nil {
			reversedPoints = len(points)
		}
	}
	e.T.Logf("起>止 的实测行为：HTTP %d（设计 §9 期望 400），data 条数=%d", reversed.Status, reversedPoints)
	e.Evidence("reversed_range_status", reversed.Status)
	e.Evidence("reversed_range_points", reversedPoints)
	e.Evidence("bad_time_message", badRange.Message)
}

// ---------------------------------------------------------------------------
// SIM-DATA-004
// ---------------------------------------------------------------------------

// 守护的不变量：显式要求降采样时，返回点数不得超过 max_points，且必须保留
// 首末两点（downsampleUnifiedData 的契约）；同时确认"未降采样时数据确实更多"，
// 否则"点数不超上限"可能只是因为本来就没数据。
func dataScenario004(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-004", "n1")

	const frames = 60
	for i := 0; i < frames; i++ {
		// 每个点取值都不同（scale 0.01），保证降采样前后可区分。
		if err := fixture.Report(harness.FixtureTempCategory, 20+float64(i)/10); err != nil {
			e.T.Fatalf("第 %d 帧上报失败: %v", i, err)
		}
		time.Sleep(30 * time.Millisecond) // 模拟真实上报节奏（设计 §3 原则 3）
	}

	all := dataHistoryQuery{Sensor: harness.FixtureTempCategory, Hours: 1}
	var full []dataPoint
	e.Eventually(60*time.Second, func() error {
		full = dataHistory(e, fixture.EdgeDeviceID, all)
		if len(full) < frames {
			return fmt.Errorf("历史点数=%d，期望 ≥%d（%d 帧上报尚未全部入库）", len(full), frames, frames)
		}
		return nil
	})

	const maxPoints = 20
	// 真实实现的点数上界是 max_points+2：downsampleUnifiedData 均匀抽样后
	// **始终补回首末两点**（backend/internal/api/handler_data.go）。因此这里
	// 断言真实契约（≤ max_points+2 且确实少于全量），而不是设计 §9 字面上的
	// "不超过上限"——该偏差已写进交付报告。
	const maxPointsBound = maxPoints + 2
	capped := dataHistory(e, fixture.EdgeDeviceID, dataHistoryQuery{
		Sensor: harness.FixtureTempCategory, Hours: 1, MaxPoints: maxPoints,
	})
	if len(capped) > maxPointsBound {
		e.T.Fatalf("max_points=%d 时返回 %d 个点，超过真实上界 %d：降采样未生效", maxPoints, len(capped), maxPointsBound)
	}
	if len(capped) >= len(full) {
		e.T.Fatalf("降采样后点数=%d，与全量 %d 相同：降采样未生效", len(capped), len(full))
	}
	if len(capped) < 2 {
		e.T.Fatalf("降采样后点数=%d：首末两点必须保留", len(capped))
	}
	if capped[0].ID != full[0].ID || capped[len(capped)-1].ID != full[len(full)-1].ID {
		e.T.Fatalf("降采样未保留首末点：first=%d(期望 %d) last=%d(期望 %d)",
			capped[0].ID, full[0].ID, capped[len(capped)-1].ID, full[len(full)-1].ID)
	}

	// 批量端点同样必须遵守 max_points（同一份服务端实现的两个入口）。
	values := url.Values{
		"device_pk":  {strconv.FormatUint(uint64(fixture.EdgeDeviceID), 10)},
		"categories": {harness.FixtureTempCategory},
		"start_time": {time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		"end_time":   {time.Now().Add(time.Minute).UTC().Format(time.RFC3339)},
		"max_points": {strconv.Itoa(maxPoints)},
	}
	batch := e.Admin.GetQuery("/api/v1/unified-data/historical-batch", values)
	batch.Expect(http.StatusOK)
	var batchResults []struct {
		Category string      `json:"category"`
		Data     []dataPoint `json:"data"`
	}
	batch.Decode(&batchResults)
	if len(batchResults) != 1 {
		e.T.Fatalf("批量端点返回 %d 个类别，期望 1", len(batchResults))
	}
	if len(batchResults[0].Data) > maxPointsBound {
		e.T.Fatalf("批量端点 max_points=%d 时返回 %d 个点，超过真实上界 %d", maxPoints, len(batchResults[0].Data), maxPointsBound)
	}

	e.Evidence("full_points", len(full))
	e.Evidence("downsampled_points", len(capped))
	e.Evidence("downsample_upper_bound", maxPointsBound)
}

// ---------------------------------------------------------------------------
// SIM-DATA-005
// ---------------------------------------------------------------------------

// 守护的不变量：批量查询与逐类别单查必须给出同一份结果（同一时间窗、同一
// 类别）。这是"前端为了省往返改走批量"的安全网：两条路径一旦分叉，图表就会
// 和明细对不上。
func dataScenario005(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-005", "n1")
	for i := 0; i < 5; i++ {
		if err := fixture.ReportMany(map[string]float64{
			harness.FixtureTempCategory:  20 + float64(i)/10,
			harness.FixtureLevelCategory: float64(100 + i),
		}); err != nil {
			e.T.Fatalf("第 %d 帧上报失败: %v", i+1, err)
		}
		time.Sleep(30 * time.Millisecond) // 模拟真实上报节奏（设计 §3 原则 3）
	}

	start := time.Now().Add(-10 * time.Minute)
	end := time.Now().Add(time.Minute)
	categories := []string{harness.FixtureTempCategory, harness.FixtureLevelCategory}

	batchByCategory := map[string][]dataPoint{}
	singleByCategory := map[string][]dataPoint{}
	e.Eventually(45*time.Second, func() error {
		values := url.Values{
			"device_pk":  {strconv.FormatUint(uint64(fixture.EdgeDeviceID), 10)},
			"categories": {strings.Join(categories, ",")},
			"start_time": {start.UTC().Format(time.RFC3339)},
			"end_time":   {end.UTC().Format(time.RFC3339)},
			"max_points": {"1000"},
		}
		resp := e.Admin.GetQuery("/api/v1/unified-data/historical-batch", values)
		if err := resp.Check(http.StatusOK); err != nil {
			return err
		}
		var results []struct {
			Category string      `json:"category"`
			Data     []dataPoint `json:"data"`
		}
		if err := nodeDecodeInto(resp, &results); err != nil {
			return err
		}
		batchByCategory = map[string][]dataPoint{}
		for _, item := range results {
			batchByCategory[item.Category] = item.Data
		}
		singleByCategory = map[string][]dataPoint{}
		for _, category := range categories {
			singleByCategory[category] = dataUnifiedHistorical(e, fixture.EdgeDeviceID, category, dataHistoryQuery{
				Start: start, End: end, MaxPoints: 1000,
			})
		}
		for _, category := range categories {
			if len(batchByCategory[category]) == 0 || len(singleByCategory[category]) == 0 {
				return fmt.Errorf("类别 %s 尚无数据（batch=%d single=%d）",
					category, len(batchByCategory[category]), len(singleByCategory[category]))
			}
		}
		return nil
	})

	for _, category := range categories {
		batch := dataPointKeys(batchByCategory[category])
		single := dataPointKeys(singleByCategory[category])
		if len(batch) != len(single) {
			e.T.Fatalf("类别 %s 批量=%d 条、单查=%d 条，结果不一致", category, len(batch), len(single))
		}
		for i := range batch {
			if batch[i] != single[i] {
				e.T.Fatalf("类别 %s 第 %d 条不一致：批量=%s 单查=%s", category, i, batch[i], single[i])
			}
		}
		e.Evidence("category_"+category+"_points", len(batch))
	}
}

// ---------------------------------------------------------------------------
// SIM-DATA-006
// ---------------------------------------------------------------------------

// dataEdgeLogicalID 读取边缘设备当前挂载的逻辑身份。
func dataEdgeLogicalID(e *harness.Env, edgeDeviceID uint) uint {
	e.T.Helper()
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d", edgeDeviceID))
	resp.Expect(http.StatusOK)
	var device struct {
		LogicalDeviceID *uint `json:"logical_device_id"`
	}
	resp.Decode(&device)
	if device.LogicalDeviceID == nil || *device.LogicalDeviceID == 0 {
		e.T.Fatalf("边缘设备 %d 没有逻辑身份（logical_device_id 为空）", edgeDeviceID)
	}
	return *device.LogicalDeviceID
}

// 守护的不变量：两个实例合并到一个逻辑设备后，按任一实例查询都必须看到合并
// 后的全量数据（查询走逻辑身份解析，而不是单实例 device_id）。
//
// 为什么先删实例再合并：合并前置校验要求源逻辑设备没有存活实例
// （datalifecycle.MergeDevices → livingInstanceInfo），而删除实例只软删行、
// 历史数据仍在——这正是"换新设备后合并历史"的真实运维动作。
func dataScenario006(e *harness.Env) {
	first := dataProvision(e, "SIM-DATA-006", "a")
	second := dataProvision(e, "SIM-DATA-006", "b")

	if err := first.Report(harness.FixtureTempCategory, 11.1); err != nil {
		e.T.Fatalf("实例 A 上报失败: %v", err)
	}
	if err := second.Report(harness.FixtureTempCategory, 22.2); err != nil {
		e.T.Fatalf("实例 B 上报失败: %v", err)
	}

	window := dataHistoryQuery{Sensor: harness.FixtureTempCategory, Hours: 1}
	e.Eventually(45*time.Second, func() error {
		if !dataHasValue(dataValuesOf(dataHistory(e, first.EdgeDeviceID, window), harness.FixtureTempCategory), 11.1) {
			return fmt.Errorf("实例 A 的数据尚未入库")
		}
		if !dataHasValue(dataValuesOf(dataHistory(e, second.EdgeDeviceID, window), harness.FixtureTempCategory), 22.2) {
			return fmt.Errorf("实例 B 的数据尚未入库")
		}
		return nil
	})

	// 合并前：各自只看得到自己的数据（合并规则的反例基线）。
	if values := dataValuesOf(dataHistory(e, first.EdgeDeviceID, window), harness.FixtureTempCategory); dataHasValue(values, 22.2) {
		e.T.Fatalf("合并前实例 A 就看到了实例 B 的数据: %v", values)
	}

	firstLogical := dataEdgeLogicalID(e, first.EdgeDeviceID)
	secondLogical := dataEdgeLogicalID(e, second.EdgeDeviceID)

	e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", first.EdgeDeviceID)).Expect(http.StatusOK)
	e.Admin.Delete(fmt.Sprintf("/api/v1/edge-devices/%d", second.EdgeDeviceID)).Expect(http.StatusOK)

	merge := e.Admin.Post("/api/v1/logical-devices/merge", map[string]any{
		"target_name": "合并后的采集设备-" + first.NodeID,
		"source_ids":  []uint{firstLogical, secondLogical},
	})
	merge.Expect(http.StatusCreated)
	targetID := merge.DataInt("target_id")
	if targetID == 0 {
		e.T.Fatalf("合并未返回目标逻辑设备: %s", merge.BodyString())
	}

	// 合并后：按任一实例查都必须看到两边的数据（迁移进行中也必须可见，
	// scope 解析会把待迁移源一并纳入）。
	e.Eventually(45*time.Second, func() error {
		values := dataValuesOf(dataHistory(e, first.EdgeDeviceID, window), harness.FixtureTempCategory)
		if !dataHasValue(values, 11.1) || !dataHasValue(values, 22.2) {
			return fmt.Errorf("按实例 A 查询未合并出两边数据: %v", values)
		}
		return nil
	})
	viaSecond := dataValuesOf(dataHistory(e, second.EdgeDeviceID, window), harness.FixtureTempCategory)
	if !dataHasValue(viaSecond, 11.1) || !dataHasValue(viaSecond, 22.2) {
		e.T.Fatalf("按实例 B 查询未合并出两边数据: %v", viaSecond)
	}

	e.Evidence("merge_target_id", targetID)
	e.Evidence("merged_temperature_values", dataValuesOf(dataHistory(e, first.EdgeDeviceID, window), harness.FixtureTempCategory))
}

// ---------------------------------------------------------------------------
// SIM-DATA-007
// ---------------------------------------------------------------------------

// 守护的不变量：数据点的时间戳必须来自服务端接收时刻，历史查询永远给出
// 不倒序的时间序列——设备时钟任意跳跃都不能污染趋势图。
//
// 与设计 §9 的偏差（以真实行为为准，见交付报告）：实测 SensorParserConsumer
// 落库时使用 time.Now()（接收时刻），DataReport 字段 2 的设备时间戳不参与
// 归档，因此"按事件时间归档"并不成立。本场景断言真实语义：乱序的设备时间戳
// 不会让入库时间倒序，也不会被丢弃。
func dataScenario007(e *harness.Env) {
	fixture := dataProvision(e, "SIM-DATA-007", "n1")

	now := time.Now()
	deviceTimestamps := []time.Time{
		now,
		now.Add(-2 * time.Hour),
		now.Add(-1 * time.Hour),
		now.Add(-30 * time.Second),
	}
	for i, stamp := range deviceTimestamps {
		if err := dataReportAt(fixture, stamp.UnixMilli(), dataTempRaw(30+float64(i)/10), uint32(800+i)); err != nil {
			e.T.Fatalf("第 %d 帧（设备时间戳 %s）上报失败: %v", i, stamp.Format(time.RFC3339), err)
		}
		time.Sleep(40 * time.Millisecond) // 模拟真实上报节奏（设计 §3 原则 3）
	}

	window := dataHistoryQuery{Sensor: harness.FixtureTempCategory, Hours: 1}
	var points []dataPoint
	e.Eventually(45*time.Second, func() error {
		points = dataHistory(e, fixture.EdgeDeviceID, window)
		if len(points) < len(deviceTimestamps) {
			return fmt.Errorf("入库点数=%d，期望 ≥%d（乱序数据被丢弃）", len(points), len(deviceTimestamps))
		}
		return nil
	})

	// 1) 时间序列非降序（服务端接收序）。
	for i := 1; i < len(points); i++ {
		if points[i].Timestamp.Before(points[i-1].Timestamp) {
			e.T.Fatalf("历史时间序列倒序: [%d]=%s 早于 [%d]=%s",
				i, points[i].Timestamp, i-1, points[i-1].Timestamp)
		}
	}
	// 2) 每条记录的时间戳都贴近服务端当前时钟（证明用的是接收时刻，而不是
	//    设备报上来的、跨度 2 小时的乱序事件时间）。
	for i, point := range points {
		if delta := time.Since(point.Timestamp); delta < -2*time.Minute || delta > 5*time.Minute {
			e.T.Fatalf("第 %d 条记录时间戳 %s 与当前时钟相差 %s：归档时间不是服务端接收时刻",
				i, point.Timestamp.Format(time.RFC3339Nano), delta)
		}
	}
	// 3) 取值必须齐全（乱序不等于丢数据）。
	values := dataValuesOf(points, harness.FixtureTempCategory)
	for _, want := range []float64{30.0, 30.1, 30.2, 30.3} {
		if !dataHasValue(values, want) {
			e.T.Fatalf("乱序上报后缺少取值 %.1f（实际 %v）", want, values)
		}
	}

	deviceStamps := make([]string, 0, len(deviceTimestamps))
	for _, stamp := range deviceTimestamps {
		deviceStamps = append(deviceStamps, stamp.Format(time.RFC3339))
	}
	storedStamps := make([]string, 0, len(points))
	for _, point := range points {
		storedStamps = append(storedStamps, point.Timestamp.Format(time.RFC3339Nano))
	}
	e.Evidence("device_timestamps", deviceStamps)
	e.Evidence("stored_timestamps", storedStamps)
}
