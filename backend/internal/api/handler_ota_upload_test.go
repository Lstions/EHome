package api

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/firmwareid"
	"ehome/backend/internal/models"
)

// ── 上传夹具（单文件包：一个 .ehfw = 一次构建，方案 §4 / §5.2）──
//
// 这几份夹具同时服务三组用例：
//  1. 上传契约（§5.7）；
//  2. 删除 / 信封契约测试——它们原先上传的是「镜像 + sidecar」，
//     在新契约下必须改喂一份**合法固件包**；
//  3. 上传拒绝指标。
//
// 偏移表与 internal/firmwareid 必须一致；两处漂移会让「夹具通过、真固件被拒」。
// §5.8 #4 的告诫：先把夹具改对，再改用例 —— 只改主文件会让三组一起红，
// 而报错只会指向「expected 201, got 400」。
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

// firmwareTestManifest 生成与镜像自洽的清单（artifact.size/sha256 取自实测字节）。
func firmwareTestManifest(t *testing.T, artifactName string, artifact []byte, psram bool, profile string) map[string]interface{} {
	t.Helper()
	sum := sha256.Sum256(artifact)
	return map[string]interface{}{
		"format":         "ehome-firmware",
		"format_version": 1,
		"packer": map[string]interface{}{
			"name":    "pack_firmware.py",
			"version": "1.0.0",
		},
		"artifact": map[string]interface{}{
			"name":   artifactName,
			"size":   len(artifact),
			"sha256": hex.EncodeToString(sum[:]),
		},
		"identity": map[string]interface{}{
			"psram":   psram,
			"profile": profile,
		},
	}
}

// firmwareTestPackage 打一个单文件包：fw-manifest.json + <artifactName>。
func firmwareTestPackage(t *testing.T, artifactName string, artifact []byte, psram bool, profile string) []byte {
	t.Helper()
	return firmwareTestPackageFromManifest(t, firmwareTestManifest(t, artifactName, artifact, psram, profile), artifactName, artifact)
}

// firmwareTestPackageFromManifest 用给定清单拼包（用于构造畸形清单）。
func firmwareTestPackageFromManifest(t *testing.T, manifest map[string]interface{}, artifactName string, artifact []byte) []byte {
	t.Helper()
	rawManifest, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range []struct {
		name string
		data []byte
	}{
		{"fw-manifest.json", rawManifest},
		{artifactName, artifact},
	} {
		w, err := writer.CreateHeader(&zip.FileHeader{Name: entry.name, Method: zip.Store})
		if err != nil {
			t.Fatalf("create entry %q: %v", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("write entry %q: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close package: %v", err)
	}
	return buf.Bytes()
}

// firmwareTestBombPackage 造一个压缩比超限的包（200 KiB 全零 + DEFLATE）。
//
// 这不是「损坏的包」：清单与实测自洽、结构完全合法，唯一的问题是压缩比
// 远超 100:1 —— INV-5 必须在**解压前**按中央目录的声明值拒掉（AC-P4）。
func firmwareTestBombPackage(t *testing.T) []byte {
	t.Helper()
	const artifactName = "ehome_collector.bin"
	artifact := make([]byte, 200<<10) // 全零：deflate 后约 200 B
	rawManifest, err := json.Marshal(firmwareTestManifest(t, artifactName, artifact, false, "c6-n8"))
	if err != nil {
		t.Fatalf("encode manifest: %v", err)
	}
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	entries := []struct {
		name   string
		data   []byte
		method uint16
	}{
		{"fw-manifest.json", rawManifest, zip.Store},
		{artifactName, artifact, zip.Deflate},
	}
	for _, entry := range entries {
		w, err := writer.CreateHeader(&zip.FileHeader{Name: entry.name, Method: entry.method})
		if err != nil {
			t.Fatalf("create entry %q: %v", entry.name, err)
		}
		if _, err := w.Write(entry.data); err != nil {
			t.Fatalf("write entry %q: %v", entry.name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close package: %v", err)
	}
	return buf.Bytes()
}

// firmwareUploadMultipart 组一个上传请求体：**只有 file 一个 part**（§4.1）。
func firmwareUploadMultipart(t *testing.T, baseName string, pkg []byte) (*bytes.Buffer, string) {
	t.Helper()
	return firmwareUploadMultipartWithContentType(t, baseName, "", pkg)
}

// firmwareUploadMultipartWithContentType 允许显式设置 file part 的 Content-Type。
//
// 这条路径必须真的被测到：浏览器给 .ehfw 打的是 application/zip，而
// Go 的 CreateFormFile 固定 octet-stream —— 两边都「测不出来」（§5.8 #1）。
func firmwareUploadMultipartWithContentType(t *testing.T, baseName, contentType string, pkg []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	header := textproto.MIMEHeader{}
	header.Set("Content-Disposition", fmt.Sprintf("form-data; name=\"file\"; filename=\"%s\"", baseName))
	if contentType != "" {
		header.Set("Content-Type", contentType)
	}
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatalf("create file part: %v", err)
	}
	if _, err := part.Write(pkg); err != nil {
		t.Fatalf("write file part: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	return &body, writer.FormDataContentType()
}

// firmwareUploadWithPackage 组一个已鉴权的上传请求。
func firmwareUploadWithPackage(t *testing.T, baseName string, pkg []byte) *http.Request {
	t.Helper()
	body, contentType := firmwareUploadMultipart(t, baseName, pkg)
	return firmwareAuthedUploadRequest(t, body, contentType)
}

func firmwareAuthedUploadRequest(t *testing.T, body *bytes.Buffer, contentType string) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", authHeader(t))
	return req
}

// assertNoFirmwareOrphans 断言正式目录与临时目录都没有残留（§5.2 步骤 11）。
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

// ── §5.7 用例 ──

// TestFirmwareUpload_AcceptsPackage 覆盖 AC-P1：一个文件上传成功，身份字段全部正确，
// 落盘的是包里的 artifact（裸 app image），不是 ZIP 容器。
func TestFirmwareUpload_AcceptsPackage(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	const artifactName = "ehome_collector.bin"
	image := firmwareTestImage(0x000D, 4, "2.8.0", "c6-n16")
	pkg := firmwareTestPackage(t, artifactName, image, false, "c6-n16")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "ehome_collector-2.8.0-c6-n16.ehfw", pkg))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var fw models.Firmware
	if err := db.First(&fw, "filename = ?", artifactName).Error; err != nil {
		t.Fatalf("uploaded firmware row not found: %v", err)
	}
	if fw.Version != "2.8.0" {
		t.Fatalf("version = %q, want image version %q (the form carries none)", fw.Version, "2.8.0")
	}
	if fw.Chip != "esp32c6" || fw.FlashMB != 16 || fw.ModelKey != "esp32c6-n16" {
		t.Fatalf("identity = chip %q flash %d model_key %q", fw.Chip, fw.FlashMB, fw.ModelKey)
	}
	if fw.Psram != "no" {
		t.Fatalf("psram = %q, want %q (the manifest is the only source)", fw.Psram, "no")
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

	// checksum / size 必须覆盖**设备下载的那串字节**（artifact），不是容器（§5.6 / AC-P9）。
	sum := sha256.Sum256(image)
	if fw.Checksum != hex.EncodeToString(sum[:]) {
		t.Fatalf("checksum = %q, want the artifact sha256", fw.Checksum)
	}
	if fw.SizeBytes != uint64(len(image)) {
		t.Fatalf("size_bytes = %d, want artifact size %d", fw.SizeBytes, len(image))
	}

	// Filename = 清单声明的 artifact.name；package_name = 上传时的原始文件名（排障）。
	if fw.Filename != artifactName {
		t.Fatalf("filename = %q, want the manifest artifact.name %q", fw.Filename, artifactName)
	}
	if fw.PackageName != "ehome_collector-2.8.0-c6-n16.ehfw" {
		t.Fatalf("package_name = %q, want the uploaded file name", fw.PackageName)
	}
	if fw.PackageFormatVersion != 1 {
		t.Fatalf("package_format_version = %d, want 1", fw.PackageFormatVersion)
	}

	// StoragePath 恒非空，且落盘名 = elf 前缀 + artifact.name。
	wantStored := "elf" + fw.ElfSHA256[:16] + "_" + artifactName
	if fw.StoragePath == "" {
		t.Fatal("storage_path is empty; the delete path would look for the package name instead")
	}
	if got := filepath.Base(fw.StoragePath); got != wantStored {
		t.Fatalf("storage name = %q, want %q", got, wantStored)
	}
	if dir := filepath.Dir(fw.StoragePath); dir != firmwareDir {
		t.Fatalf("storage dir = %q, want %q", dir, firmwareDir)
	}
	if !strings.Contains(fw.URL, "/api/v1/firmwares/"+wantStored+"/download?expires=") {
		t.Fatalf("download URL %q does not point at the stored file %q", fw.URL, wantStored)
	}

	// 落盘物必须逐字节等于 artifact：把容器落盘会让设备刷 ZIP（§5.6 / G7）。
	stored, err := os.ReadFile(fw.StoragePath)
	if err != nil {
		t.Fatalf("read stored firmware: %v", err)
	}
	if !bytes.Equal(stored, image) {
		t.Fatalf("stored bytes (%d) differ from the artifact (%d): the device would download a ZIP", len(stored), len(image))
	}
	assertNoTempLeftovers(t)
}

// TestFirmwareUpload_AcceptsZipContentType 覆盖 §5.8 #1：浏览器给 .ehfw 打
// application/zip 或 application/x-zip-compressed 时不能被白的白名单挡住。
func TestFirmwareUpload_AcceptsZipContentType(t *testing.T) {
	for _, contentType := range []string{"application/zip", "application/x-zip-compressed", "application/octet-stream"} {
		t.Run(contentType, func(t *testing.T) {
			t.Chdir(t.TempDir())
			t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
			r, _, _ := setupOTATest(t)

			image := firmwareTestImage(0x000D, 3, "2.8.0", "ct-"+contentType)
			pkg := firmwareTestPackage(t, "ehome_collector.bin", image, false, "c6-n8")
			body, boundary := firmwareUploadMultipartWithContentType(t, "fw.ehfw", contentType, pkg)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareAuthedUploadRequest(t, body, boundary))
			if w.Code != http.StatusCreated {
				t.Fatalf("content-type %q: expected 201, got %d: %s", contentType, w.Code, w.Body.String())
			}
		})
	}
}

// TestFirmwareUpload_RejectsRawBin 覆盖 AC-P2：传裸 .bin → 400 FW_PKG_REQUIRED，
// 文案给出 pack_firmware.py。
func TestFirmwareUpload_RejectsRawBin(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	image := firmwareTestImage(0x0009, 4, "2.8.0", "s3p-n16")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "ehome_collector.bin", image))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("raw .bin: expected 400, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_PKG_REQUIRED" {
		t.Fatalf("error_code = %q, want FW_PKG_REQUIRED", code)
	}
	if !strings.Contains(w.Body.String(), "pack_firmware.py") {
		t.Fatalf("message must point at pack_firmware.py: %s", w.Body.String())
	}
	// 硬切换的部署过渡期，旧前端只会原样展示这条 message：刷新提示必须在这里（§9.3）。
	if !strings.Contains(w.Body.String(), "刷新页面") {
		t.Fatalf("message must tell stale-JS users to refresh the page: %s", w.Body.String())
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

// TestFirmwareUpload_RejectsZipBomb 覆盖 AC-P4 的 413 一半：压缩比超限的包必须在
// **解压前**被拒（FW_PKG_TOO_LARGE → 413），而不是先把它解出 200 KiB。
func TestFirmwareUpload_RejectsZipBomb(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "bomb.ehfw", firmwareTestBombPackage(t)))
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413, got %d: %s", w.Code, truncateBody(w.Body.String()))
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_PKG_TOO_LARGE" {
		t.Fatalf("error_code = %q, want FW_PKG_TOO_LARGE", code)
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

// TestFirmwareUpload_DigestMismatch 覆盖 AC-P3：artifact 摘要与清单不符 → 400，磁盘无残留。
//
// 两个子例刻意分开，因为它们落在**不同的规则**上：
//
//	· 清单摘要写错 → ZIP 本身完好，走到 §5.2 步骤 5 → FW_PKG_DIGEST_MISMATCH；
//	· 包内字节被篡改 → ZIP 的 CRC 先不服，ExtractArtifactTo 直接失败 → FW_PKG_INVALID。
func TestFirmwareUpload_DigestMismatch(t *testing.T) {
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")

	image := firmwareTestImage(0x000D, 3, "2.8.0", "digest-mismatch")
	wrongManifest := firmwareTestManifest(t, "ehome_collector.bin", image, false, "c6-n8")
	wrongManifest["artifact"].(map[string]interface{})["sha256"] = strings.Repeat("ab", 32)
	goodManifest := firmwareTestManifest(t, "ehome_collector.bin", image, false, "c6-n8")

	corrupted := firmwareTestPackageFromManifest(t, goodManifest, "ehome_collector.bin", image)
	idx := bytes.Index(corrupted, image)
	if idx < 0 {
		t.Fatal("could not locate the artifact payload inside the package")
	}
	corrupted[idx+8] ^= 0xFF

	cases := []struct {
		name string
		pkg  []byte
		code string
	}{
		{"manifest digest differs", firmwareTestPackageFromManifest(t, wrongManifest, "ehome_collector.bin", image), "FW_PKG_DIGEST_MISMATCH"},
		{"artifact bytes corrupted", corrupted, "FW_PKG_INVALID"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			r, db, _ := setupOTATest(t)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithPackage(t, "fw.ehfw", tc.pkg))
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

// TestFirmwareUpload_ProfileConflict 覆盖 AC-P5（INV-3）：清单 profile 不能由
// 镜像的 (chip, flash) + 清单 psram 还原 → 400 FW_PKG_CONFLICT。
func TestFirmwareUpload_ProfileConflict(t *testing.T) {
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")

	// s3 / 8MB 的镜像配 s3p-n16 的清单：chip 与容量都不匹配。
	s3Image := firmwareTestImage(0x0009, 3, "2.8.0", "profile-conflict")
	// psram=false 但清单自称 s3p-n16：同一镜像的另一个错配方向。
	psramLie := firmwareTestImage(0x0009, 4, "2.8.0", "profile-conflict-2")

	cases := []struct {
		name    string
		image   []byte
		psram   bool
		profile string
	}{
		{"s3p manifest on s3 image", s3Image, true, "s3p-n16"},
		{"psram false but profile says s3p", psramLie, false, "s3p-n16"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			r, db, _ := setupOTATest(t)
			pkg := firmwareTestPackage(t, "ehome_collector.bin", tc.image, tc.psram, tc.profile)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithPackage(t, "fw.ehfw", pkg))
			if w.Code != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %s", w.Code, w.Body.String())
			}
			if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_PKG_CONFLICT" {
				t.Fatalf("error_code = %q, want FW_PKG_CONFLICT (%s)", code, truncateBody(w.Body.String()))
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

// TestFirmwareUpload_DuplicateIsAlwaysConflict 覆盖 AC-P7：同 elf_sha256 → 409，
// **无 200 补齐路径**（§9.2 删掉 legacy 就地补齐之后，legacy 行同样只能 409）。
func TestFirmwareUpload_DuplicateIsAlwaysConflict(t *testing.T) {
	t.Chdir(t.TempDir())
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-upload-test.local")
	r, db, _ := setupOTATest(t)

	image := firmwareTestImage(0x000D, 3, "2.8.0", "dup-elf")
	pkg := firmwareTestPackage(t, "ehome_collector.bin", image, false, "c6-n8")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "first.ehfw", pkg))
	if w.Code != http.StatusCreated {
		t.Fatalf("first upload: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	// 同一份内容、不同包名：唯一键是 elf_sha256，不是文件名。
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "second.ehfw", pkg))
	if w.Code != http.StatusConflict {
		t.Fatalf("second upload: expected 409, got %d: %s", w.Code, w.Body.String())
	}
	if _, code := decodeEnvelopeErrorCode(t, w.Body.String()); code != "FW_DUPLICATE" {
		t.Fatalf("error_code = %q, want FW_DUPLICATE", code)
	}

	// legacy 状态的行同样不可「就地补齐」：这是旧路径被彻底删除的判据。
	var first models.Firmware
	if err := db.First(&first).Error; err != nil {
		t.Fatalf("reload uploaded firmware: %v", err)
	}
	if err := db.Model(&first).Update("identity_state", "legacy").Error; err != nil {
		t.Fatalf("mark legacy: %v", err)
	}
	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "third.ehfw", pkg))
	if w.Code != http.StatusConflict {
		t.Fatalf("legacy-state re-upload: expected 409 (no completion path), got %d: %s", w.Code, w.Body.String())
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

// TestFirmwareStorageName_ArtifactName 覆盖 §5.8 #3：入参改成 manifest.artifact.name
// 之后，落盘名必须恒以 .bin 结尾，且 96 字符截断不能切坏扩展名。
func TestFirmwareStorageName_ArtifactName(t *testing.T) {
	const elf = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	stored := firmwareStorageName(elf, "ehome_collector.bin")
	if want := "elf" + elf[:16] + "_ehome_collector.bin"; stored != want {
		t.Fatalf("storage name = %q, want %q", stored, want)
	}

	// 超长 artifact 名：截断后仍然以 .bin 结尾，且总长不失控。
	longName := strings.Repeat("a", 200) + ".bin"
	stored = firmwareStorageName(elf, longName)
	if !strings.HasSuffix(stored, ".bin") {
		t.Fatalf("storage name %q lost its .bin extension", stored)
	}
	if strings.HasSuffix(stored, "...bin") || strings.HasSuffix(stored, ".bi") {
		t.Fatalf("storage name %q was truncated inside the extension", stored)
	}
	if base := strings.TrimPrefix(stored, "elf"+elf[:16]+"_"); len(base) > 96 {
		t.Fatalf("truncated artifact name is %d chars, want <= 96", len(base))
	}
}

// TestFirmwareUpload_RejectsBadImages 覆盖镜像解析的拒绝码：包结构合法、摘要一致，
// 只有镜像本身不合格 —— 每条拒绝带精确机器码。
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
		{name: "truncated", image: valid[:0x40], code: "FW_TRUNCATED"},
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
			// 清单用「镜像自己的值」：这样拒绝只能来自镜像解析，不是清单校验。
			pkg := firmwareTestPackage(t, "ehome_collector.bin", tc.image, false, "c6-n8")
			w := httptest.NewRecorder()
			r.ServeHTTP(w, firmwareUploadWithPackage(t, "fw.ehfw", pkg))
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

// TestFirmwareUpload_RejectsImageBeyondSlotCapacity 覆盖 UT-UP-03：8MB 档的 artifact
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
	pkg := firmwareTestPackage(t, "ehome_collector.bin", image, false, "c6-n8")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadWithPackage(t, "big.ehfw", pkg))
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
