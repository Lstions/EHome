// Package livedevices 发布边缘设备的 **1Hz 状态快照**（WS 事件 edge_devices_live）。
//
// # 为什么需要这条通道（而不是让前端从 data_update 逐帧事件里自己聚合）
//
//   - 逐帧速率 = 设备数 × 每设备开启的轮询指令数 / 指令间隔。100 台设备各开 3 条
//     1s 指令就是 300 条/秒。列表页只需要"现在是什么状态"，却要解析 300 条 JSON
//     再自行合并 —— 快照把这件事收成 **1 条/秒**，与设备规模、指令条数无关。
//   - 逐帧事件只带**本帧的物理量**（多命令设备一帧只有一个命令的字段），跨命令的
//     读数全集只能靠客户端累加。服务端本来就有这份缓存（api.LatestValues），
//     直接下发全集更准确，也不会因为客户端漏收一条就永久少一个字段。
//   - 快照是**幂等**的：丢一条下一拍就补齐，不需要序号、重传、补洞。
//
// # 与 data_update 的分工（长期约定）
//
//	事件流 data_update / edge_device_status —— 详情页、图表、终端：要"刚收到了什么"
//	状态快照 edge_devices_live              —— 列表、看板：要"现在是什么状态"
//
// # 成本
//
//	无客户端连接时**不发、也不查库**（clientCount 门控）。有客户端时每拍一次
//	只取三列的 SELECT + 内存缓存遍历，不写库、不阻塞数据面（BroadcastEvent 走
//	hub 的缓冲通道）。
package livedevices

import (
	"context"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

const (
	// Interval 是快照推送周期。与前端渲染节拍（1Hz）一致：
	// 更快只是把同样的数字重复推一遍，更慢会让"看起来实时"打折扣。
	Interval = time.Second

	// errorLogSuppressWindow 是同一错误重复告警的抑制窗口。
	// 本循环每秒跑一次，DB 故障时不抑制会把日志刷爆，而每拍一条对排查没有增量信息。
	errorLogSuppressWindow = time.Minute
)

// Item 是快照里的单台设备。
//
// 只放**会变的字段**：配置（device_config / command_intervals / 名称）不在这里，
// 它们由 HTTP 列表接口负责且不会每秒变化 —— 把它们塞进 1Hz 快照等于每秒重发一遍
// 静态配置（这正是列表接口不能用来做 1Hz 轮询的原因）。
type Item struct {
	ID     uint   `json:"id"`
	Status string `json:"status"`
	// LastDataAt 是后端记录的最近一次成功采样时刻（nil = 从未采到数据）。
	LastDataAt *time.Time `json:"last_data_at,omitempty"`
	// Data 是该设备跨命令聚合后的最新读数；没有缓存值时省略（不是空对象）。
	Data map[string]float64 `json:"data,omitempty"`
}

// Payload 是 edge_devices_live 的载荷。
type Payload struct {
	// At 是快照生成时刻（UTC，RFC3339）。客户端目前只用它做诊断，
	// 每设备的时序以 last_data_at 为准。
	At    string `json:"at"`
	Items []Item `json:"items"`
}

// Publisher 按固定周期构建并广播状态快照。
type Publisher struct {
	db        *gorm.DB
	broadcast func(eventType string, payload interface{})
	// values 返回该设备**全部**物理量的最新值（接线到 api.LatestValues）。
	// 为 nil 时快照仍会下发状态，只是没有读数。
	values func(deviceID uint) []models.UnifiedData
	// clientCount 返回当前 WS 客户端数；为 nil 表示不做门控（测试/单机场景）。
	clientCount func() int
	interval    time.Duration

	// 错误日志抑制状态。只在 Run 的单 goroutine 内读写，无需加锁。
	lastErr    string
	lastErrLog time.Time
}

// Option 配置 Publisher。
type Option func(*Publisher)

// WithInterval 覆盖推送周期（测试用；生产保持 Interval）。
func WithInterval(d time.Duration) Option {
	return func(p *Publisher) {
		if d > 0 {
			p.interval = d
		}
	}
}

// New 构造 Publisher。
//
// 四个依赖都刻意用函数注入而不是直接引用包：
//   - values    → api.LatestValues（避免内部服务反向依赖 api 包）
//   - broadcast → wsHub.BroadcastEvent
//   - clientCount → wsHub.ClientCount
//
// 这样本包只依赖 models/events，测试可以全部用假实现。
func New(
	db *gorm.DB,
	broadcast func(eventType string, payload interface{}),
	values func(deviceID uint) []models.UnifiedData,
	clientCount func() int,
	opts ...Option,
) *Publisher {
	p := &Publisher{
		db:          db,
		broadcast:   broadcast,
		values:      values,
		clientCount: clientCount,
		interval:    Interval,
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Run 阻塞运行直到 ctx 取消。单 goroutine：不会有并发拍堆积。
func (p *Publisher) Run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.publish()
		}
	}
}

// publish 是单拍：门控 → 构建 → 广播。
func (p *Publisher) publish() {
	// 没有订阅者：连查询都不做（空闲系统零成本）。
	if p.clientCount != nil && p.clientCount() == 0 {
		return
	}

	payload, err := p.build()
	if err != nil {
		p.logBuildError(err)
		return
	}
	p.lastErr = ""

	if p.broadcast != nil {
		p.broadcast(events.EdgeDevicesLive, payload)
	}
}

// build 组装一拍快照。
//
// 查询只取三列：id / status / last_data_at。软删除由 GORM 的默认 scope 处理
// （DeletedAt 非空的行不会出现在结果里），因此已删除设备会从快照中消失 ——
// 这正是客户端"整体替换"语义需要的（否则删除后客户端会一直显示幽灵设备）。
func (p *Publisher) build() (Payload, error) {
	rows := make([]deviceRow, 0, 32)
	if err := p.db.WithContext(context.Background()).
		Model(&models.EdgeDevice{}).
		Select("id", "status", "last_data_at").
		Order("id").
		Scan(&rows).Error; err != nil {
		return Payload{}, err
	}

	items := make([]Item, 0, len(rows))
	for _, row := range rows {
		item := Item{ID: row.ID, Status: row.Status, LastDataAt: row.LastDataAt}
		if p.values != nil {
			if records := p.values(row.ID); len(records) > 0 {
				data := make(map[string]float64, len(records))
				for _, rec := range records {
					data[rec.SensorName] = rec.Value
				}
				item.Data = data
			}
		}
		items = append(items, item)
	}

	return Payload{At: time.Now().UTC().Format(time.RFC3339), Items: items}, nil
}

// deviceRow 是 SELECT 的投影目标（只映射被选中的三列）。
type deviceRow struct {
	ID         uint
	Status     string
	LastDataAt *time.Time
}

// logBuildError 在一分钟内只记一次同样的错误。
func (p *Publisher) logBuildError(err error) {
	msg := err.Error()
	now := time.Now()
	if msg == p.lastErr && now.Sub(p.lastErrLog) < errorLogSuppressWindow {
		return
	}
	p.lastErr = msg
	p.lastErrLog = now
	logger.Warnf("livedevices: state snapshot build failed (repeats suppressed for %s): %v", errorLogSuppressWindow, err)
}
