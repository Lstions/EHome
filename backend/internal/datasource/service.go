// Package datasource 提供 data_sources 自动派生台账的**只读**领域服务 +
// 来源组 (source_groups) 的完整主备状态机（见 group_service.go / group_state.go）。
//
// 2026-10-24（遗留处置方案 A）：Service 上原有的 Delete / Activate / Deactivate /
// Reset 四个手工运维方法**已删除**，连同它们的通知链（recordHealth /
// writeFailoverLog / newNotification / emitAll / emit / SetNotifier /
// SetDispatchNotifier）。原因见 handler_data_source.go:registerDataSourceRoutes 的注释：
// 组键二元化后这些操作在数据模型层必然失败或必然被投影器覆盖，
// 而真正可用的手工干预能力全在 GroupService（且真接入 UI）。
//
// 权威设计：docs/设计/数据源主备与故障转移.md v1.0 §3/§4/§5。
// 组 (group) 键 = (edge_device_id, category)；不变量：每组至多一条 status=active。
//
// 2026-10-23: 逻辑设备概念层已移除（.contract/logical-device-removal.md）。原组键
// (device_id=逻辑设备, category) 已废除 —— data_sources 不再有 device_id 列，
// 一条来源就是「某台边缘设备的某个类别」。同一物理设备换节点后 edge_device_id 不变
// （原地换节点）或历史经 merge-history 迁移，来源台账不需要第三套 id 空间。
package datasource

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/models"

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

// Service 是 data_sources 自动派生台账的**只读**服务（Create/Update 仅保留给
// 历史数据修正与单测夹具；生产写路径是投影器）。
//
// 2026-10-24 方案 A：通知注入点 (notify / notifySink) 与四个手工运维方法一并删除 ——
// 本服务已不再是通知生产者（组域通知走 GroupService 自己的 notifySink）。
type Service struct {
	db   *gorm.DB
	opts Options
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

func (s *Service) now() time.Time { return s.opts.Now() }

// CreateInput 新建来源入参。
//
// 组键 = (EdgeDeviceID, Category)：逻辑设备层删除后不再有 DeviceID 维度
// （2026-10-23, .contract/logical-device-removal.md §6）。
type CreateInput struct {
	EdgeDeviceID                                    uint
	Category, Name, Description, SourceType, Config string
	Priority, MaxFailCount                          int
	IsPrimary                                       bool
}

// Create 新建来源。校验失败返回 ErrInvalidRequest；同组同 edge_device 重复返回 ErrConflict。
// 组内首条自动 active (R7 反向：首条无 active 可被采纳)，其余 standby。
func (s *Service) Create(in CreateInput) (*models.DataSource, error) {
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
		// 组键 = (edge_device_id, category)：同键只能有一条（唯一索引 uniq_ds_source）。
		var dup int64
		if err := tx.Model(&models.DataSource{}).
			Where("edge_device_id = ? AND category = ?", in.EdgeDeviceID, in.Category).
			Count(&dup).Error; err != nil {
			return err
		}
		if dup > 0 {
			return fmt.Errorf("%w: data source already exists for (edge_device_id=%d, category=%s)", ErrConflict, in.EdgeDeviceID, in.Category)
		}
		// 同组（同 edge_device + category）首条自动 active —— 与上面的 dup 判定同键，
		// 因而已存在时 groupCount 必然 >0。保留查询以维持「0 条 ⇒ active」的语义可读性。
		var groupCount int64
		if err := tx.Model(&models.DataSource{}).
			Where("edge_device_id = ? AND category = ?", in.EdgeDeviceID, in.Category).
			Count(&groupCount).Error; err != nil {
			return err
		}
		status := StatusStandby
		if groupCount == 0 {
			status = StatusActive
		}
		ds := models.DataSource{
			Category:     in.Category,
			EdgeDeviceID: in.EdgeDeviceID,
			SourceType:   sourceType,
			Name:         name,
			Description:  in.Description,
			Priority:     in.Priority,
			// is_primary 是 status 的**派生量**，不允许独立设置（2026-10-24 不变量）。
			//
			// 历史问题：它曾是「人工声明的主来源」标记，但主备状态机在 M3 下线后
			// 已无任何读者（grep 'WHERE is_primary' 为空），所有「谁在供数」的判定
			// 一律看 status；而状态迁移只 Update("status") 不同步它 ⇒ 出现
			// active + is_primary=false 的死列漂移。现在统一由 status 派生。
			IsPrimary:    status == StatusActive,
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
		// is_primary 不接受外部写入：它恒等于 (status == active)。
		// 保留 UpdateInput.IsPrimary 字段只为 API 兼容（入参被忽略并在文档中说明），
		// 让它可写就等于重新引入「两处记载谁是主来源」的漂移。
		ds.IsPrimary = ds.Status == StatusActive
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

// ListFilter 列表过滤条件。
//
// EdgeDeviceID 取代原 DeviceID（= 逻辑设备 id）：组键已回到
// (edge_device_id, category)，逻辑设备层删除后没有第二个 id 空间可筛。
type ListFilter struct {
	EdgeDeviceID     uint
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
	if f.EdgeDeviceID != 0 {
		q = q.Where("edge_device_id = ?", f.EdgeDeviceID)
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
//
// 列名仍是 failover_logs.device_id（历史命名），取值是**边缘设备 id**。
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
