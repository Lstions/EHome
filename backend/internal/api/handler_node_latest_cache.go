package api

// handler_node_latest_cache.go — /nodes/:id/latest 的缓存路径（task-24 步骤 1）。
//
// ============ 为什么这条查询必须改 ============
//
// 原实现对 unified_data 做**无时间界**的
//
//	GROUP BY device_id 取 MAX(timestamp) 再 JOIN 回原表
//
// 生产实测 **0.863s** —— 是唯一「无缓存、直查 DB」的最新值读者。
// 而 unified_data 正在退役，停写之后这条查询会在过渡期结束时失效。
//
// ============ 为什么不需要新建节点级缓存 ============
//
// 数据早就在内存里了：SensorParserConsumer 每写入成功一条就 SetLatestValue，
// 按 (设备, sensor_name) 分槽。overview 与设备列表接口已经在用它。
//
// 节点级结果只是对每设备缓存的一次**廉价聚合**（N 设备 x ~29 字段 = 几百项，微秒级）。
// 若另建节点级缓存，就必须回答「哪些配置变化要进缓存键」——通道启用/禁用、设备增删、
// 软删/恢复……**任何一个漏项都是跨配置串味**。
// 直接读每设备缓存 => 这一层**没有键** => 该问题不存在。
//
// ============ 一个刻意的行为变更（不是回归）============
//
// 原 SQL 是裸 SQL，JOIN edge_devices **不经过软删 scope** => 软删设备仍会出现在结果里；
// 本文件用 GORM Find(&devices)，**默认排除软删**。
//
// 选择与 overview、设备列表保持一致（都排除软删），而不是 bug-for-bug 兼容旧 SQL ——
// 兼容一个前后不一致的行为只会把不一致固化下来。演练清单里单列一条核对。

import (
	"sort"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// nodeLatestValue 是 /nodes/:id/latest 的线上形状（字段名与 JSON tag 必须与历史一致）。
type nodeLatestValue struct {
	ChannelID  uint      `json:"channel_id"`
	SensorName string    `json:"sensor_name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	Timestamp  time.Time `json:"timestamp"`
}

// nodeLatestValuesFromCache 从每设备最新值缓存聚合出节点级结果。
//
// 返回 ok=false 表示**缓存不完整**（该节点下没有设备 / 任一设备还没上报过），
// 调用方必须回落原 SQL —— 冷缓存返回空结果会让刚部署后的首屏空掉。
func nodeLatestValuesFromCache(db *gorm.DB, channelIDs []uint) ([]nodeLatestValue, bool) {
	if db == nil || len(channelIDs) == 0 {
		return nil, false
	}
	var devices []models.EdgeDevice
	// 默认 scope 排除软删设备（见文件头的「刻意的行为变更」）。
	if err := db.Model(&models.EdgeDevice{}).
		Where("channel_id IN ?", channelIDs).Order("id ASC").Find(&devices).Error; err != nil {
		return nil, false
	}
	if len(devices) == 0 {
		return nil, false
	}
	values := make([]nodeLatestValue, 0, len(devices)*16)
	for i := range devices {
		cached := LatestValues(devices[i].ID)
		if len(cached) == 0 {
			// 任一设备冷 => 整节点回落。不能只返回热的那部分：
			// 前端看到「某台设备的数据凭空消失」比多一次查询糟得多。
			return nil, false
		}
		for j := range cached {
			values = append(values, nodeLatestValue{
				ChannelID:  devices[i].ChannelID,
				SensorName: cached[j].SensorName,
				Value:      cached[j].Value,
				Unit:       cached[j].Unit,
				Timestamp:  cached[j].Timestamp,
			})
		}
	}
	// 每设备缓存是 map，迭代顺序随机。不排序会让同一请求两次返回的顺序不同 ——
	// 前端按名取值不受影响，但会让「响应形状不变」的断言随机失败。
	sort.SliceStable(values, func(a, b int) bool {
		if values[a].ChannelID != values[b].ChannelID {
			return values[a].ChannelID < values[b].ChannelID
		}
		return values[a].SensorName < values[b].SensorName
	})
	return values, true
}
