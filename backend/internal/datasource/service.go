// Package datasource 实现"数据源主备与故障转移"的领域服务：
// 显式状态机 (R1-R11)、健康事件与故障切换日志留痕、通知投递。
//
// 权威设计：docs/设计/数据源主备与故障转移.md v1.0 §3/§4/§5。
// 组 (group) 键 = (device_id, category)；不变量：每组至多一条 status=active。
package datasource

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// 来源状态。
const (
	StatusActive   = "active"
	StatusStandby  = "standby"
	StatusError    = "error"
	StatusDisabled = "disabled"
)

// 来源类型；v1.0 仅支持 edge_device。
const SourceTypeEdgeDevice = "edge_device"

// 切换原因（FailoverLog.Reason）。
const (
	ReasonAuto             = "auto"
	ReasonManual           = "manual"
	ReasonManualDeactivate = "manual_deactivate"
)

// 自动切换触发信号（FailoverLog.Trigger）。
const (
	TriggerDeviceOffline = "device_offline"
	TriggerStaleData     = "stale_data"
)

// 健康事件状态（DataSourceHealth.Status）。
const (
	healthStatusFailure    = "failure"
	healthStatusTransition = "transition"
)

// 通知类型与来源标识。
const (
	notifyTypeWarning = "warning"
	notifyTypeInfo    = "info"
	notifySource      = "data_source"
)

// 哨兵错误：handler 用 errors.Is 映射 400/404/409。
var (
	ErrInvalidRequest = errors.New("invalid request")
	ErrNotFound       = errors.New("not found")
	ErrConflict       = errors.New("conflict")
)

const (
	defaultCooldown     = 5 * time.Minute
	defaultMinResidency = 2 * time.Minute
	defaultStaleness    = 5 * time.Minute
	defaultScanInterval = 60 * time.Second

	defaultPageSize = 20
	maxPageSize     = 100
	defaultLimit    = 50
	maxLimit        = 200

	defaultMaxFailCount = 3
	minMaxFailCount     = 1
	maxMaxFailCount     = 20
)

// Options 状态机可调参数；零值字段在 New 中回落到默认值。
type Options struct {
	Cooldown     time.Duration    // 默认 5m (R10)
	MinResidency time.Duration    // 默认 2m (R11)
	Staleness    time.Duration    // 默认 5m (§4 停滞判定)
	ScanInterval time.Duration    // 默认 60s (Start)
	Now          func() time.Time // nil → time.Now
}

// Service 数据源主备领域服务。
type Service struct {
	db   *gorm.DB
	opts Options
	// notify 是既有的**回调式**通知注入点 (早于 D-1 存在, 测试用它收集通知,
	// 历史上 main.go 也用它直接落库)。保留不动以维持既有断言与兼容。
	notify func(models.Notification)
	// notifySink 是 D-1 步骤 3 的"通知写入 + 投递"入口: main.go 经 SetNotifier
	// 注入 notify.Dispatcher, 由它写 notifications 行并顺带外发投递。
	//
	// 与上面的 notify 回调并存, 优先级: notifySink > notify > 直接落库。
	// **nil 是显式支持的合法状态**: 既有测试只注 notify 或不注入, 行为不变
	// (见 emit)。
	notifySink notify.Notifier
}

// New 构造 Service，补齐 Options 默认值。
func New(db *gorm.DB, opts Options) *Service {
	if opts.Cooldown <= 0 {
		opts.Cooldown = defaultCooldown
	}
	if opts.MinResidency <= 0 {
		opts.MinResidency = defaultMinResidency
	}
	if opts.Staleness <= 0 {
		opts.Staleness = defaultStaleness
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = defaultScanInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{db: db, opts: opts}
}

// SetNotifier 注入通知投递回调；nil → 回落到直接写 notifications 表。
//
// 注: 这是**既有**的回调式注入点 (测试收集通知用)。D-1 步骤 3 的生产接线走
// SetDispatchNotifier, 两者可共存 (见 emit 的优先级)。
func (s *Service) SetNotifier(fn func(models.Notification)) {
	s.notify = fn
}

// SetDispatchNotifier 注入"通知写入 + 投递"入口 (D-1 步骤 3, main.go 接线)。
//
// 契约: 注入后通知经它落库并顺带外发投递; 传 nil 显式回落
// (先回 notify 回调, 再回直接写 notifications 表)。
// 窄接口 (notify.Notifier) 而非 *notify.Dispatcher: 沿用本仓"setter 二阶段注入 +
// 避免包间编译期依赖"的既有范式。
func (s *Service) SetDispatchNotifier(n notify.Notifier) {
	s.notifySink = n
}

func (s *Service) now() time.Time { return s.opts.Now() }

// CreateInput 新建来源入参。
type CreateInput struct {
	DeviceID, EdgeDeviceID                          uint
	Category, Name, Description, SourceType, Config string
	Priority, MaxFailCount                          int
	IsPrimary                                       bool
}

// Create 新建来源。校验失败返回 ErrInvalidRequest；同组同 edge_device 重复返回 ErrConflict。
// 组内首条自动 active (R7 反向：首条无 active 可被采纳)，其余 standby。
func (s *Service) Create(in CreateInput) (*models.DataSource, error) {
	if in.DeviceID == 0 {
		return nil, fmt.Errorf("%w: device_id is required", ErrInvalidRequest)
	}
	if strings.TrimSpace(in.Category) == "" {
		return nil, fmt.Errorf("%w: category is required", ErrInvalidRequest)
	}
	if in.EdgeDeviceID == 0 {
		return nil, fmt.Errorf("%w: edge_device_id is required", ErrInvalidRequest)
	}
	sourceType := in.SourceType
	if sourceType == "" {
		sourceType = SourceTypeEdgeDevice
	}
	if sourceType != SourceTypeEdgeDevice {
		return nil, fmt.Errorf("%w: unsupported source_type %q", ErrInvalidRequest, in.SourceType)
	}
	maxFail := in.MaxFailCount
	if maxFail == 0 {
		maxFail = defaultMaxFailCount
	}
	if maxFail < minMaxFailCount || maxFail > maxMaxFailCount {
		return nil, fmt.Errorf("%w: max_fail_count %d out of range [%d,%d]", ErrInvalidRequest, in.MaxFailCount, minMaxFailCount, maxMaxFailCount)
	}
	name := in.Name
	if name == "" {
		name = fmt.Sprintf("edge-device-%d@%s", in.EdgeDeviceID, in.Category)
	}

	var out models.DataSource
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var dup int64
		if err := tx.Model(&models.DataSource{}).
			Where("device_id = ? AND category = ? AND edge_device_id = ?", in.DeviceID, in.Category, in.EdgeDeviceID).
			Count(&dup).Error; err != nil {
			return err
		}
		if dup > 0 {
			return fmt.Errorf("%w: data source already exists for (device_id=%d, category=%s, edge_device_id=%d)", ErrConflict, in.DeviceID, in.Category, in.EdgeDeviceID)
		}
		var groupCount int64
		if err := tx.Model(&models.DataSource{}).
			Where("device_id = ? AND category = ?", in.DeviceID, in.Category).
			Count(&groupCount).Error; err != nil {
			return err
		}
		status := StatusStandby
		if groupCount == 0 {
			status = StatusActive
		}
		ds := models.DataSource{
			DeviceID:     in.DeviceID,
			Category:     in.Category,
			EdgeDeviceID: in.EdgeDeviceID,
			SourceType:   sourceType,
			Name:         name,
			Description:  in.Description,
			Priority:     in.Priority,
			IsPrimary:    in.IsPrimary,
			MaxFailCount: maxFail,
			FailCount:    0,
			Status:       status,
			Config:       in.Config,
		}
		if err := tx.Create(&ds).Error; err != nil {
			return err
		}
		out = ds
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Get 按 ID 读取来源；不存在返回 ErrNotFound。
func (s *Service) Get(id uint) (*models.DataSource, error) {
	var ds models.DataSource
	if err := s.db.First(&ds, id).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: data source %d", ErrNotFound, id)
		}
		return nil, err
	}
	return &ds, nil
}

// UpdateInput 白名单更新入参；nil 字段表示不改。
type UpdateInput struct {
	Name, Description, Config *string
	Priority, MaxFailCount    *int
	IsPrimary                 *bool
}

// Update 按白名单更新字段；不改变 status，不影响组不变量。
func (s *Service) Update(id uint, in UpdateInput) (*models.DataSource, error) {
	if in.MaxFailCount != nil && (*in.MaxFailCount < minMaxFailCount || *in.MaxFailCount > maxMaxFailCount) {
		return nil, fmt.Errorf("%w: max_fail_count %d out of range [%d,%d]", ErrInvalidRequest, *in.MaxFailCount, minMaxFailCount, maxMaxFailCount)
	}
	var out models.DataSource
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var ds models.DataSource
		if err := tx.First(&ds, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: data source %d", ErrNotFound, id)
			}
			return err
		}
		if in.Name != nil {
			ds.Name = *in.Name
		}
		if in.Description != nil {
			ds.Description = *in.Description
		}
		if in.Config != nil {
			ds.Config = *in.Config
		}
		if in.Priority != nil {
			ds.Priority = *in.Priority
		}
		if in.MaxFailCount != nil {
			ds.MaxFailCount = *in.MaxFailCount
		}
		if in.IsPrimary != nil {
			ds.IsPrimary = *in.IsPrimary
		}
		if err := tx.Save(&ds).Error; err != nil {
			return err
		}
		out = ds
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Delete 删除来源 (设计 §7)。
// 删除 active 且组内存在健康候选时自动接替并写日志；
// 组内无其他来源时通知"该类别失去来源"；其余情况直接删除。
func (s *Service) Delete(id uint) error {
	var notes []models.Notification
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var ds models.DataSource
		if err := tx.First(&ds, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: data source %d", ErrNotFound, id)
			}
			return err
		}
		if ds.Status == StatusActive {
			var others []models.DataSource
			if err := tx.Where("device_id = ? AND category = ? AND id <> ?", ds.DeviceID, ds.Category, ds.ID).
				Order("priority DESC, id ASC").Find(&others).Error; err != nil {
				return err
			}
			if len(others) == 0 {
				notes = append(notes, s.newNotification(notifyTypeInfo, "数据源移除",
					fmt.Sprintf("设备 %d 类别 %s 删除来源 %d 后该类别失去来源", ds.DeviceID, ds.Category, ds.ID), ds.ID))
			} else {
				var candidate *models.DataSource
				for i := range others {
					if others[i].Status == StatusStandby {
						candidate = &others[i]
						break
					}
				}
				if candidate == nil {
					notes = append(notes, s.newNotification(notifyTypeWarning, "数据源降级",
						fmt.Sprintf("设备 %d 类别 %s 删除权威来源 %d 后无可用备用来源 (degraded)", ds.DeviceID, ds.Category, ds.ID), ds.ID))
				} else {
					candidate.Status = StatusActive
					if err := tx.Save(candidate).Error; err != nil {
						return err
					}
					if err := s.writeFailoverLog(tx, ds.DeviceID, ds.Category, ds.ID, candidate.ID, ReasonManual, ""); err != nil {
						return err
					}
					notes = append(notes, s.newNotification(notifyTypeInfo, "数据源切换",
						fmt.Sprintf("设备 %d 类别 %s 删除权威来源 %d，来源 %d 接替 (原因: %s)", ds.DeviceID, ds.Category, ds.ID, candidate.ID, ReasonManual), candidate.ID))
				}
			}
		}
		if err := tx.Delete(&models.DataSource{}, id).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.emitAll(notes)
	return nil
}

// ListFilter 列表过滤条件。
type ListFilter struct {
	DeviceID         uint
	Category, Status string
	Page, PageSize   int
}

// List 分页查询。Page<1 → 1；PageSize<=0 → 20，>100 → 100；priority DESC, id ASC。
func (s *Service) List(f ListFilter) ([]models.DataSource, int64, error) {
	page := f.Page
	if page < 1 {
		page = 1
	}
	size := f.PageSize
	if size <= 0 {
		size = defaultPageSize
	}
	if size > maxPageSize {
		size = maxPageSize
	}

	q := s.db.Model(&models.DataSource{})
	if f.DeviceID != 0 {
		q = q.Where("device_id = ?", f.DeviceID)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	items := make([]models.DataSource, 0)
	if err := q.Order("priority DESC, id ASC").Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		return nil, 0, err
	}
	return items, total, nil
}

// clampLimit 健康记录/切换日志的 limit 归一化：<=0 → 50，>200 → 200。
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultLimit
	}
	if limit > maxLimit {
		return maxLimit
	}
	return limit
}

// Health 返回来源健康事件，id DESC。
func (s *Service) Health(id uint, limit int) ([]models.DataSourceHealth, error) {
	items := make([]models.DataSourceHealth, 0)
	if err := s.db.Where("source_id = ?", id).Order("id DESC").Limit(clampLimit(limit)).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// FailoverLogs 返回设备的切换日志，可按 category 过滤，id DESC。
func (s *Service) FailoverLogs(deviceID uint, category string, limit int) ([]models.FailoverLog, error) {
	q := s.db.Where("device_id = ?", deviceID)
	if category != "" {
		q = q.Where("category = ?", category)
	}
	items := make([]models.FailoverLog, 0)
	if err := q.Order("id DESC").Limit(clampLimit(limit)).Find(&items).Error; err != nil {
		return nil, err
	}
	return items, nil
}

// Activate 手动切换为权威 (R7)。
//
// v1.1 修订：activate 的语义是"使该来源成为权威"，disabled 只是"人工停用"、必须可逆，
// 因此目标为 disabled 时不再返回 ErrConflict，而是正常走切换流程（即"重新启用"）。
//   - 不存在 → ErrNotFound
//   - 已是 active → 幂等返回，不写日志/通知
//   - 目标为 standby/error/disabled → 目标 active、原 active → standby，
//     写 failover_logs(reason=manual) + health(transition) + 通知
func (s *Service) Activate(id uint) (*models.DataSource, error) {
	var out models.DataSource
	var notes []models.Notification
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var target models.DataSource
		if err := tx.First(&target, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: data source %d", ErrNotFound, id)
			}
			return err
		}
		if target.Status == StatusActive {
			out = target
			return nil
		}
		var current models.DataSource
		err := tx.Where("device_id = ? AND category = ? AND status = ? AND id <> ?",
			target.DeviceID, target.Category, StatusActive, target.ID).First(&current).Error
		hasCurrent := err == nil
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var fromID uint
		if hasCurrent {
			fromID = current.ID
			if err := tx.Model(&models.DataSource{}).Where("id = ?", current.ID).Update("status", StatusStandby).Error; err != nil {
				return err
			}
		}
		if err := tx.Model(&models.DataSource{}).Where("id = ?", target.ID).Update("status", StatusActive).Error; err != nil {
			return err
		}
		if err := s.writeFailoverLog(tx, target.DeviceID, target.Category, fromID, target.ID, ReasonManual, ""); err != nil {
			return err
		}
		msg := fmt.Sprintf("设备 %d 类别 %s 来源 %d → active (原因: %s)", target.DeviceID, target.Category, target.ID, ReasonManual)
		if hasCurrent {
			msg = fmt.Sprintf("设备 %d 类别 %s 来源 %d → active，原权威来源 %d → standby (原因: %s)",
				target.DeviceID, target.Category, target.ID, fromID, ReasonManual)
		}
		if err := s.recordHealth(tx, target.ID, target.DeviceID, target.Category, healthStatusTransition, msg, 0); err != nil {
			return err
		}
		target.Status = StatusActive
		out = target
		notes = append(notes, s.newNotification(notifyTypeInfo, "数据源切换", msg, target.ID))
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emitAll(notes)
	return &out, nil
}

// Deactivate 停用来源 (R5/R6/R8)。
//   - 不存在 → ErrNotFound
//   - active 且组内无健康候选 → ErrConflict (R6，要求先指定接替者)
//   - active 且有候选 → 候选接替 (reason=manual_deactivate) + health(transition)，原来源 disabled (R5)
//   - 非 active → 直接 disabled (R8)
func (s *Service) Deactivate(id uint) (*models.DataSource, error) {
	var out models.DataSource
	var notes []models.Notification
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var target models.DataSource
		if err := tx.First(&target, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: data source %d", ErrNotFound, id)
			}
			return err
		}
		if target.Status != StatusActive {
			if err := tx.Model(&models.DataSource{}).Where("id = ?", target.ID).Update("status", StatusDisabled).Error; err != nil {
				return err
			}
			target.Status = StatusDisabled
			out = target
			return nil
		}

		var candidate models.DataSource
		err := tx.Where("device_id = ? AND category = ? AND id <> ? AND status = ?",
			target.DeviceID, target.Category, target.ID, StatusStandby).
			Order("priority DESC, id ASC").First(&candidate).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return fmt.Errorf("%w: no healthy standby candidate for (device_id=%d, category=%s); specify a successor first", ErrConflict, target.DeviceID, target.Category)
		}
		if err != nil {
			return err
		}
		if err := tx.Model(&models.DataSource{}).Where("id = ?", candidate.ID).Update("status", StatusActive).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.DataSource{}).Where("id = ?", target.ID).Update("status", StatusDisabled).Error; err != nil {
			return err
		}
		if err := s.writeFailoverLog(tx, target.DeviceID, target.Category, target.ID, candidate.ID, ReasonManualDeactivate, ""); err != nil {
			return err
		}
		msg := fmt.Sprintf("设备 %d 类别 %s 来源 %d → disabled，来源 %d → active (原因: %s)",
			target.DeviceID, target.Category, target.ID, candidate.ID, ReasonManualDeactivate)
		if err := s.recordHealth(tx, candidate.ID, target.DeviceID, target.Category, healthStatusTransition, msg, 0); err != nil {
			return err
		}
		target.Status = StatusDisabled
		out = target
		notes = append(notes, s.newNotification(notifyTypeInfo, "数据源切换", msg, candidate.ID))
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emitAll(notes)
	return &out, nil
}

// Reset 重置熔断来源 (R9)。
//   - 不存在 → ErrNotFound
//   - 非 error → ErrInvalidRequest
//   - error → standby、fail_count=0 (保留 last_failure)；组内无 active 时提升为 active
func (s *Service) Reset(id uint) (*models.DataSource, error) {
	var out models.DataSource
	var notes []models.Notification
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var ds models.DataSource
		if err := tx.First(&ds, id).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: data source %d", ErrNotFound, id)
			}
			return err
		}
		if ds.Status != StatusError {
			return fmt.Errorf("%w: data source %d is not in error state", ErrInvalidRequest, id)
		}
		status := StatusStandby
		var active models.DataSource
		err := tx.Where("device_id = ? AND category = ? AND status = ?", ds.DeviceID, ds.Category, StatusActive).First(&active).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			status = StatusActive
		} else if err != nil {
			return err
		}
		ds.Status = status
		ds.FailCount = 0
		if err := tx.Save(&ds).Error; err != nil {
			return err
		}
		out = ds
		notes = append(notes, s.newNotification(notifyTypeInfo, "数据源恢复",
			fmt.Sprintf("设备 %d 类别 %s 来源 %d 重置为 %s (原因: reset)", ds.DeviceID, ds.Category, ds.ID, status), ds.ID))
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.emitAll(notes)
	return &out, nil
}

// MarkSuccess 成功事件 (R1)。按 edge_device_id 定位来源，仅 categories 命中的来源：
// last_success=at、fail_count=0；error → standby。不抢占 active；成功事件不落健康表。
func (s *Service) MarkSuccess(edgeDeviceID uint, categories []string, at time.Time) {
	if len(categories) == 0 {
		return
	}
	want := make(map[string]struct{}, len(categories))
	for _, c := range categories {
		want[c] = struct{}{}
	}
	var sources []models.DataSource
	if err := s.db.Where("edge_device_id = ? AND status <> ?", edgeDeviceID, StatusDisabled).Find(&sources).Error; err != nil {
		logger.Warn("datasource: MarkSuccess query sources failed", "edge_device_id", edgeDeviceID, "error", err)
		return
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		for i := range sources {
			src := sources[i]
			if _, ok := want[src.Category]; !ok {
				continue
			}
			updates := map[string]interface{}{
				"last_success": at,
				"fail_count":   0,
			}
			if src.Status == StatusError {
				updates["status"] = StatusStandby
			}
			if err := tx.Model(&models.DataSource{}).Where("id = ?", src.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		logger.Warn("datasource: MarkSuccess update failed", "edge_device_id", edgeDeviceID, "error", err)
	}
}

// MarkFailure 失败事件 (R2/R3/R4/R10)。仅 active|standby 计次；
// 达到 max_fail_count → error 后触发自动切换。
func (s *Service) MarkFailure(edgeDeviceID uint, trigger string) {
	var sources []models.DataSource
	if err := s.db.Where("edge_device_id = ?", edgeDeviceID).Find(&sources).Error; err != nil {
		logger.Warn("datasource: MarkFailure query sources failed", "edge_device_id", edgeDeviceID, "error", err)
		return
	}
	now := s.now()
	for i := range sources {
		src := sources[i]
		if src.Status != StatusActive && src.Status != StatusStandby {
			continue
		}
		var notes []models.Notification
		err := s.db.Transaction(func(tx *gorm.DB) error {
			var cur models.DataSource
			if err := tx.First(&cur, src.ID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if cur.Status != StatusActive && cur.Status != StatusStandby {
				return nil
			}
			cur.FailCount++
			cur.LastFailure = &now
			if err := s.recordHealth(tx, cur.ID, cur.DeviceID, cur.Category, healthStatusFailure,
				fmt.Sprintf("来源连续失败计数 %d/%d (触发: %s)", cur.FailCount, cur.MaxFailCount, trigger), 0); err != nil {
				return err
			}
			if cur.FailCount >= cur.MaxFailCount {
				cur.Status = StatusError
				if err := tx.Save(&cur).Error; err != nil {
					return err
				}
				_, err := s.maybeFailover(tx, cur.DeviceID, cur.Category, trigger, cur.ID, &notes)
				return err
			}
			return tx.Save(&cur).Error
		})
		if err != nil {
			logger.Warn("datasource: MarkFailure apply failed", "source_id", src.ID, "error", err)
			continue
		}
		s.emitAll(notes)
	}
}

// ScanStale 停滞扫描 (§4)。只处理 active 来源；LastSuccess 为 nil 视为超期。
// R11：来源 UpdatedAt (成为 active 的近似时间) 距今 < MinResidency 时跳过。
// 返回实际发生切换的次数。
func (s *Service) ScanStale(at time.Time) (int, error) {
	var actives []models.DataSource
	if err := s.db.Where("status = ?", StatusActive).Find(&actives).Error; err != nil {
		return 0, err
	}
	switched := 0
	for i := range actives {
		src := actives[i]
		if at.Sub(src.UpdatedAt) < s.opts.MinResidency {
			continue
		}
		if src.LastSuccess != nil && at.Sub(*src.LastSuccess) <= s.opts.Staleness {
			continue
		}
		n, err := s.failStaleSource(src, at)
		if err != nil {
			return switched, err
		}
		switched += n
	}
	return switched, nil
}

// failStaleSource 对单个停滞来源计一次失败 (不按 edge_device 广播)。
func (s *Service) failStaleSource(src models.DataSource, at time.Time) (int, error) {
	var notes []models.Notification
	switched := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var cur models.DataSource
		if err := tx.First(&cur, src.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if cur.Status != StatusActive {
			return nil
		}
		if cur.LastSuccess != nil && at.Sub(*cur.LastSuccess) <= s.opts.Staleness {
			return nil
		}
		respTime := 0
		if cur.LastSuccess != nil {
			respTime = int(at.Sub(*cur.LastSuccess).Milliseconds())
		}
		cur.FailCount++
		cur.LastFailure = &at
		if err := s.recordHealth(tx, cur.ID, cur.DeviceID, cur.Category, healthStatusFailure,
			fmt.Sprintf("来源数据停滞 %dms (触发: %s)", respTime, TriggerStaleData), respTime); err != nil {
			return err
		}
		if cur.FailCount >= cur.MaxFailCount {
			cur.Status = StatusError
			if err := tx.Save(&cur).Error; err != nil {
				return err
			}
			didSwitch, err := s.maybeFailover(tx, cur.DeviceID, cur.Category, TriggerStaleData, cur.ID, &notes)
			if err != nil {
				return err
			}
			if didSwitch {
				switched = 1
			}
			return nil
		}
		return tx.Save(&cur).Error
	})
	if err != nil {
		return switched, err
	}
	s.emitAll(notes)
	return switched, nil
}

// Start 按 ScanInterval 周期调用 ScanStale，直到 ctx.Done()。
func (s *Service) Start(ctx context.Context) {
	ticker := time.NewTicker(s.opts.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := s.ScanStale(s.now()); err != nil {
				logger.Warn("datasource: stale scan failed", "error", err)
			}
		}
	}
}

// maybeFailover 自动切换 (R3/R4/R10)，必须在调用方事务内执行。
// 返回是否真正发生了切换。
func (s *Service) maybeFailover(tx *gorm.DB, deviceID uint, category, trigger string, failedID uint, notes *[]models.Notification) (bool, error) {
	// 组内仍有 active (standby 熔断场景)：无需切换，避免出现两条 active。
	var active models.DataSource
	err := tx.Where("device_id = ? AND category = ? AND status = ?", deviceID, category, StatusActive).First(&active).Error
	if err == nil {
		return false, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	// R10 冷却：距该组最近一次切换 < Cooldown → 只记健康事件与通知，不切换。
	var last models.FailoverLog
	err = tx.Where("device_id = ? AND category = ?", deviceID, category).Order("id DESC").First(&last).Error
	if err == nil {
		if s.now().Sub(last.CreatedAt) < s.opts.Cooldown {
			msg := fmt.Sprintf("设备 %d 类别 %s 距上次切换不足冷却时间 (cooldown)，跳过切换", deviceID, category)
			if err := s.recordHealth(tx, failedID, deviceID, category, healthStatusTransition, msg, 0); err != nil {
				return false, err
			}
			*notes = append(*notes, s.newNotification(notifyTypeWarning, "数据源切换被抑制", msg, failedID))
			return false, nil
		}
	} else if !errors.Is(err, gorm.ErrRecordNotFound) {
		return false, err
	}

	// R3 候选：priority DESC, id ASC 的第一条健康待命来源。
	var candidate models.DataSource
	err = tx.Where("device_id = ? AND category = ? AND status = ?", deviceID, category, StatusStandby).
		Order("priority DESC, id ASC").First(&candidate).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// R4 降级：无候选，不改任何来源状态。
		msg := fmt.Sprintf("设备 %d 类别 %s 无可用备用来源 (degraded)", deviceID, category)
		*notes = append(*notes, s.newNotification(notifyTypeWarning, "数据源降级", msg, failedID))
		return false, nil
	}
	if err != nil {
		return false, err
	}

	candidate.Status = StatusActive
	if err := tx.Save(&candidate).Error; err != nil {
		return false, err
	}
	if err := s.writeFailoverLog(tx, deviceID, category, failedID, candidate.ID, ReasonAuto, trigger); err != nil {
		return false, err
	}
	msg := fmt.Sprintf("设备 %d 类别 %s 来源 %d 故障，自动切换至来源 %d (原因: %s, 触发: %s)",
		deviceID, category, failedID, candidate.ID, ReasonAuto, trigger)
	if err := s.recordHealth(tx, candidate.ID, deviceID, category, healthStatusTransition, msg, 0); err != nil {
		return false, err
	}
	*notes = append(*notes, s.newNotification(notifyTypeWarning, "数据源切换", msg, candidate.ID))
	return true, nil
}

// recordHealth 写一条健康事件。
func (s *Service) recordHealth(tx *gorm.DB, sourceID, deviceID uint, category, status, message string, responseTime int) error {
	h := models.DataSourceHealth{
		SourceID:     sourceID,
		DeviceID:     deviceID,
		Category:     category,
		Status:       status,
		Message:      message,
		ResponseTime: responseTime,
		CreatedAt:    s.now(),
	}
	return tx.Create(&h).Error
}

// writeFailoverLog 写一条故障切换日志。
func (s *Service) writeFailoverLog(tx *gorm.DB, deviceID uint, category string, from, to uint, reason, trigger string) error {
	l := models.FailoverLog{
		DeviceID:     deviceID,
		Category:     category,
		FromSourceID: from,
		ToSourceID:   to,
		Reason:       reason,
		Trigger:      trigger,
		CreatedAt:    s.now(),
	}
	return tx.Create(&l).Error
}

// newNotification 构造本域通知 (Type=warning|info, Source=data_source)。
func (s *Service) newNotification(typ, title, message string, sourceID uint) models.Notification {
	return models.Notification{
		Type:        typ,
		Title:       title,
		Message:     message,
		Description: message,
		Source:      notifySource,
		SourceID:    fmt.Sprintf("%d", sourceID),
		Read:        false,
		CreatedAt:   s.now(),
	}
}

// emitAll 事务提交后投递通知；投递失败不影响状态迁移 (fail-open)。
//
// 调用时序 (D-1 步骤 3 的投递时机依据): 全部 7 个调用点都在
// s.db.Transaction(...) **返回之后** (事务已提交/已回滚), 因此把同步投递接在这里
// 不会把出站 HTTP 拖进业务事务。详见 notify/dispatcher.go 的 Create 投递时机裁决。
func (s *Service) emitAll(notes []models.Notification) {
	for i := range notes {
		s.emit(notes[i])
	}
}

// emit 落库一条通知, 优先级: notifySink (Dispatcher, 落库+外发) > notify (既有回调,
// 只收集/落库) > 直接写 notifications 表。
//
// nil 回落是**显式设计**而非"碰巧没 nil": 既有测试只注入 notify 回调 (或不注入),
// 未接线的构造必须保持改造前逐字节相同的行为, 不得 panic。
func (s *Service) emit(n models.Notification) {
	if s.notifySink != nil {
		s.notifySink.Create(context.Background(), &n)
		return
	}
	if s.notify != nil {
		s.notify(n)
		return
	}
	if err := s.db.Create(&n).Error; err != nil {
		logger.Warn("datasource: failed to write notification", "source_id", n.SourceID, "error", err)
	}
}
