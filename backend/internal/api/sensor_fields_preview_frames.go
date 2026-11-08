package api

// sensor_fields_preview_frames.go — /sensor-fields/preview 的**帧表数据源**（2026-11-08 硬切换）。
//
// ============ 为什么必须换源 ============
//
// 原实现在 handler_sensor_fields.go 里直接读 unified_data：
//
//	db.Model(&models.UnifiedData{}).
//	    Where("device_id = ? AND timestamp >= ? AND sensor_name IN ?", ...)
//
// 停写之后（2026-11-08）那张表不再增长，于是：
//
//	now-24h 窗口内的行数迅速趋零 => samples_considered = 0 => frames = []
//	=> 前端显示「规则不匹配 / 没有数据」=> **用户以为是自己配错了 selector**
//
// 这是最坏的一类错误：**它不报错，而且把「系统换了存储」伪装成「你的配置有问题」**。
//
// ============ 为什么不能简单地把 SQL 换成帧表 ============
//
// preview 的响应里 frames 是「逐帧的判定结果」，它依赖一个语义：
// **同一帧的字段集合**。unified_data 靠「同一时间戳」分组，但 L1 收窄之后，
// 某帧只有变化的字段落了库 => 「字段数 < matched」=> 触发 sparse_frames 口径标注。
//
// 帧表天然没这个问题：帧编码的是**该 slot 的全部字段**，解码出来就是完整的一帧。
// 所以换源之后 sparse_frames 恒为 false —— 那不是「口径丢了」，而是
// **帧表本来就给出真实帧内容**，sparse_frames 这个补丁在新数据源上不再需要。
//
// ============ 与 unified_data 路径的差别（必须让调用方知道）============
//
//	· unified_data 路径：帧内字段 = 该帧**落库**的字段（可能是稀疏的）
//	· 帧表路径       ：帧内字段 = 该帧**实际解析出**的字段（完整）
//
// 后者才是「设备在那时刻的状态」，与 preview 的用途（试算规则会怎么判）一致。

import (
	"context"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
)

// sensorFieldPreviewMaxFrames 是 preview 单次最多解码的帧数。
//
// 为什么要有上限：窗口是用户给的（最大 168h），一台 1.85 帧/秒的设备一天约 16 万帧。
// 全解会既慢又占内存，而 preview 的用途是「抽样看规则怎么判」，不是全量审计。
// 取 20000 与 MaxDecodeFrames 同量级，足够覆盖 24h 内的中低速设备；
// 超出时截断并置 truncated=true（沿用既有响应字段，前端不需要改）。
const sensorFieldPreviewMaxFrames = 20000

// previewRowsFromFrames 把 [since, now] 内的帧解成**逐帧的 models.UnifiedData 行**。
//
// 返回 ok=false 表示帧表路径不可用（字典 nil / 解码失败 / 该窗口无帧），
// 调用方必须回落 unified_data —— 回落不是错误，是过渡期的正常路径。
//
// 行的形状与原 SQL 完全一致：(device_id, sensor_name, value, timestamp)，
// 单位从 unitMapForDevice 补齐（帧表不存单位）。这样 handler 里那段分组/判定
// 逻辑可以**一行不改**地复用，两条路径的行为差异被限制在「数据源」这一层。
func previewRowsFromFrames(
	db *gorm.DB, deviceID uint, since time.Time, fields []string,
) ([]models.UnifiedData, bool) {
	if db == nil || deviceID == 0 || len(fields) == 0 {
		return nil, false
	}
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, false
	}

	// 用 Bounded 而不是 DecodeRangeAnchored：窗口由用户请求的 window_hours 决定
	// （最大 168h = 7d），超过生产护栏 MaxDecodeWindow(24h) 时后者会直接报错。
	// 这正是 device_data_sensors_frames.go 注释里点名的场景（"需要放宽护栏的场景
	// 请显式用 Bounded"）。maxFrames 仍用本文件的上限，所以「帧数炸弹」照样被挡。
	to := time.Now()
	points, stats, err := telemetry.DecodeRangeAnchoredBounded(
		context.Background(), db, dict, deviceID, since, to, fields,
		// 比实际窗口宽一倍即可（护栏是"不超过"语义）。
		to.Sub(since)*2, sensorFieldPreviewMaxFrames)
	if err != nil || len(points) == 0 {
		return nil, false
	}
	// ============ 截断必须显式失败，不能静默给「看起来完整」的预览（2026-11-08）============
	//
	// DecodeRangeAnchoredBounded 在帧数超 sensorFieldPreviewMaxFrames 时**保留最新、
	// 丢弃最旧**，只把数量记进 stats。而本函数的返回值是 (rows, ok)：
	//
	//	若这里不检查 => 预览显示「最近 N 小时的样本」但标签写的是 window_hours=168，
	//	                用户看到的是一份**被静默截断**的样本集，且没有任何提示。
	//
	// 实测（2026-11-08，生产只读）：168h 窗口下
	//	device14 = 601,220 帧 / 上限 20,000 => **30 倍**
	//	device15 = 390,802 帧 / 上限 20,000 => 19 倍
	// 即这个分支在生产上是**常态触发**，不是理论边界。
	//
	// 额外一层坑：调用方还有一个 maxSamples=5000 的行数截断，它的 truncated 字段会
	// 把这份「帧级截断」掩盖掉 —— 两层截断叠加时，用户只看到「样本数到上限了」，
	// 完全看不到「最旧的那些小时根本没被解」。
	//
	// 返回 false 让调用方回落（过渡期）/ 走明确的错误分支（硬切换后），
	// 比返回一份错标签的样本集诚实。
	if stats.FramesTruncated > 0 {
		return nil, false
	}

	// 单位一次查全（帧表没有 unit 列，见 device_data_sensors_frames.go 文件头）。
	units := unitMapForDevice(db, deviceID, fields)

	// 按时间戳分组 —— 与原 SQL 路径**同一构造**，这样下游 frameRows 的键语义不变。
	// 解码结果已按时间升序，所以同 TS 的点是连续的。
	out := make([]models.UnifiedData, 0, len(points))
	for _, pt := range points {
		out = append(out, models.UnifiedData{
			DeviceID:   deviceID,
			SensorName: pt.Name,
			Value:      pt.Value,
			Unit:       units[pt.Name],
			Timestamp:  pt.TS,
			CreatedAt:  pt.TS,
		})
	}
	return out, true
}
