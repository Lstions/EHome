package commandexec

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// 本文件把 MaxCapabilityAge 与**固件真实上报节奏**之间的耦合钉死。
//
// 为什么需要它（2026-09-21 生产缺陷）：
//   - 固件没有周期性 ResourceReport，只有 Hello 成功 / 清单提交成功 / 服务端
//     主动 QueryResources 三处触发；
//   - 周期 Hello 由 sync_manager 的 600s 周期 sync 驱动；
//   - 而 MaxCapabilityAge 曾经是 5 分钟 < 10 分钟 ⇒ 健康节点周期性地被判 stale。
//
// 这是一个**跨仓常量耦合**：backend 的一个常量必须 > 固件的一个 Kconfig 值。
// 单侧重构（改固件节奏、或调后端阈值）都不会被任何编译期检查拦住，
// 所以这里用一条源码级门禁 + 一条行为级边界测试来兜住。

const (
	// firmwarePeriodicSyncSec 是固件周期 sync 的间隔（秒）。
	// 取自 esp32-collector/components/sync_manager/sync_manager.c 的
	//   #ifndef CONFIG_COLLECTOR_SYNC_PERIODIC_SEC
	//   #define CONFIG_COLLECTOR_SYNC_PERIODIC_SEC 600
	// 下面的 TestCapabilityWindowMatchesFirmwareReportCadence 会**去源码里核对**
	// 这个数字，而不是只相信这里的注释。
	firmwarePeriodicSyncSec = 600

	// firmwarePeriodicPollSec 是 sync_manager_periodic_task 的轮询粒度（秒）：
	//   vTaskDelay(pdMS_TO_TICKS(60 * 1000));
	// 周期判定是 now-last_sync > 600，而轮询每 60s 才跑一次，
	// 因此真实的上报间隔上界是 600 + 60 秒。
	firmwarePeriodicPollSec = 60
)

// firmwareRoot 定位 esp32-collector 目录。go test 的工作目录是包目录
// （backend/internal/commandexec），仓库根在其上三级。
func firmwareRoot(t *testing.T) string {
	t.Helper()
	candidates := []string{
		filepath.Join("..", "..", "..", "esp32-collector"),
		filepath.Join("esp32-collector"),
	}
	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate
		}
	}
	t.Fatalf("找不到 esp32-collector 目录，尝试过: %v", candidates)
	return ""
}

func readFirmwareFile(t *testing.T, root string, parts ...string) string {
	t.Helper()
	path := filepath.Join(append([]string{root}, parts...)...)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取固件源码 %s 失败: %v", path, err)
	}
	if len(raw) == 0 {
		t.Fatalf("固件源码 %s 为空 —— 门禁会因空文件而假绿", path)
	}
	return string(raw)
}

var syncPeriodDefine = regexp.MustCompile("#define[ \\t]+CONFIG_COLLECTOR_SYNC_PERIODIC_SEC[ \\t]+([0-9]+)")

// TestCapabilityWindowMatchesFirmwareReportCadence 是本次修复的**不变式门禁**：
//
//	MaxCapabilityAge > 固件周期 sync 间隔 + 周期任务轮询粒度
//
// 并且固件源码里的数字必须与门禁记录的一致。
func TestCapabilityWindowMatchesFirmwareReportCadence(t *testing.T) {
	if MaxCapabilityAge <= 0 {
		t.Fatalf("MaxCapabilityAge=%v 非正数", MaxCapabilityAge)
	}

	// --- 分支 A：固件源码核对（防止门禁里的常量与真实固件漂移）---
	root := firmwareRoot(t)
	syncSource := readFirmwareFile(t, root, "components", "sync_manager", "sync_manager.c")

	found := syncPeriodDefine.FindStringSubmatch(syncSource)
	if found == nil {
		t.Fatalf("在 %s/components/sync_manager/sync_manager.c 中找不到 "+
			"CONFIG_COLLECTOR_SYNC_PERIODIC_SEC 的 #define —— 固件重构了，本门禁必须同步更新",
			root)
	}
	declared, err := strconv.Atoi(found[1])
	if err != nil {
		t.Fatalf("解析 CONFIG_COLLECTOR_SYNC_PERIODIC_SEC=%q 失败: %v", found[1], err)
	}
	if declared != firmwarePeriodicSyncSec {
		t.Fatalf("固件周期 sync 间隔已变为 %ds（本门禁记录的是 %ds）。"+
			"后端 MaxCapabilityAge 必须重新评估：它至少要比固件的实际上报间隔更长。"+
			"更新 firmwarePeriodicSyncSec 与本断言前，请先确认 MaxCapabilityAge 仍然安全。",
			declared, firmwarePeriodicSyncSec)
	}
	if !strings.Contains(syncSource, "vTaskDelay(pdMS_TO_TICKS(60 * 1000))") {
		t.Fatalf("周期任务轮询粒度不再是 60s —— 上报间隔上界的推导（600+60）失效，请重新推导")
	}

	// --- 分支 B：ResourceReport 触发点的分母自证 ---
	// 若固件新增了**周期性**上报，本门禁必须变红：那意味着后端阈值可以（也应该）
	// 重新收紧，而这需要一次人的裁决，而不是让门禁静默通过。
	reportCallSites := 0
	productionFiles := []string{
		filepath.Join("main", "hello_handshake.c"),
		filepath.Join("main", "app_callbacks.c"),
		filepath.Join("components", "msg_handler", "handler_config.c"),
	}
	for _, relative := range productionFiles {
		body := readFirmwareFile(t, root, strings.Split(relative, string(filepath.Separator))...)
		reportCallSites += strings.Count(body, "msg_handler_send_resource_report();")
	}
	if reportCallSites != 3 {
		t.Fatalf("固件的 ResourceReport 触发点数量已变为 %d（原为 3：hello_handshake.c:44 / "+
			"app_callbacks.c:363 / handler_config.c:70）。若新增的是**周期性**上报，"+
			"后端 MaxCapabilityAge 应当重新收紧 —— 请重新评估并更新本门禁。", reportCallSites)
	}

	// --- 分支 C：不变式本身 ---
	required := time.Duration(firmwarePeriodicSyncSec+firmwarePeriodicPollSec) * time.Second
	if MaxCapabilityAge <= required {
		t.Fatalf("MaxCapabilityAge=%v 必须 > 固件上报间隔上界 %v"+
			"（周期 sync %ds + 轮询粒度 %ds）。"+
			"小于等于它会让**健康节点**被周期性判为 capability_stale。"+
			"（原先此处引用「生产上 38%% 时间不可用」，该组数字在仓库内既无 artifact、"+
			"也无复跑命令，不可复核，故已移除：口径/来源待补。）",
			MaxCapabilityAge, required, firmwarePeriodicSyncSec, firmwarePeriodicPollSec)
	}
	if want := resourceReportInterval + capabilityReportMargin; MaxCapabilityAge != want {
		t.Fatalf("MaxCapabilityAge=%v 与声明的构成 %v+%v=%v 不一致："+
			"阈值必须能由「上报节奏 + 显式裕量」推导出来，不能是凭感觉的数字",
			MaxCapabilityAge, resourceReportInterval, capabilityReportMargin, want)
	}
}

// TestCapabilityWindowBoundaryMatchesReportCadence 是**行为级**边界测试：
// 直接用导出的常量构造「固件最坏上报间隔」与「刚过阈值」两种快照，
// 断言前者的年龄仍在窗口内、后者必须被拒。
func TestCapabilityWindowBoundaryMatchesReportCadence(t *testing.T) {
	now := time.Now().UTC()

	// 最坏情况下仍然健康：Hello 成功后 660s 没有新的触发（纯周期 sync 驱动）。
	worstHealthyAge := time.Duration(firmwarePeriodicSyncSec+firmwarePeriodicPollSec) * time.Second
	if worstHealthyAge > MaxCapabilityAge {
		t.Fatalf("固件最坏上报间隔 %v 已经超过 MaxCapabilityAge=%v —— "+
			"健康节点会被判 stale", worstHealthyAge, MaxCapabilityAge)
	}

	// 超过阈值必须仍然被拒（放宽不等于取消）：阈值外 1 秒。
	staleNode := freshCapabilityNode(now.Add(-MaxCapabilityAge - time.Second))
	if _, _, err := currentCapabilities(staleNode, func() time.Time { return now }); err == nil {
		t.Fatalf("超过 MaxCapabilityAge 的快照必须仍然 fail-closed，得到 err=nil")
	}

	// 阈值内必须放行。
	freshNode := freshCapabilityNode(now.Add(-MaxCapabilityAge + time.Second))
	if _, _, err := currentCapabilities(freshNode, func() time.Time { return now }); err != nil {
		t.Fatalf("阈值内的快照必须放行，得到 err=%v", err)
	}

	// 固件最坏间隔那一刻的快照同样必须放行（这条才真正代表本轮修复的目标态）。
	worstNode := freshCapabilityNode(now.Add(-worstHealthyAge))
	if _, _, err := currentCapabilities(worstNode, func() time.Time { return now }); err != nil {
		t.Fatalf("按固件真实节奏（%v 无新上报）仍必须放行，得到 err=%v", worstHealthyAge, err)
	}
}

// freshCapabilityNode 构造一台能力齐全、ResourceReportedAt 为 reported 的节点。
// 字段与 service_test.go 的 setupService 保持一致，确保只有“年龄”这一个变量在变。
func freshCapabilityNode(reported time.Time) models.Node {
	return models.Node{
		NodeID: "cadence-node", Name: "cadence", Status: "online",
		ConfigVersion: "manifest-cadence", ConfigStatus: "applied", ConfigSyncState: "in_sync",
		BootID: "boot-cadence", ResourceReportedAt: &reported, CommandEngineRevision: 1,
		CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,` +
			`"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`,
	}
}
