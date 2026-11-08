package api

import (
	"sort"
	"strconv"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handler_data_report.go — GET /api/v1/data-reports（最近数据条目 feed）。
//
// ============ 演练实测（2026-11-08）============
//
//	#20 /data-reports  →  .data = 0（基线 20）
//
// 原因不用说：原实现只有一条路 —— unified_data。藏表之后表还在但行没了。
//
// ============ 顺带量出来的独立性能问题 ============
//
// 原查询 `ORDER BY created_at DESC LIMIT 20` **没有任何 WHERE**，而 unified_data
// 上没有 created_at 单列索引（只有 (device_id, created_at DESC)，无 device 谓词
// 用不上）⇒ 生产实测 **2213 ms**：
//
//	Parallel Seq Scan on unified_data_202611 (actual rows=5,753,811)
//	  + top-N heapsort，外部无临时文件（20 行很小，但**扫描量是整张表**）
//
// 这不是本轮引入的，但它和「藏表」无关地存在：**一个「取最近 20 条」的接口
// 在扫 575 万行**。修藏表时顺手把它换成帧表路径，同时解决两个问题。
//
// ============ 语义映射（为什么帧表能表达「最近数据」）============
//
// 原语义：「全库最近的 N 条 UnifiedData」。帧表是 (device_id, ts) 一行的
// 增量编码，**一条原始行被压缩进帧里**，所以「最近的 N 条」= 解码最近的帧
// 再摊平成行。
//
// 这里复用 LatestValuesRowsFromFrames：它就是「每台设备最后一帧 → 该帧
// 全部物理量 → []UnifiedData」，正是最近数据 feed 的形状。再按 timestamp
// DESC 排序取前 limit 条，即「全库最近 N 条」。
//
// ============ 为什么帧表放在数据源 1 ============
//
//  1. 语义方向：帧表是长期存储，unified_data 正在退役 —— 和 task-26 的
//     /devices/:id/history 采用同一顺序（帧表优先，裸 SQL 兜底）；
//  2. 性能方向：2213ms → 帧表路径实测 490ms 量级（全表 group-by 那部分是
//     已知的第二阶问题，见下），**两个方向同向**，没有取舍；
//  3. 兜底方向：帧表不可用（SQLite 测试库 / 字典未就绪 / 空表）时回落裸 SQL，
//     冷启动首屏不会空。
//
// ============ 已知的第二阶问题（不在本轮范围）============
//
// LatestValuesRowsFromFrames 内部是 `SELECT device_id, max(ts) ... GROUP BY device_id`，
// **无时间界**。生产实测 490ms / 353 万帧，且随帧数线性增长 —— 因为 PostgreSQL
// 不做 loose index scan，全局 group-by 只能全表扫。修法是按设备做一次
// `WHERE device_id = ? ` 的 index-only 反向扫（实测 0.15ms/设备），但那要改
// latest_values_frames.go（/latest-values 与启动预热的共用实现），不属于本任务
// 的写范围。**先记录，不顺手改别人的文件。**
func registerDataReportRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	// GET /api/v1/data-reports — recent UnifiedData records
	v1.GET("/data-reports", func(c *gin.Context) {
		limit := 20
		if l := c.Query("limit"); l != "" {
			if n, err := strconv.Atoi(l); err == nil && n > 0 && n <= 100 {
				limit = n
			}
		}

		// 数据源 1：帧表（长期存储）
		if rows, ok := recentDataReportsFromFrames(db, limit); ok {
			Success(c, rows)
			return
		}

		// ============ 帧表不可用：返回空，**不再回落 unified_data**（2026-11-08）============
		//
		// 原回落：db.Order("created_at DESC").Limit(limit).Find(&data)
		//
		// 停写 ≠ TRUNCATE ⇒ 这条 SQL 仍能返回**冻结在停写那一刻**的「最近记录」。
		// 对一个以「最近」为语义的 feed 端点，这是最坏的一种假数据：
		// 它看起来完全正常（有 ID、有值、按时间倒序），只是全部过期。
		//
		// 什么情况下会走到这里（帧表 ok=false）：字典未就绪、解码失败、帧表为空。
		// 三种情况返回空都是**诚实**的：我们确实拿不到最近记录。
		//
		// ⚠ 空值形态：必须是 `[]` 而不是 `null`。
		//	· 直接 `Success(c, []models.UnifiedData{})` 保证是 `[]`；
		//	· 原实现用 dataReportsFallbackRows 的 nil 守卫兜「查询报错」那条路
		//	  （实测：0 行时 GORM 给的是非 nil 空切片，守卫不生效）。
		//	  现在没有查询了，也就没有「报错 ⇒ nil」这条路径，守卫随之失去对象。
		Success(c, []models.UnifiedData{})
	})
}

// recentDataReportsFromFrames 用帧表回答「全库最近的 N 条数据」。
//
// 返回 ok=false 表示**帧表不可用**（db 为空 / 字典未就绪 / 解码失败 / 无数据），
// 调用方必须回落裸 SQL。注意「无数据」和「查询失败」都返回 false 是**刻意的**：
// 两者对调用方的含义相同 —— 「这条路给不出结果，去走另一条」，区分开来只会让
// 调用方多一个分支却做不出不同的事。
func recentDataReportsFromFrames(db *gorm.DB, limit int) ([]models.UnifiedData, bool) {
	if db == nil || limit <= 0 {
		return nil, false
	}
	dict := frameDictionaryFor(db)
	if dict == nil {
		return nil, false
	}

	rows, err := LatestValuesRowsFromFrames(db, dict, false)
	if err != nil || len(rows) == 0 {
		return nil, false
	}

	// LatestValuesRowsFromFrames 不填 ID（那个函数只服务于预热缓存，没人看 ID）。
	// 本端点对外是 feed，ID 是列表 key —— 缺了它前端 v-for 会拿 undefined 当 key。
	// 用 device_history_frames.go 的同一套合成规则，保证「同一条采样每次拿到同一个 ID」。
	for i := range rows {
		rows[i].ID = stableHistoryRowID(rows[i].DeviceID, rows[i].SensorName, rows[i].Timestamp)
	}

	// 原实现是 ORDER BY created_at DESC。帧表里 CreatedAt 就取 Timestamp
	// （见 LatestValuesRowsFromFrames），所以这里按 Timestamp 排即可。
	// 副键用 (device_id, sensor_name) 保证**跨请求稳定**：同一 timestamp 下
	// 多台设备多字段是常态（29 个物理量同一个 ts），只用时间排序的话两次请求
	// 的行序可以不同，前端列表会闪。
	sort.Slice(rows, func(i, j int) bool {
		if !rows[i].Timestamp.Equal(rows[j].Timestamp) {
			return rows[i].Timestamp.After(rows[j].Timestamp)
		}
		if rows[i].DeviceID != rows[j].DeviceID {
			return rows[i].DeviceID < rows[j].DeviceID
		}
		return rows[i].SensorName < rows[j].SensorName
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, true
}
