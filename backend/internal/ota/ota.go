package ota

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/audit"
	"ehome/backend/internal/downlink"
	"ehome/backend/internal/events"
	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/firmwareticket"
	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// OTA 状态机字面量 (与 docs/v2.0/requirements.md §6.1 一致)
const (
	StatusPending     = "pending"
	StatusDownloading = "downloading"
	StatusVerifying   = "verifying"
	StatusInstalling  = "installing"
	StatusSuccess     = "success"
	StatusFailed      = "failed"
	StatusTimeout     = "timeout"
	StatusNeedsRetry  = "needs_retry"
)

// OtaProgress 状态码 (ESP→SVR, type=0x0B) — 与 docs §6.3 / protocol-spec.md 一致
//
// 当前 ESP32 固件实际只发 0/1/2/3 四个值, verifying 与 installing 阶段共用
// status=1。依据是固件源码本身（不依赖任何 OTA_STAGE_*/OTA_STATUS_* 宏 ——
// 那组符号在 esp32-collector 中已不存在, 复核命令:
//
//	grep -rn 'OTA_STAGE_\|OTA_STATUS_' esp32-collector   # 0 命中）:
//	- 消息表: frame_codec.h:49  #define MSG_OTA_PROG 0x0B
//	- 发送端: handler_data.c:276-289  msg_handler_send_ota_prog() 把 status 原样
//	  编进 field 2（无任何枚举翻译层）
//	- 唯一的调用注入点: main.c:310  ota_set_progress_callback(msg_handler_send_ota_prog)
//	- 全部 status 取值: ota.c 中 ota_report_progress() 的 8 处调用只出现 0/1/3
//	  （行 517、609、713 传 0=downloading; 829 传 1=installing/完成;
//	   797、809、820 传 3=failed）—— **从不出现 2**, 也从不出现 4
//	- ota.c:828-829 在 ota_nvs_set_state(OTA_STATE_VERIFYING) 之后立刻
//	  ota_report_progress(..., 1, 100, NULL), 即固件内部确有"校验/写入"阶段,
//	  但 wire 上仍报 1
//
// 注意 ota.h:71 的注释写作 "1=completed, 2=verifying"，与 ota.c 的实际实参
// 不一致, 属于**文档债**（本次不改固件, 仅记录）。
// WireVerifying=4 是 server 内部扩展, 允许未来 ESP32 升级时显式区分
// verifying 和 installing 阶段（当前固件永不发送该值）。
const (
	WireDownloading = 0
	WireInstalling  = 1
	WireSuccess     = 2
	WireFailed      = 3
	WireVerifying   = 4 // server-side extension (future ESP32 升级使用)
)

// 终态集合
var terminalStates = map[string]bool{
	StatusSuccess: true,
	StatusFailed:  true,
	StatusTimeout: true,
}

// cancellableStates 用户可主动取消的阶段: 仅下载阶段 (pending/downloading)。
//
// 一旦进入 verifying/installing, 设备已在写/准备写引导分区; 此时服务端单方面把任务
// 置为终态只会造成 "账上说已取消、设备照样刷入新固件" 的账实不符 —— 故服务端拒绝取消
// (API 层返回 409)。下载阶段取消不触碰引导分区, 因此允许。
var cancellableStates = []string{StatusPending, StatusDownloading}

// ErrTaskNotCancellable 任务已过可取消窗口 (verifying/installing) 或状态在
// 读取后被并发改写。API 层用 errors.Is 判定并映射为 HTTP 409。
var ErrTaskNotCancellable = errors.New("task is no longer cancellable")

// ErrForceReasonRequired：force 必须给理由（REQ-8 / AC-8）。
// API 层用 errors.Is 判定并映射为 HTTP 400 —— force 本身合法，缺的是理由。
var ErrForceReasonRequired = errors.New("force requires a non-empty force_reason")

// DeliveryError 是「任务已落库、但下发失败」的 typed error（D2）。
//
// 语义（HTTP 层可照此映射 503 + data.task_id）：
//   - 任务行与 force 审计**保留**：它们是这次尝试的证据，不因下发失败回滚/删除；
//   - 任务被有条件推进到 failed（MarkDeliveryFailed），客户端能按 task_id 定位；
//   - 重试是一次全新的任务：旧任务已是终态，不参与抢占，不会被新任务覆盖。
type DeliveryError struct {
	TaskID uint
	OtaID  string
	// Status 是任务**实际**持久化状态（通常 failed；并发改写时按回读值如实上报）。
	Status string
	Err    error
}

func (e *DeliveryError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("ota task %d delivery failed: %v", e.TaskID, e.Err)
}

// Unwrap 让调用方仍能 errors.Is/As 到原始下发错误。
func (e *DeliveryError) Unwrap() error { return e.Err }

// MarkDeliveryFailed 把「下发失败」的任务有条件推进到 failed（D2），返回实际持久化状态。
//
// 为什么由 Manager 做而不是 handler 直接 db.Save：
//   - AutoRollback 也走「建任务 + 下发」，埋在 handler 会让回滚留下幽灵 pending；
//   - Save 是整行覆盖且错误被忽略；这里用 WHERE status=pending 的条件更新，
//     RowsAffected!=1 时回读真实状态，绝不覆盖并发推进，也不谎报状态。
func (m *Manager) MarkDeliveryFailed(task *models.OTATask, sendErr error) string {
	if task == nil {
		return StatusPending
	}
	msg := fmt.Sprintf("send failed: %v", sendErr)
	now := time.Now()
	result := m.db.Model(&models.OTATask{}).
		Where("id = ? AND status = ?", task.ID, StatusPending).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    msg,
			"completed_at": &now,
		})
	if result.Error == nil && result.RowsAffected == 1 {
		task.Status = StatusFailed
		task.ErrorMsg = msg
		task.CompletedAt = &now
		return StatusFailed
	}
	logger.Errorf("[OTA] delivery failed but task %d could not be marked failed: err=%v rows=%d",
		task.ID, result.Error, result.RowsAffected)
	var persisted models.OTATask
	if err := m.db.First(&persisted, task.ID).Error; err == nil && persisted.Status != "" {
		return persisted.Status
	}
	return StatusPending
}

// cancelErrorMsg 用户取消的 error_msg。取消不引入 cancelled 终态, 复用 failed +
// 本常量区分 (与设备侧上报的失败 reason 天然可区分)。
const cancelErrorMsg = "cancelled by user (during download)"

// 抢占 (supersede) 旧 OTA 记录: 同 node 下所有非终态记录标记为 failed
// 实现 docs §6.4.1
var activeStates = []string{
	StatusPending,
	StatusDownloading,
	StatusVerifying,
	StatusInstalling,
}

// 超时扫描用状态集合
var timeoutProneStates = []string{
	StatusPending,
	StatusDownloading,
	StatusInstalling,
}

// auto-rollback 阈值
const (
	rollbackWindow   = 24 * time.Hour
	rollbackFailMax  = 3
	timeoutThreshold = 30 * time.Minute
	timeoutScanTick  = 60 * time.Second
)

// ack retry parameters
const (
	ackTimeout    = 30 * time.Second // wait for device to acknowledge OtaCmd
	ackMaxRetries = 3
)

// retry backoff intervals
var ackRetryBackoff = [ackMaxRetries]time.Duration{
	5 * time.Second,
	10 * time.Second,
	15 * time.Second,
}

// Manager handles OTA operations
// bridge holds shared mutable state; sync.Once singleton across all Manager instances.
type bridge struct {
	pendingCmds map[string]chan struct{}
	pendingMu   sync.Mutex
	seqCounter  uint32
	stopCh      chan struct{}
	wg          sync.WaitGroup
}

var (
	_bridgeOnce sync.Once
	_bridge     *bridge
)

func getBridge() *bridge {
	_bridgeOnce.Do(func() {
		_bridge = &bridge{
			pendingCmds: make(map[string]chan struct{}),
			stopCh:      make(chan struct{}),
		}
	})
	return _bridge
}

// FirmwareURLIssuer 在**发送时刻**签一张新的固件下载票据（方案 T-1.2）。
//
// 为什么是接口而不是直接 import 签名实现：签名密钥与外部主机名都在 api 层
// （jwtSecret / EHOME_EXTERNAL_HOST），而 api 已经 import ota，本包不能反向依赖它。
type FirmwareURLIssuer interface {
	// IssueDownloadURL 返回 filename 的新签名下载地址；无法签发时返回 ""。
	IssueDownloadURL(filename string) string
}

// Option 是可选的 Manager 依赖（保持既有三参构造调用点不变）。
type Option func(*Manager)

// WithFirmwareURLIssuer 注入发送时签票的签发器（cmd/server/main.go 注入）。
func WithFirmwareURLIssuer(issuer FirmwareURLIssuer) Option {
	return func(m *Manager) {
		m.firmwareURLIssuer = issuer
	}
}

type Manager struct {
	db       *gorm.DB
	downlink downlink.Publisher
	wsHub    *websocket.Hub
	// firmwareURLIssuer 为 nil 时退回 firmware.URL（单测/场景仿真）；
	// 生产由 main.go 注入，保证 OtaCmd 里的票从发送时刻起算 30 分钟。
	firmwareURLIssuer FirmwareURLIssuer
	wg                sync.WaitGroup // for timeoutScanner graceful shutdown
	started           sync.Once      // ensures Start() is re-entrant safe
	closeOnce         sync.Once      // ensures Close() does not double-close stopCh
}

// NewManager creates a new OTA manager
func NewManager(db *gorm.DB, downlinkPublisher downlink.Publisher, wsHub *websocket.Hub, opts ...Option) *Manager {
	getBridge() // ensure bridge singleton is initialized (idempotent)
	mgr := &Manager{
		db:       db,
		downlink: downlinkPublisher,
		wsHub:    wsHub,
	}
	for _, opt := range opts {
		opt(mgr)
	}
	return mgr
}

// Close stops background goroutines (timeout scanner + ack waiters).
func (m *Manager) Close() {
	m.closeOnce.Do(func() {
		close(_bridge.stopCh)
	})
	_bridge.wg.Wait()
	m.wg.Wait()
}

// Start launches background goroutines (timeout scanner must run for the manager lifetime).
func (m *Manager) Start() {
	m.started.Do(func() {
		m.wg.Add(1)
		go m.timeoutScanner()
	})
}

// timeoutScanner runs every 60s and marks stale downloading/installing tasks as timeout.
func (m *Manager) timeoutScanner() {
	defer m.wg.Done()
	ticker := time.NewTicker(timeoutScanTick)
	defer ticker.Stop()
	for {
		select {
		case <-_bridge.stopCh:
			return
		case <-ticker.C:
			m.scanTimeouts()
		}
	}
}

// scanTimeouts marks tasks in downloading/installing with no progress update for >30min as timeout.
func (m *Manager) scanTimeouts() {
	now := time.Now()
	threshold := now.Add(-timeoutThreshold)

	var tasks []models.OTATask
	if err := m.db.Where("status IN ? AND updated_at < ?", timeoutProneStates, threshold).Find(&tasks).Error; err != nil {
		logger.Errorf("[OTA] timeout scan DB error: %v", err)
		return
	}

	for i := range tasks {
		t := tasks[i]
		errorMsg := fmt.Sprintf("Timed out: no progress for >%v", timeoutThreshold)
		result := m.db.Model(&models.OTATask{}).
			Where("id = ? AND status = ? AND updated_at = ?", t.ID, t.Status, t.UpdatedAt).
			Updates(map[string]interface{}{"status": StatusTimeout, "error_msg": errorMsg, "completed_at": &now})
		if result.Error != nil || result.RowsAffected != 1 {
			logger.Warnf("[OTA] stale timeout transition rejected task=%s err=%v rows=%d", t.OtaID, result.Error, result.RowsAffected)
			continue
		}
		t.Status = StatusTimeout
		t.ErrorMsg = errorMsg
		t.CompletedAt = &now

		// WebSocket push
		if m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":   t.OtaID,
				"status":   t.Status,
				"progress": t.Progress,
				"reason":   "timeout",
			})
		}
		logger.Infof("[OTA] task %s timed out (no progress since %s)", t.OtaID, t.UpdatedAt.Format(time.RFC3339))

		// Check auto-rollback after timeout
		m.maybeAutoRollback(t.NodeID)
	}
}

// errorMsgDeviceRolledBack 是设备自动回滚的记账标记（方案 §6.6）。
//
// 它必须同时出现在两处：
//   - HandleHelloOTACompletion 置 failed 时写它（可诊断）；
//   - countRecentFailures 把它排除出失败计数，否则「回滚的回执」会凑满 3 次，
//     再触发一次 AutoRollback（回滚的回滚，RK-12）。
const errorMsgDeviceRolledBack = "device rolled back"

// maybeAutoRollback checks if the same node has had >=3 OTA failures in 24h,
// and if so, automatically rolls back to the last known stable firmware.
func (m *Manager) maybeAutoRollback(nodeID string) {
	failCount, err := m.countRecentFailures(nodeID)
	if err != nil {
		logger.Errorf("[OTA] count recent failures for node %s: %v", nodeID, err)
		return
	}

	if failCount < int64(rollbackFailMax) {
		return
	}

	// Trigger auto-rollback
	logger.Infof("[OTA] node %s has %d failures in 24h, triggering auto-rollback", nodeID, failCount)
	if err := m.AutoRollback(nodeID); err != nil {
		logger.Errorf("[OTA] auto-rollback failed for node %s: %v", nodeID, err)
	}
}

// countRecentFailures 统计 AutoRollback 窗口内的**真失败**（方案 §6.6 / RK-12）。
//
// 「device rolled back」是设备自动回滚的回执，不是升级失败：把它算进失败计数，
// 下一次 maybeAutoRollback 就会「为回滚再回滚」一次，形成循环。
func (m *Manager) countRecentFailures(nodeID string) (int64, error) {
	since := time.Now().Add(-rollbackWindow)
	var failCount int64
	err := m.db.Model(&models.OTATask{}).
		Where("node_id = ? AND status IN ? AND completed_at >= ?",
			nodeID,
			[]string{StatusFailed, StatusTimeout, StatusNeedsRetry},
			since,
		).
		Where("NOT (status = ? AND error_msg = ?)", StatusFailed, errorMsgDeviceRolledBack).
		Count(&failCount).Error
	return failCount, err
}

// AutoRollback finds the last known stable firmware for a node and creates a rollback OTA task.
// Same node with 3+ OTA failures in 24h triggers this automatically.
func (m *Manager) AutoRollback(nodeID string) error {
	// Find the node's current firmware version
	var node models.Node
	if err := m.db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return fmt.Errorf("node not found: %w", err)
	}

	// Find the last successful OTA for this node — that tells us the previous stable version
	var lastSuccess models.OTATask
	err := m.db.Where("node_id = ? AND status = ?", nodeID, StatusSuccess).
		Order("completed_at DESC").
		First(&lastSuccess).Error
	if err != nil {
		return fmt.Errorf("no successful OTA found for node %s, cannot determine stable version: %w", nodeID, err)
	}

	// Find the firmware record for that version
	var stableFW models.Firmware
	if err := m.db.First(&stableFW, lastSuccess.FirmwareID).Error; err != nil {
		return fmt.Errorf("stable firmware record not found (id=%d): %w", lastSuccess.FirmwareID, err)
	}

	// If the current version already matches the stable version, skip
	if node.FirmwareVersion == stableFW.Version {
		return fmt.Errorf("node %s already on stable version %s, no rollback needed", nodeID, stableFW.Version)
	}

	// Create rollback task.
	// source=system：回滚目标是更旧版本，必须跳过闸门第 9/10 条版本方向判定，
	// 否则 AutoRollback 每天都会被 OTA_GATE_NOT_AN_UPGRADE 拒掉（AC-9）。
	// 硬件类判定照常生效（方案 §6.3「系统回滚只校硬件、不校方向」）。
	task, err := m.CreateTaskWithOptions(CreateTaskOptions{
		NodeID:     nodeID,
		FirmwareID: stableFW.ID,
		Source:     firmwareid.SourceSystem,
	})
	if err != nil {
		return fmt.Errorf("failed to create rollback task: %w", err)
	}

	// Send the OTA command. 下发失败同样不能留幽灵 pending（D2）：任务置 failed，
	// 返回带 task_id 的 typed error（HTTP 层统一映射 503 OTA_DELIVERY_FAILED）。
	if err := m.SendOtaCommand(task); err != nil {
		return &DeliveryError{
			TaskID: task.ID,
			OtaID:  task.OtaID,
			Status: m.MarkDeliveryFailed(task, err),
			Err:    fmt.Errorf("send failed: %w", err),
		}
	}

	logger.Infof("[OTA] auto-rollback: node %s → firmware %s (task %s)", nodeID, stableFW.Version, task.OtaID)
	return nil
}

// GetNodeOTAStatus returns the OTA status for a node.
func (m *Manager) GetNodeOTAStatus(nodeID string) (map[string]interface{}, error) {
	var node models.Node
	if err := m.db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		return nil, fmt.Errorf("node not found: %w", err)
	}

	// Last successful OTA
	var lastSuccess models.OTATask
	lastUpgradeTime := (*time.Time)(nil)
	err := m.db.Where("node_id = ? AND status = ?", nodeID, StatusSuccess).
		Order("completed_at DESC").
		First(&lastSuccess).Error
	if err == nil {
		lastUpgradeTime = lastSuccess.CompletedAt
	}

	// Count failures in 24h
	since := time.Now().Add(-rollbackWindow)
	var failCount int64
	m.db.Model(&models.OTATask{}).
		Where("node_id = ? AND status IN ? AND completed_at >= ?",
			nodeID,
			[]string{StatusFailed, StatusTimeout, StatusNeedsRetry},
			since,
		).
		Count(&failCount)

	result := map[string]interface{}{
		"node_id":           nodeID,
		"current_version":   node.FirmwareVersion,
		"last_upgrade_time": lastUpgradeTime,
		"fail_count_24h":    failCount,
	}

	return result, nil
}

// AuditActor 是 force 落审计的主体（方案评审 P0-6 的 opts{Force,Reason,Actor,Source}）。
//
// HTTP 层从登录态填（subject_id → UserID）；为空时按 "user" + 空快照记录，
// 绝不因为「拿不到 actor」就跳过审计（REQ-8：危险操作必须留痕）。
type AuditActor struct {
	Type      string // "user" / "admin" / "system"
	UserID    *uint
	Snapshot  string
	RequestID string
	SourceIP  string
}

// CreateTaskOptions 是建任务参数（方案 §5.4 / §6.3）。
//
// 为什么把 NodeID/FirmwareID 也放进 opts：闸门需要 Force/ForceReason/Source/Actor
// 四个额外维度；继续用位置参数会变成 CreateTask(id, fw, false, "", "user", actor)，
// 调用点读不出语义，也最容易把 force 与 source 传反。
type CreateTaskOptions struct {
	NodeID      string
	FirmwareID  uint
	Force       bool
	ForceReason string
	Source      string
	Actor       AuditActor
}

// CreateTaskWithOptions 建 OTA 任务 —— 闸门的唯一入口（方案 §6.3 / AC-8）。
//
// 顺序：
//  1. 读固件与节点硬件档案（节点不存在 = 硬件档案未知，交给闸门第 3 条，不报 500）；
//  2. firmwareid.CheckCompat 十分支判定；不兼容**先返回 typed GateError，不写库**；
//  3. force 只对版本方向两条（9/10）生效；放行时必须带理由并落审计；
//  4. 抢占旧任务（沿用既有「失败只告警」语义）→ 落库 +（强制时）审计同一事务。
func (m *Manager) CreateTaskWithOptions(opts CreateTaskOptions) (*models.OTATask, error) {
	if strings.TrimSpace(opts.NodeID) == "" {
		return nil, fmt.Errorf("create task: node_id is required")
	}
	firmware, err := m.loadFirmware(opts.FirmwareID)
	if err != nil {
		return nil, err
	}
	node, err := m.loadNodeForGate(opts.NodeID)
	if err != nil {
		return nil, fmt.Errorf("create task: load node %s: %w", opts.NodeID, err)
	}

	compatReq := NewCompatRequest(&node, firmware, opts.Source)
	// 节点行不存在时 loadNodeForGate 返回零值：NodeID 仍要填，第 3 条的
	// data.fix 命令才拼得出来；fix 是运维唯一出口。
	compatReq.Node.NodeID = opts.NodeID
	gate := firmwareid.CheckCompat(compatReq)

	var gateErr *firmwareid.GateError
	forced := false
	if !gate.Compatible {
		gateErr = gate.GateError()
		if !opts.Force || !gate.Forceable {
			// 硬件类不匹配一律拒绝（方案 §2.4 R5）：force 只买「版本方向」这一种例外。
			//
			// 指标埋在这里而不是 HTTP 层：AutoRollback/手动回滚也走本函数，
			// 埋在 handler 会漏掉系统回滚通道的拒绝（§9.5 按 reason 观测闸门）。
			metrics.OTAGateRejected.WithLabelValues(gateErr.Code).Inc()
			return nil, gateErr
		}
		if strings.TrimSpace(opts.ForceReason) == "" {
			return nil, ErrForceReasonRequired
		}
		forced = true
	}
	return m.createTaskRecord(opts, firmware, forced, gateErr)
}

// NewCompatRequest 把库里的节点/固件行映射成闸门输入。
//
// 只做字段搬运：chip 别名与 psram 大小写的规范化、以及 10 条分支判定全在
// firmwareid.CheckCompat 内。因此 ehomectl ota gate-check 的只读预检与
// CreateTaskWithOptions 的判定**同源**（v1.4 删除 gateMode 的前提）。
func NewCompatRequest(node *models.Node, firmware *models.Firmware, source string) firmwareid.CompatRequest {
	req := firmwareid.CompatRequest{
		Firmware: firmwareid.FirmwareFacts{
			ID:             firmware.ID,
			ModelKey:       firmware.ModelKey,
			Chip:           firmware.Chip,
			FlashMB:        firmware.FlashMB,
			Psram:          firmware.Psram,
			Version:        firmware.Version,
			SizeBytes:      firmware.SizeBytes,
			MinFromVersion: firmware.MinFromVersion,
			IdentityState:  firmware.IdentityState,
			Withdrawn:      firmware.WithdrawnAt != nil,
		},
		Source: normalizeSource(source),
	}
	if node != nil {
		req.Node = firmwareid.NodeHardware{
			NodeID:   node.NodeID,
			Platform: node.Platform,
			FlashMB:  node.HwFlashMB,
			Psram:    node.HwPsram,
		}
		req.CurrentVersion = node.FirmwareVersion
	}
	return req
}

// createTaskRecord 只做「抢占旧任务 + 落库 + 强制时审计」，不含闸门判定。
//
// §6.4.1 的抢占语义保持不变：同节点所有非终态记录先标 failed("Superseded by new attempt")。
// 事务只包住「新任务 + force 审计」：这两者必须同生共死；抢占失败按既有语义只告警。
func (m *Manager) createTaskRecord(opts CreateTaskOptions, firmware *models.Firmware, forced bool, gateErr *firmwareid.GateError) (*models.OTATask, error) {
	now := time.Now()
	task := &models.OTATask{
		OtaID:       fmt.Sprintf("ota-%d", now.UnixNano()),
		NodeID:      opts.NodeID,
		FirmwareID:  opts.FirmwareID,
		ToVersion:   firmware.Version,
		Status:      StatusPending,
		Progress:    0,
		Forced:      forced,
		ForceReason: "",
		Source:      normalizeSource(opts.Source),
	}
	if forced {
		task.ForceReason = strings.TrimSpace(opts.ForceReason)
	}

	// §6.4.1 的抢占保持既有语义：失败只告警，不阻断新任务（一次脏锁不该让升级永久不可发）。
	// 因此它刻意留在事务外 —— 放进事务后，PG 里失败的语句会污染整个事务，
	// 「告警后继续」就变成了假语义。
	res := m.db.Model(&models.OTATask{}).
		Where("node_id = ? AND status IN ?", opts.NodeID, activeStates).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    "Superseded by new attempt",
			"completed_at": &now,
		})
	if res.Error != nil {
		logger.Warnf("[OTA] Failed to supersede prior tasks for collector %s: %v", opts.NodeID, res.Error)
	} else if res.RowsAffected > 0 {
		logger.Infof("[OTA] Superseded %d in-flight OTA record(s) for collector %s", res.RowsAffected, opts.NodeID)
	}

	// 任务行与 force 审计同事务：审计是危险操作的凭据，不允许「任务在、审计丢」。
	err := m.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(task).Error; err != nil {
			return fmt.Errorf("failed to create task: %w", err)
		}
		if !forced {
			return nil
		}
		return writeForceAudit(tx, opts, firmware, gateErr)
	})
	if err != nil {
		return nil, err
	}
	return task, nil
}

// forceAuditEventName 是「强制放行建任务」的审计事件名（security_audit_events.event_name）。
const forceAuditEventName = "ota.task.force_created"

// writeForceAudit 落一条 force 审计（与任务行同事务，AC-8 的审计判据）。
//
// metadata 里必须同时有 target（firmware）、reason 与命中的闸门码：事后追责时要能
// 回答「对哪台节点、放行了哪条判定、为什么」。actor 由 Event 的 Actor* 字段承担。
func writeForceAudit(tx *gorm.DB, opts CreateTaskOptions, firmware *models.Firmware, gateErr *firmwareid.GateError) error {
	actorType := strings.TrimSpace(opts.Actor.Type)
	if actorType == "" {
		actorType = "user"
	}
	metadata := map[string]interface{}{
		"node_id":     opts.NodeID,
		"firmware_id": opts.FirmwareID,
		"to_version":  firmware.Version,
		"reason":      strings.TrimSpace(opts.ForceReason),
	}
	if gateErr != nil {
		metadata["gate_code"] = gateErr.Code
		metadata["gate_reasons"] = gateErr.Reasons
	}
	if err := audit.NewWriter(tx).Write(audit.Event{
		ActorType:     actorType,
		ActorUserID:   opts.Actor.UserID,
		ActorSnapshot: opts.Actor.Snapshot,
		EventName:     forceAuditEventName,
		EventVersion:  1,
		Result:        "success",
		RequestID:     opts.Actor.RequestID,
		SourceIP:      opts.Actor.SourceIP,
		TargetType:    "firmware",
		TargetID:      strconv.FormatUint(uint64(opts.FirmwareID), 10),
		Metadata:      metadata,
	}); err != nil {
		return fmt.Errorf("write force audit: %w", err)
	}
	return nil
}

// loadFirmware 读固件行；错误文案保持既有口径（调用方与测试按 firmware not found 识别）。
func (m *Manager) loadFirmware(firmwareID uint) (*models.Firmware, error) {
	var firmware models.Firmware
	if err := m.db.First(&firmware, firmwareID).Error; err != nil {
		return nil, fmt.Errorf("firmware not found: %w", err)
	}
	return &firmware, nil
}

// loadNodeForGate 读节点硬件档案。节点不存在返回零值而不是错误：
// 交给闸门第 3 条（OTA_GATE_HARDWARE_UNKNOWN）拒绝 —— 前端拿到的是可解释的 409
// （含 data.fix），而不是一句 500 record not found。
func (m *Manager) loadNodeForGate(nodeID string) (models.Node, error) {
	var node models.Node
	err := m.db.Where("node_id = ?", nodeID).First(&node).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return models.Node{}, err
	}
	return node, nil
}

// normalizeSource 把来源收敛到 user/system 两个值（方案 §5.4）。
//
// 未知值一律按 user：只有 source=system 能跳过版本方向判定，默认必须落在严格一侧。
func normalizeSource(source string) string {
	if strings.ToLower(strings.TrimSpace(source)) == firmwareid.SourceSystem {
		return firmwareid.SourceSystem
	}
	return firmwareid.SourceUser
}

// InFlightTaskCount 返回引用该固件的在途任务数（pending/downloading/verifying/installing）。
//
// 撤回/删除固件前的保护判据（方案 §4.3 / DEF-10 → 409 FIRMWARE_IN_USE）：
// 固件被在途任务引用时撤回或删除，设备会拿到一份「已不可选/已删」的下载地址，
// 造成账实不符。判据与抢占/AutoRollback 共用 activeStates，不另写一份状态表。
func InFlightTaskCount(db *gorm.DB, firmwareID uint) (int64, error) {
	var count int64
	err := db.Model(&models.OTATask{}).
		Where("firmware_id = ? AND status IN ?", firmwareID, activeStates).
		Count(&count).Error
	return count, err
}

// SendOtaCommand sends OtaCommand to a device and waits for acknowledgement.
// It registers a pending channel, then spawns a goroutine that:
//  1. Waits up to 30s for the device to reply with OtaProgress(status=0)
//  2. If no ack within 30s, resends the command (up to 3 retries with backoff)
//  3. After 3 retries with no ack, marks the task as failed
func (m *Manager) SendOtaCommand(task *models.OTATask) error {
	var firmware models.Firmware
	if err := m.db.First(&firmware, task.FirmwareID).Error; err != nil {
		return err
	}

	// Get device_id
	var nodeRecord models.Node
	if err := m.db.Where("node_id = ?", task.NodeID).First(&nodeRecord).Error; err != nil {
		return err
	}

	// Allocate a sequence number for this OtaCmd
	seq := atomic.AddUint32(&_bridge.seqCounter, 1)

	// Build the frame once; reuse on retries
	payload := m.buildOtaCmdPayload(task, &firmware, seq)

	// Register pending ack channel
	ackCh := make(chan struct{})
	_bridge.pendingMu.Lock()
	_bridge.pendingCmds[task.OtaID] = ackCh
	_bridge.pendingMu.Unlock()

	// Initial publish
	if err := m.downlink.Publish(nodeRecord.NodeID, payload); err != nil {
		_bridge.pendingMu.Lock()
		delete(_bridge.pendingCmds, task.OtaID)
		_bridge.pendingMu.Unlock()
		return fmt.Errorf("publish ota_cmd: %w", err)
	}
	logger.Infof("[OTA] sent OtaCmd ota_id=%s seq=%d to %s", task.OtaID, seq, nodeRecord.NodeID)

	// Spawn ack-wait goroutine
	_bridge.wg.Add(1)
	go func() {
		defer _bridge.wg.Done()
		defer func() {
			// Cleanup: remove from pending map
			_bridge.pendingMu.Lock()
			delete(_bridge.pendingCmds, task.OtaID)
			_bridge.pendingMu.Unlock()
		}()

		for attempt := 0; attempt < ackMaxRetries; attempt++ {
			select {
			case <-ackCh:
				// Device acknowledged — done
				logger.Infof("[OTA] ota_id=%s acknowledged (attempt %d)", task.OtaID, attempt+1)
				return
			case <-time.After(ackTimeout):
				// No ack — retry
				backoff := ackRetryBackoff[attempt]
				logger.Warnf("[OTA] ota_id=%s no ack after %v (attempt %d/%d), retrying in %v",
					task.OtaID, ackTimeout, attempt+1, ackMaxRetries, backoff)

				// Wait backoff before resending
				select {
				case <-ackCh:
					logger.Infof("[OTA] ota_id=%s acknowledged during backoff (attempt %d)", task.OtaID, attempt+1)
					return
				case <-time.After(backoff):
				}

				// Resend
				if err := m.downlink.Publish(nodeRecord.NodeID, payload); err != nil {
					logger.Errorf("[OTA] ota_id=%s retry publish failed: %v", task.OtaID, err)
				} else {
					logger.Infof("[OTA] ota_id=%s resent (attempt %d/%d)", task.OtaID, attempt+1, ackMaxRetries)
				}
				if attempt == ackMaxRetries-1 {
					select {
					case <-ackCh:
						logger.Infof("[OTA] ota_id=%s acknowledged after final resend", task.OtaID)
						return
					case <-time.After(ackTimeout):
					}
				}
			}
		}

		// All retries exhausted — mark task as failed
		logger.Errorf("[OTA] ota_id=%s failed: no ack after %d attempts", task.OtaID, ackMaxRetries)
		now := time.Now()
		result := m.db.Model(&models.OTATask{}).
			Where("ota_id = ? AND status IN ?", task.OtaID, []string{StatusPending, StatusDownloading, StatusVerifying, StatusInstalling}).
			Updates(map[string]interface{}{
				"status": StatusFailed, "error_msg": fmt.Sprintf("No device acknowledgement after %d retries", ackMaxRetries), "completed_at": &now,
			})
		if result.Error != nil || result.RowsAffected != 1 {
			return
		}

		if m.wsHub != nil {
			m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
				"ota_id":   task.OtaID,
				"status":   StatusFailed,
				"progress": 0,
				"reason":   "ack_timeout",
			})
		}

		// Check auto-rollback
		m.maybeAutoRollback(task.NodeID)
	}()

	return nil
}

// buildOtaCmdPayload constructs the binary payload for an OtaCmd frame.
//
// M-1（方案 T-1.2 / DEF-2）：URL **在发送时刻现签**，不复用上传时写进
// firmware.URL 的那张票 —— 那张票的 30 分钟有效期从上传时刻算起，
// 隔天再下发必然 401（AC-11）。重试复用同一个 payload，票在 30 分钟内足够。
func (m *Manager) buildOtaCmdPayload(task *models.OTATask, firmware *models.Firmware, seq uint32) []byte {
	enc := frame.NewEncoder(frame.MsgOtaCmd)
	enc.EncodeString(1, task.OtaID)
	enc.EncodeString(2, m.firmwareDownloadURL(firmware))
	enc.EncodeString(3, firmware.Checksum)
	enc.EncodeVarint(4, firmware.SizeBytes)
	enc.EncodeString(5, firmware.Version)
	enc.EncodeVarint(frame.OtaCmdFieldSequence, uint64(seq))
	return enc.Bytes()
}

// firmwareDownloadURL 现签下载地址；签发器缺失（单测/仿真）时退回库里的 URL。
//
// filename 取落盘名（StoragePath 的 basename）：上传会按 elf_sha256 重命名，
// URL 必须指向磁盘上真实存在的那个文件。
func (m *Manager) firmwareDownloadURL(firmware *models.Firmware) string {
	if m.firmwareURLIssuer == nil {
		return firmware.URL
	}
	filename := firmwareticket.SanitizeName(firmware.StoragePath)
	if filename == "" {
		filename = firmwareticket.SanitizeName(firmware.Filename)
	}
	if filename == "" {
		return firmware.URL
	}
	fresh := m.firmwareURLIssuer.IssueDownloadURL(filename)
	if fresh == "" {
		logger.Warnf("[OTA] firmware URL issuer returned nothing for %q; falling back to stored URL", filename)
		return firmware.URL
	}
	return fresh
}

// HandleOtaProgress processes OtaProgress messages from devices per docs §6.4.2:
//   - Map wire status code to state machine literal (0/1/2/3 → downloading/installing/success/failed)
//   - When transitioning out of pending, set started_at
//   - When reaching a terminal state (success or failed), set completed_at
//   - Push ota_progress over WebSocket for live UI updates
func (m *Manager) HandleOtaProgress(deviceID string, payload []byte) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return
	}

	var taskID string
	var status, progressPct uint64
	var errorMsg string
	seen := map[uint8]bool{}

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil || field.FieldNum < 1 || field.FieldNum > 4 || seen[field.FieldNum] {
			return
		}
		seen[field.FieldNum] = true
		expected := uint8(frame.WireVarint)
		if field.FieldNum == 1 || field.FieldNum == 4 {
			expected = frame.WireLengthDelimited
		}
		if field.WireType != expected {
			return
		}
		switch field.FieldNum {
		case 1:
			taskID = frame.GetString(field)
		case 2:
			status = frame.GetUint64(field)
		case 3:
			progressPct = frame.GetUint64(field)
		case 4:
			errorMsg = frame.GetString(field)
		}
	}
	if !seen[1] || !seen[2] || !seen[3] || taskID == "" || status > WireVerifying || progressPct > 100 {
		return
	}
	var task models.OTATask
	if err := m.db.Where("ota_id = ? AND node_id = ?", taskID, deviceID).First(&task).Error; err != nil {
		return
	}
	if terminalStates[task.Status] || task.Status == StatusTimeout {
		logger.Warnf("[%s] OTA progress rejected for inactive task %s state=%s", deviceID, taskID, task.Status)
		return
	}

	// Update task in DB

	// Idempotency: terminal state records are immutable
	if terminalStates[task.Status] {
		_bridge.pendingMu.Lock()
		if ch, ok := _bridge.pendingCmds[taskID]; ok {
			delete(_bridge.pendingCmds, taskID)
			close(ch)
		}
		_bridge.pendingMu.Unlock()
		logger.Infof("[%s] OTA task %s already in terminal state %s, ignoring progress update",
			deviceID, taskID, task.Status)
		return
	}

	now := time.Now()
	task.Progress = uint8(progressPct)

	// Map wire code → state literal
	originalStatus := task.Status
	wasActive := originalStatus == StatusPending
	switch status {
	case WireDownloading:
		task.Status = StatusDownloading
	case WireVerifying:
		// server-side extension: ESP32 未来可能显式发 verifying 状态
		// (当前 OTA_STAGE_VERIFYING 在 ESP32 端映射为 OTA_STATUS_INSTALLING=1)
		task.Status = StatusVerifying
	case WireInstalling:
		// downloading → installing (could also be after verifying, but that's a server-only state)
		task.Status = StatusInstalling
	case WireSuccess:
		task.Status = StatusSuccess
		task.Progress = 100
		task.CompletedAt = &now
	case WireFailed:
		task.Status = StatusFailed
		if errorMsg != "" {
			task.ErrorMsg = errorMsg
		}
		task.CompletedAt = &now
	default:
		// Unknown wire code: log warning, keep current state (fail-open)
		logger.Warnf("[%s] OtaProgress unknown wire code: %d (ota_id=%s)",
			task.OtaID, status, taskID)
	}

	if wasActive && task.Status == StatusDownloading {
		task.StartedAt = &now
	}

	updates := map[string]interface{}{"status": task.Status, "progress": task.Progress, "started_at": task.StartedAt, "completed_at": task.CompletedAt, "error_msg": task.ErrorMsg}
	result := m.db.Model(&models.OTATask{}).
		Where("id = ? AND status = ?", task.ID, originalStatus).
		Updates(updates)
	if result.Error != nil || result.RowsAffected != 1 {
		logger.Errorf("[%s] Failed conditional save OTA task %s: err=%v rows=%d", deviceID, taskID, result.Error, result.RowsAffected)
		return
	}
	_bridge.pendingMu.Lock()
	if ch, ok := _bridge.pendingCmds[taskID]; ok {
		delete(_bridge.pendingCmds, taskID)
		close(ch)
	}
	_bridge.pendingMu.Unlock()

	// WebSocket push
	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":    taskID,
			"status":    task.Status,
			"progress":  task.Progress,
			"device_id": deviceID,
		})
	}

	logger.Infof("[%s] OTA progress: %s -> %s (%d%%)", deviceID, taskID, task.Status, task.Progress)

	// Check auto-rollback on failure
	if task.Status == StatusFailed {
		m.maybeAutoRollback(task.NodeID)
	}
}

// helloCompletionErrorMsg 是「Hello 补完成」写入的 error_msg（方案 §6.2 的字面值）。
const helloCompletionErrorMsg = "auto-completed via Hello"

// rollbackDetectableStates 是允许判定「设备已回滚」的任务状态（方案 §6.6）。
//
// 为什么不判全部 active 状态：设备在 downloading 阶段也会周期性发同步 Hello
// （sync_manager 周期 600s），那时它当然还跑着旧版本。若对 pending/downloading
// 也按「当前版本 == 上一个成功版本」判回滚，每次正常升级都会被自己的周期 Hello
// 误杀。只有镜像已经进入写入阶段（verifying/installing）后 Hello 仍报旧版本，
// 才是「写完、重启、退回旧分区」的证据；检测不到的回滚仍由 30 分钟超时扫描兜底
// （§6.6 自己写明的兜底路径）。
var rollbackDetectableStates = []string{StatusVerifying, StatusInstalling}

// HandleHelloOTACompletion reconciles in-flight OTA tasks when a device Hello reports
// its running firmware version (docs §6.2 / §6.4.3 / §6.6).
//
// 三条出口：
//  1. 版本相等且任务**已离开 pending** → 条件更新为 success（补完成，AC-10）；
//  2. 版本相等但仍是 pending → 只记日志，不判成功（从未开始的任务不能凭版本相等成功）；
//  3. 版本不相等 → 设备回滚识别：已进入写入阶段 + 当前版本 == 上一个成功版本
//     → 条件更新为 failed(device rolled back)，且不触发 AutoRollback（§6.6）。
//
// 用条件更新（WHERE id + node_id + status）而不是整行 Save：读与写之间设备可能
// 又推进了状态，RowsAffected != 1 时绝不覆盖（AC-10 的并发判据）。
func (m *Manager) HandleHelloOTACompletion(collectorID string, deviceID, firmwareVersion string) {
	if collectorID == "" {
		return
	}

	// Find the latest non-terminal OTA record for this node
	var task models.OTATask
	err := m.db.Where("node_id = ? AND status IN ?", collectorID, activeStates).
		Order("id DESC").
		First(&task).Error
	if err != nil {
		return // No in-flight OTA or DB error
	}

	if task.ToVersion == "" || task.ToVersion != firmwareVersion {
		m.handleDeviceRollback(&task, deviceID, firmwareVersion)
		return
	}

	// 从未开始的任务不能凭「版本相等」判成功：pending 说明 OtaCmd 还没被设备确认，
	// 版本相等只说明它本来就在这个版本上（同版本重刷场景）。
	if task.Status == StatusPending {
		logger.Warnf("[%s] OTA %s not completed via Hello: task never left pending (reason=hello_without_progress)",
			deviceID, task.OtaID)
		return
	}

	now := time.Now()
	result := m.db.Model(&models.OTATask{}).
		Where("id = ? AND node_id = ? AND status = ?", task.ID, collectorID, task.Status).
		Updates(map[string]interface{}{
			"status":       StatusSuccess,
			"progress":     100,
			"completed_at": &now,
			"error_msg":    helloCompletionErrorMsg,
		})
	if result.Error != nil {
		logger.Errorf("[%s] OTA %s Hello completion failed: %v", deviceID, task.OtaID, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		logger.Warnf("[%s] OTA %s Hello completion skipped: concurrent change (read_status=%s rows=%d)",
			deviceID, task.OtaID, task.Status, result.RowsAffected)
		return
	}

	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":    task.OtaID,
			"status":    StatusSuccess,
			"progress":  100,
			"device_id": deviceID,
			"reason":    "hello_completion",
		})
	}
	logger.Infof("[%s] OTA %s auto-completed via Hello (firmware_version=%s)",
		deviceID, task.OtaID, firmwareVersion)
}

// handleDeviceRollback 识别「设备自动回滚」（方案 §6.6）。
//
// 判据（两条都必须成立）：
//   - 任务已进入写入阶段（rollbackDetectableStates）—— 下载中的 Hello 不算证据；
//   - 设备当前版本 == 该节点在该任务之前**最后一次成功**的版本 —— 即它退回了旧分区。
//
// 判据不成立时什么都不做：可能只是设备还在下载（Hello 报旧版本是正常的），
// 也可能回滚到了没有成功记录的更老版本 —— 后者仍由 30 分钟超时扫描兜底。
//
// 命中后写 error_msg="device rolled back"，并**刻意不调用 maybeAutoRollback**：
// 回滚不是升级失败，不能再触发一次「回滚的回滚」（§6.6 / RK-12）。
func (m *Manager) handleDeviceRollback(task *models.OTATask, deviceID, firmwareVersion string) {
	if firmwareVersion == "" || task.ToVersion == "" || firmwareVersion == task.ToVersion {
		return
	}
	if !slices.Contains(rollbackDetectableStates, task.Status) {
		return
	}
	previous := m.previousSuccessVersion(task)
	if previous == "" || previous != firmwareVersion {
		return
	}

	now := time.Now()
	result := m.db.Model(&models.OTATask{}).
		Where("id = ? AND node_id = ? AND status = ?", task.ID, task.NodeID, task.Status).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    errorMsgDeviceRolledBack,
			"completed_at": &now,
		})
	if result.Error != nil {
		logger.Errorf("[%s] OTA %s rollback accounting failed: %v", deviceID, task.OtaID, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		logger.Warnf("[%s] OTA %s rollback accounting skipped: concurrent change (read_status=%s rows=%d)",
			deviceID, task.OtaID, task.Status, result.RowsAffected)
		return
	}

	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":    task.OtaID,
			"status":    StatusFailed,
			"progress":  task.Progress,
			"device_id": deviceID,
			"reason":    "device_rolled_back",
		})
	}
	logger.Warnf("[%s] OTA %s failed: device rolled back from %s to %s",
		deviceID, task.OtaID, task.ToVersion, firmwareVersion)
}

// previousSuccessVersion 返回该节点在本任务之前最后一次成功升级到的版本。
//
// 用 id < task.ID 而不是 completed_at 比较：id 单调递增，不受时钟回拨影响；
// completed_at 允许为空（历史行）。
func (m *Manager) previousSuccessVersion(task *models.OTATask) string {
	var previous models.OTATask
	err := m.db.Where("node_id = ? AND status = ? AND id < ?", task.NodeID, StatusSuccess, task.ID).
		Order("id DESC").
		First(&previous).Error
	if err != nil {
		return ""
	}
	if previous.ToVersion == "" || previous.ToVersion == task.ToVersion {
		return ""
	}
	return previous.ToVersion
}

// CancelTask marks an in-flight OTA task as cancelled (failed with reason).
// F6.x: user-initiated cancel from UI.
//
// 只允许在下载阶段取消 (status ∈ cancellableStates: pending/downloading)。
// verifying/installing 阶段返回 ErrTaskNotCancellable (API 层 → HTTP 409), 因为设备
// 已进入写引导分区流程, 服务端单方面置终态会造成 "账上说已取消、设备照样刷入新固件"。
//
// 状态转移使用**条件更新** (WHERE id=? AND status=<读到的状态>)，不是先读后写整行 Save:
// 并发竞态下 RowsAffected != 1 说明状态已被其它路径改写, 此时返回
// ErrTaskNotCancellable 包装错误, 绝不覆盖新状态。
func (m *Manager) CancelTask(taskID uint) error {
	var task models.OTATask
	if err := m.db.First(&task, taskID).Error; err != nil {
		return fmt.Errorf("task not found: %w", err)
	}
	if _, ok := terminalStates[task.Status]; ok {
		return fmt.Errorf("task %s is already in terminal state %s", task.OtaID, task.Status)
	}
	// 仅下载阶段可取消; verifying/installing 已过窗口
	if !slices.Contains(cancellableStates, task.Status) {
		return fmt.Errorf("task %s in state %s: %w", task.OtaID, task.Status, ErrTaskNotCancellable)
	}

	now := time.Now()
	readStatus := task.Status
	result := m.db.Model(&models.OTATask{}).
		Where("id = ? AND status = ?", task.ID, readStatus).
		Updates(map[string]interface{}{
			"status":       StatusFailed,
			"error_msg":    cancelErrorMsg,
			"completed_at": &now,
		})
	if result.Error != nil {
		return fmt.Errorf("cancel task %s: %w", task.OtaID, result.Error)
	}
	if result.RowsAffected != 1 {
		// 竞态: 读到的状态在 UPDATE 前被其它路径改写 (例如设备已推进到 installing)
		return fmt.Errorf("task %s state changed concurrently (read=%s): %w",
			task.OtaID, readStatus, ErrTaskNotCancellable)
	}
	task.Status = StatusFailed
	task.ErrorMsg = cancelErrorMsg
	task.CompletedAt = &now

	// 清理 ack 等待通道: 取消后设备再也不会回 ack, 若不清理则该协程会一直等到
	// ackTimeout×重试 (最长 90s) 才退出 —— 每次取消泄漏一个最长 90s 的等待协程。
	// 所有 close 都在 pendingMu 内先查 ok 再关, delete 后其它路径查不到 → 不会二次 close。
	_bridge.pendingMu.Lock()
	if ch, ok := _bridge.pendingCmds[task.OtaID]; ok {
		delete(_bridge.pendingCmds, task.OtaID)
		close(ch)
	}
	_bridge.pendingMu.Unlock()

	if m.wsHub != nil {
		m.wsHub.BroadcastEvent(events.OTAProgress, map[string]interface{}{
			"ota_id":   task.OtaID,
			"status":   task.Status,
			"progress": task.Progress,
			"reason":   "cancelled",
		})
	}
	logger.Infof("[OTA %s] cancelled by user (was %s)", task.OtaID, readStatus)
	return nil
}
