package firmwarepkg

import "testing"

// TestOpen_DefaultLimits：Limits{} 与 Open 归一化后等价于 DefaultLimits()；
// 只改一项不影响其余（评审 B-1「零值上限 = 全拒」）。
func TestOpen_DefaultLimits(t *testing.T) {
	defaults := DefaultLimits()
	if got := (Limits{}).normalized(); got != defaults {
		t.Fatalf("Limits{}.normalized() = %+v, want %+v", got, defaults)
	}
	if got := defaults.normalized(); got != defaults {
		t.Fatalf("DefaultLimits().normalized() = %+v, want identity", got)
	}

	// 只改一项：其余字段仍是默认值。
	partial := Limits{MaxEntries: 4}.normalized()
	if partial.MaxEntries != 4 {
		t.Fatalf("MaxEntries = %d, want 4", partial.MaxEntries)
	}
	if partial.MaxUncompressed != defaults.MaxUncompressed ||
		partial.MaxManifest != defaults.MaxManifest ||
		partial.MaxRatio != defaults.MaxRatio {
		t.Fatalf("partial limits leaked into other fields: %+v", partial)
	}

	// 归一化真的作用在 Open 上：零值 Limits 与 DefaultLimits 对同一个包给出同样结果。
	spec := testPackageSpec{Artifact: []byte("artifact"), Profile: "c6-n8"}
	if _, err := openTestPackage(t, spec, Limits{}); err != nil {
		t.Fatalf("Open with zero Limits: %v (zero-value limits must fall back to the defaults)", err)
	}
	if _, err := openTestPackage(t, spec, DefaultLimits()); err != nil {
		t.Fatalf("Open with DefaultLimits: %v", err)
	}

	// 显式收紧一项必须生效：1 个条目放不进 2 条目的包。
	if _, err := openTestPackage(t, spec, Limits{MaxEntries: 1}); err == nil {
		t.Fatal("Open with MaxEntries:1 accepted a 2-entry package")
	}
}
