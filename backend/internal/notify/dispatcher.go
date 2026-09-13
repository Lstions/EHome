package notify

import (
	"context"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// Dispatcher 是外发通知的唯一入口 (设计 §2.1 取向 A):
// 所有通知写入点改为经它落库, 它同时按通道配置投递。
//
// 与 internal/commandexec/dispatcher.go 的关系: 那是"设备指令出站"的 lease/outbox
// 范式 (需要跨实例互斥、崩溃重投, 因为一条指令只能被物理执行一次); 通知投递是
// **幂等且可重复**的旁路动作, 因此这里不需要 fencing/lease —— 但沿用同一个工程
// 纪律: 单一入口、独立可测的 dispatcher、状态与审计同事务落库、失败只记指标与日志。
//
// fail-open (设计 §2.2): 投递失败不回滚业务事务、不阻塞采集热路径、不返回错误码。
// 因此对外方法均无 error 返回, 失败一律落到 NotificationDelivery 审计行 +
// metrics + 日志。

// Outcome 是一次 Deliver 的可观测结果 (供调用方/测试断言, 不参与业务事务)。
type Outcome struct {
	NotificationID uint
	// Channels 是参与判定的启用通道数 (含被 MinLevel 过滤掉的)。
	Channels int
	// Skipped 是被 MinLevel 过滤掉的通道数 (不是失败)。
	Skipped int
	// Delivered / Failed 分别是有通道级终态为 delivered / failed 的通道数。
	Delivered int
	Failed    int
	// Deliveries 是本次产生的全部审计行 (每次尝试一行, 含中间重试)。
	Deliveries []models.NotificationDelivery
	// PersistErr 是"通知本身没写进库"的错误 (投递不可能发生)。
	PersistErr error
}

// Dispatcher 持有 db 与出站客户端; 零值不可用, 必须用 NewDispatcher 构造。
type Dispatcher struct {
	db     *gorm.DB
	client *Client
	now    func() time.Time
	// retrySleep 是重试退避的等待实现; 测试替换它以避免真实等待。
	retrySleep func(ctx context.Context, d time.Duration) error

	mu          sync.Mutex
	lastRefresh time.Time
}

// NewDispatcher 构造投递器。
func NewDispatcher(db *gorm.DB) *Dispatcher {
	return &Dispatcher{
		db:         db,
		client:     NewClient(),
		now:        func() time.Time { return time.Now().UTC() },
		retrySleep: sleepWithContext,
	}
}

// SetClient 替换出站客户端 (测试注入拨号观察点用)。
func (d *Dispatcher) SetClient(client *Client) {
	if client != nil {
		d.client = client
	}
}

// SetRetrySleep 替换退避等待 (仅测试使用; 生产保持 sleepWithContext)。
func (d *Dispatcher) SetRetrySleep(fn func(ctx context.Context, d time.Duration) error) {
	if fn != nil {
		d.retrySleep = fn
	}
}

// Client 暴露出站客户端 (供上层构造测试消息/自检)。
func (d *Dispatcher) Client() *Client { return d.client }

// Create 是通知写入点的统一替代: 先落库 notifications 行 (保持现有语义),
// 再旁路投递。永远不返回错误 —— 通知失败不得影响主流程 (fail-open)。
func (d *Dispatcher) Create(ctx context.Context, n *models.Notification) {
	defer func() {
		if rec := recover(); rec != nil {
			// 通知路径上的 panic 绝不能掀翻调用方 (采集热路径/告警求值) 的事务或协程。
			logger.Errorf("notify: recovered panic while creating notification: %v", rec)
			metrics.DataConsumerDBWriteFailures.WithLabelValues("notify_dispatcher", "notifications").Inc()
		}
	}()
	if n == nil {
		return
	}
	if err := d.db.WithContext(ctx).Create(n).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("notify_dispatcher", "notifications").Inc()
		logger.Warnf("notify: failed to persist notification: %s", RedactText(err.Error()))
		return
	}
	d.Deliver(ctx, *n)
}

// CreateAsync 在独立 goroutine 中完成 Create, 供采集/求值热路径使用。
// 使用 context.WithoutCancel: 调用方请求结束时投递不应被连带取消。
func (d *Dispatcher) CreateAsync(ctx context.Context, n *models.Notification) {
	go d.Create(context.WithoutCancel(ctx), n)
}

// Deliver 把一条已经落库的通知投递到所有符合条件的通道。
// 永不返回错误 (设计 §2.2): 失败只体现在 Outcome 与审计行里。
func (d *Dispatcher) Deliver(ctx context.Context, n models.Notification) Outcome {
	outcome := Outcome{NotificationID: n.ID}
	channels, err := d.enabledChannels(ctx)
	if err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("notify_dispatcher", "notification_channels").Inc()
		logger.Warnf("notify: failed to load channels: %s", RedactText(err.Error()))
		return outcome
	}
	outcome.Channels = len(channels)
	d.refreshEnabledGauge(ctx)

	msg := MessageFromNotification(n)
	for _, channel := range channels {
		// 设计 §5: enabled=false 不投递 (由 SQL 保证), 级别不足同样不投递,
		// 且都**不是失败** —— 不写 delivery 行、不计 failed 指标。
		if !ShouldDeliver(channel, msg) {
			outcome.Skipped++
			continue
		}
		d.deliverToChannel(ctx, &outcome, channel, msg)
	}
	return outcome
}

// DeliverAsync 在独立 goroutine 中投递 (不阻塞调用方)。
func (d *Dispatcher) DeliverAsync(ctx context.Context, n models.Notification) {
	go d.Deliver(context.WithoutCancel(ctx), n)
}

// enabledChannels 读取参与投递的通道 (enabled=true), 按 id 稳定排序。
//
// 注意 where 必须写成 "enabled IS NOT NULL AND enabled = 1": SQLite 里布尔列
// 可能存成 NULL (GORM 把 false 当零值省略, 由 default 决定), 只写 "enabled = ?"
// 在那种行上会静默漏判。Secret 随行读入内存用于出站认证, 但它 json:"-" 且永不
// 进日志/审计。
func (d *Dispatcher) enabledChannels(ctx context.Context) ([]models.NotificationChannel, error) {
	var channels []models.NotificationChannel
	err := d.db.WithContext(ctx).Where("enabled IS NOT NULL AND enabled = ?", true).Order("id").Find(&channels).Error
	return channels, err
}

// deliverToChannel 投递一条消息到单个通道, 并按设计 §5 的状态机重试:
// 每次尝试都先落一行 pending 审计, 得到结论后改为 delivered/failed;
// 失败按 max_retries 重试 (指数退避 1s/2s/4s..., 上限 30s), 最终 failed。
//
// 注意"每次尝试一行"是设计 §3 的 1:N:M 要求: 一条通知 × N 个通道 × M 次尝试,
// 因此重试不覆盖上一行, 而是新开一行 AttemptNo+1。
func (d *Dispatcher) deliverToChannel(ctx context.Context, outcome *Outcome, channel models.NotificationChannel, msg Message) {
	payload, err := BuildPayload(channel, msg)
	if err != nil {
		// 模板错误不是网络失败: 再试一次也不会变好, 直接终态失败 (不重试)。
		row := d.beginAttempt(ctx, outcome.NotificationID, channel, 1)
		d.finishAttempt(ctx, outcome, channel, row, 1, Result{Err: err}, true)
		return
	}
	retries := NormalizeRetries(channel.MaxRetries)
	for attemptNo := uint32(1); ; attemptNo++ {
		row := d.beginAttempt(ctx, outcome.NotificationID, channel, attemptNo)
		result := d.client.Post(ctx, channel, payload)
		if result.OK || !result.Retryable || int(attemptNo) > retries {
			d.finishAttempt(ctx, outcome, channel, row, attemptNo, result, true)
			return
		}
		// 中间失败: 这一行也必须落 failed 终态 (设计 §3 每次尝试一行),
		// 但通道还没定论, 因此不计入 outcome.Failed、计 retrying 指标。
		d.finishAttempt(ctx, outcome, channel, row, attemptNo, result, false)
		if err := d.retrySleep(ctx, BackoffFor(attemptNo)); err != nil {
			// 调用方取消: 不再等待, 通道按已发生的最后一次失败定论 (取消不得变成静默成功)。
			outcome.Failed++
			metrics.NotificationDeliveriesTotal.WithLabelValues(channel.Type, "failed").Inc()
			return
		}
	}
}

// beginAttempt 在发出请求**之前**落一行 pending 审计:
// 进程在请求途中崩溃时, 这行 pending 就是"这次尝试发生过"的唯一证据。
// 审计写失败只记指标与日志 (fail-open), 不影响投递本身。
func (d *Dispatcher) beginAttempt(ctx context.Context, notificationID uint, channel models.NotificationChannel, attemptNo uint32) *models.NotificationDelivery {
	row := &models.NotificationDelivery{
		NotificationID: notificationID,
		ChannelID:      channel.ID,
		State:          models.DeliveryStatePending,
		AttemptNo:      attemptNo,
		CreatedAt:      d.now(),
	}
	if err := d.db.WithContext(ctx).Create(row).Error; err != nil {
		metrics.DataConsumerDBWriteFailures.WithLabelValues("notify_dispatcher", "notification_deliveries").Inc()
		logger.Warnf("notify: failed to persist pending delivery audit row (channel=%d attempt=%d): %s",
			channel.ID, attemptNo, RedactText(err.Error()))
		return nil
	}
	return row
}

// finishAttempt 把 pending 行推进到终态并更新指标。
// terminal 表示这是该通道的最后一次尝试 (决定 outcome.Failed 与 failed 指标)。
func (d *Dispatcher) finishAttempt(ctx context.Context, outcome *Outcome, channel models.NotificationChannel, row *models.NotificationDelivery, attemptNo uint32, result Result, terminal bool) {
	state := models.DeliveryStateFailed
	errorMessage := ClientMessage(result.Err)
	if result.OK {
		state = models.DeliveryStateDelivered
		errorMessage = ""
	}
	record := models.NotificationDelivery{
		NotificationID: outcome.NotificationID,
		ChannelID:      channel.ID,
		State:          state,
		AttemptNo:      attemptNo,
		StatusCode:     result.StatusCode,
		ErrorMessage:   errorMessage,
		DurationMs:     result.Duration.Milliseconds(),
	}
	if row != nil {
		record.ID = row.ID
		record.CreatedAt = row.CreatedAt
		updates := map[string]interface{}{
			"state":         state,
			"status_code":   result.StatusCode,
			"error_message": errorMessage,
			"duration_ms":   record.DurationMs,
		}
		if err := d.db.WithContext(ctx).Model(&models.NotificationDelivery{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
			metrics.DataConsumerDBWriteFailures.WithLabelValues("notify_dispatcher", "notification_deliveries").Inc()
			logger.Warnf("notify: failed to finalize delivery audit row id=%d: %s", row.ID, RedactText(err.Error()))
		}
	}
	outcome.Deliveries = append(outcome.Deliveries, record)

	metrics.NotificationDeliveryDuration.WithLabelValues(channel.Type).Observe(result.Duration.Seconds())
	if result.OK {
		outcome.Delivered++
		metrics.NotificationDeliveriesTotal.WithLabelValues(channel.Type, "delivered").Inc()
		return
	}
	if !terminal {
		metrics.NotificationDeliveriesTotal.WithLabelValues(channel.Type, "retrying").Inc()
	} else {
		outcome.Failed++
		metrics.NotificationDeliveriesTotal.WithLabelValues(channel.Type, "failed").Inc()
	}
	// 脱敏 + 截断后才允许进日志 (设计 §7.3/§7.5)。
	logger.Warnf("notify: delivery to channel %d (%s) failed on attempt %d: %s",
		channel.ID, channel.Type, attemptNo, TruncateMessage(errorMessage, MaxErrorMessageBytes))
}

// refreshEnabledGauge 更新"启用通道数"gauge。按 30s 节流, 因为它挂在投递热路径上。
func (d *Dispatcher) refreshEnabledGauge(ctx context.Context) {
	d.mu.Lock()
	if !d.lastRefresh.IsZero() && d.now().Sub(d.lastRefresh) < 30*time.Second {
		d.mu.Unlock()
		return
	}
	d.lastRefresh = d.now()
	d.mu.Unlock()

	var count int64
	if err := d.db.WithContext(ctx).Model(&models.NotificationChannel{}).
		Where("enabled IS NOT NULL AND enabled = ?", true).Count(&count).Error; err != nil {
		return
	}
	metrics.NotificationChannelsEnabled.Set(float64(count))
}

// 重试参数 (设计 §5): 指数退避 1s, 2s, 4s... 上限 30s。
// 风格对齐 internal/ota/ota.go:107 的 ackRetryBackoff —— 显式常量表而非运行时计算,
// 上限写死可见; 通知量低, 不需要 jitter 去同步。
const (
	// MaxBackoff 是单次重试等待的硬上限。
	MaxBackoff = 30 * time.Second
	// MaxRetriesLimit 防止配置出"几乎永远重试"的通道 (失败必须能收敛到 failed 终态)。
	MaxRetriesLimit = 5
)

var retryBackoff = [6]time.Duration{
	1 * time.Second,
	2 * time.Second,
	4 * time.Second,
	8 * time.Second,
	16 * time.Second,
	MaxBackoff,
}

// BackoffFor 返回第 attempt 次失败后的等待时长 (从 1 开始计数)。
func BackoffFor(attempt uint32) time.Duration {
	if attempt == 0 {
		return retryBackoff[0]
	}
	index := int(attempt) - 1
	if index >= len(retryBackoff) {
		index = len(retryBackoff) - 1
	}
	return retryBackoff[index]
}

// DefaultMaxRetries 是通道未配置 max_retries 时的默认重试次数 (设计 §5)。
const DefaultMaxRetries = 2

// NormalizeRetries 归一化通道的 max_retries 配置。
//
// nil = 用户没配 → DefaultMaxRetries; 0 = 用户显式要求**不重试**, 必须原样尊重
// (设计 §7.6 的整条裁决就是为它); 负数视为未配置; 超过 MaxRetriesLimit 截断,
// 保证失败总能收敛到 failed 终态。
func NormalizeRetries(maxRetries *int) int {
	if maxRetries == nil || *maxRetries < 0 {
		return DefaultMaxRetries
	}
	if *maxRetries > MaxRetriesLimit {
		return MaxRetriesLimit
	}
	return *maxRetries
}

// sleepWithContext 可中断的等待。
func sleepWithContext(ctx context.Context, duration time.Duration) error {
	if duration <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
