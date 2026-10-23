package api

import (
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

		items := make([]sourceOverviewItem, 0, len(order))
		for _, k := range order {
			it := byGroup[k]
			it.LogicalDeviceName = logicalNames[k.deviceID]
			it.MemberCount = len(it.Members)
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
