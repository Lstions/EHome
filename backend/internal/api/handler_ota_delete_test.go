package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// ==================== Firmware delete: disk cleanup ====================

// firmwareDeleteArtifactName 是夹具包的 artifact 名（§4.2：包内恒为 ehome_collector.bin）。
const firmwareDeleteArtifactName = "ehome_collector.bin"

//
// Regression cover for the disk leak in DELETE /api/v1/firmwares/:id: the handler
// derived the file name from fw.URL, which is a signed download ticket
// (http://host/api/v1/firmwares/<name>.bin/download?expires=...&signature=...),
// so filepath.Base(URL) produced "download?expires=..." and the binary was never
// removed. Both the upload path (StoragePath) and the delete path are pinned here.

// firmwareUploadRequest builds an authenticated multipart POST /api/v1/firmwares/upload.
//
// 2026-11-19（单文件包）：上传契约改成「一个 .ehfw = 一次构建」，表单不再接收
// version/sidecar，DB 里的 Filename 也改由清单的 artifact.name 决定。本用例只关心
// 「上传落盘 → 删除清盘」，所以造一份最小合法包；filename 参数现在只作为上传时的
// 原始包名（落进 package_name），payload 保留只为不改调用点的语义。
func firmwareUploadRequest(t *testing.T, version, filename string, payload []byte) *http.Request {
	t.Helper()
	_ = payload
	image := firmwareTestImage(0x000D, 3, version, "firmware-delete-test")
	pkg := firmwareTestPackage(t, firmwareDeleteArtifactName, image, false, "c6-n8")
	body, contentType := firmwareUploadMultipart(t, filename, pkg)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/firmwares/upload", body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", authHeader(t))
	return req
}

// firmwareDeleteRequest builds an authenticated DELETE /api/v1/firmwares/:id.
func firmwareDeleteRequest(t *testing.T, id uint) *http.Request {
	t.Helper()
	req := httptest.NewRequest(http.MethodDelete, "/api/v1/firmwares/"+strconv.FormatUint(uint64(id), 10), nil)
	req.Header.Set("Authorization", authHeader(t))
	return req
}

func mustStatMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("binary still present at %s after delete (stat err = %v)", path, err)
	}
}

// TestFirmwareDelete_RemovesUploadedBinaryFromDisk drives a real upload through the
// router and asserts the upload records StoragePath and that DELETE removes exactly
// that file. Without StoragePath the delete path has to guess and the leak returns.
func TestFirmwareDelete_RemovesUploadedBinaryFromDisk(t *testing.T) {
	t.Chdir(t.TempDir()) // the handlers resolve "firmwares/..." against the CWD
	t.Setenv("EHOME_EXTERNAL_HOST", "firmware-delete-test.local")

	r, db, _ := setupOTATest(t)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareUploadRequest(t, "9.9.9", "delete-me.ehfw", []byte("FIRMWARE-BYTES")))
	if w.Code != http.StatusCreated {
		t.Fatalf("upload: expected 201, got %d: %s", w.Code, w.Body.String())
	}

	var fw models.Firmware
	if err := db.First(&fw, "package_name = ?", "delete-me.ehfw").Error; err != nil {
		t.Fatalf("uploaded firmware row not found: %v", err)
	}
	// 落盘名带 elf 前缀（同名重传不得覆盖旧二进制），但仍是 firmwares/ 下的真实文件。
	stored := filepath.Base(fw.StoragePath)
	wantStored := "_" + firmwareDeleteArtifactName
	if !strings.HasPrefix(stored, "elf") || !strings.HasSuffix(stored, wantStored) {
		t.Fatalf("StoragePath basename = %q, want an elf-prefixed unique name ending in %q", stored, wantStored)
	}
	if dir := filepath.Dir(fw.StoragePath); dir != "firmwares" {
		t.Fatalf("StoragePath dir = %q, want firmwares", dir)
	}
	// Filename 现在存清单声明的 artifact.name（§5.2）：落盘物必须是裸 app image。
	if fw.Filename != firmwareDeleteArtifactName {
		t.Fatalf("Filename = %q, want the manifest artifact.name %q", fw.Filename, firmwareDeleteArtifactName)
	}
	if fw.PackageName != "delete-me.ehfw" {
		t.Fatalf("PackageName = %q, want the uploaded package name", fw.PackageName)
	}
	if _, err := os.Stat(fw.StoragePath); err != nil {
		t.Fatalf("uploaded binary missing on disk at %s: %v", fw.StoragePath, err)
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, firmwareDeleteRequest(t, fw.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	mustStatMissing(t, fw.StoragePath)
	if err := db.First(&models.Firmware{}, fw.ID).Error; err == nil {
		t.Fatalf("firmware row %d still present after delete", fw.ID)
	}
}

// TestFirmwareDelete_LegacyTicketURLStillRemovesBinary covers historical rows that
// predate StoragePath: only Filename is usable and URL carries a query string.
func TestFirmwareDelete_LegacyTicketURLStillRemovesBinary(t *testing.T) {
	t.Chdir(t.TempDir())

	binary := filepath.Join("firmwares", "legacy.bin")
	if err := os.MkdirAll("firmwares", 0o755); err != nil {
		t.Fatalf("mkdir firmwares: %v", err)
	}
	if err := os.WriteFile(binary, []byte("OLD-FIRMWARE"), 0o644); err != nil {
		t.Fatalf("seed legacy binary: %v", err)
	}

	r, db, _ := setupOTATest(t)
	legacy := models.Firmware{
		Version:   "0.1.0",
		Checksum:  "deadbeef",
		SizeBytes: 12,
		Filename:  "legacy.bin",
		// Exactly the shape leaked by the old code: no StoragePath, URL is a ticket
		// whose base name is "download?expires=...".
		URL: "http://firmware-delete-test.local/api/v1/firmwares/legacy.bin/download?expires=1700000000&signature=abc",
	}
	if err := db.Create(&legacy).Error; err != nil {
		t.Fatalf("create legacy firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareDeleteRequest(t, legacy.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	mustStatMissing(t, binary)
}

// TestFirmwareDelete_CannotEscapeFirmwareDir pins the path-traversal guard: a
// corrupted or hostile row must never delete outside firmwares/.
func TestFirmwareDelete_CannotEscapeFirmwareDir(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)

	outside := filepath.Join(dir, "outside.bin")
	if err := os.WriteFile(outside, []byte("DO-NOT-DELETE"), 0o644); err != nil {
		t.Fatalf("seed outside file: %v", err)
	}

	r, db, _ := setupOTATest(t)
	hostile := models.Firmware{
		Version: "1.0.0", Checksum: "c", URL: "http://firmware-delete-test.local/f.bin",
		Filename: "../../outside.bin", StoragePath: "../outside.bin",
	}
	if err := db.Create(&hostile).Error; err != nil {
		t.Fatalf("create hostile firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareDeleteRequest(t, hostile.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("path traversal escaped firmwares/: %s was removed (stat err = %v)", outside, err)
	}
}

// TestFirmwareDelete_RowWithoutBinaryMetadataStillDeletes keeps the no-op branch
// honest: no StoragePath and no Filename must not fail the request.
func TestFirmwareDelete_RowWithoutBinaryMetadataStillDeletes(t *testing.T) {
	t.Chdir(t.TempDir())
	r, db, _ := setupOTATest(t)

	orphan := models.Firmware{Version: "0.0.1", Checksum: "c", URL: "http://elsewhere.example/fw.bin"}
	if err := db.Create(&orphan).Error; err != nil {
		t.Fatalf("create firmware row: %v", err)
	}

	w := httptest.NewRecorder()
	r.ServeHTTP(w, firmwareDeleteRequest(t, orphan.ID))
	if w.Code != http.StatusOK {
		t.Fatalf("delete: expected 200, got %d: %s", w.Code, w.Body.String())
	}
}

// TestFirmwareBinaryDiskPath pins resolution order and re-basing directly.
func TestFirmwareBinaryDiskPath(t *testing.T) {
	cases := []struct {
		name        string
		storagePath string
		filename    string
		want        string
	}{
		{"storage path wins", filepath.Join("firmwares", "a.bin"), "b.bin", filepath.Join("firmwares", "a.bin")},
		{"filename fallback", "", "c.bin", filepath.Join("firmwares", "c.bin")},
		{"both empty skips", "", "", ""},
		{"storage traversal is re-based", "../evil.bin", "", filepath.Join("firmwares", "evil.bin")},
		{"filename traversal is re-based", "", "../../evil.bin", filepath.Join("firmwares", "evil.bin")},
		{"absolute storage is re-based", "/etc/passwd", "", filepath.Join("firmwares", "passwd")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := firmwareBinaryDiskPath(tc.storagePath, tc.filename); got != tc.want {
				t.Fatalf("firmwareBinaryDiskPath(%q, %q) = %q, want %q", tc.storagePath, tc.filename, got, tc.want)
			}
		})
	}
}
