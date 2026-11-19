// 闸门兼容判定：固件 × 节点硬件 × 版本方向（方案 §6.3 的 10 条分支）。
//
// 本文件是**纯函数 + 无 IO + 无 DB**：ota.Manager（建任务）与 ehomectl
// （`ota gate-check` 只读预检）都只调用 CheckCompat，因此「预检通过」与
// 「建任务通过」判定同源（v1.4 删除 gateMode 双模式的前提，方案 §6.3）。
//
// 判定顺序即方案 §6.3 的表序，**首个命中即返回**：先数据状态（撤回/身份/
// 硬件档案），再硬件匹配（芯片/容量/PSRAM/槽容量），最后版本方向。
// force 只对版本方向两条（9/10）生效；硬件类一律不可 force（方案 §2.4 R5：
// 放行未知等于按设计重开误刷通道）。
package firmwareid

import (
	"fmt"
	"strconv"
	"strings"
)

// 闸门机器码（方案附录 A「错误码总表」是唯一真源；这里与文档逐字对齐）。
//
// 它们是**机器码**而不是文案：前端置灰、e2e 与日志都按码断言，改文案不算破坏契约。
const (
	GateWithdrawn          = "OTA_GATE_WITHDRAWN"
	GateFirmwareUnverified = "OTA_GATE_FIRMWARE_UNVERIFIED"
	GateHardwareUnknown    = "OTA_GATE_HARDWARE_UNKNOWN"
	GateModelMismatch      = "OTA_GATE_MODEL_MISMATCH"
	GateFlashMismatch      = "OTA_GATE_FLASH_MISMATCH"
	GatePsramRequired      = "OTA_GATE_PSRAM_REQUIRED"
	GatePsramMismatch      = "OTA_GATE_PSRAM_MISMATCH"
	GateImageTooLarge      = "OTA_GATE_IMAGE_TOO_LARGE"
	GateNotAnUpgrade       = "OTA_GATE_NOT_AN_UPGRADE"
	GateBelowMinVersion    = "OTA_GATE_BELOW_MIN_VERSION"
)

// IdentityVerified 是「固件身份已验证」的取值，与 internal/dbschema 的
// IdentityVerified 同值。
//
// 为什么不在本包 import dbschema：dbschema 已经 import 本包（解析/BuildProfile），
// 反向引用会成 import cycle。字面量只在这一处重复，改值时必须两处同时改。
const IdentityVerified = "verified"

// 建任务来源（方案 §5.4）：system = 系统回滚，跳过闸门第 9/10 条版本方向判定。
const (
	SourceUser   = "user"
	SourceSystem = "system"
)

// 10 条分支的序号（与方案 §6.3 表一致；gate-check 与日志按它报告「命中第几条」）。
const (
	BranchNone = 0

	BranchWithdrawn          = 1
	BranchFirmwareUnverified = 2
	BranchHardwareUnknown    = 3
	BranchModelMismatch      = 4
	BranchFlashMismatch      = 5
	BranchPsramRequired      = 6
	BranchPsramMismatch      = 7
	BranchImageTooLarge      = 8
	BranchNotAnUpgrade       = 9
	BranchBelowMinVersion    = 10
)

// gateBranchMeta 是每条分支的固定属性。
//
// `data.reasons` 里放的是 OTA_GATE_* **机器码**（附录 A 的唯一真源），不是 §4.3
// 示例里的短名：前端 `OTA_GATE_REASON_TEXT` / `OTA_FORCEABLE_REASONS` 按机器码做
// 文案映射与 force 准入判定（`OTA_GATE_NOT_AN_UPGRADE` 等）；短名会让前端既译不出
// 文案、也无法识别「版本方向可 force」，等于把 force 入口永久关掉。
type gateBranchMeta struct {
	Code      string
	Forceable bool
}

var gateBranches = map[int]gateBranchMeta{
	BranchWithdrawn:          {Code: GateWithdrawn},
	BranchFirmwareUnverified: {Code: GateFirmwareUnverified},
	BranchHardwareUnknown:    {Code: GateHardwareUnknown},
	BranchModelMismatch:      {Code: GateModelMismatch},
	BranchFlashMismatch:      {Code: GateFlashMismatch},
	BranchPsramRequired:      {Code: GatePsramRequired},
	BranchPsramMismatch:      {Code: GatePsramMismatch},
	BranchImageTooLarge:      {Code: GateImageTooLarge},
	BranchNotAnUpgrade:       {Code: GateNotAnUpgrade, Forceable: true},
	BranchBelowMinVersion:    {Code: GateBelowMinVersion, Forceable: true},
}

// NodeHardware 是闸门看到的节点硬件档案（nodes.platform / hw_flash_mb / hw_psram）。
//
// JSON 形状即 409 的 data.node_hw（方案 §4.3）：{"chip","flash_mb","psram"}。
// NodeID 只用于拼 `data.fix` 里的更正命令，不出现在响应里。
type NodeHardware struct {
	NodeID   string `json:"-"`
	Platform string `json:"chip"`
	FlashMB  uint16 `json:"flash_mb"`
	Psram    string `json:"psram"`
}

// FirmwareFacts 是闸门看到的固件身份（firmwares 的身份列）。
//
// JSON 形状即 409 的 data.firmware。相对方案 §4.3 的示例补了 flash_mb / size_bytes：
// FLASH_MISMATCH 与 IMAGE_TOO_LARGE 只有把「哪个值超了」一起给出，拒绝才可解释。
// MinFromVersion / IdentityState / Withdrawn 是判定输入，不属于展示字段。
type FirmwareFacts struct {
	ID             uint   `json:"-"`
	ModelKey       string `json:"model_key"`
	Chip           string `json:"chip"`
	FlashMB        uint16 `json:"flash_mb"`
	Psram          string `json:"psram"`
	Version        string `json:"version"`
	SizeBytes      uint64 `json:"size_bytes"`
	MinFromVersion string `json:"-"`
	IdentityState  string `json:"-"`
	Withdrawn      bool   `json:"-"`
}

// CompatRequest 是闸门的输入。
type CompatRequest struct {
	Node     NodeHardware
	Firmware FirmwareFacts
	// CurrentVersion 是节点当前运行的固件版本（nodes.firmware_version），
	// 第 9/10 条版本方向判定只拿它作基准。空值表示从未上报（只能靠 force 放行）。
	CurrentVersion string
	// Source 是任务来源：""/"user" 人工；"system" 系统回滚（跳过第 9/10 条）。
	Source string
}

// CompatResult 是闸门的输出。
//
// 兼容时 Compatible=true、Branch=0；不兼容时 Code/Reasons/Message/Forceable/Fix
// 一起构成 409 需要的全部信息（映射见 (*GateError).Data）。
// `Reasons` 的元素是 OTA_GATE_* 机器码：409 的 data.reasons 与固件列表的
// Firmware.reasons 用同一份值，前端只维护一张「机器码 → 文案」表。
type CompatResult struct {
	Compatible bool          `json:"compatible"`
	Branch     int           `json:"branch"`
	Code       string        `json:"code,omitempty"`
	Reasons    []string      `json:"reasons,omitempty"`
	Message    string        `json:"message,omitempty"`
	Forceable  bool          `json:"forceable"`
	NodeHW     NodeHardware  `json:"node_hw"`
	Firmware   FirmwareFacts `json:"firmware"`
	Fix        string        `json:"fix"`
}

// CompatData 是 409 响应 envelope.data 的形状（方案 §4.3）。
//
// `reasons` 里是 OTA_GATE_* 机器码（与 Firmware.reasons 同源），前端的文案映射
// 与 force 准入都按它判定；`fix` 恒存在（无更正命令时为 ""）。
//
// phase 2 的 HTTP 层照这份形状序列化，**不要**把 CompatResult 整个塞进 data：
// compatible/branch/code/message 属于 envelope 的 code/error_code/message 三个位置。
type CompatData struct {
	Reasons   []string      `json:"reasons"`
	NodeHW    NodeHardware  `json:"node_hw"`
	Firmware  FirmwareFacts `json:"firmware"`
	Forceable bool          `json:"forceable"`
	Fix       string        `json:"fix"`
}

// CheckCompat 按方案 §6.3 的 10 条分支顺序判定，首个命中即返回。
//
// 判定只读输入，不写库、不记日志：调用方（ota.Manager / ehomectl）负责把结果
// 变成 409、审计或报告。
func CheckCompat(req CompatRequest) CompatResult {
	// 规范化只发生在比较处：响应里回显库中真值，排障时不会被「美化」误导。
	nodeChip := CanonicalChip(req.Node.Platform)
	nodePSRAM := normalizePSRAM(req.Node.Psram)
	fwChip := CanonicalChip(req.Firmware.Chip)
	fwPSRAM := normalizePSRAM(req.Firmware.Psram)

	result := CompatResult{
		Compatible: true,
		Branch:     BranchNone,
		NodeHW:     req.Node,
		Firmware:   firmwareSummary(req.Firmware),
	}
	reject := func(branch int, message string) CompatResult {
		meta := gateBranches[branch]
		result.Compatible = false
		result.Branch = branch
		result.Code = meta.Code
		result.Reasons = []string{meta.Code}
		result.Message = message
		result.Forceable = meta.Forceable
		return result
	}

	// 1. 已撤回：撤回状态由 withdrawn_at 表达（方案 §5.2），不可 force。
	if req.Firmware.Withdrawn {
		return reject(BranchWithdrawn, "firmware has been withdrawn")
	}

	// 2. 固件身份未验证：legacy/duplicate 不可选（方案 §2.4 R3）。
	//
	// 防御：identity_state=verified 但 psram 不是 yes/no 的「半可信行」也按本条拒绝。
	// 迁移曾产生过这种行（回填拿不到 psram 位，而 verified 只应来自 sidecar），
	// 放过去会让第 6/7 条在 psram="" 时静默不判，等于给误刷留一条通道。
	if req.Firmware.IdentityState != IdentityVerified {
		return reject(BranchFirmwareUnverified, fmt.Sprintf(
			"firmware identity is not verified (identity_state=%q); re-upload it with a .meta.json sidecar",
			req.Firmware.IdentityState))
	}
	if !validPSRAM(fwPSRAM) {
		return reject(BranchFirmwareUnverified, fmt.Sprintf(
			"firmware is marked verified but psram=%q; re-upload it with a .meta.json sidecar",
			req.Firmware.Psram))
	}

	// 3. 节点硬件档案不完整：platform 空 / 容量未识别 / psram 空。
	//
	// 容量判据用 SlotCapacity 而不是「== 0」：4MB 之类不在档位表里的值同样无法判定
	// 第 8 条槽容量，把它留在第 3 条（硬件未识别）比让它落进第 5/8 条更诚实。
	nodeCapacity, nodeCapacityOK := SlotCapacity(req.Node.FlashMB)
	if nodeChip == "" || !nodeCapacityOK || !validPSRAM(nodePSRAM) {
		incomplete := reject(BranchHardwareUnknown, "node hardware profile is incomplete")
		incomplete.Fix = nodeHardwareFix(req.Node)
		return incomplete
	}

	// 4. 芯片不匹配。
	if fwChip != nodeChip {
		return reject(BranchModelMismatch, fmt.Sprintf(
			"firmware chip %q does not match node platform %q", req.Firmware.Chip, req.Node.Platform))
	}

	// 5. 容量档位不匹配。
	if req.Firmware.FlashMB != req.Node.FlashMB {
		return reject(BranchFlashMismatch, fmt.Sprintf(
			"firmware is built for %dMB flash but the node has %dMB", req.Firmware.FlashMB, req.Node.FlashMB))
	}

	// 6. 固件要求 PSRAM，节点不是 yes。
	if fwPSRAM == PSRAMYes && nodePSRAM != PSRAMYes {
		return reject(BranchPsramRequired, "firmware requires PSRAM but the node does not have it")
	}

	// 7. 固件无 PSRAM，节点是 yes（反向：s3 镜像刷进 s3p 板）。
	if fwPSRAM == PSRAMNo && nodePSRAM == PSRAMYes {
		return reject(BranchPsramMismatch, "firmware is built without PSRAM but the node has PSRAM")
	}

	// 8. 镜像超过该容量档位的应用槽上限（与分区 CSV 同源，见 hardware.go）。
	if req.Firmware.SizeBytes > nodeCapacity {
		return reject(BranchImageTooLarge, fmt.Sprintf(
			"firmware image is %d bytes, larger than the %dMB slot capacity %d",
			req.Firmware.SizeBytes, req.Node.FlashMB, nodeCapacity))
	}

	// 9. 同版本或降级。系统回滚目标是更旧版本，跳过（方案 §6.3「系统回滚例外」）。
	if req.Source != SourceSystem {
		if CompareVersion(req.Firmware.Version, req.CurrentVersion) <= 0 {
			return reject(BranchNotAnUpgrade, fmt.Sprintf(
				"firmware version %q is not an upgrade over the running version %q",
				req.Firmware.Version, req.CurrentVersion))
		}
	}

	// 10. 低于最小可升级版本（同样对系统回滚跳过）。
	if req.Source != SourceSystem && req.Firmware.MinFromVersion != "" {
		if CompareVersion(req.CurrentVersion, req.Firmware.MinFromVersion) < 0 {
			return reject(BranchBelowMinVersion, fmt.Sprintf(
				"running version %q is below the minimum upgradable version %q",
				req.CurrentVersion, req.Firmware.MinFromVersion))
		}
	}

	return result
}

// CompareVersion 比较两个 Major.Minor.Patch 形式的版本串：
// 返回 -1（a<b）、0（a==b）、1（a>b）；非数字段按 0 处理。
//
// 实现从 internal/ota/ota.go 原样迁来（原处已无调用点）：闸门第 9/10 条需要它，
// 而 ota 已经 import 本包，反向引用会成环。全仓只保留这一份实现。
func CompareVersion(a, b string) int {
	aParts := strings.Split(a, ".")
	bParts := strings.Split(b, ".")
	for i := 0; i < 3; i++ {
		av, bv := 0, 0
		if i < len(aParts) {
			av, _ = strconv.Atoi(aParts[i])
		}
		if i < len(bParts) {
			bv, _ = strconv.Atoi(bParts[i])
		}
		if av < bv {
			return -1
		} else if av > bv {
			return 1
		}
	}
	return 0
}

// GateError 是闸门拒绝的 typed error。
//
// phase 2 的 HTTP 层这样映射（errors.As，**不要按字符串前缀匹配**）：
//
//	var gateErr *firmwareid.GateError
//	if errors.As(err, &gateErr) {
//	    ErrorWithCodeData(c, http.StatusConflict, gateErr.Code, gateErr.Message, gateErr.Data())
//	}
//
// 字段与 JSON 形状：
//
//	Code      → envelope.error_code（OTA_GATE_* 机器码）
//	Message   → envelope.message（人话）
//	Data()    → envelope.data，形状为 CompatData：
//	            {"reasons":["OTA_GATE_HARDWARE_UNKNOWN"],
//	             "node_hw":{"chip":"esp32s3","flash_mb":0,"psram":""},
//	             "firmware":{"model_key":"esp32c6-n8","chip":"esp32c6","flash_mb":8,
//	                         "psram":"no","version":"2.8.0","size_bytes":1024},
//	             "forceable":false,
//	             "fix":"ehomectl node hw set --id F0F5BD02F35C --flash-mb 8|16 --psram no"}
type GateError struct {
	Branch    int
	Code      string
	Message   string
	Reasons   []string
	NodeHW    NodeHardware
	Firmware  FirmwareFacts
	Forceable bool
	Fix       string
}

func (e *GateError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Data 返回 409 响应的 data 部分。
func (e *GateError) Data() CompatData {
	if e == nil {
		return CompatData{}
	}
	return CompatData{
		Reasons:   e.Reasons,
		NodeHW:    e.NodeHW,
		Firmware:  e.Firmware,
		Forceable: e.Forceable,
		Fix:       e.Fix,
	}
}

// Data 返回该结果对应的 409 data（与 (*GateError).Data 同一形状）。
//
// 只读预检 / 未来「兼容性列表页」用 CompatResult，请求路径用 GateError；
// 两者共用这一个映射，避免同一份拒绝在两条路径上说两套话。
func (r CompatResult) Data() CompatData {
	return CompatData{
		Reasons:   r.Reasons,
		NodeHW:    r.NodeHW,
		Firmware:  r.Firmware,
		Forceable: r.Forceable,
		Fix:       r.Fix,
	}
}

// GateError 把「不兼容」结果转成 typed error；兼容时返回 nil。
func (r CompatResult) GateError() *GateError {
	if r.Compatible {
		return nil
	}
	return &GateError{
		Branch:    r.Branch,
		Code:      r.Code,
		Message:   r.Message,
		Reasons:   r.Reasons,
		NodeHW:    r.NodeHW,
		Firmware:  r.Firmware,
		Forceable: r.Forceable,
		Fix:       r.Fix,
	}
}

// firmwareSummary 补齐 model_key：库里历史行可能为空，但 409 里必须能看出档位。
func firmwareSummary(fw FirmwareFacts) FirmwareFacts {
	if fw.ModelKey == "" {
		fw.ModelKey = ModelKey(fw.Chip, fw.FlashMB)
	}
	return fw
}

// normalizePSRAM 大小写/空白归一化（只影响比较，不回写库）。
func normalizePSRAM(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

// validPSRAM 只接受 yes/no（方案 §5.2：固件与节点都只有这两值加空）。
func validPSRAM(value string) bool {
	return value == PSRAMYes || value == PSRAMNo
}

// nodeHardwareFix 生成第 3 条（OTA_GATE_HARDWARE_UNKNOWN）的更正命令示例。
//
// 已识别的字段填真值，未识别的填占位符（8|16 / yes|no）而不是猜一个值：
// 这条命令会直接改节点档案，猜错等于把误刷的数据通道从另一个方向重新打开。
// 只有 platform 未识别时改用 backfill —— `node hw set` 没有 platform 参数，
// backfill 会从 model 重新解析（dbschema.BackfillNodeHardware）。
func nodeHardwareFix(node NodeHardware) string {
	id := strings.TrimSpace(node.NodeID)
	if id == "" {
		id = "<node_id>"
	}
	_, flashOK := SlotCapacity(node.FlashMB)
	psram := normalizePSRAM(node.Psram)
	psramOK := validPSRAM(psram)

	switch {
	case !flashOK && !psramOK:
		return fmt.Sprintf("ehomectl node hw set --id %s --flash-mb 8|16 --psram yes|no", id)
	case !flashOK:
		return fmt.Sprintf("ehomectl node hw set --id %s --flash-mb 8|16 --psram %s", id, psram)
	case !psramOK:
		return fmt.Sprintf("ehomectl node hw set --id %s --flash-mb %d --psram yes|no", id, node.FlashMB)
	default:
		// 只剩 platform 未识别：hw set 补不了，backfill 从 model 重新解析。
		return "ehomectl node hw backfill"
	}
}
