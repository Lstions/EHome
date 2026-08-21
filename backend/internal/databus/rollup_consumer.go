package databus

import (
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// RollupConsumer 将解析后的 unified_data 记录聚合进分钟级 rollup 表
// (数据层时序化, 方案 v3.4 §3.2.2)。
//
// 挂接方式: 非 DataConsumer——物理量由 SensorParserConsumer 解析得到,
// 通过 rollupSink 回调注入 (复用 deviceActivity 回调先例), 避免独立
// consumer 重复解析 RawData (10000 events/s 基线下 CPU 翻倍, §5.1.2 裁决)。
// 仅 PG 执行; SQLite (单测) no-op。
type RollupConsumer struct {
	db *gorm.DB
}

// NewRollupConsumer creates a RollupConsumer.
func NewRollupConsumer(db *gorm.DB) *RollupConsumer {
	return &RollupConsumer{db: db}
}

// Name identifies the sink in logs/metrics.
func (c *RollupConsumer) Name() string { return "rollup" }

// Upsert aggregates parsed records into unified_data_rollup_1m.
// 失败仅 WARN + metrics 计数 (fail-open): rollup 缺失只影响长跨度查询精度,
// 不阻塞采集链路。SQLite no-op。
func (c *RollupConsumer) Upsert(records []models.UnifiedData) {
	if c == nil || c.db == nil || len(records) == 0 {
		return
	}
	if c.db.Dialector == nil || c.db.Dialector.Name() != "postgres" {
		return // SQLite 测试环境: rollup 表不存在, no-op
	}
	for i := range records {
		r := &records[i]
		bucket := r.Timestamp.Truncate(time.Minute)
		// 增量聚合: min/max 用 LEAST/GREATEST, avg/cnt 增量更新,
		// last_v/last_id 取新值 (同分钟最新样本), 保形去重锚点 MAX(id) 语义由
		// last_id = GREATEST(表内 last_id, 新 id) 维持。
		ups := fmt.Sprintf(
			`INSERT INTO %s (device_id, sensor_name, bucket, min_v, max_v, avg_v, last_v, last_id, cnt)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 1)
			 ON CONFLICT (device_id, sensor_name, bucket) DO UPDATE SET
			   min_v  = LEAST(unified_data_rollup_1m.min_v, EXCLUDED.min_v),
			   max_v  = GREATEST(unified_data_rollup_1m.max_v, EXCLUDED.max_v),
			   avg_v  = (unified_data_rollup_1m.avg_v * unified_data_rollup_1m.cnt + EXCLUDED.avg_v) / (unified_data_rollup_1m.cnt + 1),
			   last_v = EXCLUDED.last_v,
			   last_id = GREATEST(unified_data_rollup_1m.last_id, EXCLUDED.last_id),
			   cnt    = unified_data_rollup_1m.cnt + 1`,
			models.UnifiedDataRollup1m{}.TableName(),
		)
		if err := c.db.Exec(ups,
			r.DeviceID, r.SensorName, bucket,
			r.Value, r.Value, r.Value, r.Value, r.ID,
		).Error; err != nil {
			slog.Warn("rollup: upsert failed", "device_id", r.DeviceID, "sensor", r.SensorName, "error", err)
		}
	}
}
