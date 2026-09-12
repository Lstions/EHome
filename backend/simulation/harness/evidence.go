//go:build simulation

package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// ---------- 子进程日志缓冲 ----------

// LogBuffer 全量承接服务子进程的 stdout/stderr（设计 §5.1 第 5 步）。
// 它是"失败可诊断"的基石：HTTP 断言失败时附日志尾部，启动凭据也从这里解析。
type LogBuffer struct {
	mu      sync.Mutex
	lines   []string
	partial string
}

func NewLogBuffer() *LogBuffer { return &LogBuffer{} }

// Write 实现 io.Writer。zap 的 console encoder 以换行结束每行，
// 但仍按"可能跨多次 Write"处理：残留半行留在 partial，补齐后再入行。
func (b *LogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.partial += string(p)
	for {
		idx := strings.IndexByte(b.partial, '\n')
		if idx < 0 {
			break
		}
		b.lines = append(b.lines, strings.TrimRight(b.partial[:idx], "\r"))
		b.partial = b.partial[idx+1:]
	}
	// 防止无换行的异常输出把内存吃光。
	if len(b.partial) > 1<<20 {
		b.lines = append(b.lines, b.partial)
		b.partial = ""
	}
	return len(p), nil
}

// Snapshot 返回当前已知的全部行（含未换行的尾部）。
func (b *LogBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]string, len(b.lines))
	copy(out, b.lines)
	if b.partial != "" {
		out = append(out, b.partial)
	}
	return out
}

// Full 返回完整日志文本，用于落盘到 .logs/simulation/<runid>/server.log。
func (b *LogBuffer) Full() string {
	return strings.Join(b.Snapshot(), "\n") + "\n"
}

// Tail 返回最后 n 行，用于失败时打印。
func (b *LogBuffer) Tail(n int) string {
	lines := b.Snapshot()
	if n > 0 && len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// Contains 报告日志中是否已出现子串。
func (b *LogBuffer) Contains(sub string) bool {
	for _, line := range b.Snapshot() {
		if strings.Contains(line, sub) {
			return true
		}
	}
	return false
}

// Find 返回第一条匹配 re 的行。
func (b *LogBuffer) Find(re *regexp.Regexp) (string, bool) {
	for _, line := range b.Snapshot() {
		if re.MatchString(line) {
			return line, true
		}
	}
	return "", false
}

// WaitForMatch 轮询等待日志出现匹配行。这是 harness 唯一允许的
// "等待子进程状态"手段——轮询而非 sleep 同步（设计 §3 原则 3）。
func (b *LogBuffer) WaitForMatch(re *regexp.Regexp, timeout time.Duration) ([]string, error) {
	deadline := time.Now().Add(timeout)
	for {
		if line, ok := b.Find(re); ok {
			return re.FindStringSubmatch(line), nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待日志匹配 %s 超时 %s；当前日志尾部:\n%s",
				re.String(), timeout, b.Tail(40))
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// ---------- 证据与 summary.json ----------

// KV 是一条有序证据。用切片而非 map，保证 summary.json 里证据顺序
// 与场景书写顺序一致，便于人工比对（设计 §8 证据产物要求）。
type KV struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
}

// ScenarioRecord 是 summary.json 中的一个场景条目（设计 §8）。
type ScenarioRecord struct {
	ID         string `json:"id"`
	Title      string `json:"title"`
	Domain     string `json:"domain"`
	Status     string `json:"status"` // running | passed | failed
	DurationMS int64  `json:"duration_ms"`
	Evidence   []KV   `json:"evidence,omitempty"`
	Failure    string `json:"failure,omitempty"`

	startedAt time.Time
}

// Summary 是一次仿真运行的完整证据文件。
type Summary struct {
	RunID      string            `json:"run_id"`
	Database   string            `json:"database"`
	BaseURL    string            `json:"base_url"`
	MQTTAddr   string            `json:"mqtt_addr"`
	ServerBin  string            `json:"server_binary"`
	StartedAt  time.Time         `json:"started_at"`
	FinishedAt time.Time         `json:"finished_at"`
	Status     string            `json:"status"` // passed | failed
	Scenarios  []*ScenarioRecord `json:"scenarios"`
	Notes      []KV              `json:"notes,omitempty"`

	path string
}

// record 追加/更新一条场景记录。
func (s *Summary) record(rec *ScenarioRecord) {
	for i, existing := range s.Scenarios {
		if existing.ID == rec.ID {
			s.Scenarios[i] = rec
			return
		}
	}
	s.Scenarios = append(s.Scenarios, rec)
}

// write 落盘 summary.json。任何失败都通过 err 返回，由调用方决定
// 是否让测试失败（收尾阶段的失败不应掩盖场景本身的失败）。
func (s *Summary) write() error {
	if s.path == "" {
		return fmt.Errorf("summary 路径为空")
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return fmt.Errorf("创建证据目录失败: %w", err)
	}
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 summary 失败: %w", err)
	}
	body = append(body, '\n')
	if err := os.WriteFile(s.path, body, 0o644); err != nil {
		return fmt.Errorf("写入 summary 失败: %w", err)
	}
	return nil
}

// normalizeEvidence 把任意证据值折叠成可 JSON 序列化的形态。
// 无法序列化的值退化为 fmt 文本，避免"记证据"本身把测试弄挂。
func normalizeEvidence(value any) any {
	if value == nil {
		return nil
	}
	if _, err := json.Marshal(value); err == nil {
		return value
	}
	return fmt.Sprintf("%v", value)
}
