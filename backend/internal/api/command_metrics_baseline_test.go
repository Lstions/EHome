package api

// 清理前置条件 B 的不变式守护 (见 docs/分析/清理前置条件-冷却锚点与监控基线-2026-09-14.md §3)。
//
// B-INV-1:  删掉 command_executions 的历史行后, control 的【事件累计量】
//           (operations_total/succeeded/failed/unknown/cancelled) 不得减少。
// B-INV-1b: unresolved_unknown 是【集合成员数】而非累计量 ——
//           ① 它【不接受】基线补偿 (接受会让被删的悬案永久虚高且不可纠正);
//           ② 未处置的 UNKNOWN 行【不得被删除】, 否则处置入口
//              (commandexec/service.go:594-600 先加载该行) 永远失败, 悬案不可结案。
// B-INV-2:  control 的 12 个 JSON 字段名与语义不变 (前端零改动)。
// B-INV-3:  基线只增不减 (upsert 必须是累加, 不是覆盖)。
//
// 变异自证:
//   - 把 handler 的 `resp.Control.Failed += baseline.Failed` 删掉 ⇒ B-INV-1 必红;
//   - 把 `resp.Control.UnresolvedUnknown += baseline.UnresolvedUnknown` 加上 ⇒ B-INV-1b 必红;
//   - 把 AccumulateMetricsBaseline 的 gorm.Expr 累加改成直接赋值 ⇒ B-INV-3 必红。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func newMetricsBaselineRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.CommandExecution{}, &models.CommandManualResolution{},
		&models.CommandMetricsBaseline{}, &models.SecurityAuditEvent{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	registerMetricsRoutes(r.Group("/api/v1"), db)
	return r, db
}

// fetchControl 走真实 HTTP 链路取 control 段, 并保留原始 JSON 对象用于字段名断言。
func fetchControl(t *testing.T, r *gin.Engine) (map[string]int64, map[string]json.RawMessage) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/metrics/summary", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics/summary = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Data struct {
			Control map[string]json.RawMessage `json:"control"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode envelope: %v", err)
	}
	counts := make(map[string]int64, len(envelope.Data.Control))
	for k, raw := range envelope.Data.Control {
		var v int64
		if err := json.Unmarshal(raw, &v); err != nil {
			t.Fatalf("control.%s is not an integer: %s", k, raw)
		}
		counts[k] = v
	}
	return counts, envelope.Data.Control
}

// executedRow 造一条指定状态/时间的执行行。CommandID 是主键, 必须唯一。
func executedRow(t *testing.T, db *gorm.DB, commandID, status string, at time.Time) {
	t.Helper()
	row := models.CommandExecution{
		CommandID: commandID, EdgeDeviceID: 1, NodeID: "node-metrics",
		ActionID: "low_read", ActionVersion: 1, ActorUserID: 1,
		IdempotencyScope: "test", IdempotencyKey: "idem-" + commandID,
		Status: status, CreatedAt: at, UpdatedAt: at,
	}
	if status == "SUCCEEDED" || status == "FAILED" || status == "UNKNOWN" || status == "CANCELLED" {
		done := at
		row.CompletedAt = &done
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
}

// pruneWithBaseline 模拟清理器: 【同一事务】内统计将被删的行 → 删除 → 累加基线。
// 这正是下一任务要实现的写方形状, 本测试用它证明"清理后累计计数不变"。
//
// 删除范围【直接调用生产代码】commandexec.PrunableExecutionsQuery —— 若有人把
// 未处置的 UNKNOWN 加进删除白名单, TestUnresolvedUnknownStaysActionable 会立刻变红,
// 而不是等上线后由用户发现悬案结不了案。
func pruneWithBaseline(t *testing.T, db *gorm.DB, cutoff time.Time) {
	t.Helper()
	err := db.Transaction(func(tx *gorm.DB) error {
		var rows []models.CommandExecution
		if err := commandexec.PrunableExecutionsQuery(tx, cutoff).Find(&rows).Error; err != nil {
			return err
		}
		delta := commandexec.MetricsBaselineDelta{Total: int64(len(rows))}
		ids := make([]string, 0, len(rows))
		for _, row := range rows {
			ids = append(ids, row.CommandID)
			switch row.Status {
			case "SUCCEEDED":
				delta.Succeeded++
			case "FAILED":
				delta.Failed++
			case "UNKNOWN":
				delta.Unknown++
			case "CANCELLED":
				delta.Cancelled++
			}
		}
		if len(ids) > 0 {
			if err := tx.Where("command_id IN ?", ids).Delete(&models.CommandExecution{}).Error; err != nil {
				return err
			}
		}
		return commandexec.AccumulateMetricsBaseline(tx, delta)
	})
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
}

// TestMetricsCountsSurviveCommandPruning B-INV-1: 清理历史终态行后 5 个累计量不得减少。
func TestMetricsCountsSurviveCommandPruning(t *testing.T) {
	r, db := newMetricsBaselineRouter(t)
	now := time.Now().UTC()
	old := now.Add(-800 * 24 * time.Hour)

	executedRow(t, db, "cmd-failed-old-1", "FAILED", old)
	executedRow(t, db, "cmd-failed-old-2", "FAILED", old.Add(-time.Hour))
	executedRow(t, db, "cmd-failed-new", "FAILED", now)
	executedRow(t, db, "cmd-succeeded-new", "SUCCEEDED", now)
	executedRow(t, db, "cmd-cancelled-old", "CANCELLED", old)

	before, _ := fetchControl(t, r)
	if before["failed"] != 3 || before["operations_total"] != 5 {
		t.Fatalf("前置条件失败: 清理前 failed=%d operations_total=%d, 期望 3/5",
			before["failed"], before["operations_total"])
	}

	pruneWithBaseline(t, db, now.Add(-730*24*time.Hour))

	after, _ := fetchControl(t, r)
	for _, field := range []string{"operations_total", "succeeded", "failed", "unknown", "cancelled"} {
		if after[field] < before[field] {
			t.Fatalf("清理后 %s 从 %d 降到 %d —— 累计计数被清理改写 "+
				"(面板会显示成故障自愈, B-INV-1 被破坏)", field, before[field], after[field])
		}
	}
	if after["failed"] != 3 {
		t.Fatalf("清理后 failed=%d, 期望仍为 3 (2 条历史行已进基线)", after["failed"])
	}
	if after["operations_total"] != 5 {
		t.Fatalf("清理后 operations_total=%d, 期望仍为 5", after["operations_total"])
	}
}

// TestUnresolvedUnknownIgnoresBaseline B-INV-1b①:
// unresolved_unknown 不接受基线补偿 —— 否则被删的悬案会永久虚高且不可纠正。
func TestUnresolvedUnknownIgnoresBaseline(t *testing.T) {
	r, db := newMetricsBaselineRouter(t)
	now := time.Now().UTC()
	executedRow(t, db, "cmd-unknown-open", "UNKNOWN", now)

	before, _ := fetchControl(t, r)
	if before["unresolved_unknown"] != 1 {
		t.Fatalf("前置条件失败: unresolved_unknown=%d, 期望 1", before["unresolved_unknown"])
	}

	// 恶意/错误的基线: 声称有 5 条悬案已被清理。
	if err := db.Create(&models.CommandMetricsBaseline{
		ID: commandexec.MetricsBaselineID, OperationsTotal: 5, Unknown: 5, UpdatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	after, _ := fetchControl(t, r)
	if after["unresolved_unknown"] != 1 {
		t.Fatalf("unresolved_unknown=%d, 期望仍为 1 —— 它被基线污染了。它是【集合成员数】"+
			"(当前 UNKNOWN 且尚未被处置), 不是累计量; 基线化会让被删的悬案永久虚高, "+
			"而用户再也无法处置它 (B-INV-1b 被破坏)", after["unresolved_unknown"])
	}
	// 正向对照: 同一基线下 unknown (累计量) 必须被补偿, 证明本断言非空洞。
	if after["unknown"] != 6 {
		t.Fatalf("unknown=%d, 期望 1+5=6 (累计量必须吃基线)", after["unknown"])
	}
}

// TestUnresolvedUnknownStaysActionable B-INV-1b② (更强的一条):
// 未处置的 UNKNOWN 行【不得被删除】—— 处置入口必须先加载该行, 行没了悬案永远结不了案。
// 这条不变式同时约束清理器: 若有人删了它, 本测试会以"处置失败"的形式变红。
func TestUnresolvedUnknownStaysActionable(t *testing.T) {
	_, db := newMetricsBaselineRouter(t)
	now := time.Now().UTC()
	old := now.Add(-900 * 24 * time.Hour)
	executedRow(t, db, "cmd-unknown-ancient", "UNKNOWN", old)

	// 模拟"遵守上游 §2.4 的清理器": 未处置的 UNKNOWN 不参与时间窗删除。
	pruneWithBaseline(t, db, now.Add(-730*24*time.Hour))
	var remaining int64
	if err := db.Model(&models.CommandExecution{}).
		Where("command_id = ?", "cmd-unknown-ancient").Count(&remaining).Error; err != nil {
		t.Fatal(err)
	}
	if remaining != 1 {
		t.Fatal("未处置的 UNKNOWN 行被清理器删除了 —— 它将永远无法被处置 " +
			"(commandexec/service.go:594-600 的处置入口必须先加载该行), B-INV-1b 被破坏")
	}

	// 端到端: 该行仍可被人工处置 (这正是"不删"要保住的能力)。
	svc := commandexec.NewService(db, nil)
	if _, _, err := svc.ResolveUnknown(context.Background(), commandexec.ResolveUnknownInput{
		CommandID: "cmd-unknown-ancient", ActorUserID: 1, Outcome: commandexec.ResolutionAcknowledgedUnknown,
		Reason: "测试: 悬案仍可结案", SourceIP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("古老但未处置的 UNKNOWN 已无法结案: %v —— 清理器删掉了它, 悬案永久悬挂", err)
	}
}

// TestControlContractFieldNamesUnchanged B-INV-2: 前端契约字段名逐字不变。
func TestControlContractFieldNamesUnchanged(t *testing.T) {
	r, _ := newMetricsBaselineRouter(t)
	_, raw := fetchControl(t, r)
	want := []string{
		"operations_total", "active", "queued", "succeeded", "failed", "unknown",
		"unresolved_unknown", "cancelled", "outbox_pending", "outbox_leased",
		"capability_stale_nodes", "audit_write_failures",
	}
	for _, field := range want {
		if _, ok := raw[field]; !ok {
			t.Fatalf("control.%s 缺失 —— 前端契约 (frontend-shared/src/api/monitor.ts) 会被破坏", field)
		}
	}
	if len(raw) != len(want) {
		keys := make([]string, 0, len(raw))
		for k := range raw {
			keys = append(keys, k)
		}
		t.Fatalf("control 字段数=%d, 期望 %d (字段集合变了: %v)", len(raw), len(want), keys)
	}
}

// TestBaselineIsMonotonic B-INV-3: 基线累加只增不减 (upsert 不能是覆盖写)。
func TestBaselineIsMonotonic(t *testing.T) {
	_, db := newMetricsBaselineRouter(t)
	err := db.Transaction(func(tx *gorm.DB) error {
		if err := commandexec.AccumulateMetricsBaseline(tx,
			commandexec.MetricsBaselineDelta{Total: 2, Failed: 2}); err != nil {
			return err
		}
		return commandexec.AccumulateMetricsBaseline(tx,
			commandexec.MetricsBaselineDelta{Total: 3, Failed: 1, Succeeded: 2})
	})
	if err != nil {
		t.Fatalf("累加: %v", err)
	}
	got := commandexec.GetMetricsBaseline(db)
	if got.Failed != 3 || got.Succeeded != 2 || got.OperationsTotal != 5 {
		t.Fatalf("基线=%+v, 期望 failed=3 succeeded=2 operations_total=5 —— "+
			"第二次累加抹掉了第一次的结果 (upsert 被写成覆盖, B-INV-3 被破坏)", got)
	}
	// 单行不变量: 无论累加多少次, 表里都只有一行。
	var rows int64
	db.Model(&models.CommandMetricsBaseline{}).Count(&rows)
	if rows != 1 {
		t.Fatalf("基线表有 %d 行, 期望单行 (单行表是表不可能增长的保证)", rows)
	}
}
