package api

import (
	"math"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

// handler_source_overview.go 实现 GET /api/v1/data-sources/overview。
//
// # 这个页面到底是什么（2026-10-23 重做）
//
// **数据来源 = 只读的「已接入字段目录」。**
//
// 它回答的唯一问题是：*边缘设备解析成功之后，系统里现在有哪些字段、什么类型、
// 什么单位、最新值是多少、多久以前更新的。*
//
// 它**不是**主备来源的配置台账。本文件早期版本把 data_sources 表（那是一张
// 主备配置台账）当成字段目录来渲染，于是页面上出现了「来源类型 / 成员 /
// 当前来源 / 来源组」四列 —— 这些是"多设备里用哪台供数"的配置问题，
// 与"有哪些字段"完全无关：
//
//   - 「来源类型：直连」「来源组：单设备直连」是在描述**配置状态**，
//     而本页是只读事实目录，用户在这里根本无法配置它；
//   - 「当前来源：bms 权威」与第一列「逻辑设备 bms #14」重复；
//   - 「成员」在只有一台设备时恒为 1，没有任何信息量。
//
// 来源组的配置与成员关系在「来源组」Tab 里维护，两页职责不重叠。
// 现在本页只输出**字段事实**：逻辑设备、类别、描述、类型、单位、实时值、
// 更新时间、状态。
//
// # 只读如何保证
//
// 本组只注册 GET（写方法显式 405，见 registerSourceOverviewRoute）。
// 前端组件契约里也没有任何写操作。

// sourceOverviewItem 是字段目录的一行（= 一个逻辑设备的一个数据类别）。
type sourceOverviewItem struct {
	LogicalDeviceID   uint   `json:"logical_device_id"`
	LogicalDeviceName string `json:"logical_device_name"`

	// Category 是物理名（= unified_data.sensor_name，不归一）。
	Category string `json:"category"`

	// ── 字段元数据 ──
	//
	// 三层来源，任一缺失都有降级路径：
	//   LabelZh   ← sensor_field_meta（声明层）
	//   ValueType ← sensor_field_meta；缺失时按观测值推断（inferValueType）
	//   Unit      ← sensor_field_obs（事实层：实际观测到的单位比声明更可信）
	LabelZh   string `json:"label_zh"`
	ValueType string `json:"value_type"`
	Unit      string `json:"unit"`

	// ── 实时值 ──
	//
	// 取自进程内最新值缓存（latest_value_cache.go），零 DB 查询。
	// 用 *float64 而非 float64：0 是合法读数，必须与"暂无数据"区分。
	LatestValue *float64 `json:"latest_value"`
	// UpdatedAt 是**该读数的时间**（nil = 从未收到数据）。
	//
	// 为什么用读数时间而不是 data_sources.last_success：后者是主备台账列，
	// 在没有来源组时恒为 nil（它由组成员健康维护）—— 与"这个字段刚更新过"
	// 是两件事。页面上显示值却显示"— 最后成功"，就是这个混淆造成的。
	UpdatedAt *time.Time `json:"updated_at"`
	// AgeSec 是读数年龄（秒）。前端据此判断实时/陈旧。
	AgeSec *int `json:"age_sec"`

	// Status 是**数据新鲜度**，由 AgeSec 派生（不是配置状态）。
	//
	// 取值：ok | delay | stale | nodata
	//   ok     年龄 ≤ 30s（正常采集）
	//   delay  30s < 年龄 ≤ 300s（上报变慢/丢帧）
	//   stale  年龄 > 300s（设备可能离线）
	//   nodata 从未收到过数据
	Status string `json:"status"`
}

// sourceOverviewResponse 是字段目录响应体。
type sourceOverviewResponse struct {
	Items []sourceOverviewItem `json:"items"`
	Total int                  `json:"total"`
	// GeneratedAt 是后端生成该视图的时间。
	GeneratedAt time.Time `json:"generated_at"`
}

// 新鲜度阈值（秒）。与前端 OverviewTab 的着色阈值必须一致。
const (
	freshnessOKSec    = 30
	freshnessDelaySec = 300
)

// deriveFreshness 把读数年龄映射为状态标签。
//
// 纯函数，便于单测穷举边界。
func deriveFreshness(ageSec *int) string {
	if ageSec == nil {
		return "nodata"
	}
	a := *ageSec
	if a < 0 {
		a = 0 // 时钟偏移容错：不把"未来时间"判成异常
	}
	switch {
	case a <= freshnessOKSec:
		return "ok"
	case a <= freshnessDelaySec:
		return "delay"
	default:
		return "stale"
	}
}

// inferValueType 在缺少 sensor_field_meta 时按观测值推断语义类型。
//
// 推断是**保守**的（宁可标 number 也不猜错）：非整数 ⇒ number；
// 整数且 ∈{0,1} ⇒ boolean；其余整数 ⇒ number。
// 不做 enum/bitmask 推断 —— 那需要值域知识，猜错比留空更糟。
func inferValueType(v float64) string {
	if v != math.Trunc(v) {
		return "number"
	}
	if v == 0 || v == 1 {
		return "boolean"
	}
	return "number"
}

// listSourceOverview 返回只读字段目录。
//
// 数据来源：data_sources（已由投影器自动填充的"字段 → 逻辑设备"关系）+
// sensor_field_meta（声明层元数据）+ sensor_field_obs（事实层单位）+
// 进程内最新值缓存（实时值）。全部批量查询，无 N+1。
func listSourceOverview(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"code": "source_overview_unavailable", "message": "数据库不可用",
			})
			return
		}

		q := db.Model(&models.DataSource{}).
			Where("status <> ?", datasource.StatusDisabled)
		if cat := c.Query("category"); cat != "" {
			q = q.Where("category = ?", cat)
		}
		if ld := c.Query("logical_device_id"); ld != "" {
			q = q.Where("device_id = ?", ld)
		}

		var rows []models.DataSource
		if err := q.Order("device_id ASC, category ASC, edge_device_id ASC").Find(&rows).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"code": "source_overview_failed", "message": "读取字段目录失败",
			})
			return
		}

		// 按 (逻辑设备, 类别) 聚合 —— 一个字段一行。
		//
		// 为什么还要聚合：同一字段可能由多台设备供数（主备组）。
		// 目录层面它是**一个**字段，实时值取权威成员的那份。
		type fieldKey struct {
			deviceID uint
			category string
		}
		order := make([]fieldKey, 0, len(rows))
		byKey := make(map[fieldKey]*sourceOverviewItem, len(rows))
		membersOfKey := make(map[fieldKey][]models.DataSource, len(rows))
		logicalIDs := make(map[uint]struct{}, len(rows))
		for i := range rows {
			k := fieldKey{rows[i].DeviceID, rows[i].Category}
			if _, ok := byKey[k]; !ok {
				byKey[k] = &sourceOverviewItem{
					LogicalDeviceID: rows[i].DeviceID,
					Category:        rows[i].Category,
				}
				order = append(order, k)
			}
			membersOfKey[k] = append(membersOfKey[k], rows[i])
			logicalIDs[rows[i].DeviceID] = struct{}{}
		}
		if len(order) == 0 {
			c.JSON(http.StatusOK, gin.H{"code": 0, "data": sourceOverviewResponse{
				Items: []sourceOverviewItem{}, Total: 0, GeneratedAt: time.Now(),
			}})
			return
		}

		// 逻辑设备名（一次查询）。
		logicalNames := make(map[uint]string, len(logicalIDs))
		{
			ids := make([]uint, 0, len(logicalIDs))
			for id := range logicalIDs {
				ids = append(ids, id)
			}
			var lds []models.LogicalDevice
			db.Select("id, name").Where("id IN ?", ids).Find(&lds)
			for _, l := range lds {
				logicalNames[l.ID] = l.Name
			}
		}

		categories := make([]string, 0, len(order))
		seenCat := make(map[string]struct{}, len(order))
		for _, k := range order {
			if _, ok := seenCat[k.category]; ok {
				continue
			}
			seenCat[k.category] = struct{}{}
			categories = append(categories, k.category)
		}

		// 声明层：中文描述 + 语义类型。
		metaByCat := make(map[string]models.SensorFieldMeta, len(categories))
		if db.Migrator().HasTable(&models.SensorFieldMeta{}) {
			var metas []models.SensorFieldMeta
			if err := db.Where("sensor_name IN ?", categories).Find(&metas).Error; err == nil {
				for _, m := range metas {
					metaByCat[m.SensorName] = m
				}
			}
		}

		// 事实层：实际观测到的单位。
		unitByCat := make(map[string]string, len(categories))
		{
			type unitRow struct {
				SensorName string
				Unit       string
			}
			var urows []unitRow
			if err := db.Model(&models.SensorFieldObs{}).
				Select("sensor_name, MAX(unit) AS unit").
				Where("sensor_name IN ? AND unit <> ''", categories).
				Group("sensor_name").Scan(&urows).Error; err == nil {
				for _, u := range urows {
					unitByCat[u.SensorName] = u.Unit
				}
			}
		}

		items := make([]sourceOverviewItem, 0, len(order))
		now := time.Now()
		for _, k := range order {
			it := byKey[k]
			it.LogicalDeviceName = logicalNames[k.deviceID]

			if m, ok := metaByCat[k.category]; ok {
				it.LabelZh = m.LabelZh
				it.ValueType = m.ValueType
			}
			it.Unit = unitByCat[k.category]

			// 实时值：优先取台账里 active 的那个成员（主备组只有它在供数）；
			// 单设备时它就是唯一成员。
			//
			// 为什么要挑 active 而不是"第一个"：主备组的备机可能早已离线，
			// 取它的值会显示一个停在几小时前的读数，看起来像"这个字段在变"。
			members := membersOfKey[k]
			pick := members[0]
			for _, m := range members {
				if m.Status == datasource.StatusActive {
					pick = m
					break
				}
			}
			if rec, hit := LatestValueBySensor(pick.EdgeDeviceID, k.category); hit {
				v := rec.Value
				ts := rec.Timestamp
				it.LatestValue = &v
				it.UpdatedAt = &ts
				age := int(now.Sub(ts).Seconds())
				if age < 0 {
					age = 0
				}
				it.AgeSec = &age
			}
			it.Status = deriveFreshness(it.AgeSec)

			// 类型兜底：没登记时按观测值推断，避免整列空白。
			if it.ValueType == "" {
				if it.LatestValue != nil {
					it.ValueType = inferValueType(*it.LatestValue)
				} else {
					it.ValueType = "number" // 无读数时按最常见的数值型标注
				}
			}
			items = append(items, *it)
		}

		c.JSON(http.StatusOK, gin.H{"code": 0, "data": sourceOverviewResponse{
			Items: items, Total: len(items), GeneratedAt: now,
		}})
	}
}

// registerSourceOverviewRoute 注册字段目录路由（**只读**）。
//
// 显式注册写方法并返回 405：gin 的 HandleMethodNotAllowed 默认 false，
// 未注册的方法会返回 404 —— 而这里的语义是"端点存在、只允许 GET"，
// 405 才准确（404 会让人以为路径写错）。
func registerSourceOverviewRoute(ds *gin.RouterGroup, db *gorm.DB) {
	ds.GET("/overview", listSourceOverview(db))
	ds.POST("/overview", rejectOverviewWrite)
	ds.PUT("/overview", rejectOverviewWrite)
	ds.DELETE("/overview", rejectOverviewWrite)
	ds.PATCH("/overview", rejectOverviewWrite)
}

// rejectOverviewWrite 是只读端点的写方法拒绝器。
func rejectOverviewWrite(c *gin.Context) {
	c.Header("Allow", "GET")
	c.JSON(http.StatusMethodNotAllowed, gin.H{
		"code":    "source_overview_read_only",
		"message": "字段目录是只读视图；来源组请在「来源组」页维护",
	})
}
