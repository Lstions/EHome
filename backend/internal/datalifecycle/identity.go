// Package datalifecycle implements the edge-device data lifecycle
// (方案 v3.3): logical device identity, startup/delete-time backfill,
// data scope resolution and the daily purge task.
package datalifecycle

import (
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ehome/backend/internal/models"
)

// identityKeyMaxLen mirrors logical_devices.identity_key VARCHAR(64).
const identityKeyMaxLen = 64

// maxSequenceTries bounds the #N retry loop when identity keys collide.
const maxSequenceTries = 256

// maxMergeChainDepth bounds followMergeChain to survive malformed chains.
const maxMergeChainDepth = 16

// Path selects the identity_key collision policy (方案 §2.3.1 三路径分流表).
type Path int

const (
	// PathStartup — P0 启动补建: 先查既有 logical_device 的存活实例数,
	// 无存活实例才复用; 有存活实例则 key 追加 #N 序号重试。
	PathStartup Path = iota
	// PathCreateNew — 创建向导"作为新设备创建": 永不复用, 直接序号 key。
	PathCreateNew
	// PathDelete — 删除时补建: 允许复用既有 key; 复用前若目标 merged_into
	// 非空先跟随链挂到最终目标; 目标 purge_requested=TRUE 时禁止复用。
	PathDelete
)

// IdentityKey derives the identity key for a device instance
// (方案 §1.1): `type:hardware_id`; hardware_id 为空时 `type:{uuid}`。
// 空 hardware_id 分支必须确定性: 基于实例主键派生 uuid v5
// (NameSpaceOID + "edge:<id>"), 同一实例跨补建/跨副本生成同一 key,
// ON CONFLICT 并发防护才真正生效 (T1.1 复审修复)。instanceID 为 0
// (未落库实例, 无稳定身份基准) 时退化为随机 uuid。
func IdentityKey(deviceType, hardwareID string, instanceID uint) string {
	deviceType = strings.TrimSpace(deviceType)
	hardwareID = strings.TrimSpace(hardwareID)
	if hardwareID == "" {
		if instanceID == 0 {
			hardwareID = uuid.NewString()
		} else {
			hardwareID = uuid.NewSHA1(uuid.NameSpaceOID,
				[]byte(fmt.Sprintf("edge:%d", instanceID))).String()
		}
	}
	key := deviceType + ":" + hardwareID
	if len(key) > identityKeyMaxLen {
		key = key[:identityKeyMaxLen]
	}
	return key
}

// BackfillLogicalDevices performs the idempotent startup backfill
// (方案 §2.3.1 路径 1): every edge_devices row (Unscoped, including
// soft-deleted) with logical_device_id IS NULL gets a logical device and a
// back-write. Re-running never duplicates rows. retentionDays is the
// system-level snapshot written into newly created logical devices (§4.1).
//
// Concurrent replicas are safe: inserts use ON CONFLICT/INSERT OR IGNORE as
// a pure concurrency guard and re-read the winner (v3.2-F2: ON CONFLICT is
// never the semantic decision).
func BackfillLogicalDevices(db *gorm.DB, retentionDays int) (backfilled int, err error) {
	var instances []models.EdgeDevice
	if err := db.Unscoped().
		Where("logical_device_id IS NULL").
		Order("id").
		Find(&instances).Error; err != nil {
		return 0, fmt.Errorf("datalifecycle: scan edge_devices for backfill: %w", err)
	}
	for i := range instances {
		ld, err := EnsureLogicalDevice(db, &instances[i], PathStartup, retentionDays)
		if err != nil {
			return backfilled, fmt.Errorf("datalifecycle: backfill edge_device %d: %w", instances[i].ID, err)
		}
		if err := db.Unscoped().
			Model(&models.EdgeDevice{}).
			Where("id = ?", instances[i].ID).
			Update("logical_device_id", ld.ID).Error; err != nil {
			return backfilled, fmt.Errorf("datalifecycle: back-write edge_device %d: %w", instances[i].ID, err)
		}
		backfilled++
	}
	return backfilled, nil
}

// EnsureLogicalDevice resolves (or creates) the logical device for an edge
// device instance according to the path's collision policy and returns it.
// It never writes edge_devices itself — callers back-write the FK.
func EnsureLogicalDevice(db *gorm.DB, dev *models.EdgeDevice, path Path, retentionDays int) (*models.LogicalDevice, error) {
	if dev == nil {
		return nil, errors.New("datalifecycle: nil edge device")
	}
	if retentionDays <= 0 {
		retentionDays = 365
	}
	baseKey := IdentityKey(dev.Type, dev.HardwareID, dev.ID)
	name := strings.TrimSpace(dev.Name)
	if name == "" {
		name = baseKey
	}

	key := baseKey
	for attempt := 0; attempt < maxSequenceTries; attempt++ {
		if attempt > 0 {
			key = sequenceKey(baseKey, attempt+1) // #2, #3, ...
		}

		var existing models.LogicalDevice
		err := db.Where("identity_key = ?", key).First(&existing).Error
		if err == nil {
			// Key exists — apply the path's reuse semantics.
			ld, reuse, ferr := evaluateReuse(db, &existing, path)
			if ferr != nil {
				return nil, ferr
			}
			if reuse {
				return ld, nil
			}
			continue // next sequence key
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("datalifecycle: lookup identity_key %q: %w", key, err)
		}

		// Key absent — insert with ON CONFLICT as pure concurrency guard.
		ld := models.LogicalDevice{
			IdentityKey:   key,
			Name:          name,
			DeviceType:    dev.Type,
			RetentionDays: retentionDays,
		}
		res := db.Clauses(clause.OnConflict{DoNothing: true}).Create(&ld)
		if res.Error != nil {
			return nil, fmt.Errorf("datalifecycle: create logical_device %q: %w", key, res.Error)
		}
		if res.RowsAffected == 1 {
			return &ld, nil
		}
		// Lost the race to a concurrent replica: re-read the winner and
		// apply the same semantics (v3.2-F2).
		if err := db.Where("identity_key = ?", key).First(&existing).Error; err != nil {
			return nil, fmt.Errorf("datalifecycle: re-read identity_key %q after conflict: %w", key, err)
		}
		ld2, reuse, ferr := evaluateReuse(db, &existing, path)
		if ferr != nil {
			return nil, ferr
		}
		if reuse {
			return ld2, nil
		}
		// Winner has living instances (or purge pending) → next sequence key.
	}
	return nil, fmt.Errorf("datalifecycle: identity key space exhausted for %q after %d tries", baseKey, maxSequenceTries)
}

// evaluateReuse decides whether an existing logical device may be reused on
// the given path, applying chain-follow and purge guards for PathDelete.
func evaluateReuse(db *gorm.DB, existing *models.LogicalDevice, path Path) (*models.LogicalDevice, bool, error) {
	switch path {
	case PathCreateNew:
		// 用户显式选择新身份: 永不复用。
		return existing, false, nil
	case PathStartup:
		living, err := countLivingInstances(db, existing.ID)
		if err != nil {
			return nil, false, err
		}
		// 无存活实例才复用; 有存活实例 → 序号 key, 防击穿"一个身份至多
		// 一个存活实例"不变量 (同 type+hardware_id 不同 channel 合法并存)。
		return existing, living == 0, nil
	case PathDelete:
		// 目标 purge_requested=TRUE 时禁止复用 (v3.3-N1): 新删实例的数据
		// 不得挂进待 purge 的逻辑设备。
		if existing.PurgeRequested {
			return existing, false, nil
		}
		// merged_into 非空: 跟随链挂到最终目标 (v3.2-终审 B5), 防挂到链
		// 中段导致历史行 stranded。
		final, err := FollowMergeChain(db, existing)
		if err != nil {
			return nil, false, err
		}
		if final.ID != existing.ID && final.PurgeRequested {
			return final, false, nil
		}
		return final, true, nil
	default:
		return nil, false, fmt.Errorf("datalifecycle: unknown path %d", path)
	}
}

// FollowMergeChain resolves the final merge target of a logical device.
// Returns the device itself when merged_into is NULL. Bounded so malformed
// chains (cycles) cannot loop forever.
func FollowMergeChain(db *gorm.DB, ld *models.LogicalDevice) (*models.LogicalDevice, error) {
	if ld == nil {
		return nil, errors.New("datalifecycle: nil logical device")
	}
	cur := ld
	for i := 0; i < maxMergeChainDepth && cur.MergedInto != nil; i++ {
		var next models.LogicalDevice
		if err := db.First(&next, *cur.MergedInto).Error; err != nil {
			return nil, fmt.Errorf("datalifecycle: follow merged_into %d: %w", *cur.MergedInto, err)
		}
		cur = &next
	}
	return cur, nil
}

// countLivingInstances counts non-deleted edge_devices attached to a logical
// device. Only living instances threaten the one-living-instance invariant.
func countLivingInstances(db *gorm.DB, logicalID uint) (int64, error) {
	var count int64
	err := db.Model(&models.EdgeDevice{}).
		Where("logical_device_id = ?", logicalID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("datalifecycle: count living instances of logical_device %d: %w", logicalID, err)
	}
	return count, nil
}

// CountInstances counts all edge_device instances attached to a logical
// device — Unscoped, including soft-deleted (方案 §1.3 instance_count).
func CountInstances(db *gorm.DB, logicalID uint) (int64, error) {
	var count int64
	err := db.Unscoped().Model(&models.EdgeDevice{}).
		Where("logical_device_id = ?", logicalID).
		Count(&count).Error
	if err != nil {
		return 0, fmt.Errorf("datalifecycle: count instances of logical_device %d: %w", logicalID, err)
	}
	return count, nil
}

// sequenceKey appends the #N suffix, truncating the base so the whole key
// still fits VARCHAR(64).
func sequenceKey(base string, n int) string {
	suffix := fmt.Sprintf("#%d", n)
	if len(base)+len(suffix) > identityKeyMaxLen {
		base = base[:identityKeyMaxLen-len(suffix)]
	}
	return base + suffix
}
