package api

import (
	"errors"
	"net/http"
	"strconv"

	"ehome/backend/internal/datasource"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
)

// 数据源主备 HTTP 层（设计/数据源主备与故障转移.md v1.0 §7 冻结契约）。
// 领域逻辑全部在 internal/datasource；本文件只做参数解析、错误映射与 envelope。

const dataSourceServiceUnavailableMsg = "data source service unavailable"

// createDataSourceRequest POST /data-sources 的独立 DTO。
// 不直接绑定 models.DataSource：防止客户端注入 status/fail_count/created_at 等
// 由领域层拥有的字段（参照本仓 PUT /nodes 的 DTO 做法）。
type createDataSourceRequest struct {
	DeviceID     uint   `json:"device_id"`
	Category     string `json:"category"`
	EdgeDeviceID uint   `json:"edge_device_id"`
	SourceType   string `json:"source_type"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	Priority     int    `json:"priority"`
	IsPrimary    bool   `json:"is_primary"`
	MaxFailCount int    `json:"max_fail_count"`
	Config       string `json:"config"`
}

// updateDataSourceRequest PUT /data-sources/:id 的白名单 DTO。
// 指针字段区分“未传”与“传零值”；device_id/category/edge_device_id/source_type/status
// 不在白名单内，客户端提交也不会被采纳。
type updateDataSourceRequest struct {
	Name         *string `json:"name"`
	Description  *string `json:"description"`
	Priority     *int    `json:"priority"`
	IsPrimary    *bool   `json:"is_primary"`
	MaxFailCount *int    `json:"max_fail_count"`
	Config       *string `json:"config"`
}

// registerDataSourceRoutes 注册 /api/v1/data-sources 下的 9 条契约路由。
func registerDataSourceRoutes(ds *gin.RouterGroup, svc *datasource.Service) {
	ds.GET("", listDataSources(svc))
	ds.GET("/:id", getDataSource(svc))
	ds.POST("", createDataSource(svc))
	ds.PUT("/:id", updateDataSource(svc))
	ds.DELETE("/:id", deleteDataSource(svc))
	ds.POST("/:id/activate", activateDataSource(svc))
	ds.POST("/:id/deactivate", deactivateDataSource(svc))
	ds.POST("/:id/reset", resetDataSource(svc))
	ds.GET("/:id/health", getDataSourceHealth(svc))
}

// registerFailoverLogRoutes 注册 /api/v1/devices/:id/failover-logs。
// 该端点已从 handler_data.go 的占位实现移交数据源域，复用同一 datasource.Service。
func registerFailoverLogRoutes(v1 *gin.RouterGroup, svc *datasource.Service) {
	v1.GET("/devices/:id/failover-logs", listFailoverLogs(svc))
}

// registerDataSourceUnavailable 在领域服务缺失（如测试未注入 option）时给出显式 503，
// 保证既有的 SetupRoutes 调用不会因 nil 解引用 panic。
func registerDataSourceUnavailable(ds *gin.RouterGroup) {
	h := func(c *gin.Context) { Error(c, http.StatusServiceUnavailable, dataSourceServiceUnavailableMsg) }
	ds.GET("", h)
	ds.GET("/:id", h)
	ds.POST("", h)
	ds.PUT("/:id", h)
	ds.DELETE("/:id", h)
	ds.POST("/:id/activate", h)
	ds.POST("/:id/deactivate", h)
	ds.POST("/:id/reset", h)
	ds.GET("/:id/health", h)
}

// registerFailoverLogUnavailable 服务缺失时的 failover-logs 占位（503）。
func registerFailoverLogUnavailable(v1 *gin.RouterGroup) {
	v1.GET("/devices/:id/failover-logs", func(c *gin.Context) {
		Error(c, http.StatusServiceUnavailable, dataSourceServiceUnavailableMsg)
	})
}

// parseDataSourcePathID 解析 :id 路径参数；非法或为 0 时写 400 并返回 false。
func parseDataSourcePathID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return uint(id), true
}

// parseLimitQuery 解析 limit；缺省/非法/越界都交给领域层 clamp，不报错。
func parseLimitQuery(c *gin.Context) int {
	if raw := c.Query("limit"); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil {
			return v
		}
	}
	return 0
}

// writeDataSourceError 把领域哨兵错误映射到契约状态码。
// 非哨兵错误只回通用文案，原始错误写日志，避免泄露 SQL/内部细节。
func writeDataSourceError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, datasource.ErrNotFound):
		Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, datasource.ErrConflict):
		Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, datasource.ErrInvalidRequest):
		Error(c, http.StatusBadRequest, err.Error())
	default:
		logger.Warn("api: data source request failed", "error", err)
		Error(c, http.StatusInternalServerError, "internal error")
	}
}

// GET /api/v1/data-sources
func listDataSources(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := datasource.ListFilter{
			Category: c.Query("category"),
			Status:   c.Query("status"),
		}
		if raw := c.Query("device_id"); raw != "" {
			id, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				Error(c, http.StatusBadRequest, "invalid device_id")
				return
			}
			filter.DeviceID = uint(id)
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		filter.Page = page
		filter.PageSize = pageSize

		items, total, err := svc.List(filter)
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		// items 由 List 保证为非 nil 切片，空集序列化为 [] 而非 null。
		Success(c, gin.H{"items": items, "total": total})
	}
}

// GET /api/v1/data-sources/:id
func getDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		item, err := svc.Get(id)
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, item)
	}
}

// POST /api/v1/data-sources
func createDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createDataSourceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		item, err := svc.Create(datasource.CreateInput{
			DeviceID:     req.DeviceID,
			EdgeDeviceID: req.EdgeDeviceID,
			Category:     req.Category,
			Name:         req.Name,
			Description:  req.Description,
			SourceType:   req.SourceType,
			Config:       req.Config,
			Priority:     req.Priority,
			MaxFailCount: req.MaxFailCount,
			IsPrimary:    req.IsPrimary,
		})
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		SuccessWithCode(c, http.StatusCreated, item)
	}
}

// PUT /api/v1/data-sources/:id
func updateDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		var req updateDataSourceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		item, err := svc.Update(id, datasource.UpdateInput{
			Name:         req.Name,
			Description:  req.Description,
			Config:       req.Config,
			Priority:     req.Priority,
			MaxFailCount: req.MaxFailCount,
			IsPrimary:    req.IsPrimary,
		})
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, item)
	}
}

// DELETE /api/v1/data-sources/:id
func deleteDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		if err := svc.Delete(id); err != nil {
			writeDataSourceError(c, err)
			return
		}
		SuccessMsg(c, nil, "deleted")
	}
}

// POST /api/v1/data-sources/:id/activate
func activateDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		item, err := svc.Activate(id)
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, item)
	}
}

// POST /api/v1/data-sources/:id/deactivate
func deactivateDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		item, err := svc.Deactivate(id)
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, item)
	}
}

// POST /api/v1/data-sources/:id/reset
func resetDataSource(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		item, err := svc.Reset(id)
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, item)
	}
}

// GET /api/v1/data-sources/:id/health
func getDataSourceHealth(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		items, err := svc.Health(id, parseLimitQuery(c))
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, items)
	}
}

// GET /api/v1/devices/:id/failover-logs?category=&limit=
func listFailoverLogs(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceID, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		items, err := svc.FailoverLogs(deviceID, c.Query("category"), parseLimitQuery(c))
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		Success(c, items)
	}
}
