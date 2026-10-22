package api

// 来源组保存期校验 + 删除守卫（方案 §7.3 V-9/V-13、§12.9 F-3/F-5；门禁 SG-01/SG-03/SG-20）。
//
// 职责边界：
//   - 规则保存期（automation_rules / alert_rules）：
//       · 两列绑定五态校验（models.ValidateSourceRefColumns，唯一实现）；
//       · source_kind=source_group ⇒ 组存在 + enabled + 锚点非 0；
//       · primary_backup 组 ⇒ cooldown_sec ≥ 成员采集间隔推导下限（F-3）；
//       · 锚点双写（N-7）：automation=trigger_edge_device_id；alert=target_type/target_id。
//   - 删除守卫（V-13 / SG-20）：删除/批量删除边缘设备、合并逻辑设备前，若其边缘设备
//     是 source_group_members 成员 ⇒ 409，文案给出组名与引用它的规则名。
//
// 为什么 GroupService 用"注入优先 + 按 db 兜底构造"：
//   routes.go 中 automation/alert 的路由注册签名没有 gsvc 参数（且本次写范围不含
//   routes.go/main.go）。为不改变既有路由签名，这里提供 SetRuleSourceGroupService
//   供 main.go 后续一行接线；未接线时按 db 惰性构造只读服务实例（AnchorEdgeDeviceID /
//   ResolveCooldownFloor / GetGroup 均为纯 DB 读，不启动 goroutine/registry）。
//   这是**读放大**而非正确性问题：最坏情况每次保存多构造一个无状态结构体。

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// 来源组相关 400/409 错误码（与 models.SourceRefError.Code 同一命名空间）。
const (
	// SourceRefCodeGroupDisabled source_kind=source_group 但组已 enabled=false。
	SourceRefCodeGroupDisabled = "source_group_disabled"
	// SourceRefCodeEmptyAnchor 组没有任何成员 ⇒ 锚点=0 ⇒ 不能绑规则（空组不能绑规则）。
	SourceRefCodeEmptyAnchor = "source_group_empty_anchor"
	// SourceRefCodeCooldownFloor cooldown_sec 低于组推导下限（V-8/F-3）。
	SourceRefCodeCooldownFloor = "source_group_cooldown_below_floor"
	// SourceRefCodeDeleteGuard 被来源组/规则引用，拒绝删除或合并（V-13）。
	SourceRefCodeDeleteGuard = "source_group_member_referenced"
)

// sourceRefValidationError 是除 models.SourceRefError 之外的保存期校验错误。
// （models 包不可改，故组级语义错误在本层用同构类型携带 code。）
type sourceRefValidationError struct {
	Code string
	Msg  string
}

func (e *sourceRefValidationError) Error() string { return e.Msg }

var (
	ruleGroupSvcMu sync.RWMutex
	ruleGroupSvc   *datasource.GroupService
)

// SetRuleSourceGroupService 注入规保存期使用的 GroupService（main.go 一行接线）。
// 未注入时按 db 惰性构造只读实例（见文件头注释）。
func SetRuleSourceGroupService(s *datasource.GroupService) {
	ruleGroupSvcMu.Lock()
	defer ruleGroupSvcMu.Unlock()
	ruleGroupSvc = s
}

// ruleGroupServiceFor 返回保存期用的 GroupService；nil 时按 db 构造并缓存。
func ruleGroupServiceFor(db *gorm.DB) *datasource.GroupService {
	ruleGroupSvcMu.RLock()
	s := ruleGroupSvc
	ruleGroupSvcMu.RUnlock()
	if s != nil {
		return s
	}
	ruleGroupSvcMu.Lock()
	defer ruleGroupSvcMu.Unlock()
	if ruleGroupSvc == nil {
		ruleGroupSvc = datasource.NewGroupService(db, datasource.Options{})
	}
	return ruleGroupSvc
}

// ruleSourceBinding 是保存期解析出的来源绑定。
type ruleSourceBinding struct {
	IsGroup           bool
	GroupID           uint
	AnchorEdgeDeviceID uint
}

// resolveRuleSourceBinding 校验并解析规则的来源绑定。
//
// kind/groupID 是**合并后**的最终值（update 路径由调用方先与库中现值合并）；
// cooldownSec 是该规则本次保存后的有效 cooldown（仅 automation 用；alert 传 0 且
// requireCooldownFloor=false）。返回 error 可能是 *models.SourceRefError 或
// *sourceRefValidationError（均映射 400 + error_code）。
func resolveRuleSourceBinding(db *gorm.DB, kind *string, groupID *uint, cooldownSec int, requireCooldownFloor bool) (ruleSourceBinding, error) {
	if err := models.ValidateSourceRefColumns(kind, groupID); err != nil {
		return ruleSourceBinding{}, err
	}
	if kind == nil || *kind != string(models.SourceKindSourceGroup) {
		return ruleSourceBinding{}, nil
	}
	svc := ruleGroupServiceFor(db)
	group, err := svc.GetGroup(*groupID)
	if err != nil {
		if errors.Is(err, datasource.ErrGroupNotFound) {
			return ruleSourceBinding{}, &sourceRefValidationError{
				Code: models.SourceRefCodeGroupMissing,
				Msg:  fmt.Sprintf("来源组 %d 不存在（source_group_id 指向不存在的组）", *groupID),
			}
		}
		return ruleSourceBinding{}, err
	}
	if !group.Enabled {
		return ruleSourceBinding{}, &sourceRefValidationError{
			Code: SourceRefCodeGroupDisabled,
			Msg:  fmt.Sprintf("来源组「%s」(%d) 已停用，不能绑定规则", group.Name, group.ID),
		}
	}
	anchor, err := svc.AnchorEdgeDeviceID(*groupID)
	if err != nil {
		return ruleSourceBinding{}, err
	}
	if anchor == 0 {
		return ruleSourceBinding{}, &sourceRefValidationError{
			Code: SourceRefCodeEmptyAnchor,
			Msg:  fmt.Sprintf("来源组「%s」(%d) 没有成员（锚点=0），不能绑定规则：请先添加成员", group.Name, group.ID),
		}
	}
	if requireCooldownFloor && group.Kind == models.SourceGroupKindPrimaryBackup {
		floor, err := svc.ResolveCooldownFloor(*groupID)
		if err != nil {
			return ruleSourceBinding{}, err
		}
		floorSec := int((floor + time.Second - 1) / time.Second) // 向上取整秒
		if floorSec < 1 {
			floorSec = 1
		}
		if cooldownSec < floorSec {
			return ruleSourceBinding{}, &sourceRefValidationError{
				Code: SourceRefCodeCooldownFloor,
				Msg: fmt.Sprintf("cooldown_sec=%d 低于来源组「%s」推导下限 %ds（= 成员采集间隔；方案 F-3）",
					cooldownSec, group.Name, floorSec),
			}
		}
	}
	return ruleSourceBinding{IsGroup: true, GroupID: *groupID, AnchorEdgeDeviceID: anchor}, nil
}

// respondSourceRefError 把保存期校验错误写成 400 envelope；返回 true 表示已写响应。
func respondSourceRefError(c *gin.Context, err error) bool {
	if err == nil {
		return false
	}
	var sre *models.SourceRefError
	if errors.As(err, &sre) {
		ErrorWithCode(c, httpStatusBadRequest, sre.Code, sre.Msg)
		return true
	}
	var vre *sourceRefValidationError
	if errors.As(err, &vre) {
		ErrorWithCode(c, httpStatusBadRequest, vre.Code, vre.Msg)
		return true
	}
	ErrorWithCode(c, httpStatusBadRequest, "invalid_source_ref", err.Error())
	return true
}

// httpStatusBadRequest 是本文件的局部常量（Gin 依赖已在别处引入，避免再引 net/http
// 只为一个常量）。值必须等于 400。
const httpStatusBadRequest = 400

// ── 删除守卫（V-13 / SG-20）──

// groupReferenceInfo 汇总"某批边缘设备被来源组引用"的证据（用于 409 文案）。
type groupReferenceInfo struct {
	EdgeDeviceIDs []uint
	GroupNames    []string
	RuleNames     []string
}

func (i groupReferenceInfo) empty() bool {
	return len(i.GroupNames) == 0 && len(i.RuleNames) == 0
}

// findGroupReferencesByEdgeDevices 查询给定边缘设备集合被哪些来源组/规则引用。
// 直查 source_group_members + source_groups + 两规则表（任务要求：不依赖
// GroupService 内部方法，避免循环依赖）。
func findGroupReferencesByEdgeDevices(db *gorm.DB, edgeDeviceIDs []uint) (groupReferenceInfo, error) {
	info := groupReferenceInfo{}
	if len(edgeDeviceIDs) == 0 {
		return info, nil
	}
	// 表不存在 ⇒ 该库未启用来源组（例如仅迁移了设备域的单测库）⇒ 视为无引用。
	// 生产/全量测试库由 dbschema.EnsureSourceGroupSchema 保证四表存在。
	if !db.Migrator().HasTable(&models.SourceGroupMember{}) ||
		!db.Migrator().HasTable(&models.SourceGroup{}) {
		return info, nil
	}
	var members []models.SourceGroupMember
	if err := db.Where("edge_device_id IN ?", edgeDeviceIDs).Find(&members).Error; err != nil {
		return info, err
	}
	if len(members) == 0 {
		return info, nil
	}
	groupIDs := make([]uint, 0, len(members))
	seen := map[uint]bool{}
	for _, m := range members {
		if !seen[m.GroupID] {
			seen[m.GroupID] = true
			groupIDs = append(groupIDs, m.GroupID)
		}
	}
	var groups []models.SourceGroup
	if err := db.Where("id IN ?", groupIDs).Order("id ASC").Find(&groups).Error; err != nil {
		return info, err
	}
	nameByID := make(map[uint]string, len(groups))
	groupNames := make([]string, 0, len(groups))
	for _, g := range groups {
		nameByID[g.ID] = g.Name
		groupNames = append(groupNames, g.Name)
	}
	// 规则引用：两表按 source_group_id 直查（V-13「被规则直接引用」）。
	ruleSeen := map[string]bool{}
	ruleNames := make([]string, 0)
	var automationNames []string
	if db.Migrator().HasTable(&models.AutomationRule{}) {
		if err := db.Model(&models.AutomationRule{}).Where("source_group_id IN ?", groupIDs).
			Pluck("name", &automationNames).Error; err != nil {
			return info, err
		}
	}
	var alertNames []string
	if db.Migrator().HasTable(&models.AlertRule{}) {
		if err := db.Model(&models.AlertRule{}).Where("source_group_id IN ?", groupIDs).
			Pluck("name", &alertNames).Error; err != nil {
			return info, err
		}
	}
	for _, n := range append(automationNames, alertNames...) {
		if n != "" && !ruleSeen[n] {
			ruleSeen[n] = true
			ruleNames = append(ruleNames, n)
		}
	}
	info.EdgeDeviceIDs = edgeDeviceIDs
	info.GroupNames = groupNames
	info.RuleNames = ruleNames
	return info, nil
}

// findGroupReferencesByLogicalDevice 经其下边缘设备间接判断逻辑设备是否被组引用。
func findGroupReferencesByLogicalDevice(db *gorm.DB, logicalDeviceID uint) (groupReferenceInfo, error) {
	var edgeIDs []uint
	if err := db.Model(&models.EdgeDevice{}).
		Where("logical_device_id = ?", logicalDeviceID).
		Pluck("id", &edgeIDs).Error; err != nil {
		return groupReferenceInfo{}, err
	}
	return findGroupReferencesByEdgeDevices(db, edgeIDs)
}

// guardReferences 若 info 非空 ⇒ 写 409 并返回 false（调用方应立即 return）。
func guardReferences(c *gin.Context, info groupReferenceInfo, subject string) bool {
	if info.empty() {
		return true
	}
	groups := strings.Join(info.GroupNames, "、")
	if groups == "" {
		groups = "（未找到组名）"
	}
	rules := strings.Join(info.RuleNames, "、")
	if rules == "" {
		rules = "（无规则直接引用，仅成员关系）"
	}
	ErrorWithCode(c, 409, SourceRefCodeDeleteGuard,
		fmt.Sprintf("%s被来源组引用，禁止删除/合并：引用组=[%s]；引用规则=[%s]。请先解除成员关系或删除规则。",
			subject, groups, rules))
	return false
}

// guardEdgeDeviceDelete 删除/批量删除边缘设备前的守卫；true=放行。
func guardEdgeDeviceDelete(c *gin.Context, db *gorm.DB, edgeDeviceID uint) bool {
	info, err := findGroupReferencesByEdgeDevices(db, []uint{edgeDeviceID})
	if err != nil {
		Error(c, 500, "检查来源组引用失败: "+err.Error())
		return false
	}
	return guardReferences(c, info, fmt.Sprintf("边缘设备 %d ", edgeDeviceID))
}

// guardEdgeDevicesBatchDelete 批量删除前的守卫：任一台被引用 ⇒ 整体 409（不做部分删除，
// 避免"删了一半才发现被引用"的非原子行为）。
func guardEdgeDevicesBatchDelete(c *gin.Context, db *gorm.DB, edgeDeviceIDs []uint) bool {
	info, err := findGroupReferencesByEdgeDevices(db, edgeDeviceIDs)
	if err != nil {
		Error(c, 500, "检查来源组引用失败: "+err.Error())
		return false
	}
	return guardReferences(c, info, fmt.Sprintf("边缘设备（批量 %d 台）", len(edgeDeviceIDs)))
}

// guardLogicalDeviceDelete 删除/合并逻辑设备前的守卫（经其下边缘设备判断）；true=放行。
func guardLogicalDeviceDelete(c *gin.Context, db *gorm.DB, logicalDeviceID uint) bool {
	info, err := findGroupReferencesByLogicalDevice(db, logicalDeviceID)
	if err != nil {
		Error(c, 500, "检查来源组引用失败: "+err.Error())
		return false
	}
	return guardReferences(c, info, fmt.Sprintf("逻辑设备 %d ", logicalDeviceID))
}

// ptrString / ptrUint 是 DTO 指针小工具（避免 tests/其他文件重复定义冲突时用）。
func ptrString(s string) *string { return &s }
func ptrUint(v uint) *uint      { return &v }
