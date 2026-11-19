package firmwarepkg

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"

	"ehome/backend/internal/firmwareid"
)

// ── §5.7 单测：Open 的结构校验 ──

// TestOpen_HappyPackage：两条目包 → 解析出清单，Manifest() 字段正确。
func TestOpen_HappyPackage(t *testing.T) {
	artifact := []byte("artifact-bytes-0123456789")
	pkg := mustOpenTestPackage(t, testPackageSpec{Artifact: artifact, PSRAM: true, Profile: "s3p-n16"})

	manifest := pkg.Manifest()
	if manifest == nil {
		t.Fatal("Manifest() = nil")
	}
	if manifest.Format != FormatName || manifest.FormatVersion != FormatVersion {
		t.Fatalf("format = %q/%d", manifest.Format, manifest.FormatVersion)
	}
	if manifest.Packer.Name != "pack_firmware.py" || manifest.Packer.Version != "1.0.0" {
		t.Fatalf("packer = %+v", manifest.Packer)
	}
	if manifest.Artifact.Name != "ehome_collector.bin" || manifest.Artifact.Size != int64(len(artifact)) {
		t.Fatalf("artifact = %+v", manifest.Artifact)
	}
	if !manifest.Identity.PSRAMEnabled() || manifest.Identity.Profile != "s3p-n16" {
		t.Fatalf("identity = %+v", manifest.Identity)
	}

	// Manifest() 必须返回副本：调用方改不到包内状态。
	manifest.Identity.Profile = "tampered"
	*manifest.Identity.PSRAM = false
	again := pkg.Manifest()
	if again.Identity.Profile != "s3p-n16" || !again.Identity.PSRAMEnabled() {
		t.Fatalf("Manifest() exposed package state: %+v", again.Identity)
	}
}

// TestOpen_EntryCountNotTwo：多一个条目 / 少一个 → FW_PKG_INVALID（INV-1）。
func TestOpen_EntryCountNotTwo(t *testing.T) {
	artifact := []byte("artifact")
	three := testPackageSpec{
		Artifact:     artifact,
		ExtraEntries: []pkgEntry{{Name: "extra.txt", Data: []byte("nope"), Method: zip.Store}},
	}
	manifestOnly := testPackageSpec{
		Entries: []pkgEntry{{Name: ManifestEntry, Data: marshalTestManifest(t, testManifest("ehome_collector.bin", artifact, false, "c6-n8")), Method: zip.Store}},
	}
	for _, tc := range []struct {
		name string
		spec testPackageSpec
	}{
		{"three entries", three},
		{"one entry", manifestOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := openTestPackage(t, tc.spec, DefaultLimits())
			if got := firmwareid.Code(err); got != CodePkgInvalid {
				t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgInvalid, err)
			}
		})
	}
}

// TestOpen_BadEntryName：../evil.bin、大写、含 / → FW_PKG_INVALID（INV-4）。
func TestOpen_BadEntryName(t *testing.T) {
	artifact := []byte("artifact")
	manifest := marshalTestManifest(t, testManifest("ehome_collector.bin", artifact, false, "c6-n8"))
	for _, name := range []string{"../evil.bin", "Evil.bin", "a/b.bin", ".."} {
		t.Run(name, func(t *testing.T) {
			spec := testPackageSpec{
				Entries: []pkgEntry{
					{Name: ManifestEntry, Data: manifest, Method: zip.Store},
					{Name: name, Data: artifact, Method: zip.Store},
				},
			}
			_, err := openTestPackage(t, spec, DefaultLimits())
			if got := firmwareid.Code(err); got != CodePkgInvalid {
				t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgInvalid, err)
			}
		})
	}
}

// TestOpen_UnknownFormatVersion：format_version: 99 → FW_PKG_UNSUPPORTED（§4.6）。
//
// 这里用合成包钉住「升版必须显式拒」；golden 向量 bad_format_version.ehfw 在
// vectors_test.go 里再核一次同一个规则（两侧消费同一批用例）。
func TestOpen_UnknownFormatVersion(t *testing.T) {
	manifest := testManifest("ehome_collector.bin", []byte("artifact"), false, "c6-n8")
	manifest.FormatVersion = 99
	spec := testPackageSpec{OverrideManifest: marshalTestManifest(t, manifest), Artifact: []byte("artifact")}
	_, err := openTestPackage(t, spec, DefaultLimits())
	if got := firmwareid.Code(err); got != CodePkgUnsupported {
		t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgUnsupported, err)
	}
}

// TestOpen_ManifestMissingRequiredField：清单缺 psram 键（不是 false，是缺）→ INVALID。
func TestOpen_ManifestMissingRequiredField(t *testing.T) {
	artifact := []byte("artifact")
	raw := []byte(`{"format":"ehome-firmware","format_version":1,` +
		`"packer":{"name":"pack_firmware.py","version":"1.0.0"},` +
		`"artifact":{"name":"ehome_collector.bin","size":8,` +
		`"sha256":"` + strings.Repeat("a", 64) + `"},` +
		`"identity":{"profile":"c6-n8"}}`)
	spec := testPackageSpec{OverrideManifest: raw, Artifact: artifact}
	_, err := openTestPackage(t, spec, DefaultLimits())
	if got := firmwareid.Code(err); got != CodePkgInvalid {
		t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgInvalid, err)
	}
	if err == nil || !strings.Contains(err.Error(), "identity.psram") {
		t.Fatalf("error %v does not name the missing field", err)
	}
}

// TestOpen_NonZip：不是 ZIP 容器 → FW_PKG_INVALID（裸镜像的 FW_PKG_REQUIRED 由
// handler 的内容嗅探给出，见 api 侧的 TestFirmwareUpload_RejectsRawBin）。
func TestOpen_NonZip(t *testing.T) {
	raw := bytes.Repeat([]byte{0xE9}, 1024)
	_, err := Open(bytes.NewReader(raw), int64(len(raw)), DefaultLimits())
	if got := firmwareid.Code(err); got != CodePkgInvalid {
		t.Fatalf("error code = %q, want %q (err: %v)", got, CodePkgInvalid, err)
	}
}
