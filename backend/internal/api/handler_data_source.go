package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
	"ehome/backend/internal/telemetry"
	"ehome/backend/pkg/logger"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 数据源主备 HTTP 层（设计/数据源主备与故障转移.md v1.0 §7 冻结契约）。
// 领域逻辑全部在 internal/datasource；本文件只做参数解析、错误映射与 envelope。

const dataSourceServiceUnavailableMsg = "data source service unavailable"

// createDataSourceRequest POST /data-sources 的独立 DTO。
// 不直接绑定 models.DataSource：防止客户端注入 status/fail_count/created_at 等
// 由领域层拥有的字段（参照本仓 PUT /nodes 的 DTO 做法）。
type createDataSourceRequest struct {
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

// registerDataSourceRoutes 注册 /api/v1/data-sources 下的**只读**路由。
//
// 2026-10-24（遗留处置方案 A，见 .analysis/review-removal/legacy-issue-analysis.md）：
// 原来还有 6 条写路由（POST/PUT/DELETE/activate/deactivate/reset），**全部删除**。理由：
//
//  1. data_sources 是**自动派生**的台账（投影器按「设备 × 观测字段」维护，
//     managed_by=auto），用户手动创建的只有**来源组**（SourceGroup）。
//  2. 组键二元化（edge_device_id, category）后唯一索引 uniq_ds_source 使
//     「同组多行 standby 候选」在数据模型层不可能存在 ⇒ Deactivate 恒 409
//     （100% 必失败，不是偶发）；Reset 要求的 status='error' 无任何生产写入者
//     （熔断状态机 M3 已删）⇒ 恒 400。留着它们 = 留着「看起来能用、永远失败」的陷阱。
//  3. Delete 删掉的 auto 行会在下一个 60s ticker 被投影器以**新 id** 重建；
//     手工改 status 也会被覆盖（25–37s）⇒ 写操作对用户既不生效也不可预期。
//  4. data_sources **没有任何数据面消费者**（规则读 automation_rules.source_group_id，
//     告警读 alert_rules.*）⇒ 手工切主备即使成功也不影响实际供数。
//  5. 需要手工干预「谁在供数」时，能力已在 source_groups 完整具备且真接入 UI
//     （SourceGroupList.vue 调 activateGroup）。
//
// **仍在**的路由：GET 列表 / GET 详情 / GET health，以及 POST 创建 / PUT 更新。
//
// 为什么 POST/PUT 保留而上面四个删除：它们是**可用**的（创建出的行 managed_by=manual，
// 投影器永不覆盖），只是当前没有 UI 入口。删除它们会把「数据修正」这条后路也关掉；
// 而四个写端点的问题是**根本不可能成功或结果不可预期**，性质不同。
// 若日后确认也不需要手工建行，可一并删除（属独立决策，不在本次方案 A 范围）。
//
// db 参数保留：GET 列表仍需要它做 category 过滤与设备名回填。
func registerDataSourceRoutes(ds *gin.RouterGroup, svc *datasource.Service, db *gorm.DB) {
	ds.GET("", listDataSources(svc))
	ds.GET("/:id", getDataSource(svc))
	ds.GET("/:id/health", getDataSourceHealth(svc))
	ds.POST("", createDataSource(svc, db))
	ds.PUT("/:id", updateDataSource(svc))
}

// registerFailoverLogRoutes 注册 /api/v1/edge-devices/:id/failover-logs。
// 该端点已从 handler_data.go 的占位实现移交数据源域，复用同一 datasource.Service。
//
// 2026-10-23（契约 §5/§6）: 路径从 /devices/:id 改到 /edge-devices/:id，且入参
// group 已是 `/api/v1/edge-devices` 这一级 —— 本函数只负责拼 `/:id/failover-logs`。
// 语义也随之前移: :id 是**边缘设备 id**，不再是逻辑设备 id。
func registerFailoverLogRoutes(e *gin.RouterGroup, svc *datasource.Service) {
	e.GET("/:id/failover-logs", listFailoverLogs(svc))
}

// registerDataSourceUnavailable 在领域服务缺失（如测试未注入 option）时给出显式 503，
// 保证既有的 SetupRoutes 调用不会因 nil 解引用 panic。
func registerDataSourceUnavailable(ds *gin.RouterGroup) {
	h := func(c *gin.Context) { Error(c, http.StatusServiceUnavailable, dataSourceServiceUnavailableMsg) }
	// 与 registerDataSourceRoutes 保持一致（4 个已删的手工运维端点不注册）。
	ds.GET("", h)
	ds.GET("/:id", h)
	ds.GET("/:id/health", h)
	ds.POST("", h)
	ds.PUT("/:id", h)
}

// registerFailoverLogUnavailable 服务缺失时的 failover-logs 占位（503）。
// 入参 group 与 registerFailoverLogRoutes 一致（已是 /api/v1/edge-devices）。
func registerFailoverLogUnavailable(e *gin.RouterGroup) {
	e.GET("/:id/failover-logs", func(c *gin.Context) {
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
		// 过滤参数 = edge_device_id（组键的一半）。旧参数 device_id 曾是逻辑设备
		// id，逻辑层删除后它不再有第二个 id 空间可表达（契约 §6）。
		if raw := c.Query("edge_device_id"); raw != "" {
			id, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				Error(c, http.StatusBadRequest, "invalid edge_device_id")
				return
			}
			filter.EdgeDeviceID = uint(id)
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
func createDataSource(svc *datasource.Service, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createDataSourceRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		// 保存期 category 校验（一期只字面量 + 是否上报过；特殊 selector 拒绝）。
		if code, msg := validateDataSourceCategory(db, req.EdgeDeviceID, req.Category); msg != "" {
			ErrorWithCode(c, http.StatusBadRequest, code, msg)
			return
		}
		item, err := svc.Create(datasource.CreateInput{
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
// dataSourceHealthItem 是 GET /data-sources/:id/health 的响应项（API 层 DTO）。
//
// 为什么不直接序列化 models.DataSourceHealth：模型的字段名仍是 `DeviceID`
// → json `device_id`，那是逻辑设备 id 时代的命名。逻辑身份层删除后该列的取值
// 是**边缘设备 id**（见 datasource.Service.recordHealth 注释：列名保留为历史
// 命名），继续对外发 `device_id` 会让前端以为还有第二个 id 空间。
//
// 因此在 API 层做一次显式字段映射（模型不改，列名不动 —— 那是数据层的事）。
type dataSourceHealthItem struct {
	ID           uint      `json:"id"`
	SourceID     uint      `json:"source_id"`
	EdgeDeviceID uint      `json:"edge_device_id"`
	Category     string    `json:"category"`
	Status       string    `json:"status"`
	Message      string    `json:"message"`
	ResponseTime int       `json:"response_time"`
	CreatedAt    time.Time `json:"created_at"`
}

// failoverLogItem 是 GET /edge-devices/:id/failover-logs 的响应项（API 层 DTO）。
// 与 dataSourceHealthItem 同理由：`device_id` 在逻辑层删除后语义即 edge_device_id。
type failoverLogItem struct {
	ID           uint      `json:"id"`
	EdgeDeviceID uint      `json:"edge_device_id"`
	Category     string    `json:"category"`
	FromSourceID uint      `json:"from_source_id"`
	ToSourceID   uint      `json:"to_source_id"`
	Reason       string    `json:"reason"`
	Trigger      string    `json:"trigger"`
	CreatedAt    time.Time `json:"created_at"`
}

// GET /api/v1/data-sources/:id/health
func getDataSourceHealth(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		rows, err := svc.Health(id, parseLimitQuery(c))
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		items := make([]dataSourceHealthItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, dataSourceHealthItem{
				ID:           r.ID,
				SourceID:     r.SourceID,
				EdgeDeviceID: r.DeviceID,
				Category:     r.Category,
				Status:       r.Status,
				Message:      r.Message,
				ResponseTime: r.ResponseTime,
				CreatedAt:    r.CreatedAt,
			})
		}
		Success(c, items)
	}
}

// validateDataSourceCategory 是 POST /data-sources 的保存期校验（方案 v0.3 §9.2#8 / §9.4）。
//
// 一期缩范围（评审 C5 裁决）：data_sources.category **只支持字面量物理名**：
//  1. 特殊 selector（@semantic / virtual:x / prefix_*）一律拒绝 —— 组键必须与
//     unified_data.sensor_name 逐字节一致；让组键走解析层会把数据面依赖带进
//     数据源域（违反方案 §3.2 红线：datasource 一期不依赖 sensorfield）。
//  2. 校验该边缘设备是否**上报过**该物理名：否则组键永远等不到 MarkSuccess，
//     会被停滞扫描误熔断（与数据源域 DG 类缺陷同一失效方向）。
//
// 未接线（currentSelectorResolver()==nil，单测/embedding）时跳过校验，保持旧行为；
// 与 alert/automation 保存期校验的"未接线不校验"口径一致。
func validateDataSourceCategory(db *gorm.DB, edgeDeviceID uint, category string) (string, string) {
	resolver := currentSelectorResolver()
	if resolver == nil {
		return "", ""
	}
	cat := strings.TrimSpace(category)
	if cat == "" {
		// 空值由领域层给统一的 "category is required"（单一事实源），这里不抢答。
		return "", ""
	}
	if strings.HasPrefix(cat, "@") || strings.HasPrefix(cat, "virtual:") || strings.Contains(cat, "_*") {
		code := "invalid_selector_syntax"
		if !resolver.Enabled() {
			code = "field_resolver_disabled"
		}
		return code, "data_sources.category 一期只支持字面量物理名，不接受 selector: " + cat
	}
	if db == nil || edgeDeviceID == 0 {
		return "", ""
	}
	if dataSourceCategoryObserved(db, edgeDeviceID, cat) {
		return "", ""
	}
	return "category_not_observed", "边缘设备 " + strconv.FormatUint(uint64(edgeDeviceID), 10) +
		" 尚未上报字段 " + cat + "，无法作为数据源类别（请先让该设备成功上报一帧）"
}

// dataSourceCategoryObserved 判断某边缘设备是否上报过该物理名。
//
// ============ 三层来源（2026-11-08 硬切换后重排）============
//
//	① sensor_field_obs   投影表，主键点查，微秒级 —— 主来源
//	② telemetry_frame    原始帧，两跳 JOIN（不走解码）—— 补 60s flush 窗口
//	③ unified_data       已停写，**已移除**
//
// 原实现是 ① + ③。停写后 ③ 恒为空，于是「60s 内刚上报的新字段」会被判未上报，
// 用户看到 400 category_not_observed 而以为自己填错了字段名。
//
// 换成 ② 的理由：判断「这个物理名有没有被上报过」**不需要 payload**。
// telemetry_frame 有 slot_id，telemetry_slot 有 fields_json —— 一次 JOIN 就有答案，
// 与解码成本、slot 字典是否 WarmUp 全都无关。详见 telemetry.DeviceObservedField。
//
// 顺序必须是「投影优先、帧表兜底」：投影表就是为这个场景设计的（主键点查），
// 反过来会让**每次校验**都多一次 JOIN。
func dataSourceCategoryObserved(db *gorm.DB, edgeDeviceID uint, category string) bool {
	var count int64
	if err := db.Model(&models.SensorFieldObs{}).
		Where("device_id = ? AND sensor_name = ?", edgeDeviceID, category).
		Count(&count).Error; err == nil && count > 0 {
		return true
	}
	// 投影表滞后（每 observe_interval 默认 60s 才 flush 一次）或尚未建：
	// 直接查原始帧的存在性（不解码，两跳 JOIN 就够）。
	// 注意这条**不是**「历史兜底」，是补一个 60s 的窗口。
	return telemetry.DeviceObservedField(db, edgeDeviceID, category)
}

// GET /api/v1/edge-devices/:id/failover-logs?category=&limit=
//
// 2026-10-23（契约 §6）: 路径由 /devices/:id 迁到 /edge-devices/:id，:id 的语义
// 从「逻辑设备 id」变为「边缘设备 id」。领域层查询列名仍是 failover_logs.device_id
// （历史命名，取值已经是边缘设备 id），本层只做参数与字段名映射。
func listFailoverLogs(svc *datasource.Service) gin.HandlerFunc {
	return func(c *gin.Context) {
		edgeDeviceID, ok := parseDataSourcePathID(c)
		if !ok {
			return
		}
		rows, err := svc.FailoverLogs(edgeDeviceID, c.Query("category"), parseLimitQuery(c))
		if err != nil {
			writeDataSourceError(c, err)
			return
		}
		items := make([]failoverLogItem, 0, len(rows))
		for _, r := range rows {
			items = append(items, failoverLogItem{
				ID:           r.ID,
				EdgeDeviceID: r.DeviceID,
				Category:     r.Category,
				FromSourceID: r.FromSourceID,
				ToSourceID:   r.ToSourceID,
				Reason:       r.Reason,
				Trigger:      r.Trigger,
				CreatedAt:    r.CreatedAt,
			})
		}
		Success(c, items)
	}
}
