package datasource

// 独立验证（impl-core）：composite 禁 active（V-6/I5）+ 启动自检（N-1）。
//
// 自检的职责不是"防 API bug"（API 已 400），而是**防绕过 API 的脏数据**：
// 直连 DB 修库/迁移残留/并发写窗口都可能留下 composite active 或单组多 active；
// 启动时必须 fail-closed，绝不带着自相矛盾状态发布快照。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// ── composite 无 active 语义（领域层）─────────────────────────────────────

func TestVerifyComposite_NoActiveSemantics(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createComposite(models.CombineAvg, a, b)

	// 创建后不得有 active，快照 ActiveMemberID=0。
	if edge := f.activeEdge(g.ID); edge != 0 {
		t.Fatalf("composite 不得有 active，got %d", edge)
	}
	if snap := f.snapshot(g.ID); snap.ActiveMemberID != 0 {
		t.Fatalf("composite 快照 ActiveMemberID=%d want 0", snap.ActiveMemberID)
	}

	// V-6：禁止 activate。
	bm := f.memberOf(g.ID, b.ID)
	if _, err := f.gsvc.ActivateGroupMember(g.ID, bm.ID); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("composite activate 应 ErrInvalidRequest（V-6），got %v", err)
	}
	// 观察项 D-3：deactivate 在领域层**不拒绝**（非 active 分支直接 disabled）。
	// handler 注释声明 composite deactivate 应 400；两处口径不一致，已记报告。
	if _, err := f.gsvc.DeactivateGroupMember(g.ID, bm.ID); err == nil {
		if cur := f.memberOf(g.ID, b.ID); cur.Status != models.SourceMemberStatusDisabled {
			t.Logf("DEVIATION D-3: composite deactivate 返回成功但状态=%q（handler 注释声明应 400）", cur.Status)
		} else {
			t.Logf("DEVIATION D-3: composite deactivate 被允许（R8 语义），而 handler 注释声明 composite 应 400")
		}
	}
	_ = a
}

// ── 启动自检：干净库通过；脏库必须报错且不发布 ───────────────────────────

func TestVerifySelfCheck_CleanPasses(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)

	if err := f.gsvc.SelfCheck(); err != nil {
		t.Fatalf("干净库 SelfCheck 应通过，got %v", err)
	}
	if err := f.gsvc.PublishAll(); err != nil {
		t.Fatalf("干净库 PublishAll 应通过，got %v", err)
	}
	if _, ok := f.reg.Group(g.ID); !ok {
		t.Fatal("PublishAll 后快照应可查")
	}
}

func TestVerifySelfCheck_CompositeActiveRejected(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createComposite(models.CombineAvg, a, b)
	// 干净状态下先发布一次，记录快照基线。
	if err := f.gsvc.PublishAll(); err != nil {
		t.Fatalf("PublishAll before dirty: %v", err)
	}

	// 绕过 API 直写 DB：把 composite 成员改成 active（模拟迁移残留/手工修库）。
	if err := f.db.Model(&models.SourceGroupMember{}).Where("group_id = ? AND edge_device_id = ?", g.ID, a.ID).
		UpdateColumn("status", models.SourceMemberStatusActive).Error; err != nil {
		t.Fatalf("dirty write: %v", err)
	}

	err := f.gsvc.SelfCheck()
	if err == nil {
		t.Fatal("composite active 必须被启动自检拒绝")
	}
	if !strings.Contains(err.Error(), "composite") {
		t.Fatalf("自检错误应指出 composite，got %v", err)
	}
	// PublishAll 必须返回 error（main.go 据此拒绝启动），且不得替换快照。
	if err := f.gsvc.PublishAll(); err == nil {
		t.Fatal("脏库 PublishAll 必须返回 error（fail-closed）")
	}
	snap := f.snapshot(g.ID)
	if snap.ActiveMemberID != 0 {
		t.Fatalf("脏库不得发布带 active 的 composite 快照，got %d", snap.ActiveMemberID)
	}
}

func TestVerifySelfCheck_MultiActiveRejected(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createPB(a, b)

	// 正常路径有部分唯一索引兜底；自检相关的是"索引缺失/历史脏库"。
	// 为验证自检本身，先删索引（仅内存/文件测试库），再制造双 active。
	if err := f.db.Exec("DROP INDEX IF EXISTS ux_sgm_one_active").Error; err != nil {
		t.Fatalf("drop partial unique index: %v", err)
	}
	if err := f.db.Model(&models.SourceGroupMember{}).Where("group_id = ?", g.ID).
		UpdateColumn("status", models.SourceMemberStatusActive).Error; err != nil {
		t.Fatalf("dirty write: %v", err)
	}
	var active int64
	if err := f.db.Model(&models.SourceGroupMember{}).
		Where("group_id = ? AND status = ?", g.ID, models.SourceMemberStatusActive).Count(&active).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != 2 {
		t.Fatalf("前置失败：应有 2 条 active，got %d", active)
	}

	err := f.gsvc.SelfCheck()
	if err == nil {
		t.Fatal("单组多 active 必须被启动自检拒绝（I1）")
	}
	if !strings.Contains(err.Error(), "active") {
		t.Fatalf("自检错误应指出 active 冲突，got %v", err)
	}
	if err := f.gsvc.PublishAll(); err == nil {
		t.Fatal("脏库 PublishAll 必须返回 error")
	}
}

// ── Start：自检失败时扫描器必须不启动（fail-closed），正常时可被取消 ──────

func TestVerifySelfCheck_StartFailClosed(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	g := f.createComposite(models.CombineAvg, a, b)
	// 部分唯一索引是 (group_id) WHERE active ⇒ 同组只能有 1 条 active；
	// 对 composite 而言**1 条都非法**（I5），所以只改一个成员即可构造脏数据。
	if err := f.db.Model(&models.SourceGroupMember{}).Where("group_id = ? AND edge_device_id = ?", g.ID, a.ID).
		UpdateColumn("status", models.SourceMemberStatusActive).Error; err != nil {
		t.Fatalf("dirty write: %v", err)
	}

	started := make(chan struct{})
	go func() {
		defer close(started)
		f.gsvc.Start(context.Background()) // 脏库必须立即 return（不进入 ticker 循环）
	}()
	select {
	case <-started:
		// 期望：立即返回。
	case <-time.After(2 * time.Second):
		t.Fatal("脏库 Start 未 fail-closed（仍在扫描循环）")
	}
}

func TestVerifySelfCheck_StartBlocksUntilCancel(t *testing.T) {
	f := newVF(t)
	a, b := f.addDevice("verify-a"), f.addDevice("verify-b")
	_ = f.createPB(a, b)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.gsvc.Start(ctx)
	}()
	// 干净库应进入扫描循环（不立即返回）。
	select {
	case <-done:
		t.Fatal("干净库 Start 不应立即返回")
	case <-time.After(150 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("ctx 取消后 Start 未退出")
	}
}
