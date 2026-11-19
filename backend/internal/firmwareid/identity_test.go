package firmwareid

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"strings"
	"testing"
)

// 单测全部用**构造的头部字节**：仓库里不塞固件（方案 §4.1 的偏移表就是断言依据）。
type imageOpts struct {
	chipID  uint16
	spiSize uint8
	version string
	project string
	idfVer  string
	date    string
	clock   string
	elfSeed string
	size    int
}

// buildImage 造一份「头部像真固件」的字节串。未设置的字段取合法默认值。
func buildImage(o imageOpts) []byte {
	if o.size == 0 {
		o.size = 4096
	}
	if o.chipID == 0 {
		o.chipID = 0x000D // C6
	}
	if o.spiSize == 0 {
		o.spiSize = 3 // 8MB
	}
	if o.version == "" {
		o.version = "2.8.0"
	}
	if o.project == "" {
		o.project = ProjectCollector
	}
	if o.idfVer == "" {
		o.idfVer = "v6.1"
	}
	if o.date == "" {
		o.date = "Oct  5 2026"
	}
	if o.clock == "" {
		o.clock = "07:37:54"
	}
	if o.elfSeed == "" {
		o.elfSeed = o.project + o.version
	}
	buf := make([]byte, o.size)
	buf[offMagic] = imageMagic
	buf[offSpiSize] = o.spiSize << 4
	binary.LittleEndian.PutUint16(buf[offChipID:], o.chipID)
	binary.LittleEndian.PutUint32(buf[offDescMagic:], descMagicWord)
	copy(buf[offVersion:offVersion+32], o.version)
	copy(buf[offProject:offProject+32], o.project)
	copy(buf[offTime:offTime+16], o.clock)
	copy(buf[offDate:offDate+16], o.date)
	copy(buf[offIDFVer:offIDFVer+32], o.idfVer)
	sum := sha256.Sum256([]byte(o.elfSeed))
	copy(buf[offElfSHA256:offElfSHA256+32], sum[:])
	return buf
}

// TestParseIdentity_KnownProfiles 覆盖 AC-1 的三份镜像：chip / flash_mb / model_key /
// version / elf_sha256 全部与镜像一致；并证明 s3 与 s3p 的 elf_sha256 不同
// （这正是唯一键必须用 elf_sha256 而不是 (model_key,version) 的原因，方案 §5.5）。
func TestParseIdentity_KnownProfiles(t *testing.T) {
	s3 := buildImage(imageOpts{chipID: 0x0009, spiSize: 4, version: "2.8.0", elfSeed: "s3-n16"})
	s3p := buildImage(imageOpts{chipID: 0x0009, spiSize: 4, version: "2.8.0", elfSeed: "s3p-n16"})

	cases := []struct {
		name     string
		header   []byte
		chip     string
		flashMB  uint16
		modelKey string
	}{
		{"c6-n8", buildImage(imageOpts{chipID: 0x000D, spiSize: 3}), ChipC6, 8, "esp32c6-n8"},
		{"c6-n16", buildImage(imageOpts{chipID: 0x000D, spiSize: 4}), ChipC6, 16, "esp32c6-n16"},
		{"s3-n16", s3, ChipS3, 16, "esp32s3-n16"},
		{"s3p-n16", s3p, ChipS3, 16, "esp32s3-n16"}, // model_key 不含 p
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			id, err := Parse(tc.header, int64(len(tc.header)))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if id.Chip != tc.chip {
				t.Fatalf("chip = %q, want %q", id.Chip, tc.chip)
			}
			if id.FlashMB != tc.flashMB {
				t.Fatalf("flash_mb = %d, want %d", id.FlashMB, tc.flashMB)
			}
			if id.ModelKey() != tc.modelKey {
				t.Fatalf("model_key = %q, want %q", id.ModelKey(), tc.modelKey)
			}
			if id.Version != "2.8.0" {
				t.Fatalf("version = %q, want %q", id.Version, "2.8.0")
			}
			if id.Project != ProjectCollector {
				t.Fatalf("project = %q, want %q", id.Project, ProjectCollector)
			}
			if id.IDFVersion != "v6.1" {
				t.Fatalf("idf_ver = %q, want %q", id.IDFVersion, "v6.1")
			}
			// date(16) + " " + time(16) 原文，不做时区解析（方案 §5.2）。
			if id.BuiltAtRaw != "Oct  5 2026 07:37:54" {
				t.Fatalf("built_at_raw = %q, want %q", id.BuiltAtRaw, "Oct  5 2026 07:37:54")
			}
			if len(id.ElfSHA256) != 64 {
				t.Fatalf("elf_sha256 = %q (len %d), want 64 hex chars", id.ElfSHA256, len(id.ElfSHA256))
			}
			if _, err := hex.DecodeString(id.ElfSHA256); err != nil {
				t.Fatalf("elf_sha256 %q is not hex: %v", id.ElfSHA256, err)
			}
			if id.SizeBytes != int64(len(tc.header)) {
				t.Fatalf("size_bytes = %d, want %d", id.SizeBytes, len(tc.header))
			}
		})
	}

	idS3, err := Parse(s3, int64(len(s3)))
	if err != nil {
		t.Fatalf("Parse s3: %v", err)
	}
	idS3P, err := Parse(s3p, int64(len(s3p)))
	if err != nil {
		t.Fatalf("Parse s3p: %v", err)
	}
	if idS3.ElfSHA256 == idS3P.ElfSHA256 {
		t.Fatal("s3 and s3p images produced the same elf_sha256; the unique key would collapse them")
	}
}

// TestParseIdentity_ErrorCodes 覆盖 AC-5：每条拒绝都必须带精确机器码。
func TestParseIdentity_ErrorCodes(t *testing.T) {
	cases := []struct {
		name   string
		header []byte
		size   int64
		code   string
	}{
		{"empty", nil, 0, CodeTruncated},
		{"one byte", []byte{0xE9}, 1, CodeTruncated},
		{"truncated at 0x30", buildImage(imageOpts{})[:0x30], 0x30, CodeTruncated},
		{"bad magic", func() []byte { b := buildImage(imageOpts{}); b[offMagic] = 0; return b }(), -1, CodeNotImage},
		{"bad desc magic", func() []byte {
			b := buildImage(imageOpts{})
			binary.LittleEndian.PutUint32(b[offDescMagic:], 0xDEADBEEF)
			return b
		}(), -1, CodeTruncated},
		{"project mismatch", buildImage(imageOpts{project: "other_project"}), -1, CodeProjectMismatch},
		{"chip unsupported", buildImage(imageOpts{chipID: 0xFFFF}), -1, CodeChipUnsupported},
		{"spi_size 0", buildImage(imageOpts{spiSize: 1}), -1, CodeFlashUnsupported},
		{"spi_size 2", buildImage(imageOpts{spiSize: 2}), -1, CodeFlashUnsupported},
		{"spi_size 2 (4MB)", buildImage(imageOpts{spiSize: 2}), -1, CodeFlashUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			size := tc.size
			if size < 0 {
				size = int64(len(tc.header))
			}
			_, err := Parse(tc.header, size)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			if got := Code(err); got != tc.code {
				t.Fatalf("error code = %q, want %q (err: %v)", got, tc.code, err)
			}
		})
	}
}

// TestParseIdentity_TrimsNulPaddedFields 固定「按字段宽度截断 + NUL 截断 + trim」的口径：
// version/project 字段是 [32]byte，短串后面全是 0。
func TestParseIdentity_TrimsNulPaddedFields(t *testing.T) {
	header := buildImage(imageOpts{version: "2.8.0-rc1"})
	id, err := Parse(header, int64(len(header)))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if id.Version != "2.8.0-rc1" {
		t.Fatalf("version = %q, want %q", id.Version, "2.8.0-rc1")
	}
	if strings.ContainsRune(id.Project, 0) {
		t.Fatalf("project carries NUL bytes: %q", id.Project)
	}
}

func TestReadIdentity(t *testing.T) {
	header := buildImage(imageOpts{chipID: 0x0009, spiSize: 4})
	id, err := ReadIdentity(bytes.NewReader(header), int64(len(header)))
	if err != nil {
		t.Fatalf("ReadIdentity: %v", err)
	}
	if id.Chip != ChipS3 || id.FlashMB != 16 {
		t.Fatalf("identity = %+v, want esp32s3/16MB", id)
	}

	if _, err := ReadIdentity(bytes.NewReader(header[:0x10]), 0x10); Code(err) != CodeTruncated {
		t.Fatalf("short reader: code = %q, want %q", Code(err), CodeTruncated)
	}
	// 长度声明短于头部：即便 reader 有字节也必须拒（调用方给的是文件真实大小）。
	if _, err := ReadIdentity(bytes.NewReader(header), 0x10); Code(err) != CodeTruncated {
		t.Fatalf("short size: code = %q, want %q", Code(err), CodeTruncated)
	}
}

func TestModelKey(t *testing.T) {
	if got := ModelKey(ChipC6, 8); got != "esp32c6-n8" {
		t.Fatalf("ModelKey = %q", got)
	}
	if got := ModelKey("", 8); got != "" {
		t.Fatalf("ModelKey with empty chip = %q, want \"\"", got)
	}
	if got := ModelKey(ChipS3, 0); got != "" {
		t.Fatalf("ModelKey with zero flash = %q, want \"\"", got)
	}
	var nilID *Identity
	if got := nilID.ModelKey(); got != "" {
		t.Fatalf("nil Identity ModelKey = %q", got)
	}
}

func TestCanonicalChip(t *testing.T) {
	for in, want := range map[string]string{
		"esp32s3": ChipS3, " ESP32S3 ": ChipS3, "s3": ChipS3,
		"esp32c6": ChipC6, "c6": ChipC6, "esp32": "", "": "",
	} {
		if got := CanonicalChip(in); got != want {
			t.Fatalf("CanonicalChip(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSlotCapacity(t *testing.T) {
	if cap, ok := SlotCapacity(8); !ok || cap != 0x380000 {
		t.Fatalf("SlotCapacity(8) = (%d, %v), want (0x380000, true)", cap, ok)
	}
	if cap, ok := SlotCapacity(16); !ok || cap != 0x680000 {
		t.Fatalf("SlotCapacity(16) = (%d, %v), want (0x680000, true)", cap, ok)
	}
	if _, ok := SlotCapacity(4); ok {
		t.Fatal("SlotCapacity(4) must be unknown")
	}
}

func TestParseNodeHardware(t *testing.T) {
	cases := []struct {
		name    string
		plat    string
		model   string
		chip    string
		flashMB uint16
		psram   string
	}{
		// 现行固件（app_state.c:generate_model_name）：PSRAM 版带 p，无 PSRAM 版不带。
		{"current s3 no psram", "esp32s3", "esp32s3", ChipS3, 0, PSRAMNo},
		{"current s3p", "esp32s3", "esp32s3p", ChipS3, 0, PSRAMYes},
		{"current c6", "esp32c6", "esp32c6", ChipC6, 0, PSRAMNo},
		// 未来 profile 串（H1 结论：型号是编译目标时会把容量写进串）。
		{"profile c6-n8", "", "esp32c6-n8", ChipC6, 8, PSRAMNo},
		{"profile s3p-n16", "", "s3p-n16", ChipS3, 16, PSRAMYes},
		{"profile s3_n16 underscore", "", "s3_n16", ChipS3, 16, PSRAMNo},
		// model 缺失时退回 platform；两者都不认识则不猜。
		{"platform fallback", "ESP32C6", "", ChipC6, 0, PSRAMNo},
		{"unknown", "ESP32", "", "", 0, ""},
		{"empty", "", "", "", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chip, flashMB, psram := ParseNodeHardware(tc.plat, tc.model)
			if chip != tc.chip || flashMB != tc.flashMB || psram != tc.psram {
				t.Fatalf("ParseNodeHardware(%q,%q) = (%q,%d,%q), want (%q,%d,%q)",
					tc.plat, tc.model, chip, flashMB, psram, tc.chip, tc.flashMB, tc.psram)
			}
		})
	}
}
