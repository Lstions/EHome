package offlinedetector

import (
	"sync"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// 离线可见时延预算（1+3+1=5s，用户要求 <=5s；三项必须同时收紧，单独改任一项都会静默破坏指标）：
//
//	固件 StatusReport 周期 FirmwareStatusReportPeriod 1s（esp32-collector/main/main.c status_task）
//	+ 节点离线阈值 NodeOfflineThreshold 3s
//	+ 检测循环 ticker OfflineCheckInterval 1s
//	= 最坏 5s
//
// 阈值 3s = 3 个心跳周期，用于容忍偶发丢包；旧值 90s（18 个 5s 周期）过于宽松，
// 节点断电后要 ~95s 才可见，不满足用户要求。
const (
	// FirmwareStatusReportPeriod 固件 StatusReport 上报周期。后端无法在编译期强制固件侧，
	// 这里只作为时延预算的单一记录点：改 main.c 的 STATUS_REPORT_PERIOD_MS 时必须同步改这里。
	FirmwareStatusReportPeriod = 1 * time.Second

	// NodeOfflineThreshold 节点离线阈值：last_seen 距 now 严格大于它才判离线（见 isNodeOffline）。
	// 边界语义：恰好等于阈值仍算在线，下一个 tick 才判离线，即离线可见时延上界为阈值+1 tick。
	NodeOfflineThreshold = 3 * time.Second

	// OfflineCheckInterval 离线检测循环周期。
	OfflineCheckInterval = 1 * time.Second

	// EdgeDeviceOfflineThreshold 边缘设备离线阈值。与节点阈值是两套口径，不能同样收紧：
	// 边缘设备按各自 interval_ms 轮询上报（BMS 实测 5000ms），阈值必须显著大于轮询周期，
	// 否则正常轮询间隙就会被误判离线（阈值 < 周期 ⇒ 设备永久离线）。节点是固定 1s 心跳，
	// 才敢用 3s。故此处保持 60s。
	EdgeDeviceOfflineThreshold = 60 * time.Second

	// OfflineLatencyBudget 最坏离线可见时延，SLA 为 <=5s。新增心跳链路前先看它。
	OfflineLatencyBudget = FirmwareStatusReportPeriod + NodeOfflineThreshold + OfflineCheckInterval
)

// Detector implements three-layer offline detection
type Detector struct {
	db     *gorm.DB
	wsHub  *websocket.Hub
	ticker *time.Ticker
	quit   chan struct{}

	// M5 fix: Cache active edge device IDs to avoid full table scan every 5s
	mu            sync.RWMutex
	activeDevices map[uint]time.Time // device PK → last_data_at (only active devices)
	cacheReady    bool

	// sourceOfflineHook 数据源主备 (设计/数据源主备与故障转移.md §4): 边缘设备
	// 离线回调, main.go 注入 (本包不反向依赖 datasource); nil 时跳过。
	sourceOfflineHook func(edgeDeviceID uint)
}

// NewDetector creates a new offline detector
func NewDetector(db *gorm.DB, wsHub *websocket.Hub) *Detector {
	return &Detector{
		db:            db,
		wsHub:         wsHub,
		quit:          make(chan struct{}),
		activeDevices: make(map[uint]time.Time),
	}
}

// Start begins the offline detection loop
func (d *Detector) Start() {
	// M5 fix: Initial load of active devices from DB
	d.loadActiveDevices()

	d.ticker = time.NewTicker(OfflineCheckInterval)
	go d.loop()
	logger.Infof("Offline detector started (3-layer)")
}

// Stop stops the offline detection loop
func (d *Detector) Stop() {
	close(d.quit)
	d.ticker.Stop()
}

func (d *Detector) loop() {
	for {
		select {
		case <-d.ticker.C:
			d.checkOffline()
		case <-d.quit:
			return
		}
	}
}

// checkOffline performs two-layer offline detection (parallel).
// Each layer uses a session-isolated DB handle (db.Session) to avoid
// Statement races between concurrent GORM calls.
// Redis 退役 (方案 v3.4 §4 任务B): 原 checkRedisHeartbeats (L1) 已删除，
// checkDBLastSeen (L3) 是节点离线判定的唯一路径。
func (d *Detector) checkOffline() {
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		d.checkDBLastSeen(d.db.Session(&gorm.Session{}))
	}()

	go func() {
		defer wg.Done()
		d.checkEdgeDevicesOffline(d.db.Session(&gorm.Session{}))
	}()

	wg.Wait()
}

// checkDBLastSeen checks DB last_seen for online collectors — the single
// offline-detection path since Redis retirement (方案 v3.4 §4 任务B).
// Nodes with LastSeen==nil are skipped (unchanged semantics).
func (d *Detector) checkDBLastSeen(db *gorm.DB) {
	var collectors []models.Node
	if err := db.Where("status = ?", "online").Find(&collectors).Error; err != nil {
		return
	}

	now := time.Now()
	for _, col := range collectors {
		if isNodeOffline(now, col.LastSeen) {
			d.markOffline(db, col.NodeID, "db_last_seen_timeout")
		}
	}
}

// isNodeOffline 是节点离线判定的唯一判据（生产与测试共用，避免测试另抄一份阈值）。
// 语义：last_seen 非 nil 且 now 与它之差严格大于 NodeOfflineThreshold 才算离线；
// 恰好等于阈值（含时钟精度导致的微小超出）仍算在线，等下一个 tick 再判，
// 这正是 1(心跳)+3(阈值)+1(ticker)=5s 预算里 ticker 那一秒的来源。
func isNodeOffline(now time.Time, lastSeen *time.Time) bool {
	return lastSeen != nil && now.Sub(*lastSeen) > NodeOfflineThreshold
}

// markOffline marks a node as offline using the provided session.
func (d *Detector) markOffline(db *gorm.DB, deviceID, reason string) {
	logger.Infof("[Offline] %s: %s", deviceID, reason)

	// Update DB
	db.Model(&models.Node{}).Where("node_id = ?", deviceID).Updates(map[string]interface{}{
		"status": "offline",
	})

	// Record event
	var nodeRecord models.Node
	if err := db.Where("node_id = ?", deviceID).First(&nodeRecord).Error; err == nil {
		db.Create(&models.NodeEvent{
			NodeID:    nodeRecord.NodeID,
			EventType: "offline",
			OldStatus: "online",
			NewStatus: "offline",
		})
	}

	// WebSocket push
	d.wsHub.BroadcastEvent(events.NodeStatus, map[string]interface{}{
		"node_id": deviceID,
		"status":  "offline",
		"reason":  reason,
	})
}

// loadActiveDevices loads all not-yet-offline edge devices from DB into the cache.
//
// 条件是 status <> offline（而非原来的 = active），2026-10-03 缺陷 5：
// 新建设备是 "pending"，也在"尚未判离线"之列，必须一起载入——否则重启后
// 一个从未上报数据的 pending 设备不会被任何一层检查看到，
// 又回到"永远显示在线/等待中"的老问题（只是这次连超时判离线都不生效）。
//
// 调用时机：启动时一次（Start），以及每个检测 tick 一次（见
// checkEdgeDevicesOffline）。后者是 2026-10-03 补上的**必需**一环：缓存原本
// 只在启动时装载，而生产代码从不调用 OnEdgeDeviceCreated（实测全仓无调用点）。
// 因此运行期新建的边缘设备根本不进缓存，永远不会被离线判定看到——用户会看到
// 一个"等待数据"永远不变的状态。每 tick 重新装载 DB 里尚未离线的设备，既补上
// 新建设备，也顺带修正外部改动（手工改库、其它进程改状态）。
// 代价是每秒一次按 status 过滤的索引查询，与原有的启动期开销同量级。
func (d *Detector) loadActiveDevices() {
	var devices []models.EdgeDevice
	if err := d.db.Where("status <> ?", models.EdgeDeviceStatusOffline).Find(&devices).Error; err != nil {
		logger.Warnf("[OfflineDetector] Failed to load active devices: %v", err)
		return
	}

	d.mu.Lock()
	for _, dev := range devices {
		lastData := time.Time{}
		if dev.LastDataAt != nil {
			lastData = *dev.LastDataAt
		}
		// add-only（除零值修正外不覆盖）：运行期由 OnEdgeDeviceData 写入的 time.Now()
		// 比 DB 的 last_data_at 更新，若无条件覆盖会把"刚上报过"退回成稍旧的记录，
		// 健康设备可能被误判离线。零值例外：DB 已有非零值而缓存仍是零值，说明该设备
		// 上报过却没走到 OnEdgeDeviceData，用 DB 值修正，否则它会永远停在
		// "从未上报"分支而被 created_at 判据误判离线。
		if existing, ok := d.activeDevices[dev.ID]; !ok || (existing.IsZero() && !lastData.IsZero()) {
			d.activeDevices[dev.ID] = lastData
		}
	}
	d.cacheReady = true
	d.mu.Unlock()

	logger.Infof("[OfflineDetector] Loaded %d active edge devices into cache", len(devices))
}

// OnEdgeDeviceData is called when an edge device reports data (status=active).
// M5 fix: Update the in-memory cache instead of relying on DB scan.
func (d *Detector) OnEdgeDeviceData(deviceID uint) {
	d.mu.Lock()
	d.activeDevices[deviceID] = time.Now()
	d.mu.Unlock()
}

// SetDeviceOfflineHook 设备离线回调 (设计/数据源主备与故障转移.md §4)。
func (d *Detector) SetDeviceOfflineHook(fn func(edgeDeviceID uint)) {
	d.sourceOfflineHook = fn
}

// OnEdgeDeviceOffline is called when an edge device is marked offline.
// M5 fix: Remove from the in-memory cache.
func (d *Detector) OnEdgeDeviceOffline(deviceID uint) {
	d.mu.Lock()
	delete(d.activeDevices, deviceID)
	d.mu.Unlock()

	// 数据源主备 (设计 §4): 边缘设备离线即对承载来源计一次失败。
	// 独立 goroutine + recover: 钩子阻塞/panic 绝不影响离线检测主流程。
	if hook := d.sourceOfflineHook; hook != nil {
		go func(id uint) {
			defer func() {
				if r := recover(); r != nil {
					logger.Warnf("[OfflineDetector] source offline hook panicked for edge_device %d: %v", id, r)
				}
			}()
			hook(id)
		}(deviceID)
	}
}

// OnEdgeDeviceCreated is called when a new edge device is created.
//
// 设备以"尚未采到数据"的状态入缓存（零值 last_data_at），由
// checkEdgeDevicesOffline 在超过阈值后判为 offline。
func (d *Detector) OnEdgeDeviceCreated(deviceID uint) {
	d.mu.Lock()
	d.activeDevices[deviceID] = time.Time{} // no data yet
	d.mu.Unlock()
}

// checkEdgeDevicesOffline 把「仍被视为在线、但已超过阈值没有数据」的边缘设备判为 offline。
//
// 覆盖两类设备（2026-10-03 缺陷 5 修复第二半）：
//
//  1. 曾经在线、后来失联：last_data_at 非零且早于阈值。
//  2. **新创建但从未上报过数据**：last_data_at 为零值（NULL）。
//
// 第 2 类此前被 `!lastData.IsZero()` 的零值守卫永久跳过，与「模型默认 active」叠加
// 后造成用户看到的现象：新建边缘设备明明没有数据，却永远显示「在线」。
// 零值不能当作"刚上报过"（那是把 NULL 误读成 now），所以这里用创建的时长来判断：
// 一个从未上报的设备，创建时间就是它唯一可信的起点，超过阈值即判离线。
//
// 为什么不用节点那套 3s：边缘设备按 interval_ms 轮询（BMS 实测 5000ms），
// 阈值若小于轮询周期，正常轮询间隙就会被判离线，设备会永久停在 offline。
// M5 fix: Uses in-memory cache of active device IDs instead of full DB scan.
func (d *Detector) checkEdgeDevicesOffline(db *gorm.DB) {
	// 先同步 DB 中"尚未离线"的设备集合，补上运行期新建的设备
	// （生产代码不调用 OnEdgeDeviceCreated，见 loadActiveDevices 注释）。
	d.loadActiveDevices()
	threshold := time.Now().Add(-EdgeDeviceOfflineThreshold)

	// Collect stale device IDs from cache (fast, no DB query).
	// 零值（从未上报）同样进候选：其判定推迟到下面的 DB 查询，
	// 因为缓存只存了 last_data_at，没有 created_at 可比。
	var staleIDs []uint
	var neverReportedIDs []uint
	d.mu.RLock()
	for id, lastData := range d.activeDevices {
		switch {
		case lastData.IsZero():
			neverReportedIDs = append(neverReportedIDs, id)
		case lastData.Before(threshold):
			staleIDs = append(staleIDs, id)
		}
	}
	d.mu.RUnlock()

	// neverReported 需要 created_at 才能判断是否已超时，故交给 DB 层过滤；
	// 已上报过的设备缓存里的时间戳就是判据，直接用。
	if len(neverReportedIDs) > 0 {
		var neverReported []models.EdgeDevice
		if err := db.Where("id IN ? AND (last_data_at IS NULL OR last_data_at = ?) AND created_at < ?",
			neverReportedIDs, time.Time{}, threshold).Find(&neverReported).Error; err != nil {
			logger.Warnf("[OfflineDetector] query never-reported edge devices: %v", err)
		} else {
			for _, dev := range neverReported {
				staleIDs = append(staleIDs, dev.ID)
			}
		}
	}

	if len(staleIDs) == 0 {
		return
	}

	// Fetch only stale devices from DB (targeted query, not full scan)
	var staleDevices []models.EdgeDevice
	if err := db.Where("id IN ? AND status <> ?", staleIDs, models.EdgeDeviceStatusOffline).Find(&staleDevices).Error; err != nil {
		return
	}

	for _, dev := range staleDevices {
		d.markEdgeDeviceOffline(db, dev)
	}
}

// markEdgeDeviceOffline marks an edge device as offline and broadcasts the change.
func (d *Detector) markEdgeDeviceOffline(db *gorm.DB, dev models.EdgeDevice) {
	logger.Infof("[EdgeDevice Offline] id=%d name=%s node_id=%s — no data for >%s", dev.ID, dev.Name, dev.NodeID, EdgeDeviceOfflineThreshold)

	db.Model(&dev).Updates(map[string]interface{}{
		"status": "offline",
	})

	// M5 fix: Remove from cache
	d.OnEdgeDeviceOffline(dev.ID)

	// WebSocket push
	if d.wsHub != nil {
		d.wsHub.BroadcastEvent(events.EdgeDeviceStatus, map[string]interface{}{
			"edge_device_id": dev.ID,
			"device_id":      dev.ID,
			"device_name":    dev.Name,
			"node_id":        dev.NodeID,
			"channel_id":     dev.ChannelID,
			"status":         "offline",
			"reason":         "data_timeout",
		})
	}
}

// UpdateHeartbeat updates the heartbeat for a node.
// Redis 退役 (方案 v3.4 §4 任务B): 原 redis.SetHeartbeat TTL 刷新已删除。
// 方法保留为 no-op 以维持 Detector 接口稳定——未来多实例部署时
// 在此接入分布式心跳实现即可（方案 §4.2 升级路径）。
func (d *Detector) UpdateHeartbeat(deviceID string) {
	_ = deviceID // no-op: offline detection now relies solely on DB last_seen
}
