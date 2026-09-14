package datalifecycle

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// node_events 单一时间窗清理器。
//
// 裁决 (docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.2, 原文摘录):
//
//	"裁决: 统一保留 400 天, 按 created_at 删, **不设分层档位**。"
//	"是否分层: 【不分层】。99.6% 是 offline, 而 offline 恰恰是运维时间线的主内容,
//	 按 event_type 分层等于删掉时间线本身。"
//	"为什么是 400 天": 增长速率是本批八张表里最低的 (≈20 行/天/100 节点);
//	 400 天 × 100 节点 × 2 行/天 ≈ 8×10⁴ 行, 约 5 MB —— 为 5 MB 牺牲本表保存的
//	 【唯一时间线】不划算; 且 400 天与 automation_events 档位 B 对齐,
//	 少一条需要单独解释的口径。
//	"可实现性": 删的是 created_at 之前的行 (PG 侧该列无索引, 但本表体量最小、
//	 删除频率最低 —— 每日一次分批扫, 见下方"无索引"注记)。
//
// INV-5 (保留期可承载展示窗口): 本表两个消费端都有展示上限 ——
// GET /api/v1/nodes/status-history 与 GET /api/v1/nodes/:id/status-history
// 均为 limit 默认 50 / 硬上限 200 (api/handler_node.go:104-107, :154-157)。
// §2.2 明文: "若产品坚持更短 (例如 90 天, 把本表定性为「运行遥测」),
// 必须先满足 INV-5"。本次裁决值是 400 天, 而 INV-5 的约束是
// "保留窗口内 limit=200 必须仍能取满 200 条" —— 400 天 ≫ 填满 200 条所需的
// 时间 (单节点 0.2 次转换/天 ⇒ 200 条 ≈ 1000 天/节点; 即便按 1000 节点仿真
// 的聚合时间线, 200 条也在数小时到数天内填满), 因此 400 天下 INV-5 【自然满足】,
// 不构成对本次实现的额外前置条件。反过来说: 若将来有人把本常量降到 90,
// 必须重新走 INV-5 的裁决 (并给前端加"更早记录已清理"的显式提示)。
//
// 本文件补的是本批八张表里【真正被漏掉的那一张】: §2.2 的裁决 2026-09-13 就已落下,
// 但清理器一直未实现 (实测: 全仓零 Delete NodeEvent / 零 DELETE FROM node_events;
// 表 6098 行 / 2026-08-11 → 09-12, 无任何删除路径, 随运行持续增长)。
// ⚠️ security_audit_cleanup.go 文件头曾自述"本批最后一个缺口" —— 那句对
// §2.3 成立, 对本表不成立; 本文件的注释是纠正后的口径。
//
// ─ 与命令域清理器的两处结构性不同 (都由 §2.2 裁决决定, 不是新造机制) ──
//
//  1. 【无状态白名单, 整表按时间删】。命令域三表按 status/state 分档, 因为表里混有
//     "在途 / 待人工处置"的行; 本表只有状态转换事件, 没有需要显式排除的受保护取值
//     (offline 与 online 都是要删的)。§2.2 的裁决就是"不设分层档位" ——
//     这里【刻意不】按 event_type 分档: offline 占 99.6%, 给它单独一个窗口
//     等于删掉运维时间线本身 (§2.2 理由 1)。测试
//     TestNodeEventCleaner_NotTieredByEventType 用"同一窗口"断言钉住这条,
//     防止后人"顺手"加分档。
//     与 notification_cleanup.go / security_audit_cleanup.go 同型: 整表按 created_at 删。
//
//  2. 【保留期是本地常量, 不跟系统级保留期】。系统级保留期 (SystemRetentionDays,
//     默认 90) 是"逻辑设备遥测数据"的语义; §2.2 明确拒绝把本表按 90 天定性为
//     "运行遥测", 套用 90 天正是 INV-5 要防的那次退化。
//
// ─ 与既有清理器同构 (照抄范式, 不新造) ───────────────────────────────
//
//   - 无自己 goroutine 的纯执行体: 挂在既有每日 RetentionTask.RunOnce 上 (第 8 个
//     调用点, 不新增 goroutine、不改 cmd/server/main.go 的启停编排);
//   - 分批: PG 1 万 / SQLite 1 千, 每批独立事务, 批间 sleep (默认 purgeBatchSleep),
//     避免一次删除太多行造成长事务与 WAL 膨胀 (设计 §4.3 锁交互说明);
//   - 幂等: 再跑一轮删 0 行;
//   - 旁路: 失败只 slog.Warn + 指标, 绝不影响主业务与逐设备 retention 主流程;
//   - 单表 DELETE, 不级联, 且【绝不碰 nodes】(INV-3: 删事件行不得改变 nodes 行数 ——
//     本表是"已不存在的节点"的唯一历史, 删事件不等于删节点)。
//
// ─ 关于 created_at 无索引 (设计 §2.2/§7 记录的既有隐患) ───────────────
//
// PG 实测本表只有 pkey(id) 与 idx_node_events_node_id; created_at 上无索引。
// 本清理器照裁决"按 created_at 删", 【不新增索引】—— 加索引是 schema 变更,
// 超出本任务范围, 且本表是本批里体量最小、增长最慢的一张 (≈5 MB/400 天),
// 每日一次的顺序扫代价可接受。若将来本表量级上升, 应先走 §7 的索引裁决。
const (
	// DefaultNodeEventRetentionDays 是 node_events 的保留期 (天)。
	//
	// 400 = 设计 §2.2 的裁决值, 与 automation_events 档位 B 取齐 (少一条需要
	// 单独解释的口径)。它【不是】系统级保留期 (90), 也【不是】命令域/审计的 730。
	// ⚠️ 调短它 (尤其降到 90) 会触发 §2.2 末尾的 INV-5 前置条件: 必须先确认
	// 两个消费端的展示上限 (50/200) 在保留窗口内仍能取满, 并给前端加
	// "更早记录已清理"的显式提示。
	DefaultNodeEventRetentionDays = 400
)

// NodeEventCleanupReport summarizes one node_events cleanup pass.
type NodeEventCleanupReport struct {
	Deleted int64 `json:"deleted"`
	// RetentionDays 是本次【实际生效】的窗口 (天)。报告里带上它, 让"非法值 fail-closed
	// 回落"在日志里可观测 —— 否则没人能区分"删了 0 行因为没到期"与"窗口被配成了 400
	// 以外的值"。
	RetentionDays int `json:"retention_days"`
}

// NodeEventCleaner 按保留期分批硬删 node_events (整表按 created_at)。
//
// 删除条件只有一条: created_at < now() - retentionWindowDays()*24h。
// 其余任何列 (event_type / node_id / old_status / new_status) 都不参与判定 ——
// §2.2 的裁决是"按 created_at 删", 不是"按事件类型删"。测试
// TestNodeEventCleaner_DeletesByCreatedAtOnly 用一个"created_at 旧但别的时间列新"
// 的对照钉住这条。
type NodeEventCleaner struct {
	db         *gorm.DB
	batchSize  int // 0 → dialect default (PG 1万 / SQLite 1千)
	batchSleep time.Duration
	// windowDays 是显式覆盖的保留期 (天)。> 0 时生效; 非正值【不做"永久保留"解释】,
	// 一律 fail-closed 回落到 DefaultNodeEventRetentionDays —— 与
	// security_audit_cleanup.go / command_domain_cleanup.go 同一约定
	// ("不允许 0/负数表示「永久」—— 那会重新打开无界增长")。
	windowDays int
	// now is injectable for tests.
	now func() time.Time
}

// NewNodeEventCleaner creates the cleaner with the batching defaults shared
// with purge/retention.
func NewNodeEventCleaner(db *gorm.DB) *NodeEventCleaner {
	return &NodeEventCleaner{
		db:         db,
		batchSleep: purgeBatchSleep,
		now:        time.Now,
	}
}

// SetRetention overrides the retention window in days (测试/运维注入用)。
//
// ⚠️ 非正值【不是】"永久保留": 它被 retentionWindowDays 以 fail-closed 方式回落到
// DefaultNodeEventRetentionDays。把 0/负数解释成永久 = 重新打开无界增长。
func (c *NodeEventCleaner) SetRetention(days int) { c.windowDays = days }

// SetBatchSize overrides the per-transaction delete batch size (tests only).
func (c *NodeEventCleaner) SetBatchSize(n int) {
	if n > 0 {
		c.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests only; 0 disables).
func (c *NodeEventCleaner) SetBatchSleep(d time.Duration) { c.batchSleep = d }

// retentionWindowDays resolves the effective retention window (days).
//
// 脏值防御: 非正值 (0 与负数) 一律 fail-closed 回落到 DefaultNodeEventRetentionDays:
//
//   - 0 【不得】被解释为"永久保留" (那会重新打开无界增长);
//   - 负数会算出【未来】的 cutoff, 把窗内的行也一起删掉 (静默数据丢失)。
//
// 正值按原样生效 (保留期是"可配置常量")。⚠️ 调用方若把它调到 90 附近, 会踩到
// §2.2 末尾的 INV-5 前置条件 (展示上限 200 在窗口内取不满) —— 生产路径不覆盖该值,
// 常量级由 DefaultNodeEventRetentionDays = 400 钉住。
func (c *NodeEventCleaner) retentionWindowDays() int {
	if c.windowDays > 0 {
		return c.windowDays
	}
	return DefaultNodeEventRetentionDays
}

// RunOnce deletes node_events rows older than the retention window.
//
// 幂等: 再跑一轮找不得到期行, 删 0 行。
// 返回失败批次的错误; 调用方 (runOnceLogged) 只记日志 —— 清理失败绝不影响主业务。
func (c *NodeEventCleaner) RunOnce(ctx context.Context) (NodeEventCleanupReport, error) {
	report := NodeEventCleanupReport{}
	if c.db == nil {
		return report, errCommandCleanupNoDB("node event")
	}
	days := c.retentionWindowDays()
	report.RetentionDays = days
	cutoff := c.now().Add(-time.Duration(days) * 24 * time.Hour)

	deleted, err := c.deleteBefore(ctx, cutoff)
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("node_event_cleanup").Inc()
		return report, err
	}
	report.Deleted = deleted

	if report.Deleted > 0 {
		metrics.LifecyclePurgedRows.WithLabelValues("node_event_cleanup").Add(float64(report.Deleted))
	}
	return report, nil
}

// deleteBefore batch-deletes node_events rows with created_at < cutoff.
//
// 每批一个独立事务; 批间 sleep 以限制 WAL 膨胀与锁持有时间。分批范式与
// notification_cleanup.go / security_audit_cleanup.go 完全一致:
// DELETE ... WHERE id IN (SELECT id ... ORDER BY id LIMIT ?) —— PG 与 SQLite 行为相同。
//
// ORDER BY id 让批次按主键顺序推进: 没有它, 同一条 SQL 在两种方言下选出的"这批"
// 可能不同 (PostgreSQL 无 ORDER BY 的 LIMIT 不保证稳定), 幂等断言会因此偶发。
func (c *NodeEventCleaner) deleteBefore(ctx context.Context, cutoff time.Time) (int64, error) {
	batchSize := commandDomainBatchSize(c.db, c.batchSize)

	var total int64
	for batch := 0; batch < maxPurgeBatches; batch++ {
		var affected int64
		err := c.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			res := tx.Exec(
				"DELETE FROM node_events WHERE id IN (SELECT id FROM node_events WHERE created_at < ? ORDER BY id LIMIT ?)",
				cutoff, batchSize,
			)
			affected = res.RowsAffected
			return res.Error
		})
		if err != nil {
			return total, fmt.Errorf("datalifecycle: node event cleanup batch: %w", err)
		}
		total += affected
		if affected < int64(batchSize) {
			break // 到期行删尽
		}
		if err := sleepBetweenCommandBatches(ctx, c.batchSleep); err != nil {
			return total, err
		}
	}
	return total, nil
}

// runOnceLogged is the best-effort wrapper used by the daily retention task:
// 失败只记日志与指标, 不向调用方传播 (旁路清理不得影响主业务)。
func (c *NodeEventCleaner) runOnceLogged(ctx context.Context) {
	report, err := c.RunOnce(ctx)
	if err != nil {
		slog.Warn("datalifecycle: node event cleanup failed",
			"error", err, "deleted", report.Deleted, "retention_days", report.RetentionDays)
		return
	}
	if report.Deleted > 0 {
		slog.Info("datalifecycle: node events cleaned",
			"deleted", report.Deleted, "retention_days", report.RetentionDays)
	}
}
