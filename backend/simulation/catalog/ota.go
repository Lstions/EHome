//go:build simulation

// 场景目录 · SIM-OTA 固件升级（设计 §9 SIM-OTA-001..003）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
//
// 本域三条场景守护的不变量：
//
//	OTA-001 固件包上传后"磁盘内容 / SHA256 / 元数据 / 列表"四者一致，元数据可登记修改；
//	OTA-002 创建升级任务后，目标节点必须在**下行真实帧**里收到含正确 URL/校验和/版本的 OtaCmd；
//	OTA-003 固件下载只认"配在本文件上的有效票据"，无票据/错票据一律拒绝。
package catalog

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-OTA-001",
		Title:  "上传固件包并登记版本后，可在固件列表里查到它的版本、大小与校验和",
		Domain: DomainOTA,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-OTA-001",
		Run:    otaRun001,
	})
	Register(Scenario{
		ID:     "SIM-OTA-002",
		Title:  "创建升级任务后，目标节点立即收到升级指令",
		Domain: DomainOTA,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-OTA-002",
		Run:    otaRun002,
	})
	Register(Scenario{
		ID:     "SIM-OTA-003",
		Title:  "固件下载地址必须带有效票据，没票据或票据不对都下载不了",
		Domain: DomainOTA,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-OTA-003",
		Run:    otaRun003,
	})
}

// ---------------------------------------------------------------------------
// SIM-OTA-001 上传固件包并登记版本后可在固件列表查询
// ---------------------------------------------------------------------------

// otaFirmwareEnvelope 只取断言需要的字段（models.Firmware 的 JSON 形状）。
type otaFirmwareEnvelope struct {
	ID        uint   `json:"id"`
	Version   string `json:"version"`
	Filename  string `json:"filename"`
	Checksum  string `json:"checksum"`
	SizeBytes uint64 `json:"size_bytes"`
	URL       string `json:"url"`
	Changelog string `json:"changelog"`
	Stable    bool   `json:"stable"`
}

func otaRun001(e *harness.Env) {
	t := e.T

	// 文件名带 RunID 命名空间：上传会真实落盘到服务进程 cwd/firmwares/，
	// 场景内的清理与遗留排查都依赖这个名字可辨识（§7 红线 4 之外的运维要求）。
	filename := e.NS("SIM-OTA-001", "fw") + ".bin"
	version := "9.9.1"
	body := otaFirmwareBytes(2048)

	created := otaUploadFirmware(e, filename, map[string]string{"version": version}, body).
		Expect(http.StatusCreated)
	var fw otaFirmwareEnvelope
	created.Decode(&fw)

	// 不变式 1：服务端登记的元数据必须与客户端上传的事实一致
	//（谁都能"返回 201"，但校验和/大小算错就会让设备刷成砖）。
	if fw.Version != version {
		t.Fatalf("登记版本 = %q，期望 %q（body=%s）", fw.Version, version, string(created.Raw))
	}
	if fw.Filename != filename {
		t.Fatalf("登记文件名 = %q，期望 %q", fw.Filename, filename)
	}
	wantSum := sha256.Sum256(body)
	wantChecksum := hex.EncodeToString(wantSum[:])
	if fw.Checksum != wantChecksum {
		t.Fatalf("登记校验和 = %q，期望 %q（SHA256 必须覆盖落盘的全部字节）", fw.Checksum, wantChecksum)
	}
	if fw.SizeBytes != uint64(len(body)) {
		t.Fatalf("登记大小 = %d，期望 %d", fw.SizeBytes, len(body))
	}
	if fw.URL == "" {
		t.Fatalf("登记后未生成下载地址: %s", string(created.Raw))
	}

	t.Cleanup(func() {
		rtCleanupExpect(t, "固件 "+filename,
			e.Admin.Delete("/api/v1/firmwares/"+strconv.FormatUint(uint64(fw.ID), 10)), http.StatusOK)
	})

	// 不变式 2：列表接口必须能查到它（用户刷新页面看得到）。
	found := false
	e.Eventually(10*time.Second, func() error {
		list := otaListFirmwares(t, e)
		for _, item := range list {
			if item.ID == fw.ID {
				if item.Checksum != wantChecksum || item.SizeBytes != uint64(len(body)) {
					return fmt.Errorf("列表里的固件 %d 元数据与上传结果不一致: %+v", fw.ID, item)
				}
				found = true
				return nil
			}
		}
		return fmt.Errorf("固件 %d（%s）尚未出现在 /api/v1/firmwares 列表（共 %d 条）", fw.ID, filename, len(list))
	})
	if !found {
		t.Fatalf("固件 %d 未出现在列表中", fw.ID)
	}

	// 不变式 3：版本登记信息可修改（PUT /api/v1/firmwares/:id），并真实持久化。
	changelog := e.NS("SIM-OTA-001", "changelog")
	updated := e.Admin.Put("/api/v1/firmwares/"+strconv.FormatUint(uint64(fw.ID), 10), map[string]any{
		"changelog": changelog,
		"stable":    true,
	}).Expect(http.StatusOK)
	var afterUpdate otaFirmwareEnvelope
	updated.Decode(&afterUpdate)
	if afterUpdate.Changelog != changelog {
		t.Fatalf("更新返回的 changelog = %q，期望 %q", afterUpdate.Changelog, changelog)
	}
	if !afterUpdate.Stable {
		t.Fatalf("更新后 stable 仍为 false（body=%s）", string(updated.Raw))
	}
	reloaded := false
	for _, item := range otaListFirmwares(t, e) {
		if item.ID != fw.ID {
			continue
		}
		reloaded = true
		if item.Changelog != changelog || !item.Stable {
			t.Fatalf("重新查询到的固件 %d 未持久化更新: %+v", fw.ID, item)
		}
	}
	if !reloaded {
		t.Fatalf("更新后固件 %d 从列表中消失", fw.ID)
	}

	e.Evidence("SIM-OTA-001.firmware", map[string]any{
		"id": fw.ID, "filename": filename, "version": version,
		"size_bytes": fw.SizeBytes, "checksum": fw.Checksum,
	})
}

// otaListFirmwares 读固件列表（GET /api/v1/firmwares，信封 data 为数组）。
func otaListFirmwares(t *testing.T, e *harness.Env) []otaFirmwareEnvelope {
	t.Helper()
	var list []otaFirmwareEnvelope
	e.Admin.Get("/api/v1/firmwares").Expect(http.StatusOK).Decode(&list)
	return list
}

// ---------------------------------------------------------------------------
// SIM-OTA-002 创建升级任务后目标节点收到升级指令
// ---------------------------------------------------------------------------

func otaRun002(e *harness.Env) {
	t := e.T

	version := "9.9.2"
	filename := e.NS("SIM-OTA-002", "fw") + ".bin"
	body := otaFirmwareBytes(1024)
	sum := sha256.Sum256(body)
	wantChecksum := hex.EncodeToString(sum[:])

	created := otaUploadFirmware(e, filename, map[string]string{"version": version}, body).
		Expect(http.StatusCreated)
	var fw otaFirmwareEnvelope
	created.Decode(&fw)
	t.Cleanup(func() {
		rtCleanupExpect(t, "固件 "+filename,
			e.Admin.Delete("/api/v1/firmwares/"+strconv.FormatUint(uint64(fw.ID), 10)), http.StatusOK)
	})

	// 目标节点：用 harness 共享夹具建"节点 + 通道 + 设备配置 + 边缘设备"，
	// 并让它真实连上 MQTT 订阅 nodes/<id>/down。
	// 真实实现里 SendOtaCommand 要按 node_id 反查 nodes 表，
	// 所以"节点在线且库里有记录"是这条链的前置条件，不是可选装饰。
	fx := rtProvisionFixture(e, "SIM-OTA-002", "target")
	dev := fx.Node

	// 先记下当前已收帧序号：OtaCmd 是 fire-and-forget 的 MQTT 发布，
	// AwaitFieldsAfter(startSeq) 只认此后的帧 —— 既不命中历史帧，
	// 也不存在"帧先到、监听后装"的竞态（帧在 MQTT 回调里就已被记录）。
	startSeq := dev.FrameSeq()

	task := e.Admin.Post("/api/v1/ota/tasks", map[string]any{
		"node_id":     dev.NodeID,
		"firmware_id": fw.ID,
	}).Expect(http.StatusCreated)
	otaID := task.DataString("ota_id")
	if otaID == "" {
		t.Fatalf("创建升级任务未返回 ota_id: %s", string(task.Raw))
	}
	if got := task.DataString("node_id"); got != dev.NodeID {
		t.Fatalf("任务目标节点 = %q，期望 %q", got, dev.NodeID)
	}
	if got := task.DataString("to_version"); got != version {
		t.Fatalf("任务目标版本 = %q，期望 %q", got, version)
	}
	taskDBID := task.DataInt("id")

	// 清理：取消任务，绝不留一个悬挂的 pending 任务给后续场景。
	// 断言的是"清理后任务处于终态"而不是"cancel 返回 200"：
	// 真实实现里 30s 无 ack 会重试 3 次后自行判 failed，那同样是终态，
	// 用状态做断言就不会因为这条异步路径把清理变成假失败。
	t.Cleanup(func() {
		e.Admin.Post("/api/v1/ota/tasks/"+strconv.FormatInt(taskDBID, 10)+"/cancel", map[string]any{})
		row := e.Admin.Get("/api/v1/ota/tasks/" + strconv.FormatInt(taskDBID, 10)).Expect(http.StatusOK)
		switch status := row.DataString("status"); status {
		case "failed", "success", "timeout":
		default:
			t.Errorf("清理后 OTA 任务 %s 仍处于非终态 %q", otaID, status)
		}
	})

	// ── 核心断言：目标节点在下行帧里真的收到了升级指令 ──
	// AwaitFieldsAfter 是设计 §5.3 冻结的轮询原语：按期限轮询，超时会把
	// 期间收到的全部帧类型列出来（便于一眼看出"根本没下发"还是"发错了类型"）。
	fields, err := dev.AwaitFieldsAfter(frame.MsgOtaCmd, startSeq, 30*time.Second)
	if err != nil {
		t.Fatalf("目标节点未在下行通道收到 OtaCmd（0x0A）: %v", err)
	}

	// OtaCmd 字段号见 backend/pkg/frame/frame.go（1..6），
	// 编码侧为 ota.Manager.buildOtaCmdPayload —— 两边必须对齐，否则设备拿到的是空指令。
	if got := otaFieldString(fields, 1); got != otaID {
		t.Fatalf("OtaCmd.ota_id = %q，期望 %q", got, otaID)
	}
	if got := otaFieldString(fields, 2); got == "" {
		t.Fatalf("OtaCmd.url 为空 —— 设备拿到指令也无法下载固件")
	} else if !bytes.Contains([]byte(got), []byte("/api/v1/firmwares/"+filename+"/download")) {
		t.Fatalf("OtaCmd.url = %q，未指向固件 %s 的下载路径", got, filename)
	}
	if got := otaFieldString(fields, 3); got != wantChecksum {
		t.Fatalf("OtaCmd.checksum = %q，期望 %q", got, wantChecksum)
	}
	if got := otaFieldUint(fields, 4); got != uint64(len(body)) {
		t.Fatalf("OtaCmd.size = %d，期望 %d", got, len(body))
	}
	if got := otaFieldString(fields, 5); got != version {
		t.Fatalf("OtaCmd.version = %q，期望 %q", got, version)
	}

	// 任务在列表与详情里可查（用户刷新页面看得到"正在升级"）。
	var taskRow struct {
		OtaID     string `json:"ota_id"`
		NodeID    string `json:"node_id"`
		Status    string `json:"status"`
		ToVersion string `json:"to_version"`
	}
	e.Admin.Get("/api/v1/ota/tasks/" + strconv.FormatInt(taskDBID, 10)).
		Expect(http.StatusOK).Decode(&taskRow)
	if taskRow.OtaID != otaID || taskRow.NodeID != dev.NodeID || taskRow.ToVersion != version {
		t.Fatalf("任务详情不一致: %+v", taskRow)
	}

	// 节点维度的 OTA 状态查询可用（前端 OTA 面板的数据源）。
	status := e.Admin.Get("/api/v1/ota/status/" + dev.NodeID).Expect(http.StatusOK)
	if got := status.DataString("node_id"); got != dev.NodeID {
		t.Fatalf("OTA 状态里的 node_id = %q，期望 %q", got, dev.NodeID)
	}
	// DataInt 在路径缺失时直接失败（harness 的点路径取值不静默返回零值），
	// 因此这一行同时断言了"字段存在"与"可解析"。
	status.DataInt("fail_count_24h")

	e.Evidence("SIM-OTA-002.ota_cmd", map[string]any{
		"ota_id": otaID, "node_id": dev.NodeID, "to_version": version,
		"url": otaFieldString(fields, 2), "checksum": otaFieldString(fields, 3), "size": otaFieldUint(fields, 4),
	})
}

// ---------------------------------------------------------------------------
// SIM-OTA-003 固件下载地址需带有效票据，无票据/错票据被拒绝
// ---------------------------------------------------------------------------

func otaRun003(e *harness.Env) {
	t := e.T

	version := "9.9.3"
	filenameA := e.NS("SIM-OTA-003", "fw-a") + ".bin"
	filenameB := e.NS("SIM-OTA-003", "fw-b") + ".bin"
	bodyA := otaFirmwareBytes(512)
	bodyB := otaFirmwareBytes(768)

	fwA := otaUploadFirmware(e, filenameA, map[string]string{"version": version}, bodyA).Expect(http.StatusCreated)
	fwB := otaUploadFirmware(e, filenameB, map[string]string{"version": version + "-b"}, bodyB).Expect(http.StatusCreated)
	var fwAEnvelope, fwBEnvelope otaFirmwareEnvelope
	fwA.Decode(&fwAEnvelope)
	fwB.Decode(&fwBEnvelope)
	t.Cleanup(func() {
		rtCleanupExpect(t, "固件 "+filenameA,
			e.Admin.Delete("/api/v1/firmwares/"+strconv.FormatUint(uint64(fwAEnvelope.ID), 10)), http.StatusOK)
		rtCleanupExpect(t, "固件 "+filenameB,
			e.Admin.Delete("/api/v1/firmwares/"+strconv.FormatUint(uint64(fwBEnvelope.ID), 10)), http.StatusOK)
	})

	// 上传接口返回的下载地址就是设备将来取固件用的地址，从它里面取真实票据。
	ticket, err := url.Parse(fwAEnvelope.URL)
	if err != nil {
		t.Fatalf("下载地址不可解析: %q (%v)", fwAEnvelope.URL, err)
	}
	if ticket.Query().Get("signature") == "" || ticket.Query().Get("expires") == "" {
		t.Fatalf("下载地址未携带票据参数: %q", fwAEnvelope.URL)
	}

	// DownloadURL(url, false) = 摘掉令牌后再请求，模拟"设备侧没有 JWT"的真实情形；
	// 直接用上传接口返回的绝对地址，顺带证明交给设备的那个 URL 原样可用。
	//
	// 正例：仅凭票据即可取到与上传完全一致的字节。
	downloaded := e.Admin.DownloadURL(fwAEnvelope.URL, false).Expect(http.StatusOK)
	if !bytes.Equal(downloaded.Raw, bodyA) {
		t.Fatalf("下载内容与上传不一致: 得到 %d 字节，期望 %d 字节", len(downloaded.Raw), len(bodyA))
	}

	// 反例：票据缺失/不完整/被篡改/指向别的文件/超出有效期上限，一律拒绝。
	// 注意"票据过期"这一支无法在场景里构造（签名需要服务端密钥），
	// 由 internal/api 的票据单测覆盖，此处只覆盖可构造的四种错票据。
	expires := ticket.Query().Get("expires")
	signature := ticket.Query().Get("signature")
	expiresUnix, err := strconv.ParseInt(expires, 10, 64)
	if err != nil {
		t.Fatalf("票据 expires 不是合法时间戳: %q (%v)", expires, err)
	}
	tampered := otaFlipLastChar(signature)

	badCases := []struct {
		name string
		uri  string
	}{
		{"完全不带票据", ticket.Path},
		{"只有有效期没有签名", ticket.Path + "?expires=" + expires},
		{"只有签名没有有效期", ticket.Path + "?signature=" + url.QueryEscape(signature)},
		{"签名被篡改", ticket.Path + "?expires=" + expires + "&signature=" + url.QueryEscape(tampered)},
		{"有效期超出 24 小时上限", ticket.Path + "?expires=" + strconv.FormatInt(expiresUnix+25*3600, 10) + "&signature=" + url.QueryEscape(signature)},
		{"票据指向别的固件文件", "/api/v1/firmwares/" + filenameB + "/download?expires=" + expires + "&signature=" + url.QueryEscape(signature)},
	}
	for _, tc := range badCases {
		e.Admin.DownloadURL(tc.uri, false).Expect(http.StatusUnauthorized)
	}

	// 交叉验证："别的文件的票据"确实能下载它自己 —— 证明上面的 401 来自票据绑定，
	// 而不是因为第二个固件压根不存在。
	mine := e.Admin.DownloadURL(fwBEnvelope.URL, false).Expect(http.StatusOK)
	if !bytes.Equal(mine.Raw, bodyB) {
		t.Fatalf("第二个固件用自家票据下载到的内容不对: %d 字节，期望 %d 字节", len(mine.Raw), len(bodyB))
	}

	e.Evidence("SIM-OTA-003.ticket", map[string]any{
		"download_url": fwAEnvelope.URL,
		"expires":      expires,
		"bad_cases":    len(badCases),
	})
}

// otaFlipLastChar 篡改签名最后一个字符（保持长度），构造"签名不对"的票据。
func otaFlipLastChar(s string) string {
	if s == "" {
		return "x"
	}
	last := s[len(s)-1]
	if last == 'A' {
		return s[:len(s)-1] + "B"
	}
	return s[:len(s)-1] + "A"
}

// otaFirmwareBytes 生成确定性的固件包内容（字节可复现，校验和断言才有意义）。
func otaFirmwareBytes(n int) []byte {
	out := make([]byte, n)
	for i := range out {
		out[i] = byte(i % 251)
	}
	return out
}

// ---------------------------------------------------------------------------
// 下行帧字段取值（SIM-OTA-002 使用）
// ---------------------------------------------------------------------------

// otaFieldString / otaFieldUint 从 AwaitFieldsAfter 返回的字段表里取值。
// 需要变量中转是因为 map 的值不可寻址，而 frame.GetString/GetUint64 收 *frame.Field。
func otaFieldString(fields map[uint8]frame.Field, num uint8) string {
	field := fields[num]
	return frame.GetString(&field)
}

func otaFieldUint(fields map[uint8]frame.Field, num uint8) uint64 {
	field := fields[num]
	return frame.GetUint64(&field)
}

// ---------------------------------------------------------------------------
// harness 适配点
// ---------------------------------------------------------------------------

// otaUploadFirmware 走 harness 的 multipart 助手（harness/upload.go）。
//
// 设计 §5.2 的 Session.Post 只覆盖 JSON body，而固件上传是 multipart/form-data，
// 文件字段名固定为 "file"（生产端 c.FormFile("file")）。
func otaUploadFirmware(e *harness.Env, filename string, fields map[string]string, content []byte) *harness.Response {
	return e.Admin.UploadMultipart("/api/v1/firmwares/upload", fields,
		[]harness.UploadFile{{Field: "file", Filename: filename, Content: content}})
}
