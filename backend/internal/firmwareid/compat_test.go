package firmwareid

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
)

// compatibleRequest 是「全部通过」的基线：c6 / 8MB / 无 PSRAM / 2.7.0 → 2.8.0。
// 每条分支的用例只在这个基线上改**一个**维度，保证断言的是该维度而不是组合效应。
func compatibleRequest() CompatRequest {
	return CompatRequest{
		Node: NodeHardware{
			NodeID:   "F0F5BD02F35C",
			Platform: ChipC6,
			FlashMB:  8,
			Psram:    PSRAMNo,
		},
		Firmware: FirmwareFacts{
			ID:            12,
			ModelKey:      "esp32c6-n8",
			Chip:          ChipC6,
			FlashMB:       8,
			Psram:         PSRAMNo,
			Version:       "2.8.0",
			SizeBytes:     1024 * 1024,
			IdentityState: IdentityVerified,
		},
		CurrentVersion: "2.7.0",
	}
}

// branchCase 是 10 条分支的逐条夹具（UT-CP-01..10）。
type branchCase struct {
	name        string
	mutate      func(*CompatRequest)
	branch      int
	code        string
	forceable   bool
	fixContains string
}

func branchCases(t *testing.T) []branchCase {
	t.Helper()
	cap8, ok := SlotCapacity(8)
	if !ok {
		t.Fatal("SlotCapacity(8) must be known")
	}
	return []branchCase{
		{
			name:   "UT-CP-01 withdrawn",
			mutate: func(r *CompatRequest) { r.Firmware.Withdrawn = true },
			branch: BranchWithdrawn, code: GateWithdrawn,
		},
		{
			name:   "UT-CP-02 firmware not verified",
			mutate: func(r *CompatRequest) { r.Firmware.IdentityState = "legacy" },
			branch: BranchFirmwareUnverified, code: GateFirmwareUnverified,
		},
		{
			name:   "UT-CP-03 node hardware unknown (flash)",
			mutate: func(r *CompatRequest) { r.Node.FlashMB = 0 },
			branch: BranchHardwareUnknown, code: GateHardwareUnknown,
			fixContains: "ehomectl node hw set --id F0F5BD02F35C",
		},
		{
			name:   "UT-CP-04 chip mismatch",
			mutate: func(r *CompatRequest) { r.Firmware.Chip = ChipS3; r.Firmware.ModelKey = "esp32s3-n8" },
			branch: BranchModelMismatch, code: GateModelMismatch,
		},
		{
			name:   "UT-CP-05 flash mismatch",
			mutate: func(r *CompatRequest) { r.Firmware.FlashMB = 16; r.Firmware.ModelKey = "esp32c6-n16" },
			branch: BranchFlashMismatch, code: GateFlashMismatch,
		},
		{
			name:   "UT-CP-06 psram required",
			mutate: func(r *CompatRequest) { r.Firmware.Psram = PSRAMYes },
			branch: BranchPsramRequired, code: GatePsramRequired,
		},
		{
			name: "UT-CP-07 psram mismatch",
			mutate: func(r *CompatRequest) {
				r.Firmware.Psram = PSRAMNo
				r.Node.Psram = PSRAMYes
			},
			branch: BranchPsramMismatch, code: GatePsramMismatch,
		},
		{
			name:   "UT-CP-08 image too large",
			mutate: func(r *CompatRequest) { r.Firmware.SizeBytes = cap8 + 1 },
			branch: BranchImageTooLarge, code: GateImageTooLarge,
		},
		{
			name:   "UT-CP-09 same version is not an upgrade",
			mutate: func(r *CompatRequest) { r.Firmware.Version = r.CurrentVersion },
			branch: BranchNotAnUpgrade, code: GateNotAnUpgrade, forceable: true,
		},
		{
			name:   "UT-CP-10 below min from version",
			mutate: func(r *CompatRequest) { r.Firmware.MinFromVersion = "2.7.5" },
			branch: BranchBelowMinVersion, code: GateBelowMinVersion, forceable: true,
		},
	}
}

// TestCheckCompatBaselineCompatible 先证明基线确实全绿 —— 否则下面每条分支都可能
// 因为基线本身有问题而「恰好」命中，测试就失去判别力。
func TestCheckCompatBaselineCompatible(t *testing.T) {
	got := CheckCompat(compatibleRequest())
	if !got.Compatible {
		t.Fatalf("baseline must be compatible, got branch=%d code=%s (%s)", got.Branch, got.Code, got.Message)
	}
	if got.Branch != BranchNone {
		t.Errorf("compatible result must use branch %d, got %d", BranchNone, got.Branch)
	}
	if got.GateError() != nil {
		t.Error("compatible result must not produce a GateError")
	}
}

// TestCheckCompatBranches 是 UT-CP-01..10：逐条命中并返回对应机器码/可 force 性。
func TestCheckCompatBranches(t *testing.T) {
	for _, tc := range branchCases(t) {
		t.Run(tc.name, func(t *testing.T) {
			req := compatibleRequest()
			tc.mutate(&req)

			got := CheckCompat(req)
			if got.Compatible {
				t.Fatalf("expected incompatible, got compatible")
			}
			if got.Branch != tc.branch {
				t.Errorf("branch = %d, want %d", got.Branch, tc.branch)
			}
			if got.Code != tc.code {
				t.Errorf("code = %q, want %q", got.Code, tc.code)
			}
			if len(got.Reasons) != 1 || got.Reasons[0] != tc.code {
				t.Errorf("reasons = %v, want [%s]", got.Reasons, tc.code)
			}
			if got.Forceable != tc.forceable {
				t.Errorf("forceable = %v, want %v", got.Forceable, tc.forceable)
			}
			if got.Message == "" {
				t.Error("blocked result must carry a human-readable message")
			}
			if tc.fixContains != "" && !strings.Contains(got.Fix, tc.fixContains) {
				t.Errorf("fix = %q, want it to contain %q", got.Fix, tc.fixContains)
			}

			gateErr := got.GateError()
			if gateErr == nil {
				t.Fatal("blocked result must produce a GateError")
			}
			if gateErr.Code != tc.code || gateErr.Branch != tc.branch {
				t.Errorf("GateError = %s (branch %d), want %s (branch %d)",
					gateErr.Code, gateErr.Branch, tc.code, tc.branch)
			}
			var target *GateError
			if !errors.As(gateErr, &target) {
				t.Error("GateError must be reachable through errors.As")
			}
		})
	}
}

// TestCheckCompatForceOnlyForVersionDirection 把「force 仅 9/10」单独钉一次：
// 分支表里任何一行的 Forceable 被顺手改宽，本用例都会变红（AC-8 的硬件类仍 409）。
func TestCheckCompatForceOnlyForVersionDirection(t *testing.T) {
	for _, tc := range branchCases(t) {
		req := compatibleRequest()
		tc.mutate(&req)
		got := CheckCompat(req)
		wantForceable := tc.branch == BranchNotAnUpgrade || tc.branch == BranchBelowMinVersion
		if got.Forceable != wantForceable {
			t.Errorf("%s: forceable = %v, want %v", tc.name, got.Forceable, wantForceable)
		}
	}
}

// TestCheckCompatVerifiedButPSRAMEmpty 守第 2 条的**防御**：
// 迁移产生过 identity_state=verified 但 psram="" 的半可信行；若放过去，
// 第 6/7 条在 psram="" 时不会命中，等于把误刷通道按设计重新打开。
func TestCheckCompatVerifiedButPSRAMEmpty(t *testing.T) {
	req := compatibleRequest()
	req.Firmware.IdentityState = IdentityVerified
	req.Firmware.Psram = ""

	got := CheckCompat(req)
	if got.Compatible {
		t.Fatal("verified firmware with empty psram must be rejected")
	}
	if got.Branch != BranchFirmwareUnverified || got.Code != GateFirmwareUnverified {
		t.Fatalf("want branch %d / %s, got branch %d / %s",
			BranchFirmwareUnverified, GateFirmwareUnverified, got.Branch, got.Code)
	}
	if !strings.Contains(got.Message, "psram") {
		t.Errorf("message should name the missing psram field, got %q", got.Message)
	}
}

// TestCheckCompatFirstHitWins 证明「按序、首个命中」而不是「命中任意一条」：
// 组合条件必须落到序号更小的那条分支。
func TestCheckCompatFirstHitWins(t *testing.T) {
	// 撤回 + 芯片不匹配 → 第 1 条
	req := compatibleRequest()
	req.Firmware.Withdrawn = true
	req.Firmware.Chip = ChipS3
	if got := CheckCompat(req); got.Branch != BranchWithdrawn {
		t.Errorf("withdrawn + chip mismatch: branch = %d, want %d", got.Branch, BranchWithdrawn)
	}

	// legacy + 硬件档案缺失 → 第 2 条
	req = compatibleRequest()
	req.Firmware.IdentityState = "legacy"
	req.Node.FlashMB = 0
	if got := CheckCompat(req); got.Branch != BranchFirmwareUnverified {
		t.Errorf("unverified + hardware unknown: branch = %d, want %d", got.Branch, BranchFirmwareUnverified)
	}

	// 硬件档案缺失 + 芯片不匹配 → 第 3 条
	req = compatibleRequest()
	req.Node.Platform = ""
	req.Firmware.Chip = ChipS3
	if got := CheckCompat(req); got.Branch != BranchHardwareUnknown {
		t.Errorf("hardware unknown + chip mismatch: branch = %d, want %d", got.Branch, BranchHardwareUnknown)
	}
}

// TestCheckCompatCanonicalizesChipAliases: 现场 nodes.platform 可能写成 ESP32S3/esp32-s3,
// 与 firmware.chip 的规范值比较前必须归一化，否则会把真实设备误判成型号不匹配。
func TestCheckCompatCanonicalizesChipAliases(t *testing.T) {
	for _, platform := range []string{"esp32c6", "ESP32C6", "C6", " esp32c6 "} {
		req := compatibleRequest()
		req.Node.Platform = platform
		if got := CheckCompat(req); !got.Compatible {
			t.Errorf("platform %q should canonicalize to %s (got %s)", platform, ChipC6, got.Code)
		}
	}

	// 无法识别的 platform（历史值 "ESP32"）按「硬件未识别」拒绝，而不是模型不匹配：
	// 它连芯片都没写清，无从比较。
	req := compatibleRequest()
	req.Node.Platform = "ESP32"
	if got := CheckCompat(req); got.Branch != BranchHardwareUnknown {
		t.Errorf("unknown platform: branch = %d, want %d", got.Branch, BranchHardwareUnknown)
	}
}

// TestCheckCompatSlotCapacityBoundary: 上限内允许、恰好等于上限允许、超 1 字节拒绝。
func TestCheckCompatSlotCapacityBoundary(t *testing.T) {
	cap8, _ := SlotCapacity(8)
	cap16, _ := SlotCapacity(16)

	for _, tc := range []struct {
		name   string
		flash  uint16
		size   uint64
		branch int
	}{
		{"8MB exactly at capacity", 8, cap8, BranchNone},
		{"8MB one byte over", 8, cap8 + 1, BranchImageTooLarge},
		{"16MB exactly at capacity", 16, cap16, BranchNone},
		{"16MB one byte over", 16, cap16 + 1, BranchImageTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := compatibleRequest()
			req.Node.FlashMB = tc.flash
			req.Firmware.FlashMB = tc.flash
			req.Firmware.ModelKey = ModelKey(ChipC6, tc.flash)
			req.Firmware.SizeBytes = tc.size
			got := CheckCompat(req)
			if got.Branch != tc.branch {
				t.Errorf("branch = %d, want %d (code=%s)", got.Branch, tc.branch, got.Code)
			}
		})
	}
}

// TestCheckCompatSystemSourceSkipsVersionDirection 是 UT-CP-12：
// 系统回滚目标是更旧版本，第 9/10 条必须跳过，否则 AutoRollback 每天被拒。
func TestCheckCompatSystemSourceSkipsVersionDirection(t *testing.T) {
	// 同一份输入：source=user 命中第 9 条；source=system 放行。
	req := compatibleRequest()
	req.Firmware.Version = "1.9.0" // 降级
	if got := CheckCompat(req); got.Branch != BranchNotAnUpgrade {
		t.Fatalf("user source: branch = %d, want %d", got.Branch, BranchNotAnUpgrade)
	}
	req.Source = SourceSystem
	if got := CheckCompat(req); !got.Compatible {
		t.Fatalf("system source must skip branch 9, got %s", got.Code)
	}

	// 第 10 条同理：当前版本低于 min_from_version。
	req = compatibleRequest()
	req.Firmware.MinFromVersion = "2.9.0"
	if got := CheckCompat(req); got.Branch != BranchBelowMinVersion {
		t.Fatalf("user source: branch = %d, want %d", got.Branch, BranchBelowMinVersion)
	}
	req.Source = SourceSystem
	if got := CheckCompat(req); !got.Compatible {
		t.Fatalf("system source must skip branch 10, got %s", got.Code)
	}

	// 系统回滚不跳过硬件判定：chip 不匹配照样拒绝（只校硬件、不校方向）。
	req = compatibleRequest()
	req.Source = SourceSystem
	req.Firmware.Chip = ChipS3
	if got := CheckCompat(req); got.Branch != BranchModelMismatch {
		t.Errorf("system source must still enforce hardware, got branch %d", got.Branch)
	}
}

// TestCheckCompatVersionDirectionEdges: 版本方向与 min_from 的边界语义。
func TestCheckCompatVersionDirectionEdges(t *testing.T) {
	// 更高的 patch / minor / major 都是升级。
	for _, version := range []string{"2.7.1", "2.8.0", "3.0.0"} {
		req := compatibleRequest()
		req.Firmware.Version = version
		if got := CheckCompat(req); !got.Compatible {
			t.Errorf("version %s over 2.7.0 must be an upgrade, got %s", version, got.Code)
		}
	}

	// 降级命中第 9 条。
	req := compatibleRequest()
	req.Firmware.Version = "2.6.9"
	if got := CheckCompat(req); got.Branch != BranchNotAnUpgrade {
		t.Errorf("downgrade: branch = %d, want %d", got.Branch, BranchNotAnUpgrade)
	}

	// min_from_version 等于当前版本是可升级的（不是「必须严格高于」）。
	req = compatibleRequest()
	req.Firmware.MinFromVersion = "2.7.0"
	if got := CheckCompat(req); !got.Compatible {
		t.Errorf("min_from == current must be allowed, got %s", got.Code)
	}

	// 节点从未上报版本（current=""）：没有「不是升级」的证据，允许（只能靠硬件判）。
	req = compatibleRequest()
	req.CurrentVersion = ""
	if got := CheckCompat(req); !got.Compatible {
		t.Errorf("empty current version must not be treated as not-an-upgrade, got %s", got.Code)
	}

	// 但 min_from_version 在 current 未知时仍拒绝：无法证明它 >= min。
	req = compatibleRequest()
	req.CurrentVersion = ""
	req.Firmware.MinFromVersion = "2.7.0"
	if got := CheckCompat(req); got.Branch != BranchBelowMinVersion {
		t.Errorf("unknown current below min: branch = %d, want %d", got.Branch, BranchBelowMinVersion)
	}
}

// TestCheckCompatHardwareFixCommand: 第 3 条的 data.fix 必须是可执行的 ehomectl 命令，
// 已识别字段填真值、未识别填占位符（不猜）。
func TestCheckCompatHardwareFixCommand(t *testing.T) {
	req := compatibleRequest()
	req.Node.FlashMB = 0
	req.Node.Psram = ""
	got := CheckCompat(req)
	if !strings.Contains(got.Fix, "ehomectl node hw set --id F0F5BD02F35C") {
		t.Errorf("fix must name the node, got %q", got.Fix)
	}
	if !strings.Contains(got.Fix, "--flash-mb 8|16") || !strings.Contains(got.Fix, "--psram yes|no") {
		t.Errorf("unknown fields must be placeholders, got %q", got.Fix)
	}

	// 只缺 psram：容量填真值。
	req = compatibleRequest()
	req.Node.Psram = ""
	got = CheckCompat(req)
	if !strings.Contains(got.Fix, "--flash-mb 8 --psram yes|no") {
		t.Errorf("known flash must be filled in, got %q", got.Fix)
	}

	// 只剩 platform 未识别：hw set 补不了，改用 backfill。
	req = compatibleRequest()
	req.Node.Platform = "ESP32"
	got = CheckCompat(req)
	if got.Fix != "ehomectl node hw backfill" {
		t.Errorf("platform-only unknown should fall back to backfill, got %q", got.Fix)
	}
}

// TestGateErrorDataJSONShape 钉住 phase 2 HTTP 层要照抄的 409 形状（方案 §4.3）。
func TestGateErrorDataJSONShape(t *testing.T) {
	req := compatibleRequest()
	req.Node.FlashMB = 0
	req.Node.Psram = ""
	gateErr := CheckCompat(req).GateError()
	if gateErr == nil {
		t.Fatal("expected a GateError")
	}

	encoded, err := json.Marshal(gateErr.Data())
	if err != nil {
		t.Fatalf("marshal data: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &payload); err != nil {
		t.Fatalf("unmarshal data: %v", err)
	}
	if got, want := sortedKeys(payload), []string{"firmware", "fix", "forceable", "node_hw", "reasons"}; !equalStrings(got, want) {
		t.Errorf("data keys = %v, want %v (json=%s)", got, want, encoded)
	}

	var reasons []string
	if err := json.Unmarshal(payload["reasons"], &reasons); err != nil {
		t.Fatalf("reasons: %v", err)
	}
	if !equalStrings(reasons, []string{GateHardwareUnknown}) {
		t.Errorf("reasons = %v, want [%s]", reasons, GateHardwareUnknown)
	}

	var forceable bool
	if err := json.Unmarshal(payload["forceable"], &forceable); err != nil {
		t.Fatalf("forceable: %v", err)
	}
	if forceable {
		t.Error("hardware gate must not be forceable")
	}

	var nodeHW map[string]json.RawMessage
	if err := json.Unmarshal(payload["node_hw"], &nodeHW); err != nil {
		t.Fatalf("node_hw: %v", err)
	}
	if got, want := sortedKeys(nodeHW), []string{"chip", "flash_mb", "psram"}; !equalStrings(got, want) {
		t.Errorf("node_hw keys = %v, want %v", got, want)
	}

	var firmware map[string]json.RawMessage
	if err := json.Unmarshal(payload["firmware"], &firmware); err != nil {
		t.Fatalf("firmware: %v", err)
	}
	wantFirmwareKeys := []string{"chip", "flash_mb", "model_key", "psram", "size_bytes", "version"}
	if got := sortedKeys(firmware); !equalStrings(got, wantFirmwareKeys) {
		t.Errorf("firmware keys = %v, want %v", got, wantFirmwareKeys)
	}

	// fix 必须出现在第 3 条的 data 里，且就是 nodeHardwareFix 的结果。
	var fix string
	if err := json.Unmarshal(payload["fix"], &fix); err != nil {
		t.Fatalf("fix: %v", err)
	}
	if !strings.Contains(fix, "ehomectl node hw set") {
		t.Errorf("fix = %q", fix)
	}

	if !strings.Contains(gateErr.Error(), GateHardwareUnknown) {
		t.Errorf("Error() = %q, want it to contain the machine code", gateErr.Error())
	}
}

// TestCompatResultDataMatchesGateError 防止两条路径（预览结果 / typed error）漂移。
func TestCompatResultDataMatchesGateError(t *testing.T) {
	req := compatibleRequest()
	req.Firmware.Chip = ChipS3
	result := CheckCompat(req)
	fromResult, err := json.Marshal(result.Data())
	if err != nil {
		t.Fatal(err)
	}
	fromError, err := json.Marshal(result.GateError().Data())
	if err != nil {
		t.Fatal(err)
	}
	if string(fromResult) != string(fromError) {
		t.Errorf("Data() mismatch:\n result=%s\n error =%s", fromResult, fromError)
	}
}

func TestCompareVersion(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.0", "1.0.0", 0},
		{"1.0.0", "1.0.1", -1},
		{"2.8.0", "2.7.9", 1},
		{"2.10.0", "2.9.9", 1},
		{"3", "2.9.9", 1},
		{"", "1.0.0", -1},
		{"", "", 0},
		{"2.8.0-beta", "2.8.0", 0}, // 非数字段按 0 处理（与既有实现一致）
	}
	for _, tc := range cases {
		if got := CompareVersion(tc.a, tc.b); got != tc.want {
			t.Errorf("CompareVersion(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
