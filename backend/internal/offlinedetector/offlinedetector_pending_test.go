package offlinedetector

import (
	"testing"
	"time"

	ehomeModels "ehome/backend/internal/models"

	"gorm.io/gorm"
)

// =====================================================================
// 缺陷 5（2026-10-03）：新建边缘设备「明明没数据却显示在线」
//
// 修复由三处协同构成，本用例组逐一定住，缺任一处则回归：
//   1. models.EdgeDevice.Status 的默认值是 pending（不是 active）
//   2. loadActiveDevices 载入 status <> offline（含 pending）
//   3. checkEdgeDevicesOffline 对「从未上报」的设备用 created_at 判超时
//      （原实现用 !lastData.IsZero() 守卫，零值永久跳过 ⇒ 永不判离线）
// =====================================================================

// TestEdgeDeviceModelDefaultStatusIsPending 证明第 1 处：模型默认值。
// 这条断言的是**建表默认值**，因为 GORM 在 Status 为空字符串时会用该默认值
// 填充 INSERT；若仍是 active，用户新建的设备第一眼就是「在线」。
//
// 它凭什么会失败：把 models.go 的 default:pending 改回 default:active，
// 本用例立刻红。
func TestEdgeDeviceModelDefaultStatusIsPending(t *testing.T) {
	if ehomeModels.EdgeDeviceStatusPending == ehomeModels.EdgeDeviceStatusActive {
		t.Fatal("pending 与 active 必须是不同取值，否则状态机退化")
	}
	db := setupExtraTestDB(t)
	// 不显式设置 Status，完全依赖模型默认值 —— 这正是创建路径的行为。
	dev := ehomeModels.EdgeDevice{Name: "fresh", NodeID: "NODE-X", ChannelID: 1}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	var stored ehomeModels.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != ehomeModels.EdgeDeviceStatusPending {
		t.Fatalf("新建边缘设备的状态是 %q，应为 %q（无数据的设备不得显示为在线）",
			stored.Status, ehomeModels.EdgeDeviceStatusPending)
	}
}

// TestNeverReportedEdgeDeviceGoesOffline 证明第 2+3 处：一个从未上报过数据的
// 设备，在创建时间超过阈值后必须被判为 offline。
//
// 这是用户现象的**效果层**断言：修复前 last_data_at 为 NULL、缓存里是零值，
// 零值守卫让它永远跳过判定，设备永远停在在线/等待中。
//
// 它凭什么会失败：把 checkEdgeDevicesOffline 里的零值分支删掉（恢复
// !lastData.IsZero() 守卫），本用例红；把 created_at 的比较去掉，本用例红。
func TestNeverReportedEdgeDeviceGoesOffline(t *testing.T) {
	d, db := setupExtraDetector(t)

	// 一个从没上报过数据的设备：last_data_at 为 NULL，创建于阈值之前。
	created := time.Now().Add(-(EdgeDeviceOfflineThreshold + time.Minute))
	dev := ehomeModels.EdgeDevice{
		Name: "never-reported", NodeID: "NODE-1", ChannelID: 1,
		Status: ehomeModels.EdgeDeviceStatusPending,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	// GORM 的 autoUpdateTime 会覆盖 CreatedAt，故显式回写过去的时间。
	if err := db.Model(&dev).UpdateColumn("created_at", created).Error; err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	// 模拟创建后进入缓存（OnEdgeDeviceCreated 写零值 last_data_at）。
	d.OnEdgeDeviceCreated(dev.ID)

	d.checkEdgeDevicesOffline(db.Session(&gorm.Session{}))

	var stored ehomeModels.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != ehomeModels.EdgeDeviceStatusOffline {
		t.Fatalf("从未上报数据的设备状态是 %q，应被判为 %q（缺陷 5：没数据必须显示为离线，不能显示在线）",
			stored.Status, ehomeModels.EdgeDeviceStatusOffline)
	}
}

// TestFreshEdgeDeviceNotYetOffline 是上一条的**反向对照**：刚创建、还没到阈值的
// 设备不得被立刻判离线，否则「修好一个显示错误」会变成「创建即故障」。
// 两条用例合起来才钉住 created_at 这个判据（只说「会离线」无法排除「立刻离线」）。
func TestFreshEdgeDeviceNotYetOffline(t *testing.T) {
	d, db := setupExtraDetector(t)

	dev := ehomeModels.EdgeDevice{
		Name: "just-created", NodeID: "NODE-2", ChannelID: 1,
		Status: ehomeModels.EdgeDeviceStatusPending,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	d.OnEdgeDeviceCreated(dev.ID)
	d.checkEdgeDevicesOffline(db.Session(&gorm.Session{}))

	var stored ehomeModels.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != ehomeModels.EdgeDeviceStatusPending {
		t.Fatalf("刚创建的设备状态被改成 %q，应保持 %q（它还在等第一帧数据）",
			stored.Status, ehomeModels.EdgeDeviceStatusPending)
	}
}

// TestLoadActiveDevicesIncludesPending 证明第 2 处：重启后 pending 设备也要进缓存。
// 若仍只载入 active，重启会让所有「等待数据」的设备从检查视野里消失，
// 它们再也不会被判离线 —— 缺陷会以更隐蔽的形式回来。
func TestLoadActiveDevicesIncludesPending(t *testing.T) {
	d, db := setupExtraDetector(t)

	pending := ehomeModels.EdgeDevice{Name: "p", NodeID: "NODE-3", ChannelID: 1, Status: ehomeModels.EdgeDeviceStatusPending}
	offline := ehomeModels.EdgeDevice{Name: "o", NodeID: "NODE-3", ChannelID: 2, Status: ehomeModels.EdgeDeviceStatusOffline}
	active := ehomeModels.EdgeDevice{Name: "a", NodeID: "NODE-3", ChannelID: 3, Status: ehomeModels.EdgeDeviceStatusActive}
	for _, dev := range []*ehomeModels.EdgeDevice{&pending, &offline, &active} {
		if err := db.Create(dev).Error; err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	d.loadActiveDevices()

	d.mu.RLock()
	_, hasPending := d.activeDevices[pending.ID]
	_, hasActive := d.activeDevices[active.ID]
	_, hasOffline := d.activeDevices[offline.ID]
	d.mu.RUnlock()

	if !hasPending {
		t.Error("pending 设备未被载入缓存：重启后它将永远不再被判离线")
	}
	if !hasActive {
		t.Error("active 设备未被载入缓存：既有行为回归")
	}
	if hasOffline {
		t.Error("offline 设备不该被载入缓存：已判离线的设备无需重复检查")
	}
}

// TestRuntimeCreatedDeviceEntersOfflineDetection 证明「运行期新建的设备能被离线判定看到」。
//
// 背景（2026-10-03 缺陷 5 的第三处缺口）：设备缓存原本只在 Start() 时装载一次，
// 而生产代码**从不调用** OnEdgeDeviceCreated（全仓无调用点）。于是服务启动后
// 新建的边缘设备根本就不在 activeDevices 里，checkEdgeDevicesOffline 遍历不到它，
// 既不会判它离线、也不会显示任何状态变化 —— 用户看到的是"等待数据"永远不变。
//
// 修法是每个检测 tick 重新同步 DB 里尚未离线的设备集合。本用例只做一件事：
// 模拟 Start() 之后才创建设备，然后跑一次检测，断言它真的被判为离线。
//
// 它凭什么会失败：把 checkEdgeDevicesOffline 开头的 loadActiveDevices() 删掉，
// 缓存里没有这个新设备，状态将保持 pending，本用例红。
func TestRuntimeCreatedDeviceEntersOfflineDetection(t *testing.T) {
	d, db := setupExtraDetector(t)

	// 第一次装载发生在"还没有任何设备"的时候（等价于服务刚启动）。
	d.loadActiveDevices()

	// 服务运行期间新建了一个从未上报数据的设备，且创建时间已超过阈值。
	created := time.Now().Add(-(EdgeDeviceOfflineThreshold + time.Minute))
	dev := ehomeModels.EdgeDevice{
		Name: "created-later", NodeID: "NODE-9", ChannelID: 1,
		Status: ehomeModels.EdgeDeviceStatusPending,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Model(&dev).UpdateColumn("created_at", created).Error; err != nil {
		t.Fatalf("backdate created_at: %v", err)
	}

	// 关键：不调用 OnEdgeDeviceCreated（生产代码也不调用），直接跑一轮检测。
	d.checkEdgeDevicesOffline(db.Session(&gorm.Session{}))

	var stored ehomeModels.EdgeDevice
	if err := db.First(&stored, dev.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if stored.Status != ehomeModels.EdgeDeviceStatusOffline {
		t.Fatalf("运行期新建的设备状态是 %q，应为 %q：它必须进入离线判定视野",
			stored.Status, ehomeModels.EdgeDeviceStatusOffline)
	}
}

// TestPeriodicReloadKeepsFreshInMemoryTimestamp 是上一条的**反向对照**：
// 每 tick 重新装载不能把 OnEdgeDeviceData 刚写入的 time.Now() 覆盖成 DB 里
// 稍旧的 last_data_at，否则一个正在正常上报的设备会被倒退回"超时"而误判离线。
// 只说"新设备能进缓存"不足以约束这一点，故单列。
func TestPeriodicReloadKeepsFreshInMemoryTimestamp(t *testing.T) {
	d, db := setupExtraDetector(t)

	// DB 里这条记录的 last_data_at 已经很旧（超过阈值）。
	stale := time.Now().Add(-(EdgeDeviceOfflineThreshold + time.Minute))
	dev := ehomeModels.EdgeDevice{
		Name: "healthy", NodeID: "NODE-10", ChannelID: 1,
		Status: ehomeModels.EdgeDeviceStatusActive, LastDataAt: &stale,
	}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	// 设备此刻刚上报数据：内存缓存是新的，DB 还没来得及/不应该覆盖它。
	d.OnEdgeDeviceData(dev.ID)
	d.mu.RLock()
	fresh := d.activeDevices[dev.ID]
	d.mu.RUnlock()
	if fresh.IsZero() {
		t.Fatal("fixture: OnEdgeDeviceData should record a timestamp")
	}

	d.loadActiveDevices()

	d.mu.RLock()
	after := d.activeDevices[dev.ID]
	d.mu.RUnlock()
	if !after.Equal(fresh) {
		t.Fatalf("periodic reload overwrote a fresh in-memory timestamp: %v -> %v", fresh, after)
	}
}
