package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
)

// registerLogicalDeviceRoutes 注册逻辑设备管理端点 (方案 v3.3 §九):
//
//	GET  /logical-devices                 管理列表
//	POST /logical-devices/merge           多源合并 (乐观占位, 返回 job_ids)
//	POST /logical-devices/merge/preview   合并预览 (时间轴/重叠/数据量)
//	GET  /logical-devices/merge-jobs/:id  合并搬迁进度
//	PUT  /logical-devices/:id             改 name / retention_days
func registerLogicalDeviceRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	g := v1.Group("/logical-devices")

	// GET /logical-devices — 管理列表 (§3.4)。每项含实例数 (Unscoped 含已删)、
	// 数据量估算 (§1.3 降级语义: 超时省略 row_estimate)、最后数据时间
	// (MAX(timestamp) 索引扫描)、保留天数。
	g.GET("", func(c *gin.Context) {
		var devices []models.LogicalDevice
		if err := db.Order("id").Find(&devices).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		type listItem struct {
			models.LogicalDevice
			InstanceCount int64   `json:"instance_count"`
			RowEstimate   *int64  `json:"row_estimate,omitempty"`
			LastDataAt    *string `json:"last_data_at,omitempty"`
		}
		items := make([]listItem, 0, len(devices))
		for _, ld := range devices {
			item := listItem{LogicalDevice: ld}
			if count, err := datalifecycle.CountInstances(db, ld.ID); err == nil {
				item.InstanceCount = count
			}
			// 估算段挂端点级超时兜底 (T1.1 模式, §1.3)。
			estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
			if rows, ok := datalifecycle.EstimateRowCount(estCtx, db, ld.ID); ok {
				item.RowEstimate = &rows
			}
			cancel()
			if scope, err := datalifecycle.ResolveScope(db, ld.ID); err == nil {
				if _, last, terr := datalifecycle.ScopeTimeRange(c.Request.Context(), db, scope); terr == nil && last != nil {
					s := last.UTC().Format("2006-01-02T15:04:05Z")
					item.LastDataAt = &s
				}
			}
			items = append(items, item)
		}
		Success(c, gin.H{"items": items, "total": len(items)})
	})

	// POST /logical-devices/merge — §3.4 单事务合并。
	// 409 响应体带结构化 conflicts 列表 (D-1):
	//   { "code": 409, "message": "...", "conflicts": [...] }
	g.POST("/merge", func(c *gin.Context) {
		var req struct {
			TargetName string `json:"target_name"`
			SourceIDs  []uint `json:"source_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		result, err := datalifecycle.MergeDevices(db, &datalifecycle.MergeRequest{
			TargetName: req.TargetName,
			SourceIDs:  req.SourceIDs,
		})
		if err != nil {
			var verr *datalifecycle.MergeValidationError
			if errors.As(err, &verr) {
				c.JSON(http.StatusConflict, gin.H{
					"code":      http.StatusConflict,
					"message":   verr.Message,
					"conflicts": verr.Conflicts,
				})
				return
			}
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		SuccessWithCodeMsg(c, http.StatusCreated, gin.H{
			"target_id": result.TargetID,
			"job_ids":   result.JobIDs,
		}, "merge started")
	})

	// POST /logical-devices/merge/preview — §3.4 合并预览。
	g.POST("/merge/preview", func(c *gin.Context) {
		var req struct {
			SourceIDs []uint `json:"source_ids"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		preview, err := datalifecycle.PreviewMerge(c.Request.Context(), db, req.SourceIDs)
		if err != nil {
			Error(c, http.StatusBadRequest, err.Error())
			return
		}
		Success(c, preview)
	})

	// GET /logical-devices/merge-jobs/:id — §3.4 搬迁进度。
	g.GET("/merge-jobs/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			Error(c, http.StatusBadRequest, "invalid job id")
			return
		}
		var job models.MergeJob
		if err := db.First(&job, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "merge job not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		Success(c, job)
	})

	// PUT /logical-devices/:id — 改 name / retention_days (identity_key
	// 创建后只读, §1.1)。
	g.PUT("/:id", func(c *gin.Context) {
		id, err := strconv.Atoi(c.Param("id"))
		if err != nil || id <= 0 {
			Error(c, http.StatusBadRequest, "invalid id")
			return
		}
		var ld models.LogicalDevice
		if err := db.First(&ld, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "logical device not found")
				return
			}
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		var dto struct {
			Name          *string `json:"name"`
			RetentionDays *int    `json:"retention_days"`
		}
		if err := c.ShouldBindJSON(&dto); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		updates := map[string]interface{}{}
		if dto.Name != nil {
			updates["name"] = *dto.Name
		}
		if dto.RetentionDays != nil {
			if *dto.RetentionDays <= 0 {
				Error(c, http.StatusBadRequest, "retention_days must be positive")
				return
			}
			updates["retention_days"] = *dto.RetentionDays
		}
		if len(updates) == 0 {
			Error(c, http.StatusBadRequest, "nothing to update")
			return
		}
		if err := db.Model(&models.LogicalDevice{}).Where("id = ?", ld.ID).
			Updates(updates).Error; err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}
		db.First(&ld, ld.ID)
		Success(c, ld)
	})
}
