package datasource

// 组级健康扫描 + 自愈（方案 §5.4 / N-21）。与 datasource.Service.ScanStale 并列为
// 同一 60s 扫描节奏（GroupService.Start 用同一 Options.ScanInterval），但**扫描面不同**：
//   - 老 ScanStale 只扫 status=active 的 data_sources；
//   - 本扫描必须扫 **primary_backup 组，且包含当前无 active 的组** —— R4 降级后若只扫
//     active，组会永久停在"无权威"（方案 N-21 明确点名的失效方向）。
//
// composite 组不参与扫描（I5：无 active/无 failover）。

import (
	"errors"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// ScanGroups 执行一轮组健康扫描（停滞熔断）与自愈（无 active 组补选）。返回切换次数。
func (g *GroupService) ScanGroups(at time.Time) (int, error) {
	var groups []models.SourceGroup
	if err := g.db.Where("enabled = ? AND kind = ?", true, models.SourceGroupKindPrimaryBackup).
		Find(&groups).Error; err != nil {
		return 0, err
	}
	switched := 0
	for i := range groups {
		n, err := g.scanGroup(groups[i], at)
		if err != nil {
			return switched, err
		}
		switched += n
	}
	return switched, nil
}

// scanGroup 单组扫描：有 active ⇒ 停滞判定；无 active ⇒ 自愈补选。
func (g *GroupService) scanGroup(group models.SourceGroup, at time.Time) (int, error) {
	members, err := g.ListMembers(group.ID)
	if err != nil {
		return 0, err
	}
	if len(members) == 0 {
		return 0, nil
	}
	intervals, err := g.memberIntervalMillis(group.ID)
	if err != nil {
		return 0, err
	}
	staleness := DeriveStaleness(group.StalenessSec, intervals)

	var active *models.SourceGroupMember
	for i := range members {
		if members[i].Status == models.SourceMemberStatusActive {
			active = &members[i]
			break
		}
	}
	if active == nil {
		return g.selfHealGroup(group, members, at, staleness)
	}
	// R11：刚成为 active 的成员不参与停滞累计。
	if !g.minResidencyElapsed(active.UpdatedAt, at) {
		return 0, nil
	}
	if active.LastSuccess != nil && at.Sub(*active.LastSuccess) <= staleness {
		return 0, nil
	}
	return g.failStaleGroupMember(group, *active, at, staleness)
}

// failStaleGroupMember 对停滞的 active 成员计一次失败（R2/R11）；达阈值后走 R3/R4/R10。
func (g *GroupService) failStaleGroupMember(group models.SourceGroup, member models.SourceGroupMember, at time.Time, staleness time.Duration) (int, error) {
	var notes []models.Notification
	var event *ActiveChange
	switched := 0
	err := g.db.Transaction(func(tx *gorm.DB) error {
		var cur models.SourceGroupMember
		if err := tx.First(&cur, member.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			return err
		}
		if cur.Status != models.SourceMemberStatusActive {
			return nil
		}
		if cur.LastSuccess != nil && at.Sub(*cur.LastSuccess) <= staleness {
			return nil
		}
		// 归属组可能在扫描期间被修改（删除/禁用）——重新确认。
		var fresh models.SourceGroup
		if err := tx.First(&fresh, group.ID).Error; err != nil {
			return nil
		}
		if !fresh.Enabled || fresh.Kind != models.SourceGroupKindPrimaryBackup {
			return nil
		}
		maxFail := cur.MaxFailCount
		if maxFail == 0 {
			maxFail = defaultMaxFailCount
		}
		nextCount, nextStatus, reached := countFailure(cur.FailCount, maxFail, at)
		updates := map[string]interface{}{"fail_count": nextCount, "last_failure": at}
		if reached {
			updates["status"] = nextStatus
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", cur.ID).Updates(updates).Error; err != nil {
			return err
		}
		respTime := 0
		if cur.LastSuccess != nil {
			respTime = int(at.Sub(*cur.LastSuccess).Milliseconds())
		}
		if err := g.writeGroupHealth(tx, group.ID, cur.ID, "failure",
			fmt.Sprintf("成员数据停滞 %dms (触发: %s)", respTime, TriggerStaleData)); err != nil {
			return err
		}
		if !reached {
			return nil
		}
		ev, didSwitch, err := g.maybeGroupFailover(tx, group.ID, cur.ID, TriggerStaleData, &notes)
		if err != nil {
			return err
		}
		event = ev
		if didSwitch {
			switched = 1
		}
		return nil
	})
	if err != nil {
		return switched, err
	}
	g.emitAll(notes)
	// 注意：event != nil 时也必须 publish（maybeGroupFailover 已提交新的 active/无 active）。
	g.commitGroupChange(group.ID, event)
	return switched, nil
}

// selfHealGroup 是 N-21 的自愈：组无 active 时，从"近期成功过"的 standby 成员中
// 按 priority DESC, id ASC 补选权威；没有健康候选则保持降级（不选陈旧成员）。
//
// 为什么要看 last_success：无 active 往往由 R4 降级产生，此时若直接把一个从未成功/
// 早已过期的 standby 提为 active，规则会拿到陈旧值（fail-open 方向）。
func (g *GroupService) selfHealGroup(group models.SourceGroup, members []models.SourceGroupMember, at time.Time, staleness time.Duration) (int, error) {
	// R10：自愈也是权威迁移，冷却窗内不连切（防抖动）。
	suppressed, _, err := g.cooldownActive(g.db, group.ID)
	if err != nil {
		return 0, err
	}
	if suppressed {
		return 0, nil
	}
	var candidate *models.SourceGroupMember
	for i := range members {
		m := &members[i]
		if m.Status != models.SourceMemberStatusStandby {
			continue
		}
		if m.LastSuccess == nil || at.Sub(*m.LastSuccess) > staleness {
			continue
		}
		if candidate == nil || m.Priority > candidate.Priority ||
			(m.Priority == candidate.Priority && m.ID < candidate.ID) {
			candidate = m
		}
	}
	if candidate == nil {
		return 0, nil
	}
	var notes []models.Notification
	var event *ActiveChange
	err = g.db.Transaction(func(tx *gorm.DB) error {
		var fresh models.SourceGroup
		if err := tx.First(&fresh, group.ID).Error; err != nil {
			return nil
		}
		var activeCount int64
		if err := tx.Model(&models.SourceGroupMember{}).
			Where("group_id = ? AND status = ?", group.ID, models.SourceMemberStatusActive).
			Count(&activeCount).Error; err != nil {
			return err
		}
		if activeCount > 0 {
			return nil // 扫描期间已被别处恢复
		}
		rev, err := g.bumpGroupRevision(tx, group.ID)
		if err != nil {
			return err
		}
		if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", candidate.ID).
			Update("status", models.SourceMemberStatusActive).Error; err != nil {
			return err
		}
		if err := g.writeGroupFailoverLog(tx, group.ID, 0, candidate.ID, ReasonGroupSelfHeal, "selfheal"); err != nil {
			return err
		}
		msg := fmt.Sprintf("来源组 %d 自愈：无权威成员，按健康度补选成员 %d 为 active", group.ID, candidate.ID)
		if err := g.writeGroupHealth(tx, group.ID, candidate.ID, "selfheal", msg); err != nil {
			return err
		}
		notes = append(notes, g.newGroupNotification(notifyTypeInfo, "来源组自愈", msg, group.ID))
		event = &ActiveChange{
			PrevMemberID: 0, NextMemberID: candidate.ID, Reason: ReasonGroupSelfHeal, Trigger: "selfheal",
			Revision: rev, TransitionSeq: g.nextTransitionSeq(group.ID), Category: group.Category,
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	g.emitAll(notes)
	g.commitGroupChange(group.ID, event)
	if event != nil {
		return 1, nil
	}
	return 0, nil
}

var _ = logger.Warn
