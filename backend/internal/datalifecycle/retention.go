package datalifecycle

import (
	"log/slog"
	"sync/atomic"
)

// systemRetentionDays holds the system-level retention snapshot source
// (方案 §4.1). main.go sets it from config at startup; logical device
// creation paths read it. Snapshot semantics: the value is captured at
// creation time, later config changes never retroact on existing rows.
//
// ==================== 0 = 永久保留（2026-11-07）====================
//
// 用户裁决「所有数据要长期保留」，因此 0 是合法值，含义为「不自动删除」，
// 不再是「非法值」（见 docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §5）。
//
// 改之前是个反向 bug：RunOnce 里 `if days <= 0 { days = 1 }` 把 0 当成 1 天，
// 于是「想永久保留」的运维把环境变量设成 0，反而删得比 90 天更狠 —— 而且不会有
// 任何报错，只有数据一天天变少。
//
// 负值仍然拒绝：它更可能是配置写错（例如 shell 变量没展开成空串再减一），
// 而不是「比永久更永久」。拒绝 = 保持原值，不动。
var systemRetentionDays atomic.Int64

func init() {
	systemRetentionDays.Store(90) // must match config.DefaultDataRetentionDays
}

// SetSystemRetentionDays updates the snapshot source.
//
//	days > 0  ⇒ 保留窗口（天）
//	days == 0 ⇒ 永久保留（不自动删除数据；旁路清理器各按自己的窗口照常运行）
//	days < 0  ⇒ 忽略（配置写错，保持原值；不把负值解释成永久或 1 天）
//
// 为什么 0 必须能覆盖掉原值：main.go 启动时无条件调用本函数把 config 值写进来，
// 如果这里仍用 `if days > 0` 忽略 0，那么 `EHOME_DATA_RETENTION_DAYS=0` 就永远
// 到不了 RunOnce —— 配置层的语义无法传导到执行层。
func SetSystemRetentionDays(days int) {
	if days < 0 {
		// 负值忽略，但要吼一声 —— 静默忽略一个「本想永久保留」的配置写错，
		// 结果是数据按默认窗口一天天被删，而运维以为自己设对了。
		slog.Warn("datalifecycle: ignoring negative data retention days (keeping current value)",
			"requested_days", days, "effective_days", SystemRetentionDays())
		return
	}
	systemRetentionDays.Store(int64(days))
	// 启动期由 main.go 无条件调用一次，因此这条日志就是运维要看的「本轮到底删不删」。
	// 永久保留是**用户明确要求的状态**，不能只体现在「没有删除日志」上 ——
	// 「没有日志」和「任务没跑」在运维眼里长得一样。
	if days == 0 {
		slog.Info("datalifecycle: 数据永久保留（不自动删除）: retention=0，" +
			"unified_data/device_data/telemetry_frame 不会被保留期清理；" +
			"旁路清理器（通知/审计/命令域等）仍按各自窗口运行")
	} else {
		slog.Info("datalifecycle: data retention window configured", "days", days)
	}
}

// SystemRetentionDays returns the current system-level retention (days).
//
// 0 表示永久保留（不自动删除），不是「未配置」。调用方必须区分:
//   - 想知道「要不要删数据」→ 用 IsRetentionPermanent()
//   - 想算 cutoff → 先判永久；永久的 cutoff 无意义（不能用 now 当 cutoff）
func SystemRetentionDays() int {
	return int(systemRetentionDays.Load())
}

// IsRetentionPermanent reports whether data is kept forever (retention <= 0).
//
// 抽成一个具名函数而不是让每个调用方写 `days <= 0`：这个判断的语义
// （「不删」而不是「非法」）是本轮改动的核心，散落的 `<= 0` 会让下一个人
// 重新理解成「脏值兜底」并顺手改回 1 天。
func IsRetentionPermanent() bool {
	return SystemRetentionDays() <= 0
}
