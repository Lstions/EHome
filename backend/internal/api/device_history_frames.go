package api

// device_history_frames.go — /devices/:id/history 的**帧表原始历史**读路径（task-26）。
//
// ============ 为什么不能复用 queryHistoryPointsWithFallback ============
//
// 那条链的输出类型是 HistoryPoint{timestamp, value} —— **丢了 sensor_name**。
// 它的语义是「单字段降采样聚合」，不是「多字段原始样本」。无条件拿它替换裸查询，
// 必然既错形状又错语义。（第一次就是这么做的，3 个既有用例红，已完整回退。）
//
// 正确的底层是 decodeFrameWindow：返回 map[string][]HistoryPoint，
// 按 sensor_name 分组 —— 正是多字段历史需要的形状。
//
// ============ 分段解码（2026-11-08）============
//
// 帧解码有硬护栏 frameDecodeMaxWindow。⚠ **这个值有三个，必须一起看**：
//
//	代码默认   1h    （history_source.go 的 defaultFrameDecodeMaxWindow）
//	生产       6h    （部署脚本设 EHOME_TELEMETRY_FRAME_DECODE_MAX_WINDOW=6h）
//	测试       1h    （不设 env）
//
// 曾经这里写「默认 6h，测试环境 1h」—— 那句话把**生产值**当成了**默认值**，
// 读代码的人会以为不设 env 也是 6h。改任何一处都要先想清楚另外两处（见下面 :51 的教训）。
//
// 长窗按段解：
//
//	24h 拆 4 段（6h 时）=> 每段 21,600 帧 < MaxDecodeFrames(32,768)  ✓
//
// 一次性解 24h 会撞 32,768 帧上限且**静默只留最后 32,768 帧**（不报错）。
// 所以必须分段 —— 这是「能不能解」的问题，不是性能取舍。
//
// ============ 契约：保留 models.UnifiedData 的完整键 ============
//
// 该接口原本返回 []models.UnifiedData。现在返回 0 行是**因为它坏了**，不是契约可以随便改。
//
//	id          用 (device_id, sensor_name, timestamp) 的稳定哈希合成。
//	            不能给 0：前端列表很可能拿 id 当 key，全 0 会渲染错乱。
//	            不能用自增：帧解码不保证跨请求顺序一致 ⇒ 同一次请求两次调用 id 会变。
//	created_at  帧没有创建时间，用采样时间：不要填 now()，那会让"入库时间"变成谎言。
//	unit        走 unitMapForDevice（sensor_field_obs -> unified_data -> 空串 三层）。

import (
	"context"
	"hash/fnv"
	"sort"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// maxFrameHistoryWindow 是分段解码的**时长上限**（8 天）。
//
// 存在的理由不是性能，是防滥用：没有它，一个 start=1970 的请求会让服务解几万段。
// 超限时返回 ok=false 交回裸 SQL，而不是报错 —— 数据源选择失败不该是 4xx/5xx。
//
// ⚠ 上限必须按**时长**定，不能按「段数」。第一版写的是 maxFrameHistorySegments=32，
// 在 frameDecodeMaxWindow=6h（生产值）时恰好是 8 天，看起来对；但代码默认/测试环境是 **1h**，
// 于是 32 段只剩 32 小时，168h 直接被拒。**按段数定上限 = 上限随一个无关配置漂移。**
const maxFrameHistoryWindow = 8 * 24 * time.Hour

// frameHistoryRows 用帧表重建 [start, end) 内的**全部**原始采样行。
//
// 返回 ok=false 表示帧表这条路不可用，调用方必须回落原裸 SQL：
// 字典未注入 / 窗口超过 maxFrameHistoryWindow / 解码报错 / 该窗口一帧都没有。
func frameHistoryRows(
	ctx context.Context, db *gorm.DB, deviceID uint, sensorName string, start, end time.Time,
) ([]models.UnifiedData, bool) {
	byName, units, ok := decodeHistoryByName(ctx, db, deviceID, sensorName, start, end)
	if !ok {
		return nil, false
	}
	return assembleAllRows(byName, units, deviceID), true
}

// decodeHistoryByName 解码整个窗口并按字段分组，同时取回单位映射。
//
// 拆出来是为了让「全量物化」与「下推降采样」两条路共用同一份解码结果 ——
// 少一处复制就少一处不一致（这两种输出必须逐点一致，见 device_history_downsample.go）。
func decodeHistoryByName(
	ctx context.Context, db *gorm.DB, deviceID uint, sensorName string, start, end time.Time,
) (map[string][]HistoryPoint, map[string]string, bool) {
	if db == nil {
		return nil, nil, false
	}
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, nil, false
	}
	window := end.Sub(start)
	if window <= 0 || window > maxFrameHistoryWindow {
		return nil, nil, false
	}

	scope := frameDecodeAllFields
	if sensorName != "" {
		scope = sensorName
	}

	// ============ 慢请求分段计时（2026-11-08）============
	//
	// 生产现象：重启后 device14 的**第一次** 24h 请求 37.06s，第二次 3.17s；
	// 而 device15 第一次只有 2.84s。差 13 倍 => 是 **per-device 的一次性成本**，
	// 不是「全局字典加载」（字典在 main.go:576 启动时注入，frameDictionaryFor 直接返回，零成本）。
	//
	// 不打日志就只能猜，所以这里把四段耗时分别记下来，**只在总耗时 >1s 时打 Warn**
	// （正常请求 3s 也会打，但那是需要关注的范围；毫秒级请求不打，避免刷日志）。
	var (
		tUnit    time.Duration
		tDecode  time.Duration
		segments int
	)
	_t0 := time.Now()

	byName := make(map[string][]HistoryPoint, 32)
	for segStart := start; segStart.Before(end); {
		if err := ctx.Err(); err != nil {
			return nil, nil, false
		}
		segEnd := segStart.Add(frameDecodeMaxWindow)
		if segEnd.After(end) {
			segEnd = end
		}
		// 段边界严格**半开 [segStart, segEnd)**，与 decodeFrameWindow 内部
		// `ts >= from && ts < to` 的裁剪口径一致 ⇒ 相邻段既不重叠也不留缝。
		// （接缝正确性由 TestFrameHistoryRows_SegmentSeamHasNoGapOrDup 钉住。）
		segPoints, err := decodeFrameWindow(ctx, db, dict, deviceID, segStart, segEnd, scope, true)
		if err != nil {
			return nil, nil, false
		}
		for name, pts := range segPoints {
			byName[name] = append(byName[name], pts...)
		}
		segments++
		segStart = segEnd
	}
	tDecode = time.Since(_t0)
	if len(byName) == 0 {
		return nil, nil, false
	}

	// ⚠ 这一段是**prime suspect**：unitMapForDevice 每次进程每设备只算一次，
	// 内部是 `SELECT DISTINCT ON (sensor_name) ... FROM unified_data WHERE device_id = ?`。
	// device14 的 unified_data 有 1500 万行，device15 少得多 —— 与「只有 device14 首次慢」
	// 的形态吻合。计时会直接证实或推翻。
	_tu := time.Now()
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	// 只查本次真正需要的字段：① 命中就一次 DB 都不查，② 只补缺失的。
	// 这也是修 33 秒事故的关键 —— 不再无条件跑整设备的 DISTINCT ON。
	units := unitMapForDevice(db, deviceID, names)
	tUnit = time.Since(_tu)

	if total := time.Since(_t0) + tUnit; total > time.Second {
		logger.Warnf("[devices-history] 慢请求 device=%d window=%s segments=%d decode=%s units=%s total=%s",
			deviceID, end.Sub(start), segments, tDecode.Round(time.Millisecond),
			tUnit.Round(time.Millisecond), total.Round(time.Millisecond))
	}
	return byName, units, true
}

// assembleAllRows 物化**全部**行并按 (timestamp, sensor_name) 排序。
func assembleAllRows(byName map[string][]HistoryPoint, units map[string]string, deviceID uint) []models.UnifiedData {
	total := 0
	for _, pts := range byName {
		total += len(pts)
	}
	out := make([]models.UnifiedData, 0, total)
	for name, pts := range byName {
		unit := units[name]
		for i := range pts {
			ts := pts[i].Timestamp.UTC()
			out = append(out, models.UnifiedData{
				ID:         stableHistoryRowID(deviceID, name, ts),
				DeviceID:   deviceID,
				SensorName: name,
				Value:      pts[i].Value,
				Unit:       unit,
				Timestamp:  ts,
				CreatedAt:  ts,
			})
		}
	}
	// 原实现是 ORDER BY timestamp ASC。多字段时同刻多行，副键用 sensor_name
	// 保证**跨请求稳定**（否则前端同一份数据两次请求行序不同）。
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Timestamp.Equal(out[j].Timestamp) {
			return out[i].Timestamp.Before(out[j].Timestamp)
		}
		return out[i].SensorName < out[j].SensorName
	})
	return out
}

// buildHistoryRow 物化**单行**（下推降采样路径用，避免为丢掉的行分配字符串）。
func buildHistoryRow(byName map[string][]HistoryPoint, units map[string]string, deviceID uint, it sensorPoint) models.UnifiedData {
	pt := byName[it.name][it.idx]
	ts := pt.Timestamp.UTC()
	return models.UnifiedData{
		ID:         stableHistoryRowID(deviceID, it.name, ts),
		DeviceID:   deviceID,
		SensorName: it.name,
		Value:      pt.Value,
		Unit:       units[it.name],
		Timestamp:  ts,
		CreatedAt:  ts,
	}
}

// stableHistoryRowID 由 (device_id, sensor_name, timestamp) 合成稳定行 ID。
//
// 用 FNV-1a 64 位再截到 uint：目标只是「同一条采样每次请求拿到同一个 id」，
// 不是全局唯一性（真正的唯一性由三元组本身保证）。
func stableHistoryRowID(deviceID uint, sensorName string, ts time.Time) uint {
	h := fnv.New64a()
	var buf [8]byte
	v := uint64(deviceID)
	for i := 0; i < 8; i++ {
		buf[i] = byte(v >> (8 * i))
	}
	_, _ = h.Write(buf[:])
	_, _ = h.Write([]byte(sensorName))
	nanos := ts.UTC().UnixNano()
	for i := 0; i < 8; i++ {
		buf[i] = byte(uint64(nanos) >> (8 * i))
	}
	_, _ = h.Write(buf[:])
	return uint(h.Sum64())
}
