package firmwarepkg

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"
)

// ── 夹具 ──
//
// 这里用标准库自己拼包，只服务「结构 / 边界」用例；字节级的正向对照由
// vectors_test.go 的 golden 向量负责（那是 Go 与 Python 之间唯一的防漂移手段，§8）。

// testPackageSpec 描述一个待拼装的包。
type testPackageSpec struct {
	ArtifactName   string
	Artifact       []byte
	PSRAM          bool
	Profile        string
	ArtifactMethod uint16 // 默认 zip.Store
	// OverrideManifest 非 nil 时直接用它（构造畸形清单）。
	OverrideManifest []byte
	// ExtraEntries 追加到默认两条目之后（构造条目数≠2）。
	ExtraEntries []pkgEntry
	// Entries 非 nil 时完全替换默认两条目。
	Entries []pkgEntry
}

// pkgEntry 是一个待写入的条目。
type pkgEntry struct {
	Name   string
	Data   []byte
	Method uint16
}

// buildTestPackage 按 spec 拼一个 ZIP 容器（in-memory）。
func buildTestPackage(t *testing.T, spec testPackageSpec) []byte {
	t.Helper()
	if spec.ArtifactName == "" {
		spec.ArtifactName = "ehome_collector.bin"
	}
	if spec.Profile == "" {
		spec.Profile = "c6-n8"
	}

	entries := spec.Entries
	if entries == nil {
		manifest := spec.OverrideManifest
		if manifest == nil {
			manifest = marshalTestManifest(t, testManifest(spec.ArtifactName, spec.Artifact, spec.PSRAM, spec.Profile))
		}
		entries = []pkgEntry{
			{Name: ManifestEntry, Data: manifest, Method: zip.Deflate},
			{Name: spec.ArtifactName, Data: spec.Artifact, Method: spec.ArtifactMethod},
		}
		entries = append(entries, spec.ExtraEntries...)
	}

	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: entry.Method}
		w, err := writer.CreateHeader(header)
		if err != nil {
			t.Fatalf("create entry %q: %v", entry.Name, err)
		}
		if _, err := w.Write(entry.Data); err != nil {
			t.Fatalf("write entry %q: %v", entry.Name, err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// testManifest 造一份与 artifact 自洽的最小合法清单。
func testManifest(artifactName string, artifact []byte, psram bool, profile string) *Manifest {
	sum := sha256.Sum256(artifact)
	value := psram
	return &Manifest{
		Format:        FormatName,
		FormatVersion: FormatVersion,
		Packer:        Packer{Name: "pack_firmware.py", Version: "1.0.0"},
		Artifact: Artifact{
			Name:   artifactName,
			Size:   int64(len(artifact)),
			SHA256: hex.EncodeToString(sum[:]),
		},
		Identity: Identity{PSRAM: &value, Profile: profile},
	}
}

func marshalTestManifest(t *testing.T, manifest *Manifest) []byte {
	t.Helper()
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal manifest: %v", err)
	}
	return raw
}

// openTestPackage 拼包 → 落内存 → Open。
func openTestPackage(t *testing.T, spec testPackageSpec, limits Limits) (*Package, error) {
	t.Helper()
	raw := buildTestPackage(t, spec)
	return Open(bytes.NewReader(raw), int64(len(raw)), limits)
}

// mustOpenTestPackage 同 openTestPackage，但期望成功。
func mustOpenTestPackage(t *testing.T, spec testPackageSpec) *Package {
	t.Helper()
	pkg, err := openTestPackage(t, spec, DefaultLimits())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return pkg
}
