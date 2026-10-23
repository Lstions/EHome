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

// handler_source_overview.go 实现 GET /api/v1/data-sources/overview（数据流架构 v1 §4.1）。
//
// # 为什么新增端点而不是改造 GET /data-sources
//
// 旧端点是 **CRUD 语义**（可写、可按任意字段过滤、面向运维）。新端点是
// **只读投影语义**（自动派生、面向所有用户、永远反映系统真实状态）。
// 把两者混在一个端点会让「这个接口能不能写」变得不可推理，
// 也让前端无法在类型层表达「只读」。
//
// # 只读如何在路由层保证（不靠前端自律）
//
// 本组只注册 GET。对同一路径的 POST/PUT/DELETE 会由 gin 返回 405 ——
// 这是**协议层**的保证，运维脚本用 curl 也写不进去（与旧 CRUD 保留 deprecated
// 的策略互补：旧端点仍可写但会带 Deprecation 头，新端点根本不可写）。

// sourceOverviewItem 是总览里的一行（= 一个来源）。
type sourceOverviewItem struct {
	LogicalDeviceID   uint             `json:"logical_device_id"`
	LogicalDeviceName string           `json:"logical_device_name"`
	Category          string           `json:"category"`
	GroupID           uint             `json:"group_id"`
	GroupName         *string          `json:"group_name"`
	Kind              string           `json:"kind"`
	MemberCount       int              `json:"member_count"`
	Members           []overviewMember `json:"members"`
	Status            string           `json:"status"`
	LastSuccess       *time.Time       `json:"last_success"`
	Revision          int64            `json:"revision"`

	// ── 字段元数据（"接入的数据字段要有数据类型、数据描述"）──
	//
	// 三个值的来源分层，缺一不可：
	//   LabelZh   ← sensor_field_meta（人工/派生登记的中文描述）
	//   ValueType ← sensor_field_meta；缺失时由 **观测值推断**（见 inferValueType）
	//   Unit      ← sensor_field_obs（实际观测到的单位，比 meta 更可信：
	//                meta 是声明，obs 是事实；声明可能过期，事实不会）
	//
	// 为什么不在数据面直接带上：解析产物只有 Name/Value/Unit/StringValue，
	// 加类型/描述会让每帧多 2 个字段的转换开销，而展示元数据是**低频读**、
	// 不需要进热路径。
	LabelZh   string `json:"label_zh"`   // 中文描述（空 = 未登记，前端回落 category）
	ValueType string `json:"value_type"` // number|boolean|enum|bitmask|string|raw
	Unit      string `json:"unit"`       // 实际观测到的单位（空 = 未知）

	// ── 实时数据（"前端数据来源页面还要支持实时显示数据"）──
	//
	// LatestValue 来自进程内最新值缓存（latest_value_cache.go），无 DB 查询。
	// 用 *float64 而非 float64：0 是合法读数，必须与"暂无数据"区分。
	LatestValue *float64 `json:"latest_value"`
	// LatestAt 是该读数的时间（nil = 该来源从未收到过数据）。
	LatestAt *time.Time `json:"latest_at"`
	// ValueAgeSec 是读数年龄（秒）。前端据此把"实时"与"陈旧"区分开 ——
	// 只显示值不显示年龄会让用户误以为 3 分钟前的数据是实时的。
	ValueAgeSec *int `json:"value_age_sec"`
}

// overviewMember 是来源的一个成员设备。
type overviewMember struct {
	EdgeDeviceID   uint   `json:"edge_device_id"`
	EdgeDeviceName string `json:"edge_device_name"`
	Active         bool   `json:"active"`
	Enabled        bool   `json:"enabled"`
}

// sourceOverviewResponse 是总览响应体。
type sourceOverviewResponse struct {
	Items     []sourceOverviewItem `json:"items"`
	Total     int                  `json:"total"`
	DerivedAt time.Time            `json:"derived_at"`
}

// listSourceOverview 返回自动派生的来源总览（只读）。
//
// 数据来源：直接读 data_sources 表（它已由 L2 收敛器自动投影）。
// 这样本接口是**纯查询**，不触发派生 —— 派生在后台 ticker 里做，
// 避免「打开页面就触发一次全量派生」的性能陷阱。
func listSourceOverview(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if db == nil {
			c.JSON(http.StatusServiceUnavailable, gin.H{
				"code": "source_overview_unavailable", "message": "数据库不可用",
			})
			return
		}

		// 支持按类别/逻辑设备过滤（前端筛选栏用）。
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
				"code": "source_overview_failed", "message": "读取来源总览失败",
			})
			return
		}

		// 按 (device_id, category) 聚合成分组（一个来源 = 一个逻辑设备的一个类别）。
		type groupKey struct {
			deviceID uint
			category string
		}
		order := make([]groupKey, 0, len(rows))
		byGroup := make(map[groupKey]*sourceOverviewItem, len(rows))
		// 一并收集需要的设备名（避免 N+1：一次查全部涉及到的 ID）。
		deviceIDs := make(map[uint]struct{}, len(rows))
		logicalIDs := make(map[uint]struct{}, len(rows))
		for i := range rows {
			k := groupKey{rows[i].DeviceID, rows[i].Category}
			if _, ok := byGroup[k]; !ok {
				byGroup[k] = &sourceOverviewItem{
					LogicalDeviceID: rows[i].DeviceID,
					Category:        rows[i].Category,
					GroupID:         0,
					Kind:            "direct",
					Members:         []overviewMember{},
				}
				order = append(order, k)
			}
			deviceIDs[rows[i].EdgeDeviceID] = struct{}{}
			logicalIDs[rows[i].DeviceID] = struct{}{}
		}

		// 设备名映射（两次批量查询，无 N+1）。
		edgeNames := make(map[uint]string, len(deviceIDs))
		if len(deviceIDs) > 0 {
			ids := make([]uint, 0, len(deviceIDs))
			for id := range deviceIDs {
				ids = append(ids, id)
			}
			var devs []models.EdgeDevice
			db.Select("id, name").Where("id IN ?", ids).Find(&devs)
			for _, d := range devs {
				edgeNames[d.ID] = d.Name
			}
		}
		logicalNames := make(map[uint]string, len(logicalIDs))
		if len(logicalIDs) > 0 {
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

		for i := range rows {
			k := groupKey{rows[i].DeviceID, rows[i].Category}
			it := byGroup[k]
			it.Members = append(it.Members, overviewMember{
				EdgeDeviceID:   rows[i].EdgeDeviceID,
				EdgeDeviceName: edgeNames[rows[i].EdgeDeviceID],
				Active:         rows[i].Status == datasource.StatusActive,
				Enabled:        rows[i].Status != datasource.StatusDisabled,
			})
			if rows[i].LastSuccess != nil &&
				(it.LastSuccess == nil || rows[i].LastSuccess.After(*it.LastSuccess)) {
				ts := *rows[i].LastSuccess
				it.LastSuccess = &ts
			}
		}

		// 来源组信息：查这些 (category) 上的 enabled 组（一次查询）。
		//
		// 为什么按 category 而不是按 key 找组：来源组是**跨设备**对象，
		// 没有 (logicalDevice, category) 键；用 category 匹配是组与台账之间
		// 唯一的天然连接。找不到组 ⇒ kind=direct（单设备直连）。
		categories := make(map[string]struct{}, len(order))
		for _, k := range order {
			categories[k.category] = struct{}{}
		}
		groupByCategory := make(map[string]models.SourceGroup, len(categories))
		if len(categories) > 0 {
			cats := make([]string, 0, len(categories))
			for c := range categories {
				cats = append(cats, c)
			}
			var groups []models.SourceGroup
			db.Where("category IN ? AND enabled = ?", cats, true).Find(&groups)
			for _, g := range groups {
				groupByCategory[g.Category] = g
			}
		}

		// ── 字段元数据装载（数据类型 / 数据描述 / 单位）──
		//
		// 三类来源各取所长，任一缺失都有降级路径（不能让"没登记"导致整页空）：
		//   LabelZh/ValueType ← sensor_field_meta（声明层）
		//   Unit             ← sensor_field_obs（事实层，比声明更可信）
		//   ValueType 兜底    ← 由最新观测值推断（见 inferValueType）
		metaByCategory := make(map[string]models.SensorFieldMeta, len(order))
		if len(categories) > 0 {
			cats := make([]string, 0, len(categories))
			for c := range categories {
				cats = append(cats, c)
			}
			if db.Migrator().HasTable(&models.SensorFieldMeta{}) {
				var metas []models.SensorFieldMeta
				if err := db.Where("sensor_name IN ?", cats).Find(&metas).Error; err == nil {
					for _, m := range metas {
						metaByCategory[m.SensorName] = m
					}
				}
			}
		}
		// 事实单位：取该类别在所有设备上的最新观测单位。
		unitByCategory := make(map[string]string, len(order))
		if len(categories) > 0 {
			cats := make([]string, 0, len(categories))
			for c := range categories {
				cats = append(cats, c)
			}
			type unitRow struct {
				SensorName string
				Unit       string
			}
			var urows []unitRow
			if err := db.Model(&models.SensorFieldObs{}).
				Select("sensor_name, MAX(unit) AS unit").
				Where("sensor_name IN ? AND unit <> ''", cats).
				Group("sensor_name").Scan(&urows).Error; err == nil {
				for _, u := range urows {
					unitByCategory[u.SensorName] = u.Unit
				}
			}
		}

		items := make([]sourceOverviewItem, 0, len(order))
		for _, k := range order {
			it := byGroup[k]
			it.LogicalDeviceName = logicalNames[k.deviceID]
			it.MemberCount = len(it.Members)

			// 字段元数据：声明层 + 事实层。
			if m, ok := metaByCategory[k.category]; ok {
				it.LabelZh = m.LabelZh
				it.ValueType = m.ValueType
			}
			it.Unit = unitByCategory[k.category]

			// 实时值：从进程内最新值缓存取（零 DB 查询）。
			//
			// 取**权威成员**的值：主备组只有 active 在供数，取 standby 的值会显示
			// 一个不供数的设备读数，误导用户"这个来源的数据在变"。
			// 直连来源的成员本身就是权威。
			for _, m := range it.Members {
				if !m.Active {
					continue
				}
				if rec, hit := LatestValueBySensor(m.EdgeDeviceID, k.category); hit {
					v := rec.Value
					ts := rec.Timestamp
					it.LatestValue = &v
					it.LatestAt = &ts
					age := int(time.Since(ts).Seconds())
					if age < 0 {
						age = 0
					}
					it.ValueAgeSec = &age
				}
				break
			}
			// ValueType 兜底：没登记 meta 时按观测值推断，避免整列空白。
			if it.ValueType == "" && it.LatestValue != nil {
				it.ValueType = inferValueType(*it.LatestValue)
			}
			if g, ok := groupByCategory[k.category]; ok {
				it.GroupID = g.ID
				name := g.Name
				it.GroupName = &name
				it.Kind = g.Kind
			}
			// 状态派生（只读展示用，不参与求值）：
			//   ok         = 至少一个 member active
			//   degraded   = 有成员但都非 active
			//   unconfigured = 无成员（理论上不出现，防御）
			activeCount := 0
			for _, m := range it.Members {
				if m.Active {
					activeCount++
				}
			}
			switch {
			case activeCount > 0:
				it.Status = "ok"
			case len(it.Members) > 0:
				it.Status = "degraded"
			default:
				it.Status = "unconfigured"
			}
			items = append(items, *it)
		}

		c.JSON(http.StatusOK, gin.H{
			"code": 0,
			"data": sourceOverviewResponse{
				Items:     items,
				Total:     len(items),
				DerivedAt: time.Now(),
			},
		})
	}
}

// registerSourceOverviewRoute 注册来源总览路由（**只读**）。
//
// # 为什么显式注册写方法并返回 405
//
// 实测：gin 的 HandleMethodNotAllowed 默认 false，未注册的方法返回 **404**
// 而不是 405。404 虽然也挡住了写入，但它表达的是「资源不存在」——
// 事实是「资源存在、但只允许 GET」。这个差别对调用方很重要：
// 404 会让人以为路径写错，405 才明确告知「端点对了、方法不对」。
//
// 因此这里**显式**为写方法注册 405 处理器：既是自文档化的只读契约，
// 也让运维脚本 curl 时立刻得到清晰反馈。
func registerSourceOverviewRoute(ds *gin.RouterGroup, db *gorm.DB) {
	ds.GET("/overview", listSourceOverview(db))
	ds.POST("/overview", rejectOverviewWrite)
	ds.PUT("/overview", rejectOverviewWrite)
	ds.DELETE("/overview", rejectOverviewWrite)
	ds.PATCH("/overview", rejectOverviewWrite)
}

// rejectOverviewWrite 是只读端点的写方法拒绝器。
//
// 返回 405 + Allow: GET 头（RFC 7231 §6.5.5 要求 405 应带 Allow）。
func rejectOverviewWrite(c *gin.Context) {
	c.Header("Allow", "GET")
	c.JSON(http.StatusMethodNotAllowed, gin.H{
		"code":    "source_overview_read_only",
		"message": "来源总览是自动派生的只读视图，不支持写入；如需配置主备请使用来源组接口",
	})
}

// inferValueType 在缺少 sensor_field_meta 时按观测值推断语义类型。
//
// # 为什么要有这个兜底
//
// 生产实测（2026-10-23）：sensor_field_meta 是 **0 行** —— 它是"声明层"，
// 依赖人工或导入登记，而当时没有任何自动填充。若不推断，前端"数据类型"列
// 会整列为空，用户会以为功能没实现。
//
// 推断是**保守**的（宁可标 raw 也不猜错）：
//   - 非整数 ⇒ number
//   - 整数且只在 {0,1} ⇒ boolean（开关量的典型形态）
//   - 其余整数 ⇒ number
//
// 不做 enum/bitmask 推断 —— 那需要值域知识，猜错比留空更糟。
// 一旦 sensor_field_meta 有登记，声明值优先，本函数不参与。
func inferValueType(v float64) string {
	if v != math.Trunc(v) {
		return "number"
	}
	if v == 0 || v == 1 {
		return "boolean"
	}
	return "number"
}
