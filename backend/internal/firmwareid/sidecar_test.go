package firmwareid

import (
	"strings"
	"testing"
)

const validSidecar = `{
  "profile": "s3p-n16",
  "chip": "esp32s3",
  "flash_mb": 16,
  "psram": true,
  "version": "2.8.0",
  "idf_ver": "v6.1",
  "sha256": "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
}`

func TestParseSidecar_OK(t *testing.T) {
	side, err := ParseSidecar([]byte(validSidecar))
	if err != nil {
		t.Fatalf("ParseSidecar: %v", err)
	}
	if side.Profile != "s3p-n16" || side.Chip != "esp32s3" || side.FlashMB != 16 {
		t.Fatalf("sidecar = %+v", side)
	}
	if side.PSRAMValue() != PSRAMYes {
		t.Fatalf("psram value = %q, want %q", side.PSRAMValue(), PSRAMYes)
	}

	noPSRAM := strings.Replace(validSidecar, `"psram": true`, `"psram": false`, 1)
	side, err = ParseSidecar([]byte(noPSRAM))
	if err != nil {
		t.Fatalf("ParseSidecar(psram=false): %v", err)
	}
	if side.PSRAMValue() != PSRAMNo {
		t.Fatalf("psram value = %q, want %q", side.PSRAMValue(), PSRAMNo)
	}
}

// TestParseSidecar_Required 覆盖 AC-2 的一半：不合格的 sidecar 与「没有 sidecar」
// 在契约上等价——两者都拿不到 psram，一律 FW_META_REQUIRED。
func TestParseSidecar_Required(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"empty", ""},
		{"blank", "   "},
		{"not json", "profile=s3p-n16"},
		{"truncated json", `{"profile":"s3p-n16"`},
		{"unknown field", strings.Replace(validSidecar, `"profile"`, `"profile_typo"`, 1)},
		{"missing psram", strings.Replace(validSidecar, `"psram": true,`, ``, 1)},
		{"missing sha256", strings.Replace(validSidecar, `"sha256"`, `"sha256_x"`, 1)},
		{"missing version", strings.Replace(validSidecar, `"version": "2.8.0",`, ``, 1)},
		{"missing flash_mb", strings.Replace(validSidecar, `"flash_mb": 16,`, ``, 1)},
		{"empty chip", strings.Replace(validSidecar, `"chip": "esp32s3"`, `"chip": ""`, 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseSidecar([]byte(tc.raw))
			if err == nil {
				t.Fatal("expected FW_META_REQUIRED, got nil")
			}
			if got := Code(err); got != CodeMetaRequired {
				t.Fatalf("error code = %q, want %q (err: %v)", got, CodeMetaRequired, err)
			}
		})
	}
}

// TestCheckSidecar_Conflicts 覆盖 AC-3：chip / flash_mb / version / sha256 任一不符
// 都是 FW_META_CONFLICT，一个都不放过。
func TestCheckSidecar_Conflicts(t *testing.T) {
	id := &Identity{Chip: ChipS3, FlashMB: 16, Version: "2.8.0"}
	const fileSum = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	okSide, err := ParseSidecar([]byte(validSidecar))
	if err != nil {
		t.Fatalf("ParseSidecar: %v", err)
	}
	if err := CheckSidecar(okSide, id, fileSum); err != nil {
		t.Fatalf("matching sidecar rejected: %v", err)
	}
	// sha256 比较大小写不敏感（构建脚本可能大写输出）。
	if err := CheckSidecar(okSide, id, strings.ToUpper(fileSum)); err != nil {
		t.Fatalf("uppercase sha256 rejected: %v", err)
	}
	// chip 别名也要认（sidecar 写 s3 等价于 esp32s3）。
	alias := *okSide
	alias.Chip = "s3"
	if err := CheckSidecar(&alias, id, fileSum); err != nil {
		t.Fatalf("chip alias rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Sidecar)
		sum    string
	}{
		{"chip", func(s *Sidecar) { s.Chip = "esp32c6" }, fileSum},
		{"flash", func(s *Sidecar) { s.FlashMB = 8 }, fileSum},
		{"version", func(s *Sidecar) { s.Version = "2.7.0" }, fileSum},
		{"sha256", func(s *Sidecar) {}, strings.Repeat("ab", 32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			side := *okSide
			tc.mutate(&side)
			err := CheckSidecar(&side, id, tc.sum)
			if got := Code(err); got != CodeMetaConflict {
				t.Fatalf("error code = %q, want %q (err: %v)", got, CodeMetaConflict, err)
			}
		})
	}

	if err := CheckSidecar(nil, id, fileSum); Code(err) != CodeMetaRequired {
		t.Fatalf("nil sidecar code = %q, want %q", Code(err), CodeMetaRequired)
	}
	if err := CheckSidecar(okSide, nil, fileSum); Code(err) != CodeMetaRequired {
		t.Fatalf("nil identity code = %q, want %q", Code(err), CodeMetaRequired)
	}
}

func TestBuildProfile(t *testing.T) {
	cases := []struct {
		chip    string
		flashMB uint16
		psram   bool
		want    string
	}{
		{ChipC6, 8, false, "c6-n8"},
		{ChipC6, 16, false, "c6-n16"},
		{ChipS3, 16, false, "s3-n16"},
		{ChipS3, 16, true, "s3p-n16"},
		{ChipS3, 8, true, "s3p-n8"},
		{"", 16, false, ""},
		{"esp32", 16, false, ""},
		{ChipS3, 0, false, ""},
	}
	for _, tc := range cases {
		if got := BuildProfile(tc.chip, tc.flashMB, tc.psram); got != tc.want {
			t.Fatalf("BuildProfile(%q,%d,%v) = %q, want %q", tc.chip, tc.flashMB, tc.psram, got, tc.want)
		}
	}
}
