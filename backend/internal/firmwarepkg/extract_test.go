package firmwarepkg

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// TestExtractArtifact_Digest：返回值 (size, sha) 与内容一致，**不承担**比较职责（评审 B-2）。
func TestExtractArtifact_Digest(t *testing.T) {
	artifact := []byte("0123456789abcdef-artifact-payload")
	pkg := mustOpenTestPackage(t, testPackageSpec{Artifact: artifact})

	var out bytes.Buffer
	size, digest, err := pkg.ExtractArtifactTo(&out)
	if err != nil {
		t.Fatalf("ExtractArtifactTo: %v", err)
	}
	if size != int64(len(artifact)) {
		t.Fatalf("size = %d, want %d", size, len(artifact))
	}
	sum := sha256.Sum256(artifact)
	want := hex.EncodeToString(sum[:])
	if digest != want {
		t.Fatalf("sha256 = %q, want %q", digest, want)
	}
	if !bytes.Equal(out.Bytes(), artifact) {
		t.Fatal("extracted bytes differ from the artifact")
	}

	// 清单摘要写错也照样解出来：比较在 handler（§5.2 步骤 5），不是这里。
	broken := testManifest("ehome_collector.bin", artifact, false, "c6-n8")
	broken.Artifact.SHA256 = hex.EncodeToString(bytes.Repeat([]byte{0xAB}, 32))
	pkg = mustOpenTestPackage(t, testPackageSpec{
		Artifact:         artifact,
		OverrideManifest: marshalTestManifest(t, broken),
	})
	out.Reset()
	size, digest, err = pkg.ExtractArtifactTo(&out)
	if err != nil {
		t.Fatalf("ExtractArtifactTo with a wrong manifest digest: %v (extraction must not compare)", err)
	}
	if size != int64(len(artifact)) || digest != want {
		t.Fatalf("measured = %d/%q, want the real %d/%q", size, digest, len(artifact), want)
	}
}
