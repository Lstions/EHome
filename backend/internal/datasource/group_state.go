package datasource

// 组级 R1–R11 适配（方案 §5.4 / §12.9.1）。本文件只做"取数 + 调纯函数 + 写审计"，
// 决策逻辑全部在 group_service.go 的 applySuccess/countFailure/pickCandidate/cooldownActive。

import (
	"errors"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// 组级切换原因（写入 source_group_failover_logs.Reason）。
const (
	ReasonGroupAuto             = "auto"
	ReasonGroupManual           = "manual"
	ReasonGroupManualDeactivate = "manual_deactivate"
	ReasonGroupReset            = "reset"
	ReasonGroupDelete           = "delete"
	ReasonGroupSelfHeal         = "selfheal"
	// ReasonGroupCooldownSuppressed：R10 抑制切换但组已失去权威（active 已置 error 且无新 active）。
	// 抑制的是"切换动作"，不是"失去权威"这个事实；消费方必须收到 Next=0。
	ReasonGroupCooldownSuppressed = "cooldown_suppressed"
)

// MarkSuccess 是 R1 的组级适配：按 edge_device_id 定位所有组的成员，仅 category 命中时
// 写 last_success/fail_count=0，error → standby；**不抢占 active、不 bump revision**
// （F-1 白名单：运行态写不得改变窗口 identity）。
func (g *GroupService) MarkSuccess(edgeDeviceID uint, categories []string, at time.Time) {
	// 0 组快速路径（B-3 第 1 步）：生产 data_sources=0 / 组=0 时，每帧都白跑一次
	// JOIN 查询。Registry.Len() 是 atomic load + len，O(1)；0→N 由 PublishAll/Publish
	// 自动翻转，**不需要任何失效钩子**（impl-reviewer 已核）。
	if !g.groupsLive() {
		return
	}
	if edgeDeviceID == 0 || len(categories) == 0 {
		return
	}
	type memberRow struct {
		MemberID uint
		GroupID  uint
		Category string
	}
	var rows []memberRow
	err := g.db.Table("source_group_members AS sgm").
		Select("sgm.id AS member_id, sgm.group_id AS group_id, sg.category AS category").
		Joins("JOIN source_groups sg ON sg.id = sgm.group_id").
		Where("sgm.edge_device_id = ? AND sgm.status <> ? AND sg.category IN ?",
			edgeDeviceID, models.SourceMemberStatusDisabled, categories).
		Scan(&rows).Error
	if err != nil {
		logger.Warn("datasource: group MarkSuccess query failed", "edge_device_id", edgeDeviceID, "error", err)
		return
	}
	if len(rows) == 0 {
		return
	}
	affected := make(map[uint]struct{}, len(rows))
	if err := g.db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			var cur models.SourceGroupMember
			if err := tx.First(&cur, row.MemberID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					continue
				}
				return err
			}
			if cur.Status == models.SourceMemberStatusDisabled {
				continue
			}
			// 门禁 TestGormWritePathShapes 只接受 map 变量/字面量形态（不接受函数调用），
			// 因此先落局部变量；语义与直接调用完全一致（applySuccess 返回 map）。
			updates := applySuccess(cur.Status, at)
			if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", cur.ID).
				Updates(updates).Error; err != nil {
				return err
			}
			affected[row.GroupID] = struct{}{}
		}
		return nil
	}); err != nil {
		logger.Warn("datasource: group MarkSuccess update failed", "edge_device_id", edgeDeviceID, "error", err)
		return
	}
	// 快照含成员运行态（composite 用 last_success 判陈旧），必须刷新；无 active 变化不发射事件。
	for groupID := range affected {
		g.publishGroup(groupID)
	}
}

// MarkFailure 是 R2 的组级适配：active|standby 成员计次 → 达阈值 error → R3/R4/R10。
//
// composite 组成员不参与 R2（I5：复合组无 active/failover）；仅记录 health failure 供观测，
// 不改 status（否则部分成员会被主备状态机接管）。
func (g *GroupService) MarkFailure(edgeDeviceID uint, trigger string) {
	// 同 MarkSuccess 的 0 组快速路径（offline hook 低频，但保持一致性：
	// 否则「无组时 MarkSuccess 短路、MarkFailure 仍查库」会形成两套行为）。
	if !g.groupsLive() {
		return
	}
	if edgeDeviceID == 0 {
		return
	}
	type memberRow struct {
		MemberID   uint
		GroupID    uint
		Kind       string
		Status     string
		MaxFail    int
		FailCount  int
	}
	var rows []memberRow
	err := g.db.Table("source_group_members AS sgm").
		Select("sgm.id AS member_id, sgm.group_id AS group_id, sg.kind AS kind, sgm.status AS status, sgm.max_fail_count AS max_fail, sgm.fail_count AS fail_count").
		Joins("JOIN source_groups sg ON sg.id = sgm.group_id").
		Where("sgm.edge_device_id = ?", edgeDeviceID).
		Scan(&rows).Error
	if err != nil {
		logger.Warn("datasource: group MarkFailure query failed", "edge_device_id", edgeDeviceID, "error", err)
		return
	}
	now := g.now()
	for _, row := range rows {
		if row.Kind == models.SourceGroupKindComposite {
			// I5：composite 成员只记观测健康，不进入 R1–R11 状态机。
			_ = g.db.Create(&models.SourceGroupHealth{
				GroupID: row.GroupID, MemberID: row.MemberID, Event: "failure",
				Message: fmt.Sprintf("composite 成员失败（不参与主备切换，触发: %s）", trigger), CreatedAt: now,
			}).Error
			continue
		}
		if row.Status != models.SourceMemberStatusActive && row.Status != models.SourceMemberStatusStandby {
			continue
		}
		var notes []models.Notification
		var event *ActiveChange
		err := g.db.Transaction(func(tx *gorm.DB) error {
			var cur models.SourceGroupMember
			if err := tx.First(&cur, row.MemberID).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return nil
				}
				return err
			}
			if cur.Status != models.SourceMemberStatusActive && cur.Status != models.SourceMemberStatusStandby {
				return nil
			}
			maxFail := cur.MaxFailCount
			if maxFail == 0 {
				maxFail = defaultMaxFailCount
			}
			nextCount, nextStatus, reached := countFailure(cur.FailCount, maxFail, now)
			updates := map[string]interface{}{"fail_count": nextCount, "last_failure": now}
			if reached {
				updates["status"] = nextStatus
			}
			if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
				return err
			}
			if err := g.writeGroupHealth(tx, cur.GroupID, cur.ID, "failure",
				fmt.Sprintf("成员连续失败计数 %d/%d (触发: %s)", nextCount, maxFail, trigger)); err != nil {
				return err
			}
			if !reached {
				return nil
			}
			ev, _, err := g.maybeGroupFailover(tx, cur.GroupID, cur.ID, trigger, &notes)
			if err != nil {
				return err
			}
			// 注意：R4 降级（无候选）也返回非 nil 事件但 switched=false——必须上报，
			// 否则消费方不会清窗（本方案 §5.4 点名的危险点）。
			if ev != nil {
				event = ev
			}
			return nil
		})
		if err != nil {
			logger.Warn("datasource: group MarkFailure apply failed", "member_id", row.MemberID, "error", err)
			continue
		}
		g.emitAll(notes)
		if event != nil {
			g.commitGroupChange(row.GroupID, event)
			// 组失去权威（含 R10 抑制路径）⇒ 补一条组健康事件（1min 节流）。
			g.afterAuthorityLost(row.GroupID, event)
		} else {
			g.publishGroup(row.GroupID)
		}
	}
}

// maybeGroupFailover 是 R3/R4/R10 的组级实现，必须在调用方事务内执行。
//
// 返回：active 变化事件（无变化为 nil）、是否真的切换、待发射通知。
// 注意 R4（无候选）也返回事件（Next=0）—— 旧 data_sources 版只发通知，这是方案 §5.4
// 点名的危险点：组失去权威必须让消费方清窗，否则 evaluator 会继续用旧 active 过滤。
func (g *GroupService) maybeGroupFailover(tx *gorm.DB, groupID, failedMemberID uint, trigger string, notes *[]models.Notification) (*ActiveChange, bool, error) {
	var group models.SourceGroup
	if err := tx.First(&group, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, nil
		}
		return nil, false, err
	}
	if group.Kind != models.SourceGroupKindPrimaryBackup {
		return nil, false, nil
	}
	if !group.Enabled {
		return nil, false, nil
	}

	// 组内仍有 active（standby 熔断场景）⇒ 无需切换。
	var activeCount int64
	if err := tx.Model(&models.SourceGroupMember{}).
		Where("group_id = ? AND status = ?", groupID, models.SourceMemberStatusActive).
		Count(&activeCount).Error; err != nil {
		return nil, false, err
	}
	if activeCount > 0 {
		return nil, false, nil
	}

	// R10 冷却：距该组最近一次切换 < Cooldown → 只记健康事件与通知，不切换。
	suppressed, _, err := g.cooldownActive(tx, groupID)
	if err != nil {
		return nil, false, err
	}
	if suppressed {
		// B-2/架构终审：R10 抑制的是"切换动作"，不是"组失去权威"这个事实。
		// active 成员已被调用方置 error ⇒ 组内已无 active ⇒ 必须：
		//   ① bump revision（身份变了，窗口 identity 必须换）；
		//   ② 写 degraded 健康事件（运维看板可见"当前无权威来源"）；
		//   ③ 发 ActiveChange{Next:0}（消费方清窗、停止按旧 active 过滤）——Next=0 不是切换，
		//      不违反"冷却窗内不再切换"的语义。
		rev, err := g.bumpGroupRevision(tx, groupID)
		if err != nil {
			return nil, false, err
		}
		seq := g.nextTransitionSeq(groupID)
		msg := fmt.Sprintf("来源组 %d 冷却抑制且已无权威成员 (no_active_after_cooldown)", groupID)
		// 健康行不在此处写：统一由调用方提交后的 afterAuthorityLost → RecordDegraded
		// 写一条（1min 节流）。否则同一事实会落两行 degraded（事务内 1 行 + 节流行 1 行），
		// 按行计数的看板重复计数（impl-core 观测点 2）。
		*notes = append(*notes, g.newGroupNotification(notifyTypeWarning, "来源组切换被抑制（组已无权威）", msg, groupID))
		return &ActiveChange{
			PrevMemberID: failedMemberID, NextMemberID: 0,
			Reason: ReasonGroupCooldownSuppressed, Trigger: trigger,
			Revision: rev, TransitionSeq: seq, Category: group.Category,
		}, false, nil
	}

	// R3 候选：priority DESC, id ASC 的第一条 standby。
	var members []models.SourceGroupMember
	if err := tx.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return nil, false, err
	}
	candidate, ok := pickCandidate(members)
	if !ok {
		// R4 降级：无候选，组失去权威 ⇒ 必须发 ActiveChange{Next:0}。
		rev, err := g.bumpGroupRevision(tx, groupID)
		if err != nil {
			return nil, false, err
		}
		seq := g.nextTransitionSeq(groupID)
		msg := fmt.Sprintf("来源组 %d 无可用备用成员 (degraded)", groupID)
		if err := g.writeGroupHealth(tx, groupID, failedMemberID, "degraded", msg); err != nil {
			return nil, false, err
		}
		*notes = append(*notes, g.newGroupNotification(notifyTypeWarning, "来源组降级", msg, groupID))
		return &ActiveChange{
			PrevMemberID: failedMemberID, NextMemberID: 0,
			Reason: ReasonGroupAuto, Trigger: trigger,
			Revision: rev, TransitionSeq: seq, Category: group.Category,
		}, false, nil
	}

	rev, err := g.bumpGroupRevision(tx, groupID)
	if err != nil {
		return nil, false, err
	}
	candidate.Status = models.SourceMemberStatusActive
	if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", candidate.ID).
		Update("status", models.SourceMemberStatusActive).Error; err != nil {
		return nil, false, err
	}
	if err := g.writeGroupFailoverLog(tx, groupID, failedMemberID, candidate.ID, ReasonGroupAuto, trigger); err != nil {
		return nil, false, err
	}
	msg := fmt.Sprintf("来源组 %d 成员 %d 故障，自动切换至成员 %d (触发: %s)", groupID, failedMemberID, candidate.ID, trigger)
	if err := g.writeGroupHealth(tx, groupID, candidate.ID, "transition", msg); err != nil {
		return nil, false, err
	}
	*notes = append(*notes, g.newGroupNotification(notifyTypeWarning, "来源组切换", msg, groupID))
	seq := g.nextTransitionSeq(groupID)
	return &ActiveChange{
		PrevMemberID: failedMemberID, NextMemberID: candidate.ID,
		Reason: ReasonGroupAuto, Trigger: trigger,
		Revision: rev, TransitionSeq: seq, Category: group.Category,
	}, true, nil
}

// afterAuthorityLost 是"组从有 active 变无 active"的统一收尾（在 commitGroupChange 之后调用）：
// R10 冷却抑制路径额外写一条 RecordDegraded（R4 降级已在事务内写过 degraded 健康行，不重复）。
func (g *GroupService) afterAuthorityLost(groupID uint, ev *ActiveChange) {
	if ev == nil || ev.NextMemberID != 0 {
		return
	}
	if ev.Reason == ReasonGroupCooldownSuppressed {
		g.RecordDegraded(groupID, "no_active_after_cooldown")
	}
}

// ActivateGroupMember 是 R7（手动切换/重新启用）：目标→active，原 active→standby。
// composite 组禁止（I5/V-6：无 active 概念，返回 ErrInvalidRequest）。
func (g *GroupService) ActivateGroupMember(groupID, memberID uint) (*models.SourceGroup, error) {
	var notes []models.Notification
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		group, target, prevID, err := g.prepareManualActivate(tx, groupID, memberID)
		if err != nil {
			return err
		}
		if prevID == target.ID {
			// 幂等：已是 active ⇒ 不 bump revision（F-1：no-op 不得改变窗口 identity）。
			event = nil
			return nil
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("group_id = ? AND status = ?", groupID, models.SourceMemberStatusActive).
			Update("status", models.SourceMemberStatusStandby).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", target.ID).
			Update("status", models.SourceMemberStatusActive).Error; err != nil {
			return err
		}
		rev, err := g.bumpGroupRevision(tx, groupID)
		if err != nil {
			return err
		}
		seq := g.nextTransitionSeq(groupID)
		if err := g.writeGroupFailoverLog(tx, groupID, prevID, target.ID, ReasonGroupManual, ""); err != nil {
			return err
		}
		msg := fmt.Sprintf("来源组 %d 手动切换至成员 %d", groupID, target.ID)
		if err := g.writeGroupHealth(tx, groupID, target.ID, "transition", msg); err != nil {
			return err
		}
		notes = append(notes, g.newGroupNotification(notifyTypeInfo, "来源组切换", msg, groupID))
		event = &ActiveChange{
			PrevMemberID: prevID, NextMemberID: target.ID,
			Reason: ReasonGroupManual, Revision: rev, TransitionSeq: seq, Category: group.Category,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	g.emitAll(notes)
	out, err := g.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	g.commitGroupChange(groupID, event)
	return out, nil
}

// DeactivateGroupMember 是 R5/R6/R8：非 active → disabled；active 有候选 → 候选接替；
// 无候选 → ErrConflict（R6，要求先指定接替者）。
func (g *GroupService) DeactivateGroupMember(groupID, memberID uint) (*models.SourceGroup, error) {
	var notes []models.Notification
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		var group models.SourceGroup
		if err := tx.First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
			}
			return err
		}
		var target models.SourceGroupMember
		if err := tx.Where("group_id = ? AND id = ?", groupID, memberID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: member %d", ErrNotFound, memberID)
			}
			return err
		}
		if target.Status != models.SourceMemberStatusActive {
			// R8：直接 disabled（可经 Activate 重新启用）。
			// 不 bump revision：status/fail_count 属**运行态**，不进 MembersHash、也不影响
			// 窗口 identity（F-1 白名单；impl-core 验证报告 F-5）。
			if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", target.ID).
				Update("status", models.SourceMemberStatusDisabled).Error; err != nil {
				return err
			}
			return nil
		}
		if group.Kind != models.SourceGroupKindPrimaryBackup {
			return fmt.Errorf("%w: composite 组没有 active 概念，禁止 deactivate 成员", ErrInvalidRequest)
		}
		var members []models.SourceGroupMember
		if err := tx.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
			return err
		}
		candidate, ok := pickCandidate(members)
		if !ok {
			return fmt.Errorf("%w: no standby candidate for source group %d; specify a successor first", ErrConflict, groupID)
		}
		// 顺序铁律（与 legacy service.go::Deactivate 的 D-3 修复同款）：
		// 必须先降旧、再升新 —— 反序会在同一事务内制造"双 active 窗口"，
		// 语句级唯一性检查（PG/SQLite 的部分唯一索引 ux_sgm_one_active）立即报错，
		// 整个 deactivate 事务回滚。
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", target.ID).
			Update("status", models.SourceMemberStatusDisabled).Error; err != nil {
			return err
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", candidate.ID).
			Update("status", models.SourceMemberStatusActive).Error; err != nil {
			return err
		}
		rev, err := g.bumpGroupRevision(tx, groupID)
		if err != nil {
			return err
		}
		if err := g.writeGroupFailoverLog(tx, groupID, target.ID, candidate.ID, ReasonGroupManualDeactivate, ""); err != nil {
			return err
		}
		msg := fmt.Sprintf("来源组 %d 成员 %d 停用，成员 %d 接替", groupID, target.ID, candidate.ID)
		if err := g.writeGroupHealth(tx, groupID, candidate.ID, "transition", msg); err != nil {
			return err
		}
		notes = append(notes, g.newGroupNotification(notifyTypeInfo, "来源组切换", msg, groupID))
		event = &ActiveChange{
			PrevMemberID: target.ID, NextMemberID: candidate.ID,
			Reason: ReasonGroupManualDeactivate, Revision: rev,
			TransitionSeq: g.nextTransitionSeq(groupID), Category: group.Category,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	g.emitAll(notes)
	out, err := g.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	g.commitGroupChange(groupID, event)
	return out, nil
}

// ResetGroupMember 是 R9：error → standby、fail_count=0（保留 last_failure）；组内无 active 时提升。
func (g *GroupService) ResetGroupMember(groupID, memberID uint) (*models.SourceGroup, error) {
	var notes []models.Notification
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		var group models.SourceGroup
		if err := tx.First(&group, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
			}
			return err
		}
		var target models.SourceGroupMember
		if err := tx.Where("group_id = ? AND id = ?", groupID, memberID).First(&target).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: member %d", ErrNotFound, memberID)
			}
			return err
		}
		if target.Status != models.SourceMemberStatusError {
			return fmt.Errorf("%w: member %d is not in error state", ErrInvalidRequest, memberID)
		}
		status := models.SourceMemberStatusStandby
		var activeCount int64
		if err := tx.Model(&models.SourceGroupMember{}).
			Where("group_id = ? AND status = ?", groupID, models.SourceMemberStatusActive).
			Count(&activeCount).Error; err != nil {
			return err
		}
		promoted := false
		if activeCount == 0 && group.Kind == models.SourceGroupKindPrimaryBackup {
			status = models.SourceMemberStatusActive
			promoted = true
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", target.ID).
			Updates(map[string]interface{}{"status": status, "fail_count": 0}).Error; err != nil {
			return err
		}
		notes = append(notes, g.newGroupNotification(notifyTypeInfo, "来源组恢复",
			fmt.Sprintf("来源组 %d 成员 %d 重置为 %s", groupID, target.ID, status), groupID))
		if promoted {
			rev, err := g.bumpGroupRevision(tx, groupID)
			if err != nil {
				return err
			}
			event = &ActiveChange{
				PrevMemberID: 0, NextMemberID: target.ID, Reason: ReasonGroupReset,
				Revision: rev, TransitionSeq: g.nextTransitionSeq(groupID), Category: group.Category,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	g.emitAll(notes)
	out, err := g.GetGroup(groupID)
	if err != nil {
		return nil, err
	}
	g.commitGroupChange(groupID, event)
	return out, nil
}
