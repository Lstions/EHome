package api

// 来源组 HTTP 层（方案《规则数据来源与复合来源》v0.3 §7.3 / verifier 对齐的 API 形状）。
//
// 路由（与 verifier 前端契约逐字对齐，勿改口径）：
//
//	GET    /api/v1/source-groups?page&page_size&kind&enabled
//	POST   /api/v1/source-groups
//	GET    /api/v1/source-groups/:id
//	PUT    /api/v1/source-groups/:id
//	DELETE /api/v1/source-groups/:id
//	POST   /api/v1/source-groups/:id/activate   （composite 一律 400：无 active 概念）
//	POST   /api/v1/source-groups/:id/deactivate （带 member_id：R8 置 disabled，composite 亦允许；
//	                                              不带 member_id：停用当前 active，composite 无 active ⇒ 409）
//	GET    /api/v1/source-groups/:id/failover-logs?limit
//	GET    /api/v1/source-groups/:id/health?limit
//
// 响应：组对象按 models.SourceGroup 的 tag 平铺，嵌套 members[]（含 status/fail_count/
// last_success），并返回 active_member_id（composite 恒 0）。领域逻辑全部在
// internal/datasource/group*.go；本文件只做 DTO/错误映射/envelope。

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// sourceGroupView 是组详情响应：组字段平铺 + members[] + active_member_id。
//
// active_member_id 是组内权威成员（source_group_members.id），不是边缘设备 ID；
// composite 组恒 0（无 active 概念）。
type sourceGroupView struct {
	models.SourceGroup
	Members        []models.SourceGroupMember `json:"members"`
	ActiveMemberID uint                       `json:"active_member_id"`
}

// groupMemberRequest 是成员声明 DTO。
type groupMemberRequest struct {
	EdgeDeviceID uint  `json:"edge_device_id"`
	Priority     int   `json:"priority"`
	Required     *bool `json:"required"`
}

// createSourceGroupRequest 是 POST /source-groups 的独立 DTO（禁止注入 revision/status）。
type createSourceGroupRequest struct {
	Name         string               `json:"name"`
	Kind         string               `json:"kind"`
	Category     string               `json:"category"`
	Combine      string               `json:"combine"`
	MinMembers   int                  `json:"min_members"`
	StalenessSec int                  `json:"staleness_sec"`
	OnDegrade    string               `json:"on_degrade"`
	Enabled      *bool                `json:"enabled"`
	Description  string               `json:"description"`
	Members      []groupMemberRequest `json:"members"`
}

// updateSourceGroupRequest 是 PUT 白名单 DTO（kind/category 不可变）。
type updateSourceGroupRequest struct {
	Name         *string               `json:"name"`
	Combine      *string               `json:"combine"`
	MinMembers   *int                  `json:"min_members"`
	StalenessSec *int                  `json:"staleness_sec"`
	OnDegrade    *string               `json:"on_degrade"`
	Enabled      *bool                 `json:"enabled"`
	Description  *string               `json:"description"`
	Members      *[]groupMemberRequest `json:"members"`
}

// groupActionRequest 是 activate/deactivate 的请求体。
type groupActionRequest struct {
	MemberID uint `json:"member_id"`
}

// registerSourceGroupRoutes 注册来源组 9 条路由。gsvc 为 nil 时保留路由并显式 503
// （与数据源域既有范式一致：不 nil 解引用、不静默 404）。
func registerSourceGroupRoutes(v1 *gin.RouterGroup, db *gorm.DB, gsvc *datasource.GroupService) {
	group := v1.Group("/source-groups")
	if gsvc == nil {
		unavailable := func(c *gin.Context) {
			Error(c, http.StatusServiceUnavailable, "source group service unavailable")
		}
		group.GET("", unavailable)
		group.POST("", unavailable)
		group.GET("/:id", unavailable)
		group.PUT("/:id", unavailable)
		group.DELETE("/:id", unavailable)
		group.POST("/:id/activate", unavailable)
		group.POST("/:id/deactivate", unavailable)
		group.GET("/:id/failover-logs", unavailable)
		group.GET("/:id/health", unavailable)
		return
	}
	group.GET("", listSourceGroups(gsvc))
	group.POST("", createSourceGroup(gsvc, db))
	group.GET("/:id", getSourceGroup(gsvc))
	group.PUT("/:id", updateSourceGroup(gsvc))
	group.DELETE("/:id", deleteSourceGroup(gsvc))
	group.POST("/:id/activate", activateSourceGroup(gsvc))
	group.POST("/:id/deactivate", deactivateSourceGroup(gsvc))
	group.GET("/:id/failover-logs", sourceGroupFailoverLogs(gsvc))
	group.GET("/:id/health", sourceGroupHealth(gsvc))
}

// writeSourceGroupError 把领域哨兵映射为 404/409/400（其它 500 且不泄露内部细节）。
func writeSourceGroupError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, datasource.ErrGroupNotFound):
		Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, datasource.ErrNotFound):
		Error(c, http.StatusNotFound, err.Error())
	case errors.Is(err, datasource.ErrConflict):
		Error(c, http.StatusConflict, err.Error())
	case errors.Is(err, datasource.ErrInvalidRequest):
		Error(c, http.StatusBadRequest, err.Error())
	default:
		Error(c, http.StatusInternalServerError, "internal error")
	}
}

// parseSourceGroupID 解析路径参数 :id。
func parseSourceGroupID(c *gin.Context) (uint, bool) {
	raw := strings.TrimSpace(c.Param("id"))
	id, err := strconv.ParseUint(raw, 10, 64)
	if err != nil || id == 0 {
		Error(c, http.StatusBadRequest, "invalid id")
		return 0, false
	}
	return uint(id), true
}

// buildSourceGroupView 组装组详情响应（members 已排序；active_member_id 由成员状态推导）。
func buildSourceGroupView(row models.SourceGroup, members []models.SourceGroupMember) sourceGroupView {
	view := sourceGroupView{SourceGroup: row, Members: members}
	if view.Members == nil {
		view.Members = []models.SourceGroupMember{}
	}
	for i := range view.Members {
		if view.Members[i].Status == models.SourceMemberStatusActive {
			view.ActiveMemberID = view.Members[i].ID
			break
		}
	}
	return view
}

// toGroupMemberInputs DTO → 领域入参。
func toGroupMemberInputs(reqs []groupMemberRequest) []datasource.GroupMemberInput {
	out := make([]datasource.GroupMemberInput, 0, len(reqs))
	for _, r := range reqs {
		out = append(out, datasource.GroupMemberInput{
			EdgeDeviceID: r.EdgeDeviceID, Priority: r.Priority, Required: r.Required,
		})
	}
	return out
}

// validateSourceGroupMemberObserved 是 V-5 的 API 层预检（sensor_field_obs 优先、
// unified_data 兜底；与 data_sources 的 category_not_observed 同口径）。
func validateSourceGroupMemberObserved(db *gorm.DB, category string, members []groupMemberRequest) (string, string) {
	cat := strings.TrimSpace(category)
	if db == nil || cat == "" || len(members) == 0 {
		return "", ""
	}
	for _, m := range members {
		if m.EdgeDeviceID == 0 {
			continue
		}
		if dataSourceCategoryObserved(db, m.EdgeDeviceID, cat) {
			continue
		}
		return "category_not_observed", "边缘设备 " + strconv.FormatUint(uint64(m.EdgeDeviceID), 10) +
			" 尚未上报字段 " + cat + "，不能作为来源组成员（V-5）"
	}
	return "", ""
}

// listSourceGroups GET /source-groups。
func listSourceGroups(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		filter := datasource.ListGroupFilter{
			Kind:     strings.TrimSpace(c.Query("kind")),
			Category: strings.TrimSpace(c.Query("category")),
		}
		if raw := strings.TrimSpace(c.Query("enabled")); raw != "" {
			enabled := raw == "true" || raw == "1"
			filter.Enabled = &enabled
		}
		page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
		pageSize, _ := strconv.Atoi(c.DefaultQuery("page_size", "20"))
		filter.Page, filter.PageSize = page, pageSize

		rows, total, err := gsvc.ListGroups(filter)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		items := make([]sourceGroupView, 0, len(rows))
		for i := range rows {
			members, err := gsvc.ListMembers(rows[i].ID)
			if err != nil {
				writeSourceGroupError(c, err)
				return
			}
			items = append(items, buildSourceGroupView(rows[i], members))
		}
		Success(c, gin.H{
			"items": items, "total": total,
			"page": filter.Page, "page_size": filter.PageSize,
		})
	}
}

// createSourceGroup POST /source-groups。
func createSourceGroup(gsvc *datasource.GroupService, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req createSourceGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		// V-5 预检（领域层再校验一次；这里给机器可读 error_code）。
		if code, msg := validateSourceGroupMemberObserved(db, req.Category, req.Members); msg != "" {
			ErrorWithCode(c, http.StatusBadRequest, code, msg)
			return
		}
		row, err := gsvc.CreateGroup(datasource.CreateGroupInput{
			Name:         req.Name,
			Kind:         req.Kind,
			Category:     req.Category,
			Combine:      req.Combine,
			MinMembers:   req.MinMembers,
			StalenessSec: req.StalenessSec,
			OnDegrade:    req.OnDegrade,
			Enabled:      req.Enabled,
			Description:  req.Description,
			Members:      toGroupMemberInputs(req.Members),
		})
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		members, err := gsvc.ListMembers(row.ID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		SuccessWithCode(c, http.StatusCreated, buildSourceGroupView(*row, members))
	}
}

// getSourceGroup GET /source-groups/:id。
func getSourceGroup(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		row, members, err := gsvc.GetGroupWithMembers(id)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, buildSourceGroupView(*row, members))
	}
}

// updateSourceGroup PUT /source-groups/:id（kind/category 不可变；members 非 nil 即整体替换）。
func updateSourceGroup(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		var req updateSourceGroupRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body: "+err.Error())
			return
		}
		in := datasource.UpdateGroupInput{
			Name:         req.Name,
			Combine:      req.Combine,
			MinMembers:   req.MinMembers,
			StalenessSec: req.StalenessSec,
			OnDegrade:    req.OnDegrade,
			Enabled:      req.Enabled,
			Description:  req.Description,
		}
		if req.Members != nil {
			inputs := toGroupMemberInputs(*req.Members)
			in.Members = &inputs
		}
		row, err := gsvc.UpdateGroup(id, in)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		members, err := gsvc.ListMembers(row.ID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, buildSourceGroupView(*row, members))
	}
}

// deleteSourceGroup DELETE /source-groups/:id（被规则引用 409，I6）。
func deleteSourceGroup(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		if err := gsvc.DeleteGroup(id); err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, gin.H{"deleted": true})
	}
}

// activateSourceGroup POST /source-groups/:id/activate（R7；composite 400）。
func activateSourceGroup(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		var req groupActionRequest
		_ = c.ShouldBindJSON(&req) // 允许空 body（随后校验 member_id）
		if req.MemberID == 0 {
			Error(c, http.StatusBadRequest, "member_id is required")
			return
		}
		row, err := gsvc.ActivateGroupMember(id, req.MemberID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		members, err := gsvc.ListMembers(row.ID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, buildSourceGroupView(*row, members))
	}
}

// deactivateSourceGroup POST /source-groups/:id/deactivate（R5/R6/R8；composite 400）。
func deactivateSourceGroup(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		var req groupActionRequest
		_ = c.ShouldBindJSON(&req)
		memberID := req.MemberID
		if memberID == 0 {
			// 未指定成员：停用当前 active（"停用本组"语义）。
			_, members, err := gsvc.GetGroupWithMembers(id)
			if err != nil {
				writeSourceGroupError(c, err)
				return
			}
			for i := range members {
				if members[i].Status == models.SourceMemberStatusActive {
					memberID = members[i].ID
					break
				}
			}
			if memberID == 0 {
				Error(c, http.StatusConflict, "source group has no active member")
				return
			}
		}
		row, err := gsvc.DeactivateGroupMember(id, memberID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		members, err := gsvc.ListMembers(row.ID)
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, buildSourceGroupView(*row, members))
	}
}

// sourceGroupFailoverLogs GET /source-groups/:id/failover-logs?limit。
func sourceGroupFailoverLogs(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		if _, err := gsvc.GetGroup(id); err != nil {
			writeSourceGroupError(c, err)
			return
		}
		rows, err := gsvc.FailoverLogs(id, parseLimitQuery(c))
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, rows)
	}
}

// sourceGroupHealth GET /source-groups/:id/health?limit。
func sourceGroupHealth(gsvc *datasource.GroupService) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseSourceGroupID(c)
		if !ok {
			return
		}
		if _, err := gsvc.GetGroup(id); err != nil {
			writeSourceGroupError(c, err)
			return
		}
		rows, err := gsvc.Health(id, parseLimitQuery(c))
		if err != nil {
			writeSourceGroupError(c, err)
			return
		}
		Success(c, rows)
	}
}
