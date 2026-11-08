package datalifecycle

// notify_wiring_test.go — 「保留期任务不再产生任何通知」的可红断言。
//
// 2026-10-23 语义变更（逻辑设备概念层移除，见 .contract/logical-device-removal.md）：
// 本文件原有 4 个接线用例，对象全部消失：
//   - TestRetentionNotifyGoesThroughNotifier / TestRetentionNotifyNilNotifierFallsBackToDB
//     被删：30/7 天两档「临期通知」随 per-identity retention 一起移除。系统级窗口
//     下到期日对全库一致，没有「这个身份快到期了」的个体语义。
//   - TestMigratorNotifyGoesThroughNotifier / TestMigratorNotifyNilNotifierFallsBackToDB
//     被删：它们依赖 NewMigrator / seedMergeSource / startMerge /
//     NotificationSourceMergeFailed，全部随合并搬迁 worker 删除。
//
// 替换它们的是**两条互补的断言**：
//   1. 运行期：RetentionTask.RunOnce 不写任何通知（本文件 TestRetention...）；
//   2. 源码级：RetentionTask 上不存在 SetNotifier 这类空壳接入口
//      （TestRetentionTask_HasNoNotifierInjectionPoint）。
//
// 第 2 条为什么必要（而不是把 SetNotifier 留成空壳）：
// cmd/server 的 notify_wiring_gate 会扫描 main.go 里「每个通知生产者都调了
// SetNotifier」。留一个不再投递的空壳 setter，会让门禁继续"绿"，而真实的
// 投递能力已经消失 —— 那正是该门禁存在的理由（防"引擎孤儿"）。因此这里把
// 「本任务不再是通知生产者」这件事钉在**接口层**：没有注入点 ⇒ 门禁不会误绿。

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// TestRetentionTask_ProducesNoExpiringNotice 钉住「保留期不再发临期通知」。
//
// 走真实任务路径 (RetentionTask.RunOnce)，不是直接调内部函数。夹具刻意做成
// 「旧实现一定会发通知」的形状: 数据 age = 80 天、系统窗口 90 天，旧的两档
// 通知正是在「剩 30 天内」触发。
func TestRetentionTask_ProducesNoExpiringNotice(t *testing.T) {
	db := testutil.OpenTestDB(t)
	now := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	SetSystemRetentionDays(90)
	defer SetSystemRetentionDays(90)

	dev := seedDevice(t, db, "no-notice", "bms_jbd", "no-notice-hw", false)
	if err := db.Create(&models.UnifiedData{
		DeviceID:   dev.ID,
		SensorName: "voltage",
		Value:      1,
		Timestamp:  now.AddDate(0, 0, -80), // 窗口内, 且旧实现在此必发 30 天档
	}).Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}

	r := NewRetentionTask(db)
	r.now = func() time.Time { return now }
	r.SetBatchSleep(0)

	results, err := r.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(results) != 1 || results[0].Days != 90 {
		t.Fatalf("results = %+v, want one result with Days=90", results)
	}
	if results[0].RowsDeleted != 0 {
		t.Errorf("RowsDeleted = %d, want 0 (row is inside the 90-day window)", results[0].RowsDeleted)
	}
	// notifications 表里没有任何行 —— 这同时覆盖了「绕过注入点直接写库」的情形。
	var n int64
	if err := db.Model(&models.Notification{}).Count(&n).Error; err != nil {
		t.Fatalf("count notifications: %v", err)
	}
	if n != 0 {
		t.Fatalf("notifications 行数 = %d, want 0 (系统级窗口下没有临期通知)", n)
	}
	// 窗口内的数据没被删（不是用「多删」换来的安静）。
	var kept int64
	if err := db.Model(&models.UnifiedData{}).Count(&kept).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	if kept != 1 {
		t.Errorf("unified_data rows = %d, want 1 (in-window row must be kept)", kept)
	}
}

// TestRetentionTask_HasNoNotifierInjectionPoint 是源码级断言:
// RetentionTask 上不得再有 SetNotifier / notifySink 这类「通知注入点」。
//
// 为什么不只是「不调用」: cmd/server 的 notify_wiring_gate 检查 main.go 里每个
// 生产者的 SetNotifier 调用。空壳 setter 会让门禁误以为「投递仍接线」，而实际
// 一条都不会发。删除注入点 ⇒ 门禁少一条 needle 是**有意的**，且有本用例背书。
func TestRetentionTask_HasNoNotifierInjectionPoint(t *testing.T) {
	forbidden := []string{"func (r *RetentionTask) SetNotifier", "notifySink", "retentionTask.SetNotifier"}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob: %v", err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, needle := range forbidden {
			if strings.Contains(string(data), needle) {
				t.Errorf("%s 仍含 %q —— RetentionTask 不再是通知生产者，"+
					"空壳注入点会让 notify_wiring_gate 误以为投递仍接线", f, needle)
			}
		}
	}
}
