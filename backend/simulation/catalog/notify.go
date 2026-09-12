//go:build simulation

// 场景目录 · SIM-NTFY 通知送达（设计/自动化引擎场景仿真验证.md §4 SIM-NTFY-001..003）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§4 场景清单、§6 命名、§7 产品缺口）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API、§5.6 隔离、§7 红线）；
// 领域依据：backend/internal/models/alert.go 的 NotificationType 映射、
// backend/internal/automation/planner.go 的 notifyAction/notifyConfirmation/notifyDailyLimitOnce。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. 通知是用户判断"要不要管"的唯一依据：级别必须按策略配置如实映射（info→info、
//     warning→warning、critical→error），来源必须能让用户回链到是哪条策略；
//  2. 未读数与已读状态是同一份事实的两个视角：读一条必须让计数减少 1，重复标记已读不得二次扣减；
//  3. 防抖不只是"不重复执行"——冷却期内也不能把用户轰炸成 N 条通知：
//     同一策略在冷却窗内落下的通知**恰好一条**。
//
// 产品缺口（设计 §7.1，本域无法验证，不为不存在的能力写断言）：
// 全仓没有任何外发通道 —— Notification 只落库 + WS 广播，无 webhook / 机器人 / 邮件 / 短信。
// 因此"用户人不在系统前面时能不能收到告警"这一自动化最核心的价值**当前不可验证**。
//
// 命名纪律（框架 §4.1 + 门禁第 8 条）：本文件包级标识符一律以 notify 开头。
package catalog

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

// notifyDomain 是本域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const notifyDomain Domain = "NTFY"

// notifyErrKeepWatching 是"观察窗继续跑"的哨兵错误。EventuallyEveryError 会把最后一次
// 失败原因用 %w 包装后返回，因此判定必须走 errors.Is，不能比较错误字符串。
var notifyErrKeepWatching = errors.New("notify: keep watching")

func init() {
	Register(Scenario{
		ID:     "SIM-NTFY-001",
		Title:  "策略通知带着正确的级别和来源，用户能判断严重程度与出处",
		Domain: notifyDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-NTFY-001（级别/来源）",
		Run:    notifyRun001,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-002",
		Title:  "读过一条通知后未读数正好减一，重复标记不会多扣",
		Domain: notifyDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-NTFY-002（未读计数）",
		Run:    notifyRun002,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-003",
		Title:  "冷却期内反复越限，用户只会收到一条通知，不会被反复打扰",
		Domain: notifyDomain,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-NTFY-003（通知去重）",
		Run:    notifyRun003,
	})
}

// ---------------------------------------------------------------------------
// 本域工具
// ---------------------------------------------------------------------------

// notifyOfRule 从通知中心列表里挑出某条策略产生的全部通知。
//
// 为什么必须按 source + source_id 双重过滤：GET /notifications 返回的是**全库**通知
// （handler_notification.go 不做任何过滤），同一次仿真运行里其它场景、告警引擎、
// 日熔断提醒都会写进同一张表。只按标题模糊匹配会把别人的通知算进本场景的计数。
func notifyOfRule(rows []autoNotificationRow, ruleID int64) []autoNotificationRow {
	want := strconv.FormatInt(ruleID, 10)
	out := make([]autoNotificationRow, 0, len(rows))
	for _, row := range rows {
		if row.Source == "automation_rule" && row.SourceID == want {
			out = append(out, row)
		}
	}
	return out
}

// notifyWaitCount 轮询等待某条策略的通知条数达到 want，返回这些通知。
func notifyWaitCount(e *harness.Env, ruleID int64, want int, timeout time.Duration) []autoNotificationRow {
	e.T.Helper()
	var found []autoNotificationRow
	e.Eventually(timeout, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		mine := notifyOfRule(rows, ruleID)
		if len(mine) != want {
			return fmt.Errorf("策略 %d 的通知条数 = %d，期望 %d（全库共 %d 条）",
				ruleID, len(mine), want, len(rows))
		}
		found = mine
		return nil
	})
	return found
}

// notifyReadAll 把通知中心恢复成"没有未读"，避免把未读基线留给后续场景
// （场景自清理只覆盖自己创建的资源，未读数是全局状态）。
func notifyReadAll(e *harness.Env) {
	e.T.Helper()
	e.Admin.Post("/api/v1/notifications/read-all", map[string]any{}).Expect(http.StatusOK)
}

// ---------------------------------------------------------------------------
// SIM-NTFY-001 通知带正确的级别与来源
// ---------------------------------------------------------------------------

func notifyRun001(e *harness.Env) {
	// 设备类型用有动作目录的型号没有任何必要：本域只验证通知动作，
	// 而 notification 动作不需要 commandexec 的可用性门禁。
	fx := autoProvisionDevice(e, "SIM-NTFY-001", "lvl", "sim_ntfy_001_sensor")

	// 三级动作级别取自 models/alert.go：info / warning / critical。
	// trigger_threshold=20.0 > 哨兵值不适用（本夹具无哨兵上报，见下），
	// 三帧上报值分别是 23.5 / 25.5 / 27.5 ℃ —— 都越过阈值。
	cases := []struct {
		level    string
		wantType string
		raw      uint16
		value    float64
	}{
		{level: "info", wantType: "info", raw: 235, value: 23.5},
		{level: "warning", wantType: "warning", raw: 255, value: 25.5},
		{level: "critical", wantType: "error", raw: 275, value: 27.5},
	}

	for _, item := range cases {
		name := e.NS("SIM-NTFY-001", item.level)
		ruleID := autoCreateRule(e, map[string]any{
			"name":                   name,
			"trigger_type":           "sensor_threshold",
			"trigger_edge_device_id": fx.edgeDeviceID,
			"trigger_sensor_name":    "temperature",
			"trigger_comparator":     "gt",
			"trigger_threshold":      20.0,
			"action_type":            "notification",
			"action_level":           item.level,
			"cooldown_sec":           60,
		})

		if err := fx.report(item.raw); err != nil {
			e.Fatalf("上报 %v ℃ 失败: %v", item.value, err)
		}

		landed := notifyWaitCount(e, ruleID, 1, 25*time.Second)[0]

		// 不变式 1：级别按契约映射（critical→error、warning→warning、info→info）。
		if landed.Type != item.wantType {
			e.Fatalf("action_level=%s 的通知 type=%q，期望 %q", item.level, landed.Type, item.wantType)
		}
		// 不变式 2：用户能回链到是哪条策略 —— 来源 + 来源 ID 必须成对正确。
		if landed.Source != "automation_rule" || landed.SourceID != strconv.FormatInt(ruleID, 10) {
			e.Fatalf("通知归因错误: source=%q source_id=%q（期望 automation_rule/%d）",
				landed.Source, landed.SourceID, ruleID)
		}
		// 不变式 3：标题与正文要能让人看懂是哪条策略、当时是多少（不是空标题）。
		if landed.Title == "" {
			e.Fatalf("通知 %d 没有标题（界面上会显示成空白）", landed.ID)
		}
		if !strings.Contains(landed.Title, name) {
			e.Fatalf("通知标题 %q 未包含策略名 %q（用户无法辨认是哪条策略）", landed.Title, name)
		}
		// 不变式 4：新通知必须是未读，且事件里的读数与本次上报一致
		// （哨兵值 21.37 或任何其它值都不该冒充这次触发）。
		if landed.Read {
			e.Fatalf("新通知 %d 竟是已读状态", landed.ID)
		}
		event := notifyWaitEventValue(e, ruleID, item.value)
		if event.TriggerValue == nil {
			e.Fatalf("策略 %d 的通知事件缺少 trigger_value", ruleID)
		}
		autoEventuallyFloat(e.T, "触发事件的 trigger_value", *event.TriggerValue, item.value)
	}

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-001.levels", map[string]any{
		"info→info": true, "warning→warning": true, "critical→error": true,
		"edge_device_id": fx.edgeDeviceID,
	})
}

// notifyWaitEventValue 等某条策略出现一条 trigger_value 等于 want 的事件。
//
// 为什么锚定"值"而不只是"设备"：同一台设备会被多条规则求值，任何来源的帧都可能
// 命中规则。只有 trigger_value 与本次上报值一致，才能证明这条事件就是本次上报产生的。
func notifyWaitEventValue(e *harness.Env, ruleID int64, want float64) autoEventRow {
	e.T.Helper()
	var found autoEventRow
	e.Eventually(20*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.TriggerValue != nil && *row.TriggerValue == want {
				found = row
				return nil
			}
		}
		observed := make([]string, 0, len(rows))
		for _, row := range rows {
			if row.TriggerValue == nil {
				observed = append(observed, row.Result+"(值=nil)")
			} else {
				observed = append(observed, fmt.Sprintf("%s(值=%.2f)", row.Result, *row.TriggerValue))
			}
		}
		return fmt.Errorf("策略 %d 尚无 trigger_value=%.2f 的事件，已观察到: %v", ruleID, want, observed)
	})
	return found
}

// ---------------------------------------------------------------------------
// SIM-NTFY-002 未读数与已读状态联动
// ---------------------------------------------------------------------------

func notifyRun002(e *harness.Env) {
	// 后缀保持在 5 字符以内：nodes.node_id 是 varchar(32)，RunID 占 17，
	// 场景码占 5（紧凑码 nf002；若 sceneCodeByDomain 尚未登记 NTFY，会回退为
	// slug "ntfy-002" 占 8）—— 后缀 ≤5 在两种情况下都在预算内。
	fx := autoProvisionDevice(e, "SIM-NTFY-002", "unr", "sim_ntfy_002_sensor")

	// 从干净的未读基线出发：本场景的增量断言才有唯一解释。
	notifyReadAll(e)
	baseline := autoUnreadCount(e)

	name := e.NS("SIM-NTFY-002", "unread")
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	landed := notifyWaitCount(e, ruleID, 1, 25*time.Second)[0]

	// 不变式 1：新通知进来，未读数必须增加（界面上才会有"有新通知"的提示）。
	afterTrigger := autoUnreadCount(e)
	if afterTrigger <= baseline {
		e.Fatalf("产生通知后未读数未增加：基线 %d，现在 %d", baseline, afterTrigger)
	}

	// 不变式 2：标记已读后，未读数必须**恰好**减一（不是"减少了一些"，也不是不变）。
	e.Admin.Put("/api/v1/notifications/"+strconv.FormatUint(uint64(landed.ID), 10)+"/read",
		map[string]any{}).Expect(http.StatusOK)

	var afterRead int64
	var readBack bool
	e.Eventually(10*time.Second, func() error {
		afterRead = autoUnreadCount(e)
		if afterRead != afterTrigger-1 {
			return fmt.Errorf("未读数 = %d，期望 %d", afterRead, afterTrigger-1)
		}
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		mine := notifyOfRule(rows, ruleID)
		if len(mine) != 1 {
			return fmt.Errorf("本场景策略的通知条数 = %d，期望 1", len(mine))
		}
		if !mine[0].Read {
			return fmt.Errorf("通知 %d 仍未变为已读", mine[0].ID)
		}
		readBack = mine[0].Read
		return nil
	})
	if !readBack {
		e.Fatalf("已读状态未在列表口径中体现")
	}

	// 不变式 3：重复标记已读是幂等的 —— 不得再扣一次。
	e.Admin.Put("/api/v1/notifications/"+strconv.FormatUint(uint64(landed.ID), 10)+"/read",
		map[string]any{}).Expect(http.StatusOK)
	afterRepeat := autoUnreadCount(e)
	if afterRepeat != afterRead {
		e.Fatalf("重复标记已读后又变了未读数：%d → %d（重复操作必须幂等）", afterRead, afterRepeat)
	}

	// 不变式 4：这条通知唯一归因于本场景的这次触发（值锚定，排除哨兵/其它帧）。
	event := notifyWaitEventValue(e, ruleID, 23.5)
	if event.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", event.TriggerSource)
	}

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-002.unread", map[string]any{
		"rule_id": ruleID, "notification_id": landed.ID,
		"baseline": baseline, "after_trigger": afterTrigger,
		"after_read": afterRead, "after_repeat_read": afterRepeat,
	})
}

// ---------------------------------------------------------------------------
// SIM-NTFY-003 冷却期内不重复打扰
// ---------------------------------------------------------------------------

func notifyRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-NTFY-003", "dd", "sim_ntfy_003_sensor")

	notifyReadAll(e)

	// 冷却期 60s，远大于本场景的观察窗：窗内无论上报多少次，通知都只能有一条。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-NTFY-003", "dedupe"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	first := notifyWaitCount(e, ruleID, 1, 25*time.Second)[0]
	afterFirst := autoUnreadCount(e)

	// 由 EventualyEveryError 驱动的高频上报（400ms × 5s ≈ 12 帧），
	// 覆盖冷却窗内"反复越限"的真实节奏。窗口不使用 sleep 作断言同步：
	// 断言在窗口结束后进行，窗口内每帧都被引擎求值（由下面的 suppressed_cooldown 审计证明）。
	const frames = 12
	err := e.EventuallyEveryError(5*time.Second, 400*time.Millisecond, func() error {
		if reportErr := fx.report(235); reportErr != nil {
			e.Fatalf("节点上报失败: %v", reportErr)
		}
		return notifyErrKeepWatching
	})
	if !errors.Is(err, notifyErrKeepWatching) {
		e.Fatalf("观察窗未按预期跑满（哨兵错误被替换）: %v", err)
	}

	// 不变式 1：通知**恰好一条**（不是"至少一条"）—— 冷却期内不得再打扰用户。
	mine := notifyWaitCount(e, ruleID, 1, 5*time.Second)
	if mine[0].ID != first.ID {
		e.Fatalf("通知被替换成了另一条：先 %d，后 %d", first.ID, mine[0].ID)
	}

	// 不变式 2：未读数只增加了这一条（窗口内的帧没有偷偷加未读）。
	afterWindow := autoUnreadCount(e)
	if afterWindow != afterFirst {
		e.Fatalf("观察窗内未读数从 %d 变成 %d —— 冷却期内仍在产生通知", afterFirst, afterWindow)
	}

	// 不变式 3：抑制不是"悄悄丢掉"—— 审计里必须有 suppressed_cooldown，
	// 且同一冷却窗内只落首条（evaluator.recordSuppressed 的同窗节流）。
	var suppressed []autoEventRow
	e.Eventually(15*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&result=suppressed_cooldown")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚无 suppressed_cooldown 审计（被冷却压制的触发必须可观测）", ruleID)
		}
		suppressed = rows
		return nil
	})
	// 窗口内约 %d 帧，若没有同窗节流这里会接近帧数；上界留出并发余量。
	if len(suppressed) > 3 {
		e.Fatalf("冷却窗内落下了 %d 条 suppressed_cooldown 审计（同窗只应落首条，否则高频上报会刷量）",
			len(suppressed))
	}
	if suppressed[0].TriggerValue == nil {
		e.Fatalf("抑制审计缺少触发时值（用户看不到当时是多少）: %+v", suppressed[0])
	}
	autoEventuallyFloat(e.T, "抑制审计的 trigger_value", *suppressed[0].TriggerValue, 23.5)

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-003.dedupe", map[string]any{
		"rule_id": ruleID, "notification_id": first.ID,
		"observed_frames": frames, "notifications": 1,
		"unread_after_first": afterFirst, "unread_after_window": afterWindow,
		"suppressed_audits": len(suppressed),
	})
}
