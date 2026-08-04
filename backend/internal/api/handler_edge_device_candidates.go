package api

import (
	"context"
	"database/sql"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handler_edge_device_candidates.go — 创建继承 (方案 v3.3 T5 P2)。
//
// GET /edge-devices/candidates (§1.3 §九): Unscoped 聚合查询列出可继承的
// 逻辑设备 (含已软删实例), 按匹配权重五档排序 (100/80/60/40/20, 全部列出
// 由用户选, 不做自动继承)。数据量用估算 (PG reltuples / SQLite 截断
// COUNT), 整个估算段共享 3s 超时预算, 超时候选降级为不含 row_estimate
// (§1.3)。retention_days 必须显式返回 (v3.1-Q6)。

// candidateWeight 是 §1.3 匹配权重表的 Go 侧实现: 按候选逻辑设备的实例集
// (Unscoped, 含软删) 逐实例判档取最高。targetNodeID/targetChannelID/
// targetHardwareID 是创建目标的参数 (candidates 查询的 node_id/
// channel_id/hardware_id), hardware_id 比较两侧 TrimSpace + 小写归一
// (与 checkDeviceUniqueness 的 TrimSpace 语义一致)。
func candidateWeight(instances []models.EdgeDevice, targetNodeID string, targetChannelID uint, targetHardwareID, devType string) int {
	hw := strings.ToLower(strings.TrimSpace(targetHardwareID))
	hasNodeChHW, hasNodeHW, hasHW, sameNodeType := false, false, false, false
	for _, inst := range instances {
		if inst.Type != devType {
			continue
		}
		instHW := strings.ToLower(strings.TrimSpace(inst.HardwareID))
		hwMatch := hw != "" && instHW == hw
		if targetNodeID != "" && inst.NodeID == targetNodeID {
			sameNodeType = true
			if hwMatch {
				hasNodeHW = true
				if targetChannelID != 0 && inst.ChannelID == targetChannelID {
					hasNodeChHW = true
				}
			}
		} else if hwMatch {
			hasHW = true
		}
	}
	switch {
	case hasNodeChHW:
		return 100 // 同 node + 同 channel + 同 hardware_id + 同 type (原位置重建)
	case hasNodeHW:
		return 80 // 同 node + 同 hardware_id (不同 channel)
	case sameNodeType:
		return 60 // 同 node + 同 type
	case hasHW:
		return 40 // 不同 node + 同 hardware_id
	default:
		return 20 // 不同 node + 同 type
	}
}

// logicalDeviceCandidate 是 candidates 聚合查询的行。
type logicalDeviceCandidate struct {
	ID            uint       `json:"id"`
	Name          string     `json:"name"`
	DeviceType    string     `json:"device_type"`
	RetentionDays int        `json:"retention_days"`
	InstanceCount int64      `json:"instance_count"`
	LastDataAt    *time.Time `json:"last_data_at"`
	MatchWeight   int        `json:"match_weight"`
	// RowEstimate 估算超时/失败时保持 nil, JSON 省略 (降级, §1.3)。
	RowEstimate *int64 `json:"row_estimate,omitempty"`
}

func (c logicalDeviceCandidate) lastTime() time.Time {
	if c.LastDataAt == nil {
		return time.Time{}
	}
	return *c.LastDataAt
}

// candidateRow 是聚合查询的扫描目标。last_data_at 用 sql.NullString 承接:
// PG 的 MAX(timestamp) 经 database/sql 归一为 RFC3339Nano 字符串, SQLite
// 的聚合列本就返回字符串 (driver 不做列类型推断), 统一 parse 归一。
type candidateRow struct {
	ID            uint           `gorm:"column:id"`
	Name          string         `gorm:"column:name"`
	DeviceType    string         `gorm:"column:device_type"`
	RetentionDays int            `gorm:"column:retention_days"`
	InstanceCount int64          `gorm:"column:instance_count"`
	LastDataAtRaw sql.NullString `gorm:"column:last_data_at"`
}

// parseCandidateTime 归一 last_data_at 字符串。
func parseCandidateTime(s string) *time.Time {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	layouts := []string{
		time.RFC3339Nano,                      // PG (database/sql 归一格式)
		"2006-01-02 15:04:05.999999999-07:00", // SQLite 默认
		"2006-01-02T15:04:05.999999999-07:00",
		"2006-01-02 15:04:05.999999999",
		"2006-01-02T15:04:05.999999999",
		"2006-01-02 15:04:05",
	}
	for _, layout := range layouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t
		}
	}
	return nil
}

// listLogicalDeviceCandidates 执行 §1.3 伪 SQL: Unscoped JOIN edge_devices
// (含软删), LEFT JOIN unified_data 按 dataScopeCondition 同构 OR 条件取
// MAX(timestamp) (backfill 前旧行 logical_device_id 全 NULL, 过渡期不失真)。
// instance_count 用 COUNT(DISTINCT ed.id) (v3.2-终审 B7: LEFT JOIN ud 会把
// 每实例放大为其数据行数)。node_id/channel_id/hardware_id 用于权重计算。
func listLogicalDeviceCandidates(db *gorm.DB, devType, nodeID string, channelID uint, hardwareID string) ([]logicalDeviceCandidate, error) {
	const aggregateSQL = `
SELECT ld.id AS id, ld.name AS name, ld.device_type AS device_type,
       ld.retention_days AS retention_days,
       COUNT(DISTINCT ed.id) AS instance_count,
       MAX(ud.timestamp) AS last_data_at
FROM logical_devices ld
JOIN edge_devices ed ON ed.logical_device_id = ld.id
LEFT JOIN unified_data ud
       ON ud.logical_device_id = ld.id
       OR (ud.logical_device_id IS NULL AND ud.device_id = ed.id)
WHERE ld.merged_into IS NULL
  AND ld.device_type = ?
GROUP BY ld.id`

	// 注意: 按 §1.3 伪 SQL, candidates 不过滤 purge_requested — 待删除
	// 数据的候选仍然列出, 由创建时校验 (§3.3-2) 返回 409 明确拒绝并解释。
	var rawRows []candidateRow
	if err := db.Unscoped().
		Raw(aggregateSQL, devType).
		Scan(&rawRows).Error; err != nil {
		return nil, err
	}
	if len(rawRows) == 0 {
		return []logicalDeviceCandidate{}, nil
	}
	rows := make([]logicalDeviceCandidate, len(rawRows))
	for i, raw := range rawRows {
		rows[i] = logicalDeviceCandidate{
			ID:            raw.ID,
			Name:          raw.Name,
			DeviceType:    raw.DeviceType,
			RetentionDays: raw.RetentionDays,
			InstanceCount: raw.InstanceCount,
		}
		if raw.LastDataAtRaw.Valid {
			rows[i].LastDataAt = parseCandidateTime(raw.LastDataAtRaw.String)
		}
	}

	// 权重计算需要实例集 (node_id/channel_id/hardware_id/type)。
	ids := make([]uint, len(rows))
	for i := range rows {
		ids[i] = rows[i].ID
	}
	var instances []models.EdgeDevice
	if err := db.Unscoped().
		Where("logical_device_id IN ?", ids).
		Find(&instances).Error; err != nil {
		return nil, err
	}
	byLogical := make(map[uint][]models.EdgeDevice, len(rows))
	for _, inst := range instances {
		if inst.LogicalDeviceID == nil {
			continue
		}
		byLogical[*inst.LogicalDeviceID] = append(byLogical[*inst.LogicalDeviceID], inst)
	}
	for i := range rows {
		rows[i].MatchWeight = candidateWeight(byLogical[rows[i].ID], nodeID, channelID, hardwareID, devType)
	}

	// §1.3: 权重降序; 同权重按最后数据时间降序; 均同按 id 升序 (稳定输出)。
	sort.SliceStable(rows, func(i, j int) bool {
		if rows[i].MatchWeight != rows[j].MatchWeight {
			return rows[i].MatchWeight > rows[j].MatchWeight
		}
		ti, tj := rows[i].lastTime(), rows[j].lastTime()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return rows[i].ID < rows[j].ID
	})
	return rows, nil
}

// registerEdgeDeviceCandidateRoutes 挂 GET /edge-devices/candidates。
// gin 静态段优先于 :id 通配符, 与既有 /edge-devices/:id 共存 (已验证)。
func registerEdgeDeviceCandidateRoutes(v1 *gin.RouterGroup, db *gorm.DB) {
	v1.GET("/edge-devices/candidates", func(c *gin.Context) {
		devType := strings.TrimSpace(c.Query("type"))
		if devType == "" {
			Error(c, http.StatusBadRequest, "type is required")
			return
		}
		nodeID := strings.TrimSpace(c.Query("node_id"))
		hardwareID := strings.TrimSpace(c.Query("hardware_id"))
		var channelID uint
		if v := strings.TrimSpace(c.Query("channel_id")); v != "" {
			if parsed, err := strconv.ParseUint(v, 10, 64); err == nil {
				channelID = uint(parsed)
			}
		}

		candidates, err := listLogicalDeviceCandidates(db, devType, nodeID, channelID, hardwareID)
		if err != nil {
			Error(c, http.StatusInternalServerError, err.Error())
			return
		}

		// 数据量估算: 整段共享 EstimateTimeout (3s) 预算 (T1.1 模式),
		// 任一候选估算超时/失败 → 该候选省略 row_estimate, 不阻塞整体
		// 返回 (§1.3 降级)。
		estCtx, cancel := context.WithTimeout(c.Request.Context(), datalifecycle.EstimateTimeout)
		defer cancel()
		for i := range candidates {
			rows, ok := datalifecycle.EstimateRowCount(estCtx, db, candidates[i].ID)
			if !ok {
				continue
			}
			candidates[i].RowEstimate = &rows
		}

		c.JSON(http.StatusOK, gin.H{"code": 200, "message": "ok", "data": candidates})
	})
}
