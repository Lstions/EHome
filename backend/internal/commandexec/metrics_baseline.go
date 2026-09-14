package commandexec

import (
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// MetricsBaselineID 单行表的固定主键 (单行 = 表不可能增长)。
const MetricsBaselineID uint = 1

// MetricsBaselineDelta 描述【即将被删除的 command_executions 行】的按状态计数。
//
// 清理器必须: ①在同一事务内先统计将被删的行、再删除、再用本结构累加基线;
// ②累加行数与删除行数逐字段一致。原子性由调用方的事务保证 —— 分开做会留下
// "行删了但基线没加"的窗口 (面板数字凭空变小, 正是本前置条件要防的损坏)。
type MetricsBaselineDelta struct {
	Total     int64
	Succeeded int64
	Failed    int64
	Unknown   int64
	Cancelled int64
}

// AccumulateMetricsBaseline 把已删除行的计数累加进基线 (upsert, 只增不减)。
//
// 必须用 clause.OnConflict + gorm.Expr 做【数据库侧累加】而不是"读-改-写":
// 后者在并发清理/重试下会丢失更新。SQLite 与 PG 都支持 ON CONFLICT DO UPDATE。
//
// 为什么只加不覆盖: 每一次清理都是"又有一批行离开了存活集", 基线是它们的累计;
// 覆盖写会让第二次清理抹掉第一次的 failed (B-INV-3 单调性)。
//
// 传入 tx (而非 db) 是刻意的: 签名要求调用方在删除事务内调用, 让"删除 + 累加"
// 不可能被拆开。
func AccumulateMetricsBaseline(tx *gorm.DB, delta MetricsBaselineDelta) error {
	if delta.Total == 0 && delta.Succeeded == 0 && delta.Failed == 0 &&
		delta.Unknown == 0 && delta.Cancelled == 0 {
		return nil // 无事发生: 不写库, 不更新 updated_at
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"operations_total": gorm.Expr("command_metrics_baselines.operations_total + ?", delta.Total),
			"succeeded":        gorm.Expr("command_metrics_baselines.succeeded + ?", delta.Succeeded),
			"failed":           gorm.Expr("command_metrics_baselines.failed + ?", delta.Failed),
			"unknown":          gorm.Expr("command_metrics_baselines.unknown + ?", delta.Unknown),
			"cancelled":        gorm.Expr("command_metrics_baselines.cancelled + ?", delta.Cancelled),
			"updated_at":       gorm.Expr("CURRENT_TIMESTAMP"),
		}),
	}).Create(&models.CommandMetricsBaseline{
		ID:              MetricsBaselineID,
		OperationsTotal: delta.Total,
		Succeeded:       delta.Succeeded,
		Failed:          delta.Failed,
		Unknown:         delta.Unknown,
		Cancelled:       delta.Cancelled,
		UpdatedAt:       time.Now().UTC(),
	}).Error
}

// GetMetricsBaseline 读取基线 (单行)。表为空或行不存在时返回零值基线 ——
// 即"还没有任何东西被清理过", 面板行为与今天逐字一致。
//
// 读失败同样返回零值: 面板宁可少算基线 (退化成今天的 COUNT(*) 口径),
// 也不能因为基线表故障而整块 control 指标不可用。
func GetMetricsBaseline(db *gorm.DB) models.CommandMetricsBaseline {
	var baseline models.CommandMetricsBaseline
	if err := db.First(&baseline, MetricsBaselineID).Error; err != nil {
		return models.CommandMetricsBaseline{}
	}
	return baseline
}
