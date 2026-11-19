package buildinfo

// buildinfo_test.go — 构建身份的取值契约（方案 §4.3 / §6.5 / AC-18）。
//
// 这里刻意不 mock 时间/文件：被测的就是"嵌入的 release.json + 未注入的 ldflags"
// 这个干净 clone 的真实形态。唯一会改包级变量的是 SetCommit 用例（defer 复原）。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmbeddedReleaseJSONSchema(t *testing.T) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(embeddedRelease, &raw); err != nil {
		t.Fatalf("release.json 是非法 JSON（干净 clone 必须能 build）: %v", err)
	}
	for _, key := range []string{"version", "commit", "build_time", "notes"} {
		if _, ok := raw[key]; !ok {
			t.Errorf("release.json 缺少契约字段 %q（§6.5 的 schema 只有这四个）", key)
		}
	}
	var notes []string
	if err := json.Unmarshal(raw["notes"], &notes); err != nil {
		t.Errorf("notes 必须是字符串数组: %v", err)
	}
	if strings.TrimSpace(Version()) == "" {
		t.Error("version 不能为空（缺失时也应回退到 devVersion）")
	}
}

// TestGettersAgreeWithEmbeddedManifest 钉住"getter 与嵌入清单同源"：
// 测试进程没有 -ldflags 注入，因此 commit/build_time 的期望值就是清单值。
func TestGettersAgreeWithEmbeddedManifest(t *testing.T) {
	var m releaseManifest
	if err := json.Unmarshal(embeddedRelease, &m); err != nil {
		t.Fatalf("unmarshal release.json: %v", err)
	}

	wantCommit := normalizeCommit(m.Commit)
	if wantCommit == "" {
		wantCommit = UnknownCommit
	}
	if got := Commit(); got != wantCommit {
		t.Errorf("Commit()=%q want %q（未注入 ldflags 时应回退到清单）", got, wantCommit)
	}

	wantID := DevBuildID
	if wantCommit != UnknownCommit {
		wantID = wantCommit
		if len(wantID) > buildIDLength {
			wantID = wantID[:buildIDLength]
		}
	}
	if got := BuildID(); got != wantID {
		t.Errorf("BuildID()=%q want %q", got, wantID)
	}

	if got, want := BuildTime(), strings.TrimSpace(m.BuildTime); got != want {
		t.Errorf("BuildTime()=%q want %q", got, want)
	}

	wantNotes := normalizeNotes(m.Notes)
	gotNotes := Notes()
	if len(gotNotes) != len(wantNotes) {
		t.Fatalf("Notes()=%v want %v", gotNotes, wantNotes)
	}
}

// TestDevDefaultIdentity 干净 clone 的身份：没有 commit ⇒ build_id=dev、commit=unknown。
//
// 为什么单独钉：dev 默认 release.json 若被误填 commit，本地/CI 里所有"我最新"的
// 判断都会拿到一个假 SHA，前端横幅则永远不出现（两边 build id 相同）。
func TestDevDefaultIdentity(t *testing.T) {
	if got := Commit(); got != UnknownCommit {
		t.Fatalf("干净 clone 的 commit=%q want %q（dev 默认清单必须留空）", got, UnknownCommit)
	}
	if got := BuildID(); got != DevBuildID {
		t.Fatalf("干净 clone 的 build_id=%q want %q", got, DevBuildID)
	}
	if got := BuildTime(); got != "" {
		t.Fatalf("干净 clone 的 build_time=%q want 空串（未知不得编造时间）", got)
	}
	if Notes() == nil {
		t.Fatal("Notes() 返回 nil：空 notes 必须是 []，否则前端 length 判断分叉")
	}
	if len(Notes()) != 0 {
		t.Fatalf("dev 默认 notes 必须为空: %v", Notes())
	}
}

// TestSetCommitDerivesBuildID：注入完整 SHA 后 build_id 取前 7 位（与前端同源同长）。
func TestSetCommitDerivesBuildID(t *testing.T) {
	prev := injectedCommit
	defer func() { injectedCommit = prev }()

	const full = "a1b2c3d9e8f7a6b5c4d3e2f1a0b9c8d7e6f5a4b3"
	SetCommit(full)
	if got := Commit(); got != full {
		t.Errorf("Commit()=%q want 全量 SHA %q（对账要比全量）", got, full)
	}
	if got := BuildID(); got != "a1b2c3d" {
		t.Errorf("BuildID()=%q want %q", got, "a1b2c3d")
	}
}

// TestSetCommitIgnoresMissingValues 未注入/占位值不得覆盖已知来源。
func TestSetCommitIgnoresMissingValues(t *testing.T) {
	prev := injectedCommit
	defer func() { injectedCommit = prev }()

	SetCommit("deadbeefcafe")
	for _, ignored := range []string{"", "  ", "unknown", "UNKNOWN", "dev"} {
		SetCommit(ignored)
		if got := Commit(); got != "deadbeefcafe" {
			t.Errorf("SetCommit(%q) 覆盖了已知 commit: %q", ignored, got)
		}
	}
}

// TestReleaseNotesContract 把「notes ≤10 条、每条 ≤120 字」变成**源码期**门禁。
//
// 为什么在 Go 侧再钉一遍（scripts/verify-release.sh 已经校验）：verify 是发布/CI
// 的一个步骤，漏跑就没人拦；而 `go test ./...` 是每份代码的默认门禁。校验两份：
//   - 嵌进二进制的那份（干净 clone 下的 dev 默认值 / 镜像内被 deploy 清单覆盖后的值）；
//   - 仓库里的 deploy/release.json 模板 —— 它在构建时会被覆盖进 embed，因此它才是
//     "未来会进二进制"的那一份，必须在源码期就用同一套上限卡住（AC-18 的"构建期拒绝"）。
//
// 长度按**字符**数（runes）而不是字节：中文 1 字 = 3 字节，按字节算会把 40 个汉字
// 判成超限，与"每条 ≤120 字"的产品语义不符。
func TestReleaseNotesContract(t *testing.T) {
	assertNotesContract(t, "embedded release.json", Notes())

	repoManifest := filepath.Join("..", "..", "..", "deploy", "release.json")
	raw, err := os.ReadFile(repoManifest)
	if err != nil {
		t.Fatalf("读不到发布清单模板 %s: %v（它是 embed 与对账的唯一真源，不得删除）", repoManifest, err)
	}
	var m releaseManifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("deploy/release.json 是非法 JSON: %v", err)
	}
	if strings.TrimSpace(m.Version) == "" {
		t.Error("deploy/release.json 的 version 不能为空")
	}
	assertNotesContract(t, "deploy/release.json", normalizeNotes(m.Notes))
}

func assertNotesContract(t *testing.T, label string, notes []string) {
	t.Helper()
	const maxNotes = 10
	const maxRunes = 120
	if notes == nil {
		t.Errorf("%s: notes 为 nil（必须恒数组）", label)
		return
	}
	if len(notes) > maxNotes {
		t.Errorf("%s: notes %d 条，超过上限 %d（AC-18）", label, len(notes), maxNotes)
	}
	for i, n := range notes {
		if strings.TrimSpace(n) == "" {
			t.Errorf("%s: notes[%d] 是空串（构建期应剔除，运行期不截断）", label, i)
		}
		if got := utf8.RuneCountInString(n); got > maxRunes {
			t.Errorf("%s: notes[%d] 有 %d 字，超过上限 %d（AC-18）", label, i, got, maxRunes)
		}
	}
}

// TestSetBuildTimeInjection：注入生效，空串不覆盖（与 SetCommit 同语义）。
//
// 为什么要单独钉：-X 指向不存在的符号时链接器**静默忽略**，注入断链的典型表现
// 就是 build_time 永久为空而没有任何报错。至少让"空串不覆盖"这条语义有人守着。
func TestSetBuildTimeInjection(t *testing.T) {
	prev := injectedBuildTime
	defer func() { injectedBuildTime = prev }()

	const ts = "2026-11-18T06:12:00Z"
	SetBuildTime(ts)
	if got := BuildTime(); got != ts {
		t.Fatalf("BuildTime()=%q want %q", got, ts)
	}
	SetBuildTime("  ")
	if got := BuildTime(); got != ts {
		t.Fatalf("空串覆盖了已注入的 build_time: %q", got)
	}
}

// TestNormalizeNotes：trim 与剔空串；输入全空时返回**非 nil** 空切片。
func TestNormalizeNotes(t *testing.T) {
	got := normalizeNotes([]string{"  第一行  ", "", "   ", "第二行"})
	if len(got) != 2 || got[0] != "第一行" || got[1] != "第二行" {
		t.Fatalf("normalizeNotes=%v want [第一行 第二行]", got)
	}
	if empty := normalizeNotes(nil); empty == nil {
		t.Fatal("normalizeNotes(nil) 返回 nil：JSON 会变成 null，前端判断分叉")
	}
	if b, err := json.Marshal(normalizeNotes(nil)); err != nil || string(b) != "[]" {
		t.Fatalf("空 notes 的 JSON=%s err=%v want []", b, err)
	}
}
