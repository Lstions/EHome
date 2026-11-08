package api

// latest_values_frames.go — 最新值缓存的**帧表数据源**（task-24 步骤 3）。
//
// ============ 为什么必须有这一层（Lead 2026-11-07 提前点出）============
//
// 步骤 1~3 把「最新值」读者从 unified_data 迁走，但 warmup 的数据源一直是
// unified_data（LatestValuesWarmupRows）。**步骤 4 停写之后，那条查询会返回 0 行。**
//
// 危险之处在于它**不报错**：
//
//	WarmupLatestValues() -> rows=0 -> 缓存空 -> 服务正常启动 -> 首次请求 Miss 回落 SQL
//	但回落 SQL 读的也是 unified_data -> 也空 -> **接口静默返回空**
//
// 而且停写之后 unified_data **不是空的，是保留 45 天的旧数据** —— 所以 Miss 回落 SQL
// 拿到的不是空，而是**过期值**（最多 45 天前）。这比空更危险：
//
//	返回空     => 用户立刻发现
//	返回过期值 => 用户以为正常
//
// 步骤 4 的验收必须因此加一条：过渡期检查 latest 类接口返回的 timestamp 是否停留在
// 「停写那一刻」—— 如果是，说明它们在偷偷用旧数据。
//
// 也就是「启动预热静默失效 + 回落路径同时失效」，两者叠起来没有任何错误信号。
// 所以 warmup 的数据源必须在**停写之前**迁到帧表。
//
// ============ 为什么是"追加一趟"而不是替换 ============
//
// main.go 里 warmup 跑在 telemetryDict 创建**之前**（517 行 vs 563 行），
// 那时帧表字典还不可用。把 warmup 整体挪到字典之后会打乱 startupTracker 的步骤顺序。
//
// 所以采用增量方案：原来那趟（unified_data）保持不变，字典就绪后再补一趟，
// **只补缓存里还没有的设备**。效果：
//
//	过渡期   两趟都有数据 => 第二趟基本不做事（几乎零成本）
//	停写之后 第一趟返回 0 行 => 第二趟把全部设备补齐 => 预热照常工作
//
// ============ 为什么按设备取"最后一帧时刻"而不是固定回看窗口 ============
//
// 最新值的语义是「该设备最后已知状态」，对**离线设备**同样成立（界面要显示"最后已知"）。
// 而离线设备的最后一帧可能在几周前 —— 固定窗口（比如 24h）会把它们全部漏掉，
// 于是离线设备重启后缓存为空。所以先按设备取 max(ts)，再围绕它解码一个小窗口。

import (
	"context"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/logger"
)

// latestValuesFrameSlack 是围绕「设备最后一帧时刻」的解码窗口宽度。
//
// 为什么需要它而不是直接解码 [maxTS, maxTS]：解码器需要**关键帧链**。
// 从一个略早的起点开始，DecodeRangeAnchored 会自动把起点前移到最近的关键帧，
// 从而重建出完整状态；窗口太窄会在链未建立时解出空结果。
//
// 为什么只要 1 分钟（而不是页面渲染路径的 10 分钟）：
//
//	状态重建**不由这个窗口负责**。DecodeRangeAnchored 会把解码起点自动前移到
//	最近的关键帧（KeyFrameInterval = 60s），累积状态从那里一路带过来，
//	然后把 from 之前的点裁掉。所以窗口只需**包含最后一帧本身**。
//
// 窗口越大 → 解码的帧越多 → 启动越慢。实测（ehome_test，30 台设备 x 8 字段）：
//
//	1 分钟窗  => 见下方基准用例打印的耗时
//	10 分钟窗 => 解码帧数约 10 倍，耗时同比放大
//
// 所以这里取最小够用值 1 分钟，而不是照抄页面路径的 10 分钟。
const latestValuesFrameSlack = time.Minute

// latestValuesMaxDevices 是递归枚举的**硬性层数上限**，不是业务上界。
//
// 与 task-25 的 latestValuesMissMaxSensors 同一条理由：递归的终止条件依赖
// 「每次跳到的 device_id 严格大于上一次」。只要这个不变量在**任何**数据下被破坏，
// 递归就不会终止，而后果比慢查询糟得多 ——
//
//	慢查询     ：一次请求几十秒，最终会返回；
//	无限递归 CTE：一个请求永不返回，**连接池被逐个占满** ⇒ 整个 API 挂死。
//
// device_id 是 uint，正常情况下必然严格递增。但「正常情况下」不是判据：
// 有人把 `>` 改成 `>=`（看起来更"安全"）、或历史数据里混入异常行，都会让递归原地打转。
// 所以必须有一个与数据无关的上界。取 1 万：生产实测 5 台，正常部署远达不到；
// 一旦达到就说明数据畸形或不变量失效 —— 那时截断并 Warn，把问题暴露出来。
const latestValuesMaxDevices = 10000

// latestValuesDeviceLastTSQuery 枚举「每台设备最后一帧的时刻」。
//
// ============ 为什么不能用 `GROUP BY device_id`（改前的写法）============
//
// 帧表主键是 (device_id, ts, seq)，`SELECT device_id, max(ts) GROUP BY device_id`
// 看起来该走索引 —— PostgreSQL 实测却退化成**全表 Parallel Seq Scan**：
//
//	生产实测（3,561,794 帧 / 5 台设备）：
//	  GROUP BY device_id          =>  241~490 ms  (Parallel Seq Scan 全表)
//	  本查询（skip-scan + 关联）  =>  0.92 ms
//
// 原因是 PostgreSQL **不做 loose index scan**（index skip-scan）——
// 「按前导列分组取极值」拿不到索引红利，只能扫完全部行再聚合。
//
// ============ 这个写法的代价模型（成立前提，重要）============
//
// 递归 CTE 用「min(device_id) > 上一跳」逐台跳读索引，每跳只读**一个**索引项：
//
//	代价 = O(设备数) 次 index-only 跳读 + O(设备数) 次 ts 反向索引探测
//	改前 = O(帧数) 次全表扫描
//
// **成立前提：设备数远小于帧数。** 这个前提在生产极其稳固：
//
//	设备数  ~ 10^0 量级（一台网关挂几台从机）
//	帧数    ~ 10^6 量级（每设备 1 Hz，一个月就是 260 万帧/台）
//	且帧数随**时间**线性增长，设备数不随时间增长。
//
// 所以中间的 10^5 倍差距会越拉越大，不会反转。
// 反过来说：如果将来出现「设备数 >> 每设备帧数」的部署（例如百万设备各只有几帧），
// 这个查询会比 GROUP BY 差 —— 但那种部署下 GROUP BY 也不会慢（表本身就小），
// 两条路都无所谓。**真正的风险区间不存在。**
//
// ============ 为什么不用「调用方传 device_ids」============
//
// 试过。两个调用方都拿不到设备列表：
//
//	main.go 启动预热   ：此时还没读 edge_devices，且预热的目的就是「把所有设备补齐」；
//	handler_data_report：/data-reports 是全局 feed，无设备维度入参。
//
// 更关键的反例（生产实测）：**设备 2 在帧表里有 100,806 帧，但 edge_devices 里
// 已经不存在了**（硬删除）。若用 `SELECT id FROM edge_devices` 当设备源，
// 这条历史数据会被静默漏掉 —— 而"最新值缓存"对已删设备的语义是「没有」而非「报错」，
// 漏掉不会有任何信号。从帧表自身枚举则天然包含它，与改前**逐字节等价**。
const latestValuesDeviceLastTSQuery = `WITH RECURSIVE ds(device_id, depth) AS (
  SELECT (SELECT min(device_id) FROM telemetry_frame WHERE device_id IS NOT NULL), 1
  UNION ALL
  SELECT (SELECT min(device_id) FROM telemetry_frame WHERE device_id > ds.device_id),
         ds.depth + 1
  FROM ds
  WHERE ds.device_id IS NOT NULL AND ds.depth < ?
)
SELECT ds.device_id AS device_id,
       (SELECT max(ts) FROM telemetry_frame WHERE device_id = ds.device_id) AS last_ts
FROM ds
WHERE ds.device_id IS NOT NULL
ORDER BY ds.device_id`

// LatestValuesRowsFromFrames 从帧表重建「每设备最后已知的全部物理量」。
//
// 返回的行与 unified_data 回填**同构**（models.UnifiedData），这样上层的
// WarmupLatestValuesFrom 不需要知道数据来自哪张表 —— 「缓存命中」与「缓存 miss」
// 两条路径的形状一致这条不变量，不能被数据源切换破坏。
//
// 只返回**缓存里还没有**的设备（skipCached=true 时），避免覆盖运行期的新鲜数据：
// 服务跑了一会儿之后，内存里的值比"最后一帧"更新（最后一帧就是内存值的来源），
// 但启动瞬间二者等价；覆盖没有收益，却可能把并发写入的值回退。
func LatestValuesRowsFromFrames(
	db *gorm.DB, dict telemetry.SlotDictionary, skipCached bool,
) ([]models.UnifiedData, error) {
	if db == nil || dict == nil {
		return nil, gorm.ErrInvalidDB
	}

	// ① 每台设备最后一帧的时刻。
	//
	// 用**递归 CTE skip-scan** 枚举设备，而不是 `GROUP BY device_id` ——
	// 详见 latestValuesDeviceLastTSQuery 的注释（含代价模型与成立前提）。
	type devLast struct {
		DeviceID uint
		LastTS   time.Time
	}
	var lasts []devLast
	if err := db.Raw(latestValuesDeviceLastTSQuery, latestValuesMaxDevices+1).
		Scan(&lasts).Error; err != nil {
		return nil, err
	}
	if len(lasts) > latestValuesMaxDevices {
		// 撞到深限：要么真有超过一万台设备，要么递归不变量失效。
		// 截断而不是返回错误 —— 预热少几台设备比整个预热失败轻得多。
		logger.Warnf("[latest-values] frame table device enumeration hit depth cap %d; "+
			"truncating (possible malformed device_id data or broken recursion invariant)",
			latestValuesMaxDevices)
		lasts = lasts[:latestValuesMaxDevices]
	}
	if len(lasts) == 0 {
		return nil, nil
	}

	unitsCache := map[uint]map[string]string{}
	out := make([]models.UnifiedData, 0, len(lasts)*16)
	for _, l := range lasts {
		if l.DeviceID == 0 || l.LastTS.IsZero() {
			continue
		}
		if skipCached && len(LatestValues(l.DeviceID)) > 0 {
			continue // 运行期已经有更新鲜的值，不用帧表覆盖
		}

		// ② 围绕最后一帧解码，累积出该设备的最后已知全部物理量。
		//
		// 这里**直接用传入的 dict**，不走 latestSensorSnapshotsFromFrames —— 后者读的是
		// 全局 frameDictionaryFor(db)。预热发生在启动早期，全局字典是否已注入取决于
		// main.go 的接线顺序；依赖它会得到一个"参数传了却不生效"的假接口，
		// 单测里表现为"补入 0 行但没有任何错误"。
		points, _, derr := telemetry.DecodeRangeAnchored(
			context.Background(), db, dict, l.DeviceID,
			l.LastTS.Add(-latestValuesFrameSlack), l.LastTS.Add(time.Second), nil)
		if derr != nil || len(points) == 0 {
			continue
		}
		final := frameSnapshot{Values: make(map[string]float64, 32)}
		for _, pt := range points {
			final.Values[pt.Name] = pt.Value
			final.TS = pt.TS
		}
		units, seen := unitsCache[l.DeviceID]
		if !seen {
			fieldNames := make([]string, 0, len(final.Values))
			for fn := range final.Values {
				fieldNames = append(fieldNames, fn)
			}
			units = unitMapForDevice(db, l.DeviceID, fieldNames)
			unitsCache[l.DeviceID] = units
		}
		for name, v := range final.Values {
			out = append(out, models.UnifiedData{
				DeviceID:   l.DeviceID,
				SensorName: name,
				Value:      v,
				Unit:       units[name],
				Timestamp:  final.TS,
				CreatedAt:  final.TS,
			})
		}
	}
	return out, nil
}

// LatestRowsFromFrames 返回某台设备**最后已知的全部物理量**（models.UnifiedData 形状）。
//
// 返回 ok=false 表示帧表路径**不可用**（不是「没有数据」），调用方必须回落 unified_data：
//
//	· dict 为 nil（未注入 / 预热失败）；
//	· 该设备在帧表里一帧都没有；
//	· 解码报错或超出护栏。
//
// ============ 为什么它是 LatestValuesRowsFromFrames 的「单设备版」而不是复制品 ============
//
// 两者解码手法完全一致（取 max(ts) → 围绕它解码 latestValuesFrameSlack → 累积），
// 差别只在「枚举哪些设备」和「返回什么形状」。
//
// 但**必须共用同一段解码语义**，因为下游有两处消费方要求口径一致：
//
//	· /latest-values 的 miss 回落（要 models.UnifiedData）
//	· /latest-data 的 sensors 合成（要 sensorView）
//
// 若各写一份，就会重现 device_data_sensors.go 文件头点名的那个缺陷：
// 「详情页与数据面板显示不同数值，且只在缓存冷热切换时出现，极难复现」。
//
// ============ 为什么没有时间窗 ============
//
// 语义是「最后已知状态」——离线几周的设备同样要显示（界面显式写「最后已知」）。
// 固定回看窗口会把它们全部漏掉，于是重启后缓存为空。所以先取该设备 max(ts)，
// 再围绕那个时刻解码。这也是 LatestValuesRowsFromFrames 的手法。
func LatestRowsFromFrames(db *gorm.DB, deviceID uint) ([]models.UnifiedData, bool) {
	if db == nil || deviceID == 0 {
		return nil, false
	}
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, false
	}

	// 单设备 max(ts)：WHERE device_id = ? 命中 (device_id, ts) 前导列，
	// 一次索引反向扫描，与设备历史长度无关（生产实测 <1 ms）。
	var lastTS *time.Time
	if err := db.Raw("SELECT max(ts) FROM telemetry_frame WHERE device_id = ?", deviceID).
		Scan(&lastTS).Error; err != nil || lastTS == nil {
		return nil, false
	}

	// 用 DecodeRangeAnchored（生产护栏 24h / MaxDecodeFrames）：窗口只有
	// latestValuesFrameSlack（1 分钟），远在护栏内。
	points, _, derr := telemetry.DecodeRangeAnchored(
		context.Background(), db, dict, deviceID,
		lastTS.Add(-latestValuesFrameSlack), lastTS.Add(time.Second), nil)
	if derr != nil || len(points) == 0 {
		return nil, false
	}

	// 解码结果按时间升序；同一 (name) 后者覆盖前者 ⇒ 最终得到每字段最新值。
	values := make(map[string]float64, len(points))
	var finalTS time.Time
	for _, pt := range points {
		values[pt.Name] = pt.Value
		if pt.TS.After(finalTS) {
			finalTS = pt.TS
		}
	}
	names := make([]string, 0, len(values))
	for n := range values {
		names = append(names, n)
	}
	units := unitMapForDevice(db, deviceID, names)

	out := make([]models.UnifiedData, 0, len(values))
	for name, v := range values {
		out = append(out, models.UnifiedData{
			DeviceID:   deviceID,
			SensorName: name,
			Value:      v,
			Unit:       units[name],
			Timestamp:  finalTS,
			CreatedAt:  finalTS,
		})
	}
	return out, true
}

// WarmupLatestValuesFromFrames 用帧表补齐最新值缓存里还空着的设备。
//
// 返回补入的行数（便于启动日志与测试断言）。
// **失败不阻塞启动**：与 WarmupLatestValues 的既有约定一致 —— 预热失败不该让服务起不来，
// 但这里的失败会比以前更值得记录，因为停写之后它可能是唯一的预热来源。
func WarmupLatestValuesFromFrames(db *gorm.DB, dict telemetry.SlotDictionary) int {
	rows, err := LatestValuesRowsFromFrames(db, dict, true)
	if err != nil || len(rows) == 0 {
		return 0
	}
	WarmupLatestValuesFrom(rows)
	return len(rows)
}
