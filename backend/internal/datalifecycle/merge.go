package datalifecycle

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// MergeConflict 是 §3.4 D-1 结构化冲突条目: 409 响应逐条呈现, 前端提供
// 实例跳转。instance 字段仅在 alive_instance 原因下有值。
type MergeConflict struct {
	LogicalDeviceID uint   `json:"logical_device_id"`
	LogicalName     string `json:"logical_name"`
	Reason          string `json:"reason"` // alive_instance / purge_requested / already_merging
	InstanceID      uint   `json:"instance_id,omitempty"`
	InstanceName    string `json:"instance_name,omitempty"`
	NodeName        string `json:"node_name,omitempty"`
}

// 冲突原因取值
const (
	MergeConflictAliveInstance  = "alive_instance"
	MergeConflictPurgeRequested = "purge_requested"
	MergeConflictAlreadyMerging = "already_merging"
)

// MergeValidationError 携带结构化 conflicts; API 层映射为 409 (§3.4)。
type MergeValidationError struct {
	Message   string
	Conflicts []MergeConflict
}

func (e *MergeValidationError) Error() string { return e.Message }

// MergeRequest 是合并 API 的入参 (§3.4 POST /logical-devices/merge)。
type MergeRequest struct {
	TargetName string
	SourceIDs  []uint
}

// MergeResult 是合并事务成功后的返回。
type MergeResult struct {
	TargetID uint
	JobIDs   []uint
}

// ValidateMergeRequest 做事务外的基础校验: 源数量、去重、目标名非空。
func ValidateMergeRequest(req *MergeRequest) error {
	req.TargetName = strings.TrimSpace(req.TargetName)
	if req.TargetName == "" {
		return errors.New("target_name is required")
	}
	if len(req.SourceIDs) < 2 {
		return errors.New("merge requires at least 2 source logical devices")
	}
	seen := make(map[uint]struct{}, len(req.SourceIDs))
	deduped := make([]uint, 0, len(req.SourceIDs))
	for _, id := range req.SourceIDs {
		if id == 0 {
			return errors.New("source_ids must be positive")
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		deduped = append(deduped, id)
	}
	req.SourceIDs = deduped
	return nil
}

// MergeDevices 执行 §3.4 合并: 单事务内新建目标 logical_device
// (identity_key=`merge:{uuid}`, retention_days 按 §4.1 快照) + 逐源校验
// (无存活实例 + purge_requested=FALSE, v3.2-F1/v3.3-N1) + 乐观占位
// (UPDATE ... WHERE merged_into IS NULL AND merge_status IS NULL,
// RowsAffected=0 即冲突整体回滚), 并为每源创建 merge_jobs 行
// (v1-H3: 搬迁全部移出 API 事务)。
//
// 校验冲突逐源收集进 conflicts 一次性返回 (前端逐条呈现), 而非首个即停。
func MergeDevices(db *gorm.DB, req *MergeRequest) (*MergeResult, error) {
	if err := ValidateMergeRequest(req); err != nil {
		return nil, err
	}

	// 事务外预估各源数据量作 merge_jobs.total_estimate (进度分母)。
	// 估算失败 (超时/解析失败) 落 0, 不阻塞合并 (§1.3 降级语义)。
	estimates := make(map[uint]int64, len(req.SourceIDs))
	for _, id := range req.SourceIDs {
		if rows, ok := EstimateRowCount(context.Background(), db, id); ok {
			estimates[id] = rows
		}
	}

	var result MergeResult
	err := db.Transaction(func(tx *gorm.DB) error {
		var sources []models.LogicalDevice
		if err := tx.Where("id IN ?", req.SourceIDs).Find(&sources).Error; err != nil {
			return fmt.Errorf("load merge sources: %w", err)
		}
		if len(sources) != len(req.SourceIDs) {
			return fmt.Errorf("one or more source logical devices not found")
		}
		// 同型号校验: 目标继承源的 device_type, 跨型号合并无意义。
		deviceType := sources[0].DeviceType
		for _, src := range sources[1:] {
			if src.DeviceType != deviceType {
				return fmt.Errorf("source logical devices have mixed device types (%s vs %s)",
					deviceType, src.DeviceType)
			}
		}

		// 先建目标 (§3.4 伪码顺序), 校验/占位失败时随事务回滚。
		target := models.LogicalDevice{
			IdentityKey:   "merge:" + uuid.NewString(),
			Name:          req.TargetName,
			DeviceType:    deviceType,
			RetentionDays: SystemRetentionDays(), // §4.1 快照语义
		}
		if err := tx.Create(&target).Error; err != nil {
			return fmt.Errorf("create merge target: %w", err)
		}

		// 阶段 1 — 逐源校验 (v3.2-F1 + v3.3-N1), 冲突全量收集。
		conflicts := make([]MergeConflict, 0)
		for _, src := range sources {
			if src.PurgeRequested {
				conflicts = append(conflicts, MergeConflict{
					LogicalDeviceID: src.ID,
					LogicalName:     src.Name,
					Reason:          MergeConflictPurgeRequested,
				})
				continue
			}
			if living, lerr := livingInstanceInfo(tx, src.ID); lerr != nil {
				return lerr
			} else if living != nil {
				conflicts = append(conflicts, MergeConflict{
					LogicalDeviceID: src.ID,
					LogicalName:     src.Name,
					Reason:          MergeConflictAliveInstance,
					InstanceID:      living.ID,
					InstanceName:    living.Name,
					NodeName:        nodeName(tx, living.NodeID),
				})
			}
		}
		if len(conflicts) > 0 {
			return &MergeValidationError{
				Message:   "merge rejected: source validation failed",
				Conflicts: conflicts,
			}
		}

		// 阶段 2 — 乐观占位: 条件 UPDATE 保证同一源不会被两个合并并发
		// 引用。PG READ COMMITTED 下并发事务的 UPDATE 在此串行: 后到者
		// 看到 merged_into 已置位, RowsAffected=0 → 409 整体回滚。
		for _, src := range sources {
			res := tx.Model(&models.LogicalDevice{}).
				Where("id = ? AND merged_into IS NULL AND (merge_status IS NULL OR merge_status = '')", src.ID).
				Updates(map[string]interface{}{
					"merged_into":  target.ID,
					"merge_status": models.MergeStatusPending,
				})
			if res.Error != nil {
				return fmt.Errorf("occupy source %d: %w", src.ID, res.Error)
			}
			if res.RowsAffected == 0 {
				conflicts = append(conflicts, MergeConflict{
					LogicalDeviceID: src.ID,
					LogicalName:     src.Name,
					Reason:          MergeConflictAlreadyMerging,
				})
			}
		}
		if len(conflicts) > 0 {
			return &MergeValidationError{
				Message:   "merge rejected: source already merged or merging",
				Conflicts: conflicts,
			}
		}

		// 阶段 3 — 每源一个 merge_jobs 行 (status=pending), 搬迁由后台
		// Migrator 执行 (§4.3 任务 3)。
		for _, src := range sources {
			job := models.MergeJob{
				SourceLogicalID: src.ID,
				TargetLogicalID: target.ID,
				Status:          models.MergeJobPending,
				TotalEstimate:   estimates[src.ID],
				WatermarkPhase:  models.MergePhaseUnifiedData,
			}
			if err := tx.Create(&job).Error; err != nil {
				return fmt.Errorf("create merge job for source %d: %w", src.ID, err)
			}
			result.JobIDs = append(result.JobIDs, job.ID)
		}
		result.TargetID = target.ID
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &result, nil
}

// livingInstanceInfo 返回逻辑设备的首个存活实例 (无则 nil)。
func livingInstanceInfo(db *gorm.DB, logicalID uint) (*models.EdgeDevice, error) {
	var dev models.EdgeDevice
	err := db.Where("logical_device_id = ?", logicalID).First(&dev).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, fmt.Errorf("query living instances of logical_device %d: %w", logicalID, err)
	}
	return &dev, nil
}

// nodeName 查节点显示名 (不存在时返回 node_id 本身)。
func nodeName(db *gorm.DB, nodeID string) string {
	var node models.Node
	if err := db.Select("name").Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return nodeID
	}
	return node.Name
}
