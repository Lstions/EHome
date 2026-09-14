package datalifecycle

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/commandexec"
)

// ─ 命令域三表清理 (设计: docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md
//   §2.4 / §2.5 / §2.6 与 §3 专项) ───────────────────────────────────────
//
// 三张表的保留策略【各不相同】, 因此本文件刻意【不】提供"一套参数删三张表"的
// 通用入口 —— 那正是设计文档 §3 专项要防的错误:
//
//	把 outbox 当审计 → 为一个已无作用的队列行付 730 天的存储与合规解释成本;
//	把 attempt 当副产物 → 抹掉设备动作责任归属的唯一物理证据。
//
//	| 表                  | 角色       | 时间字段                          | 窗口  | 档位                        |
//	|---------------------|-----------|----------------------------------|------|-----------------------------|
//	| command_executions  | 真相源     | COALESCE(completed_at, created_at)| 730d | 终态 (未处置 UNKNOWN 除外)   |
//	| command_attempts    | 防伪锚点   | created_at                        | 730d | 全部 (与 execution 同寿命)   |
//	| command_outboxes    | 协议中间态 | COALESCE(processed_at, created_at)| 30d  | 终态且对应 execution 终态    |
//
// 三条硬约束 (每条都有实测依据, 违反即缺陷):
//
//  1. 【未处置的 UNKNOWN 永不随时间删】—— commandexec/service.go:594-600 的人工处置
//     入口必须先加载 execution 行 (First(&execution, "command_id = ?")), 行没了悬案
//     永远结不了案。本文件通过复用 commandexec/retention_scope.go 的
//     PrunableExecutionsQuery 落实, 而不是自己再写一遍状态白名单。
//
//  2. 【unresolved_unknown 不吃基线】—— 它是集合成员数, 不是累计量。读侧
//     (api/handler_metrics.go) 已按此实现, 本文件【不碰】它; 与之配合的是约束 1
//     (未处置的 UNKNOWN 根本不会被删), 两者同时成立才闭合。
//
//  3. 【command_attempts 不得短留】—— nodemgr/handler_channel_cmd_v2.go:42-46 用
//     attempt.BootID + attempt.WireDigest 逐字节校验设备上报帧
//     (matchesAttemptDigest → subtle.ConstantTimeCompare), 而
//     commandexec/inbox.go:188-205 的 RecoverExpired 会把超时的 DISPATCHED 改写成
//     UNKNOWN, 抹掉"曾经发出过"这一事实 ⇒ attempt 是唯一见证。
//
// 三条通用实现约束 (照既有范式, 不新造):
//   - 挂在既有 RetentionTask.RunOnce 的旁路段, 不新增 goroutine, 不改 main.go;
//   - 分批删除 (PG 1 万 / SQLite 1 千) + 批间 sleep, 每批独立事务, 避免长事务持锁;
//   - 失败只 slog.Warn + 指标, 绝不影响逐设备 retention 主流程;
//   - 单表 DELETE, 不级联 (三表之间实测零 DB 外键, 设计 §2.1/INV-3)。

const (
	// DefaultCommandExecutionRetentionDays 是 command_executions (真相源) 的保留期。
	//
	// 730 天而非更短: 与 security_audit_events 的审计保留期取齐 (INV-6), 避免产生
	// "审计行还在、被引用的命令行没了"的孤证 (security_audit_events.request_id →
	// command_executions.command_id, 无 DB 外键, 删了不报错只静默悬空)。
	DefaultCommandExecutionRetentionDays = 730

	// DefaultCommandAttemptRetentionDays 是 command_attempts (防伪锚点) 的保留期。
	//
	// 【必须 >= DefaultCommandExecutionRetentionDays】(INV-6)。本仓已因反例被证伪过
	// 一次: notification_deliveries 曾被压到通知本体窗口的一半 (45 天), 结果是
	// 证据先于被审计对象死亡 (见 notification_delivery_cleanup.go 文件头留档)。
	// attempts 是同一类错误的更高代价版本 —— 它抹掉的是设备动作责任归属。
	DefaultCommandAttemptRetentionDays = 730

	// DefaultCommandOutboxRetentionDays 是 command_outboxes (协议中间态) 的保留期。
	//
	// 30 天是【唯一可以短留】的一张表, 依据是一次实跑过的等价性验证 (INV-9):
	// A) payload_json 逐字节等于 execution.params_json (实测 3776/3776, mismatched=0);
	// B) processed_at ≈ attempt.published_at (实测仅 11/3773 精确相等, 最大差 93µs,
	//    且 outbox_before_attempt=0 —— 无一条 outbox 早于对应 attempt, 差距恒在同一
	//    事务内的两次 time.Now() 之间)。
	// ⚠️ B 是【容差】关系, 不是等号。任何地方把它当等号断言都会永久变红, 反而阻止
	// 一个正确的清理 (设计 §INV-9 的自我修正)。
	//
	// 不建议更短: 小于 execution 死线 (2 分钟) 的窗口会让"这条命令为什么没发出去"
	// 不可查; 30 天覆盖 dispatcher.go:194-200 的崩溃窗口 ("MQTT 可能已接受报文而本
	// 事务随后回滚, 下一次租约会重新发布字节相同的命令") 的事后调查。
	DefaultCommandOutboxRetentionDays = 30
)

// terminalExecutionStatuses 返回 execution 的终态白名单 (与 commandexec/state.go 的
// IsTerminal 逐项一致)。
//
// 【白名单而非黑名单】: DELETE 只认列出的取值, 将来若新增终态 (例如 'SUPERSEDED'),
// 清理器不会把它当垃圾删掉 —— 对"看不懂的东西"必须 fail-closed 地保留。
// (与 notification_delivery_cleanup.go 的 deliveryRetentionClasses 同一约定。)
func terminalExecutionStatuses() []string {
	return []string{
		commandexec.StatusSucceeded,
		commandexec.StatusFailed,
		commandexec.StatusUnknown,
		commandexec.StatusCancelled,
	}
}

// outboxTerminalStates 返回 command_outboxes 的终态白名单。
//
// 【刻意排除 PENDING/LEASED】: 它们是"尚未交给传输层"的在途状态, 无论多老都不得
// 删除 (INV-4) —— 删掉一条 PENDING 行等于让一条已持久化的命令永远发不出去。
func outboxTerminalStates() []string {
	return []string{"PROCESSED", "CANCELLED"}
}

// commandDomainBatchSize 解析每批删除行数: PG 1 万 / SQLite 1 千,
// 与 purge/retention/notification 清理同一口径 (purge.go:19-24)。
func commandDomainBatchSize(db *gorm.DB, configured int) int {
	if configured > 0 {
		return configured
	}
	if db != nil && db.Dialector != nil && db.Dialector.Name() != "postgres" {
		return purgeBatchSizeSQLite
	}
	return purgeBatchSizePostgres
}

// placeholders 生成 n 个逗号分隔的 ? 占位符 (n<=0 → 空串)。
//
// 为什么不直接把切片当参数交给 GORM: 本文件里的白名单要出现在【裸 SQL】中,
// 显式生成占位符让 PG 与 SQLite 的展开逐字相同 (与 notification_delivery_cleanup.go
// 的 deleteBefore 一致), 不存在方言上的展开差异。
func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	s := ""
	for i := 0; i < n; i++ {
		if i > 0 {
			s += ","
		}
		s += "?"
	}
	return s
}

// sleepBetweenCommandBatches 让出批间间隔; sleep<=0 时立即返回 (测试用)。
// ctx 取消时返回 ctx.Err(), 让长清理可以被打断 (与 purge/retention 一致)。
func sleepBetweenCommandBatches(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}

// errCommandCleanupNoDB 是清理器在没有 db 时的统一报错。
func errCommandCleanupNoDB(name string) error {
	return fmt.Errorf("datalifecycle: %s cleanup requires a db", name)
}
