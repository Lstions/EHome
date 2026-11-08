package api

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 数据字段解析层 HTTP 层（设计/数据字段解析层-方案-2026-10-21.md v0.3 §8.1–§8.4）。
//
// 分层纪律（方案 §3.1 的包环裁决）：handler 放在 internal/api，领域逻辑全部在
// internal/sensorfield；本文件只做参数解析、DB 只读查询与 envelope。
//
// ⚠ 命名口径（DS-14）：本文件所有"设备 ID"一律是 **EdgeDevice.ID**
// （= unified_data.device_id 列的值）。unified_data.edge_device_id 当前全表 NULL，
// 禁止用它做任何查询（方案 §4.1 命名口径）。
//
// 响应形状严格按 §8.1–§8.4；§8.3 的请求体在方案里只写"请求同 v0.1"（v0.1 未留档），
// 本文件按 §8.3 明示的字段（window_hours / duration_sec / aggregation_trace /
// samples_considered / reduced / missing_known）还原最小请求体，不发明业务语义。

const (
	// sensorFieldsMaxFields §8.1 明示的 fields 上限（超限 truncated:true）。
	sensorFieldsMaxFields = 500
	// sensorFieldPreviewMaxWindowHours 试算窗口上限（防一次拉爆分区表）。
	sensorFieldPreviewMaxWindowHours = 24 * 14
)

// previewRequest 是 §8.3 试算请求（按方案明示字段还原的请求体）。
type previewRequest struct {
	EdgeDeviceID uint    `json:"edge_device_id"`
	Selector     string  `json:"selector"`
	Comparator   string  `json:"comparator"`
	Threshold    float64 `json:"threshold"`
	WindowHours  int     `json:"window_hours"`
	DurationSec  int     `json:"duration_sec"`
}

// previewTraceItem 是 §8.3 明示的 trace 行形状：[{field, value, valid, cmp, reason?}]。
type previewTraceItem struct {
	Field  string  `json:"field"`
	Value  float64 `json:"value"`
	Valid  bool    `json:"valid"`
	Cmp    bool    `json:"cmp"`
	Reason string  `json:"reason,omitempty"`
}

// registerSensorFieldRoutes 注册 §8.1–§8.4 四条路由。
//
// resolver 为 nil 时（如单测未注入）显式 503，与数据源域既有范式一致：
// 保持路由存在、不 nil 解引用 panic、不静默降级成"看起来能用的空壳"。
func registerSensorFieldRoutes(v1 *gin.RouterGroup, db *gorm.DB, resolver *sensorfield.Registry) {
	group := v1.Group("/sensor-fields")
	if resolver == nil {
		h := func(c *gin.Context) {
			Error(c, http.StatusServiceUnavailable, "field resolver is not available")
		}
		group.GET("", h)
		group.GET("/resolve", h)
		group.POST("/preview", h)
		group.GET("/schema", h)
		return
	}
	group.GET("", listSensorFields(db, resolver))
	group.GET("/resolve", resolveSensorField(db, resolver))
	group.POST("/preview", previewSensorField(db, resolver))
	group.GET("/schema", sensorFieldSchema(resolver))
}

// writeSensorFieldError 把领域哨兵映射为 400 + error_code（§8.6）。
func writeSensorFieldError(c *gin.Context, err error) {
	if code := sensorfield.ErrorCode(err); code != "" {
		ErrorWithCode(c, http.StatusBadRequest, code, err.Error())
		return
	}
	Error(c, http.StatusInternalServerError, "internal error")
}

// parseEdgeDeviceIDQuery 解析必填 query edge_device_id（EdgeDevice.ID）。
func parseEdgeDeviceIDQuery(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Query("edge_device_id"))
	if raw == "" {
		Error(c, http.StatusBadRequest, "edge_device_id is required")
		return 0, false
	}
	v, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || v == 0 {
		Error(c, http.StatusBadRequest, "invalid edge_device_id")
		return 0, false
	}
	return uint(v), true
}

// loadEdgeDeviceType 读取设备类型（声明归属依据）：
// DeviceConfig.DeviceType 优先，回落 EdgeDevice.Type（deprecated 兼容列）。
func loadEdgeDeviceType(db *gorm.DB, edgeDeviceID uint) (string, bool) {
	var dev models.EdgeDevice
	if err := db.First(&dev, edgeDeviceID).Error; err != nil {
		return "", false
	}
	if dev.DeviceConfigID > 0 {
		var cfg models.DeviceConfig
		if err := db.First(&cfg, dev.DeviceConfigID).Error; err == nil && strings.TrimSpace(cfg.DeviceType) != "" {
			return strings.TrimSpace(cfg.DeviceType), true
		}
	}
	return strings.TrimSpace(dev.Type), true
}

// listSensorFields 实现 §8.1 GET /api/v1/sensor-fields。
//
// 三处合并（方案 §2 "已知集合 = 声明 ∪ 观测"，meta 只提供展示元数据）：
//   - 声明：resolver.KnownFields（内部已带 Declared 标志）；
//   - 观测：sensor_field_obs 投影表（first_seen/last_seen/samples 的唯一来源，
//     也是投影滞后/冷启动的兜底，避免"注册表没加载就返回空"的假空态）；
//   - 丰富层：sensor_field_meta（label_zh/precision/status）。
//
// 关闭开关（enabled=false）时仍可用（方案 §11：kill switch 只限制特殊 selector 求值）。
func listSensorFields(db *gorm.DB, resolver *sensorfield.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID, ok := parseEdgeDeviceIDQuery(c)
		if !ok {
			return
		}
		deviceType, exists := loadEdgeDeviceType(db, deviceID)
		if !exists {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}
		// 惰性绑定 device→type：设备在启动后创建时，投影刷新前也能按声明返回。
		// SetDeviceType 幂等（同值不 bump epoch），每次 GET 调用无副作用。
		resolver.SetDeviceType(deviceID, deviceType)

		var obsRows []models.SensorFieldObs
		if err := db.Where("device_id = ?", deviceID).Order("sensor_name ASC").Find(&obsRows).Error; err != nil {
			Error(c, http.StatusInternalServerError, "failed to load field observations")
			return
		}
		obsByName := make(map[string]models.SensorFieldObs, len(obsRows))
		names := make([]string, 0, len(obsRows)+8)
		declByName := make(map[string]sensorfield.Field, 8)
		for _, row := range obsRows {
			obsByName[row.SensorName] = row
			names = append(names, row.SensorName)
		}
		for _, known := range resolver.KnownFields(deviceID) {
			if known.Declared {
				declByName[known.Name] = known
				if _, seen := obsByName[known.Name]; !seen {
					names = append(names, known.Name)
				}
			}
		}
		sort.Strings(names)

		metaByName := map[string]models.SensorFieldMeta{}
		if len(names) > 0 {
			var metas []models.SensorFieldMeta
			if err := db.Where("sensor_name IN ?", names).Find(&metas).Error; err != nil {
				Error(c, http.StatusInternalServerError, "failed to load field metadata")
				return
			}
			for _, m := range metas {
				metaByName[m.SensorName] = m
			}
		}

		includeDeprecated := parseBoolQuery(c, "include_deprecated")
		sourceFilter := strings.TrimSpace(strings.ToLower(c.Query("source")))
		items := make([]gin.H, 0, len(names))
		truncated := false
		for _, name := range names {
			meta, hasMeta := metaByName[name]
			obs, hasObs := obsByName[name]
			decl, hasDecl := declByName[name]
			if sourceFilter == "decl" && !hasDecl {
				continue
			}
			if sourceFilter == "obs" && !hasObs {
				continue
			}
			deprecated := hasMeta && meta.Status == "deprecated"
			if deprecated && !includeDeprecated {
				continue
			}
			if len(items) >= sensorFieldsMaxFields {
				truncated = true
				break
			}
			sources := make([]string, 0, 2)
			if hasDecl {
				sources = append(sources, "decl")
			}
			if hasObs {
				sources = append(sources, "obs")
			}
			semantics := resolver.SemanticsOf(name)
			if semantics == nil {
				semantics = []string{}
			}
			item := gin.H{
				"name":       name,
				"unit":       firstNonEmpty(meta.Unit, obs.Unit, decl.Unit),
				"label_zh":   meta.LabelZh,
				"value_type": firstNonEmpty(meta.ValueType, string(decl.ValueType)),
				"precision":  meta.Precision,
				"sources":    sources,
				"semantics":  semantics,
				"first_seen": nil,
				"last_seen":  nil,
				"samples":    int64(0),
				"deprecated": deprecated,
			}
			if hasObs {
				item["first_seen"] = obs.FirstSeen
				item["last_seen"] = obs.LastSeen
				item["samples"] = obs.Samples
			}
			items = append(items, item)
		}
		Success(c, gin.H{
			"edge_device_id": deviceID,
			"device_type":    deviceType,
			"fields":         items,
			"truncated":      truncated,
		})
	}
}

// resolveSensorField 实现 §8.2 GET /api/v1/sensor-fields/resolve。
//
// 200 且 status 只有 resolved/unbound；语法/未知语义/开关关闭走 400+error_code（§8.6）。
func resolveSensorField(db *gorm.DB, resolver *sensorfield.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID, ok := parseEdgeDeviceIDQuery(c)
		if !ok {
			return
		}
		selector := strings.TrimSpace(c.Query("selector"))
		if selector == "" {
			ErrorWithCode(c, http.StatusBadRequest, "empty_selector", "selector is required")
			return
		}
		if _, exists := loadEdgeDeviceType(db, deviceID); !exists {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}
		res, err := resolver.Resolve(deviceID, selector)
		if err != nil {
			writeSensorFieldError(c, err)
			return
		}
		matched := res.Matched
		if matched == nil {
			matched = []string{}
		}
		Success(c, gin.H{
			"selector":  res.Selector,
			"status":    res.Status,
			"kind":      res.Kind,
			"aggregate": res.Aggregate,
			"matched":   matched,
			"ambiguous": res.Ambiguous,
			"reason":    res.Reason,
		})
	}
}

// previewSensorField 实现 §8.3 POST /api/v1/sensor-fields/preview（只读试算）。
//
// 口径（逐条对齐 §8.3）：
//   - samples_considered = window_hours 内、该设备、selector 涉及物理名的行数，
//     上限 preview_max_samples（Registry 配置注入，缺省 5000）；
//   - 逐帧求值走 sensorfield.EvalPredicate（唯一判定口径，本 handler 不复制聚合语义）；
//     aggregation_trace 只是展示层逐字段 cmp（用导出的 sensorfield.Compare）；
//   - duration_sec 的连续满足按事件时间重放（与运行时 TriggerDurationSec 同语义）；
//     采样帧间隔意味着最长一个采样周期的近似，故 approx=true 显式标注；
//   - 只读，不写审计（§8.3 末句）。
func previewSensorField(db *gorm.DB, resolver *sensorfield.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req previewRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		if req.EdgeDeviceID == 0 {
			Error(c, http.StatusBadRequest, "edge_device_id is required")
			return
		}
		req.Selector = strings.TrimSpace(req.Selector)
		if req.Selector == "" {
			ErrorWithCode(c, http.StatusBadRequest, "empty_selector", "selector is required")
			return
		}
		op := sensorfield.CmpOp(strings.TrimSpace(strings.ToLower(req.Comparator)))
		if !sensorfield.ValidCmpOp(op) {
			Error(c, http.StatusBadRequest, "invalid comparator (want gt|gte|lt|lte|eq|neq)")
			return
		}
		if req.WindowHours <= 0 {
			req.WindowHours = 24
		}
		if req.WindowHours > sensorFieldPreviewMaxWindowHours {
			req.WindowHours = sensorFieldPreviewMaxWindowHours
		}
		if req.DurationSec < 0 {
			req.DurationSec = 0
		}
		if _, exists := loadEdgeDeviceType(db, req.EdgeDeviceID); !exists {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}

		plan, err := resolver.Compile(req.EdgeDeviceID, req.Selector)
		if err != nil {
			writeSensorFieldError(c, err)
			return
		}
		resolved, err := resolver.Resolve(req.EdgeDeviceID, req.Selector)
		if err != nil {
			writeSensorFieldError(c, err)
			return
		}
		matched := resolved.Matched
		if matched == nil {
			matched = []string{}
		}
		resp := gin.H{
			"selector":            resolved.Selector,
			"status":              resolved.Status,
			"kind":                resolved.Kind,
			"aggregate":           resolved.Aggregate,
			"matched":             matched,
			"samples_considered":  0,
			"truncated":           false,
			"aggregation_trace":   []previewTraceItem{},
			"reduced":             nil,
			"missing_known":       []string{},
			"frames":              []gin.H{},
			"duration_sec":        req.DurationSec,
			"duration_would_fire": false,
			"approx":              req.DurationSec > 0,
		}
		if resolved.Status != sensorfield.StatusResolved {
			// unbound 不是错误：没有可试算的字段，返回空 trace（前端据此提示"尚未上报"）。
			Success(c, resp)
			return
		}

		maxSamples := resolver.Config().PreviewMaxSamples
		if maxSamples <= 0 {
			maxSamples = 5000
		}
		since := time.Now().Add(-time.Duration(req.WindowHours) * time.Hour)
		// ── 首选：帧表（2026-11-08 硬切换）──
		//
		// unified_data 已停写，原 SQL 在 now-24h 窗口内会迅速趋零 =>
		// samples_considered=0 / frames=[] => 前端显示「没有数据」=> 用户以为 selector 配错了。
		// 那是最坏的一类错误：不报错，且把「系统换了存储」伪装成「你的配置有问题」。
		//
		// 帧表解出来的是**完整帧**（该 slot 的全部字段），所以下方的 sparse_frames
		// 判定在帧表路径上恒为 false —— 那不是口径丢失，而是新数据源本来就没有稀疏帧。
		var rows []models.UnifiedData
		usedFrames := false
		if fromFrames, ok := previewRowsFromFrames(db, req.EdgeDeviceID, since, matched); ok {
			rows = fromFrames
			usedFrames = true
		} else {
			// ── 回落：unified_data（过渡期 / 帧表不可用）──
			q := db.Model(&models.UnifiedData{}).
				Where("device_id = ? AND timestamp >= ? AND sensor_name IN ?", req.EdgeDeviceID, since, matched).
				Order("timestamp ASC")
			if err := q.Limit(maxSamples + 1).Find(&rows).Error; err != nil {
				Error(c, http.StatusInternalServerError, "failed to query unified_data")
				return
			}
		}
		truncated := false
		if len(rows) > maxSamples {
			truncated = true
			rows = rows[:maxSamples]
		}
		resp["samples_considered"] = len(rows)
		resp["truncated"] = truncated

		// ==================== D4：按 timestamp 分帧（2026-11-07 L1 之后的语义）====================
		//
		// 生产端同一帧的所有物理量用同一个 now 写入（consumers_heavy.go），因此
		// 「时间戳相等」即同帧 —— 这个判据本身**仍然成立**，不需要改。
		//
		// L1（值不变不入库）无条件生效后变的是**帧的密度**，不是帧的构造：
		// 某个字段值没变且未到心跳时，它在库里**没有行**，于是这一帧看起来「缺员」。
		// 举例: BMS 一帧 29 个字段，若只有 1 个字段变化，库里就只留 1 行 ⇒
		// frames 里这一帧只有 1 个 reading，missing_known（family 缺员）会把它算成缺 28 个。
		//
		// 这不是 bug 而是口径变化，因此这里**只做两件事、不改判定逻辑**:
		//   1. 在响应里显式标注口径（sparse_frames=true + frame_semantics 说明），
		//      让调用方知道「这帧字段少」是 L1 的正常结果，不是设备丢帧；
		//   2. 保留 frames 的原始构造（按 timestamp 分组），因为 duration_would_fire
		//      与 trace 都需要「真实写过库的那一帧」的字段集合 —— 补全缺失字段反而
		//      会给出从未发生过的求值。
		//
		// ⚠ 缺员判定的正确用法：`missing_known` 现在表达的是「本帧没有落库的字段」，
		// 不等于「设备没上报该字段」。要判断后者，看 /sensor-fields/obs 的观测记录
		// （那里按上报计数，不受 L1 影响）。此点已写入交付说明，待前端文案同步。
		sparseFrames := false
		frameOrder := make([]int64, 0, len(rows)/4)
		frameRows := make(map[int64][]models.UnifiedData, len(rows)/4)
		for _, row := range rows {
			key := row.Timestamp.UnixNano()
			if _, seen := frameRows[key]; !seen {
				frameOrder = append(frameOrder, key)
			}
			frameRows[key] = append(frameRows[key], row)
		}
		frames := make([]gin.H, 0, len(frameOrder))
		var runStart time.Time
		durationWouldFire := false
		var lastFrame []sensorfield.Reading
		var lastVerdict sensorfield.Verdict
		for _, key := range frameOrder {
			batch := frameRows[key]
			frame := make([]sensorfield.Reading, 0, len(batch))
			for _, row := range batch {
				frame = append(frame, sensorfield.Reading{Name: row.SensorName, Value: row.Value, Unit: row.Unit})
			}
			verdict := sensorfield.EvalPredicate(plan, frame, op, req.Threshold)
			frameTime := batch[0].Timestamp
			frames = append(frames, gin.H{
				"timestamp": frameTime,
				"ok":        verdict.OK,
				"satisfied": verdict.Satisfied,
				"value":     verdict.Value,
				"reason":    verdict.Reason,
				"degraded":  verdict.Degraded,
			})
			if verdict.OK && verdict.Satisfied {
				if runStart.IsZero() {
					runStart = frameTime
				}
				if req.DurationSec > 0 && frameTime.Sub(runStart) >= time.Duration(req.DurationSec)*time.Second {
					durationWouldFire = true
				}
			} else {
				runStart = time.Time{}
			}
			lastFrame = frame
			lastVerdict = verdict
		}
		// D4: 判定本批数据是否受 L1 影响（帧内字段数 < 匹配到的字段数 ⇒ 该帧被收窄过）。
		// 注意不能只看最后一帧：只要**任意**一帧的字段数少于 matched，就说明库里是稀疏帧。
		for _, key := range frameOrder {
			if len(frameRows[key]) < len(matched) {
				sparseFrames = true
				break
			}
		}
		resp["frames"] = frames
		resp["duration_would_fire"] = durationWouldFire
		// D4: 口径标注（新增字段，不改动既有字段语义，旧前端忽略即可）。
		//
		// 2026-11-08 起口径**随数据源变化**，必须如实报告用的是哪一个：
		//   · 帧表：帧 = 该 slot 的全部字段 => 不存在稀疏帧（sparse_frames 恒 false）
		//   · unified_data：L1 收窄后同刻只留变化字段 => 可能出现稀疏帧
		// 若在这里固定写死 unified_data 的说明，而实际数据来自帧表，那就是
		// 「响应对调用方说了假话」—— 比字段缺失更难发现。
		resp["sparse_frames"] = sparseFrames
		if usedFrames {
			resp["frame_semantics"] = "数据源：telemetry_frame（帧表）。帧 = 该 slot 的全部字段，" +
				"因此 frames 里每一帧包含设备当时解析出的完整字段集合；" +
				"missing_known 表达「本帧缺该字段」，在帧表口径下即「该时刻该字段确实没有值」。"
			resp["data_source"] = "telemetry_frame"
		} else {
			resp["frame_semantics"] = "数据源：unified_data（过渡期回落）。该表自 2026-11-07 起按" +
				"「值变化 + 60s 心跳」落库：某一帧字段数少于 matched 表示这些字段本帧没有变化（未落库），" +
				"不代表设备未上报。missing_known 表达的是「本帧未落库的字段」，不是「设备缺报」。"
			resp["data_source"] = "unified_data"
		}

		// 最近一帧的逐字段 trace（§8.3 明示格式）+ reducer 的 reduced / missing_known。
		if len(lastFrame) > 0 {
			frameNames := make(map[string]struct{}, len(lastFrame))
			trace := make([]previewTraceItem, 0, len(lastFrame))
			for _, reading := range lastFrame {
				valid := isFiniteNumber(reading.Value)
				item := previewTraceItem{Field: reading.Name, Value: reading.Value, Valid: valid}
				if !valid {
					item.Reason = "non_finite"
				} else {
					item.Cmp = sensorfield.Compare(op, reading.Value, req.Threshold)
				}
				trace = append(trace, item)
				frameNames[reading.Name] = struct{}{}
			}
			resp["aggregation_trace"] = trace
			if isReducerSelector(resolved) {
				if lastVerdict.OK {
					reduced := lastVerdict.Value
					resp["reduced"] = reduced
				}
				missing := make([]string, 0, len(matched))
				for _, name := range matched {
					if _, present := frameNames[name]; !present {
						missing = append(missing, name)
					}
				}
				resp["missing_known"] = missing
			}
		}
		Success(c, resp)
	}
}

// isReducerSelector 报告该 selector 是否 reducer 型（reduced/missing_known 只对它们有意义）。
func isReducerSelector(res sensorfield.Resolved) bool {
	if res.Kind != sensorfield.KindFamily {
		return false
	}
	switch res.Aggregate {
	case sensorfield.AggMin, sensorfield.AggMax, sensorfield.AggAvg, sensorfield.AggSum:
		return true
	}
	return false
}

// sensorFieldSchema 实现 §8.4 GET /api/v1/sensor-fields/schema。
//
// 词表唯一来源纪律：aggregates / aggregate_default / virtual_fns / aliases / limits
// 全部取自 sensorfield 包导出函数与 Registry 只读快照，禁止在 handler 里手写第二份
// 词表（方案 §8.4 "不得在代码里另存一份"）；error_codes 是对外契约表（§8.6），
// 与 errors.go 的 ErrorCode 映射必须同步。
func sensorFieldSchema(resolver *sensorfield.Registry) gin.HandlerFunc {
	return func(c *gin.Context) {
		aggValues := sensorfield.Aggregates()
		aggregates := make([]string, 0, len(aggValues))
		for _, a := range aggValues {
			aggregates = append(aggregates, string(a))
		}
		fns := sensorfield.VirtualFns()
		virtualFns := make([]gin.H, 0, len(fns))
		for _, fn := range fns {
			arity := gin.H{"min": fn.MinArgs}
			if fn.MaxArgs > 0 {
				arity["max"] = fn.MaxArgs
			}
			item := gin.H{
				"name":  fn.Name,
				"arity": arity,
			}
			if fn.FamilyArg {
				item["arg_kind"] = "family"
			}
			if fn.HasScale {
				item["has_scale"] = true
			}
			virtualFns = append(virtualFns, item)
		}
		rules := resolver.Aliases()
		aliases := make([]gin.H, 0, len(rules))
		for _, rule := range rules {
			candidates := rule.Candidates
			if candidates == nil {
				candidates = []string{}
			}
			item := gin.H{
				"semantic":   rule.Semantic,
				"candidates": candidates,
			}
			if rule.ExpectedUnit != "" {
				item["expected_unit"] = rule.ExpectedUnit
			}
			if rule.AllowOverlap {
				item["allow_overlap"] = true
			}
			aliases = append(aliases, item)
		}
		cfg := resolver.Config()
		Success(c, gin.H{
			"selector_version":  1,
			"aggregates":        aggregates,
			"aggregate_default": string(sensorfield.DefaultAggregate()),
			"virtual_fns":       virtualFns,
			"error_codes":       sensorFieldErrorCodes(),
			"aliases":           aliases,
			"limits": gin.H{
				"max_selector_chars": cfg.MaxSelectorChars,
				"max_family_size":    cfg.MaxFamilySize,
				"max_virtual_depth":  cfg.MaxVirtualDepth,
			},
		})
	}
}

// sensorFieldErrorCode 是 §8.6 错误码表的单项形状。
type sensorFieldErrorCode struct {
	Code string `json:"code"`
	HTTP int    `json:"http"`
}

// sensorFieldErrorCodes 是 §8.6 错误码全集的对外契约（HTTP 400 族 + warnings 码）。
//
// 顺序稳定（前端快照测试可逐项断言）；这是对外契约而非内部枚举：
// 新增哨兵必须同步此表，否则前端无法识别新 error_code。
func sensorFieldErrorCodes() []sensorFieldErrorCode {
	return []sensorFieldErrorCode{
		{Code: "invalid_selector_syntax", HTTP: http.StatusBadRequest},
		{Code: "empty_selector", HTTP: http.StatusBadRequest},
		{Code: "selector_too_long", HTTP: http.StatusBadRequest},
		{Code: "unknown_semantic", HTTP: http.StatusBadRequest},
		{Code: "unknown_virtual", HTTP: http.StatusBadRequest},
		{Code: "unsupported_aggregate", HTTP: http.StatusBadRequest},
		{Code: "field_resolver_disabled", HTTP: http.StatusBadRequest},
		{Code: "invalid_json", HTTP: http.StatusBadRequest},
		{Code: "resolution_unbound", HTTP: http.StatusOK},
	}
}

// parseBoolQuery 解析 bool query（缺省/非法都按 false，不报错——与 limit clamp 同口径）。
func parseBoolQuery(c *gin.Context, key string) bool {
	raw := strings.TrimSpace(c.Query(key))
	if raw == "" {
		return false
	}
	v, err := strconv.ParseBool(raw)
	return err == nil && v
}

// firstNonEmpty 返回第一个非空串（三源合并优先级：meta > obs > decl）。
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// isFiniteNumber 报告 v 是否有限数（方案 §5.3 定义 1：只有有限数算"有值"）。
func isFiniteNumber(v float64) bool {
	return v == v && v <= 1.7976931348623157e308 && v >= -1.7976931348623157e308
}
