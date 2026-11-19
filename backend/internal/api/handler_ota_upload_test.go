package api

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
)

// ── 上传夹具（M0：镜像自证 + sidecar 必填）──
//
// 这几份夹具同时服务三类用例：
//  1. 新的上传契约（AC-1..AC-6）；
//  2. 既有的删除/信封契约测试——它们原先上传的是任意字节，
//     在新契约下必须改喂一份**合法镜像 + sidecar**；
//  3. 迁移回填（dbschema 侧自带一份同形状的构造器，避免测试包互相 import）。
//
// 偏移表与 internal/firmwareid 必须一致；两处漂移会让「夹具通过、真固件被拒」。
func firmwareTestImage(chipID uint16, spiSize uint8, version, elfSeed string) []byte {
	if elfSeed == "" {
		elfSeed = version
	}
	buf := make([]byte, 2048)
	buf[0x00] = 0xE9
	buf[0x03] = spiSize << 4
	binary.LittleEndian.PutUint16(buf[0x0C:], chipID)
	binary.LittleEndian.PutUint32(buf[0x20:], 0xABCD5432)
	copy(buf[0x30:0x50], version)
	copy(buf[0x50:0x70], "ehome_collector")
	copy(buf[0x70:0x80], "07:37:54")
	copy(buf[0x80:0x90], "Oct  5 2026")
	copy(buf[0x90:0xB0], "v6.1")
	sum := sha256.Sum256([]byte(elfSeed))
	copy(buf[0xB0:0xD0], sum[:])
	return buf
}

// firmwareTestSidecar 生成与镜像自洽的 .meta.json 内容。
func firmwareTestSidecar(t *testing.T, image []byte, chip string, flashMB uint16, psram bool, version, profile string) []byte {
	t.Helper()
	sum := sha256.Sum256(image)
	payload := map[string]interface{}{
		"profile":  profile,
		"chip":     chip,
		"flash_mb": flashMB,
		"psram":    psram,
		"version":  version,
		"idf_ver":  "v6.1",
		"sha256":   hex.EncodeToString(sum[:]),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode sidecar: %v", err)
	}
	return raw
}

// firmwareUploadMultipart 组一个上传请求体；sidecar 为 nil 表示「故意不带 sidecar」。
func firmwareUploadMultipart(t *testing.T, baseName string, image, sidecar []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", baseName)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(image); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if sidecar != nil {
		metaName := strings.TrimSuffix(baseName, filepath.Ext(baseName)) + ".meta.json"
		metaPart, err := writer.CreateFormFile("meta", metaName)
		if err != nil {
			t.Fatalf("create meta part: %v", err)
		}
		if _, err := metaPart.Write(sidecar); err != nil {
			t.Fatalf("write meta part: %v", err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

// firmwareUploadRequest 组一个已鉴权的上传请求。
func firmwareUploadWithSidecar(t *testing.T, baseName string, image, sidecar []byte) *http.Request {
	t.Helper()
	body, contentType := firmwareUploadMultipart(t, baseName, image, sidecar)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", authHeader(t))
	return req
}

// assertNoFirmwareOrphans 断言正式目录与临时目录都没有残留（AC-6）。
func assertNoFirmwareOrphans(t *testing.T) {
	t.Helper()
	for _, dir := range []string{firmwareDir, filepath.Join(firmwareDir, firmwareTempDirName)} {
		entries, err := os.ReadDir(dir)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			t.Fatalf("read dir %s: %v", dir, err)
		}
		for _, entry := range entries {
			if dir == firmwareDir && entry.Name() == firmwareTempDirName {
				continue
			}
			t.Fatalf("orphan left behind after a rejected upload: %s", filepath.Join(dir, entry.Name()))
		}
	}
}

// assertNoTempLeftovers 断言临时目录已清空（成功上传后也必须如此：文件被 rename 走了）。
func assertNoTempLeftovers(t *testing.T) {
	t.Helper()
	dir := filepath.Join(firmwareDir, firmwareTempDirName)
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return
	}
	if err != nil {
		t.Fatalf("read temp dir %s: %v", dir, err)
	}
	for _, entry := range entries {
		t.Fatalf("temp file left behind after upload: %s", filepath.Join(dir, entry.Name()))
	}
}

func decodeEnvelopeErrorCode(t *testing.T, raw string) (int, string) {
	t.Helper()
	var env struct {
		Code      int    `json:"code"`
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	if err := json.Unmarshal([]byte(raw), &env); err != nil {
		t.Fatalf("parse error envelope %q: %v", raw, err)
	}
	return env.Code, env.ErrorCode
}

// TestFirmwareUpload_PersistsImageIdentity 覆盖 AC-1：身份字段与镜像一致，
// 版本取自镜像（表单不再收 version），落盘名带 elf 前缀。
func TestFirmwareUpload_PersistsImageIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	image := firmwareTestImage(0x000D, 4, "2.8.0", "c6-n16")
	sidecar := firmwareTestSidecar(t, image, "esp32c6", 16, false, "2.8.0", "c6-n16")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "ehome_collector.bin", image, sidecar))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var fw models.Firmware
	if err := db.First(&fw, "filename = ?", "ehome_collector.bin").Error; err != nil {
		t.Fatalf("uploaded firmware row not found: %v", err)
	}
	if fw.Version != "2.8.0" {
		t.Fatalf("version = %q, want image version %q (the form no longer carries one)", fw.Version, "2.8.0")
	}
	if fw.Chip != "esp32c6" || fw.FlashMB != 16 || fw.ModelKey != "esp32c6-n16" {
		t.Fatalf("identity = chip %q flash %d model_key %q", fw.Chip, fw.FlashMB, fw.ModelKey)
	}
	if fw.Psram != "no" {
		t.Fatalf("psram = %q, want %q (sidecar is the only source)", fw.Psram, "no")
	}
	if fw.BuildProfile != "c6-n16" {
		t.Fatalf("build_profile = %q, want %q", fw.BuildProfile, "c6-n16")
	}
	if fw.IdentityState != "verified" {
		t.Fatalf("identity_state = %q, want verified", fw.IdentityState)
	}
	if fw.Channel != models.FirmwareChannelStable {
		t.Fatalf("channel = %q, want %q", fw.Channel, models.FirmwareChannelStable)
	}
	if fw.BuiltAtRaw != "Oct  5 2026 07:37:54" {
		t.Fatalf("built_at_raw = %q", fw.BuiltAtRaw)
	}
	if len(fw.ElfSHA256) != 64 {
		t.Fatalf("elf_sha256 = %q, want 64 hex chars", fw.ElfSHA256)
	}
	sum := sha256.Sum256(image)
	if fw.Checksum != hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum = %q, want sha256 of the uploaded bytes", fw.Checksum)
	}

	// 落盘名 = elf 前缀 + 原名，URL 指向真实文件名（AC-6：同名重传不得覆盖）。
	wantStored := "elf" + fw.ElfSHA256[:16] + "_ehome_collector.bin"
	if got := filepath.Base(fw.StoragePath); got != wantStored {
		t.Fatalf("storage name = %q, want %q", got, wantStored)
	}
	if _, err := os.Stat(fw.StoragePath); err != nil {
		t.Fatalf("uploaded binary missing at %s: %v", fw.StoragePath, err)
	}
	if !strings.Contains(fw.URL, "/api/v1/firmwares/"+wantStored+"/download?expires=") {
		t.Fatalf("download URL %q does not point at the stored file %q", fw.URL, wantStored)
	}
	assertNoTempLeftovers(t)
}

// TestFirmwareUpload_RequiresSidecar 覆盖 AC-2：缺 sidecar 400 FW_META_REQUIRED，
// 补齐后重传成功且 psram 来自 sidecar。
func TestFirmwareUpload_RequiresSidecar(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	image := firmwareTestImage(0x0009, 4, "2.8.0", "s3p-n16")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "fw.bin", image, nil))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("upload without sidecar: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_META_REQUIRED" {
		t.Fatalf("error_code = %q, want FW_META_REQUIRED", code)
	}
	var count int64
	if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
		t.Fatalf("count firmwares: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d firmware row(s) written by a rejected upload", count)
	}
	assertNoFirmwareOrphans(t)

	sidecar := firmwareTestSidecar(t, image, "esp32s3", 16, true, "2.8.0", "s3p-n16")
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "fw.bin", image, sidecar))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload with sidecar: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var fw models.Firmware
	if err := db.First(&fw, "filename = ?", "fw.bin").Error; err != nil {
		t.Fatalf("uploaded firmware row not found: %v", err)
	}
	if fw.Psram != "yes" || fw.IdentityState != "verified" || fw.BuildProfile != "s3p-n16" {
		t.Fatalf("fw = psram %q state %q profile %q, want yes/verified/s3p-n16", fw.Psram, fw.IdentityState, fw.BuildProfile)
	}
}

// TestFirmwareUpload_SidecarConflict 覆盖 AC-3：chip / flash_mb / version / sha256
// 任一与镜像或内容不一致 → 400 FW_META_CONFLICT，且不落盘。
func TestFirmwareUpload_SidecarConflict(t *testing.T) {
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")

	image := firmwareTestImage(0x000D, 3, "2.8.0", "c6-n8")
	sum := sha256.Sum256(image)
	good := firmwareTestSidecar(t, image, "esp32c6", 8, false, "2.8.0", "c6-n8")
	badSHA := bytes.Replace(good, []byte(hex.EncodeToString(sum[:])[:8]), []byte("00000000"), 1)

	cases := []struct {
		name    string
		sidecar []byte
	}{
		{"chip", firmwareTestSidecar(t, image, "esp32s3", 8, false, "2.8.0", "c6-n8")},
		{"flash_mb", firmwareTestSidecar(t, image, "esp32c6", 16, false, "2.8.0", "c6-n8")},
		{"version", firmwareTestSidecar(t, image, "esp32c6", 8, false, "2.9.0", "c6-n8")},
		{"sha256", badSHA},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			r, db, _ := setupOTATest(t)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithSidecar(t, "fw.bin", image, tc.sidecar))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_META_CONFLICT" {
				t.Fatalf("error_code = %q, want FW_META_CONFLICT", code)
			}
			var count int64
			if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
				t.Fatalf("count firmwares: %v", err)
			}
			if count != 0 {
				t.Fatalf("%d firmware row(s) written by a rejected upload", count)
			}
			assertNoFirmwareOrphans(t)
		})
	}
}

// TestFirmwareUpload_RejectsDuplicateElf 覆盖 AC-4：同一份 .bin 二次上传 409。
func TestFirmwareUpload_RejectsDuplicateElf(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	image := firmwareTestImage(0x000D, 3, "2.8.0", "dup-elf")
	sidecar := firmwareTestSidecar(t, image, "esp32c6", 8, false, "2.8.0", "c6-n8")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "first.bin", image, sidecar))
	if w.Code != http.StatusCreated {
		t.Fatalf("first upload: expected 201, got %d: %s", w.Code, w.Body.String())
	}
	// 同一份内容、不同文件名：唯一键是 elf_sha256，不是文件名。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "second.bin", image, sidecar))
	if w.Code != http.StatusConflict {
		t.Fatalf("second upload: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_DUPLICATE" {
		t.Fatalf("error_code = %q, want FW_DUPLICATE", code)
	}

	var count int64
	if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
		t.Fatalf("count firmwares: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d firmware rows, want exactly 1", count)
	}
	entries, err := os.ReadDir(firmwareDir)
	if err != nil {
		t.Fatalf("read firmware dir: %v", err)
	}
	stored := 0
	for _, entry := range entries {
		if entry.Name() != firmwareTempDirName {
			stored++
		}
	}
	if stored != 1 {
		t.Fatalf("%d binary(ies) on disk, want exactly 1 (a rejected duplicate must not leave a second file)", stored)
	}
	assertNoTempLeftovers(t)
}

// TestFirmwareUpload_RejectsBadImages 覆盖 AC-5：每条拒绝带精确机器码。
func TestFirmwareUpload_RejectsBadImages(t *testing.T) {
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")

	valid := firmwareTestImage(0x000D, 3, "2.8.0", "bad-image")

	cases := []struct {
		name  string
		image []byte
		code  string
	}{
		{
			name: "not an image",
			image: func() []byte {
				cp := append([]byte(nil), valid...)
				cp[0x00] = 0x00
				return cp
			}(),
			code: "FW_NOT_IMAGE",
		},
		{
			name:  "truncated",
			image: valid[:0x40],
			code:  "FW_TRUNCATED",
		},
		{
			name: "wrong project",
			image: func() []byte {
				cp := append([]byte(nil), valid...)
				copy(cp[0x50:0x70], "some_other_project")
				return cp
			}(),
			code: "FW_PROJECT_MISMATCH",
		},
		{
			name: "unsupported chip",
			image: func() []byte {
				cp := append([]byte(nil), valid...)
				binary.LittleEndian.PutUint16(cp[0x0C:], 0xFFFF)
				return cp
			}(),
			code: "FW_CHIP_UNSUPPORTED",
		},
		{
			name: "unsupported flash",
			image: func() []byte {
				cp := append([]byte(nil), valid...)
				cp[0x03] = 2 << 4 // 4MB
				return cp
			}(),
			code: "FW_FLASH_UNSUPPORTED",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			r, db, _ := setupOTATest(t)
			// sidecar 用「镜像自己的值」：这样拒绝只能来自镜像解析，不是交叉校验。
			sidecar := firmwareTestSidecar(t, tc.image, "esp32c6", 8, false, "2.8.0", "c6-n8")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithSidecar(t, "fw.bin", tc.image, sidecar))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != tc.code {
				t.Fatalf("error_code = %q, want %q (%s)", code, tc.code, truncateBody(w.Body.String()))
			}
			var count int64
			if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
				t.Fatalf("count firmwares: %v", err)
			}
			if count != 0 {
				t.Fatalf("%d firmware row(s) written by a rejected upload", count)
			}
			assertNoFirmwareOrphans(t)
		})
	}
}

// TestFirmwareUpload_RejectsImageBeyondSlotCapacity 覆盖 UT-UP-03：8MB 档的镜像
// 超过 0x380000 就必须 413，而不是等设备刷成砖。
func TestFirmwareUpload_RejectsImageBeyondSlotCapacity(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	capacity, ok := slotCapacityForTest(8)
	if !ok {
		t.Fatal("8MB slot capacity is not defined")
	}
	image := firmwareTestImage(0x000D, 3, "2.8.0", "too-large")
	image = append(image, make([]byte, capacity)...) // 略超过槽上限
	sidecar := firmwareTestSidecar(t, image, "esp32c6", 8, false, "2.8.0", "c6-n8")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "big.bin", image, sidecar))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", w.Code, truncateBody(w.Body.String()))
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_TOO_LARGE" {
		t.Fatalf("error_code = %q, want FW_TOO_LARGE", code)
	}
	var count int64
	if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
		t.Fatalf("count firmwares: %v", err)
	}
	if count != 0 {
		t.Fatalf("%d firmware row(s) written by a rejected upload", count)
	}
	assertNoFirmwareOrphans(t)
}

// TestFirmwareList_RefreshesExpiredDownloadURL 覆盖 AC-11 的服务端一半：
// 存量行里存着过期的 URL，列表必须返回一条**新的**票据（而不是原样返回）。
// 设备侧那一半由 ota 发送时签票保证（firmwareticket TestURLUsesSigningMoment）。
func TestFirmwareList_RefreshesExpiredDownloadURL(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	expiredAt := time.Now().Add(-time.Hour).Unix()
	expired := fmt.Sprintf("http://firmware-upload-test.local/api/v1/firmwares/elf_x_fw.bin/download?expires=%d&signature=stale", expiredAt)
	row := models.Firmware{
		Version: "2.8.0", Checksum: "c", URL: expired, SizeBytes: 2048,
		Filename: "fw.bin", StoragePath: filepath.Join(firmwareDir, "elf_x_fw.bin"),
		IdentityState: "legacy",
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/firmwares", nil)
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("list: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data []models.Firmware `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse list: %v", err)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("list returned %d rows, want 1", len(resp.Data))
	}
	refreshed := resp.Data[0].URL
	if refreshed == expired {
		t.Fatal("list returned the stored (expired) URL unchanged")
	}
	if !strings.Contains(refreshed, "expires=") || !strings.Contains(refreshed, "signature=") {
		t.Fatalf("refreshed URL carries no ticket: %q", refreshed)
	}
	// 存库值不得被列表顺手改写（列表是只读语义）。
	var stored models.Firmware
	if err := db.First(&stored, row.ID).Error; err != nil {
		t.Fatalf("reload firmware: %v", err)
	}
	if stored.URL != expired {
		t.Fatalf("list mutated the stored URL: %q", stored.URL)
	}
}

// slotCapacityForTest 取槽容量常量（与上传/闸门共用同一张表）。
func slotCapacityForTest(flashMB uint16) (int, bool) {
	capacity, ok := firmwareid.SlotCapacity(flashMB)
	return int(capacity), ok
}

func truncateBody(body string) string {
	if len(body) > 512 {
		return body[:512] + "..."
	}
	return body
}

// TestFirmwareUpload_CompletesLegacyIdentity 是 R3 的闭环用例（be-firmware-psram）：
//
// 迁移把回填行一律标 legacy（镜像头没有 psram 位 ⇒ 不可选），而 elf_sha256 已写、
// 唯一索引已建 —— 如果没有「legacy 就地补齐」这条路，补 sidecar 重传必然 409，
// legacy 就永远补不回来。本用例钉住三件事：
//  1. 补齐成功：状态变 verified、psram/profile/version/idf_ver 以本次镜像+sidecar 为准；
//  2. 仍是**同一行**（id 不变、不新增行），旧二进制被清掉；
//  3. 补齐之后（verified）或 duplicate 行再传同一份 → 仍然 409。
func TestFirmwareUpload_CompletesLegacyIdentity(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	// elf_sha256 来自镜像内的 app_elf_sha256 字段（夹具用 sha256(elfSeed) 填充）。
	const elfSeed = "legacy-complete"
	image := firmwareTestImage(0x000D, 4, "2.8.0", elfSeed)
	elfSum := sha256.Sum256([]byte(elfSeed))
	elf := hex.EncodeToString(elfSum[:])
	sidecar := firmwareTestSidecar(t, image, "esp32c6", 16, true, "2.8.0", "c6p-n16")
	imageSum := sha256.Sum256(image)

	// 迁移形态的行：身份列已回填，psram 空，二进制在迁移前的旧名字下。
	if err := os.MkdirAll(firmwareDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", firmwareDir, err)
	}
	oldPath := filepath.Join(firmwareDir, "old_legacy.bin")
	if err := os.WriteFile(oldPath, image, 0o644); err != nil {
		t.Fatalf("seed legacy binary: %v", err)
	}
	legacy := models.Firmware{
		Version: "1.0.0", Checksum: "stale", SizeBytes: 1, URL: "http://legacy.invalid/old",
		Filename: "old_legacy.bin", StoragePath: oldPath,
		Chip: "esp32c6", FlashMB: 16, ModelKey: "esp32c6-n16", ElfSHA256: elf,
		IdentityState: "legacy",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("seed legacy firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "ehome_collector.bin", image, sidecar))
	if w.Code != http.StatusOK {
		t.Fatalf("legacy completion: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			models.Firmware
			CompletedLegacy bool `json:"completed_legacy"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse completion response: %v", err)
	}
	if resp.Code != http.StatusOK || !resp.Data.CompletedLegacy {
		t.Fatalf("response = code %d completed_legacy %v, want 200/true (%s)", resp.Code, resp.Data.CompletedLegacy, w.Body.String())
	}

	// 2) 仍是同一行、且只有一行。
	var rows []models.Firmware
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("reload firmwares: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("%d firmware rows after legacy completion, want exactly 1 (in-place, no new row)", len(rows))
	}
	got := rows[0]
	if got.ID != legacy.ID {
		t.Fatalf("firmware id changed %d -> %d; completion must keep the original row", legacy.ID, got.ID)
	}

	// 1) 身份以本次镜像 + sidecar 为准。
	if got.IdentityState != "verified" {
		t.Fatalf("identity_state = %q, want verified after completion", got.IdentityState)
	}
	if got.Psram != "yes" || got.BuildProfile != "c6p-n16" {
		t.Fatalf("psram/build_profile = %q/%q, want yes/c6p-n16", got.Psram, got.BuildProfile)
	}
	if got.Version != "2.8.0" || got.IDFVer != "v6.1" {
		t.Fatalf("version/idf_ver = %q/%q, want 2.8.0/v6.1 (image + sidecar are authoritative)", got.Version, got.IDFVer)
	}
	if got.Checksum != hex.EncodeToString(imageSum[:]) {
		t.Fatalf("checksum = %q, want the uploaded content sha256", got.Checksum)
	}
	if got.Chip != "esp32c6" || got.FlashMB != 16 || got.ModelKey != "esp32c6-n16" {
		t.Fatalf("identity = chip %q flash %d model_key %q", got.Chip, got.FlashMB, got.ModelKey)
	}
	if got.ElfSHA256 != elf {
		t.Fatalf("elf_sha256 = %q, want %q (unchanged)", got.ElfSHA256, elf)
	}

	// 落盘名换成新的 elf 唯一名，旧二进制被清掉（该行已不再引用它）。
	wantStored := "elf" + elf[:16] + "_ehome_collector.bin"
	if filepath.Base(got.StoragePath) != wantStored {
		t.Fatalf("storage name = %q, want %q", filepath.Base(got.StoragePath), wantStored)
	}
	if _, err := os.Stat(got.StoragePath); err != nil {
		t.Fatalf("completed binary missing at %s: %v", got.StoragePath, err)
	}
	if _, err := os.Stat(oldPath); !os.IsNotExist(err) {
		t.Fatalf("superseded legacy binary still present at %s (stat err = %v)", oldPath, err)
	}
	assertNoTempLeftovers(t)

	// 3) 补齐之后（verified）再传同一份 → 409，且不多写一行。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "again.bin", image, sidecar))
	if w.Code != http.StatusConflict {
		t.Fatalf("re-upload after completion: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_DUPLICATE" {
		t.Fatalf("error_code = %q, want FW_DUPLICATE", code)
	}

	// duplicate 状态同样不可补齐（只有 legacy 才可补齐）。
	if err := db.Model(&models.Firmware{}).Where("id = ?", legacy.ID).Update("identity_state", "duplicate").Error; err != nil {
		t.Fatalf("mark duplicate: %v", err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithSidecar(t, "again.bin", image, sidecar))
	if w.Code != http.StatusConflict {
		t.Fatalf("duplicate identity re-upload: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	var count int64
	if err := db.Model(&models.Firmware{}).Count(&count).Error; err != nil {
		t.Fatalf("count firmwares: %v", err)
	}
	if count != 1 {
		t.Fatalf("%d firmware rows, want exactly 1", count)
	}
}
