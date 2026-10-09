package nodemgr

import (
	"encoding/json"
	"strings"
	"testing"
)

// ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
// buildConfigWarnings 把设备上报的降级通道 id 转成用户可见的告警。
//
// 语义要点（本测试逐条锁住）：
//  1. 空输入 => "[]"（**必须**是空数组而非 ""）：这样每次成功回执都覆盖旧值，
//     上一次的降级提示不会永久粘住。
//  2. message 是**面向用户的中文**：前端原样显示，不自己拼文案。
//  3. channel_id 带进 JSON，前端可用作 key。
func TestBuildConfigWarnings(t *testing.T) {
	t.Run("空输入返回空数组（不是空串，也不是 null）", func(t *testing.T) {
		got := buildConfigWarnings(nil)
		if got != "[]" {
			t.Fatalf("空输入应返回 \"[]\"，得到 %q —— 若返回 \"\" 或 \"null\"，"+
				"前端 Array.isArray 判断会失败，且旧告警不会被清掉", got)
		}
		if got2 := buildConfigWarnings([]uint64{}); got2 != "[]" {
			t.Fatalf("空切片应返回 \"[]\"，得到 %q", got2)
		}
	})

	t.Run("单个降级通道产出可读告警", func(t *testing.T) {
		var out []map[string]any
		if err := json.Unmarshal([]byte(buildConfigWarnings([]uint64{49})), &out); err != nil {
			t.Fatalf("产出不是合法 JSON: %v", err)
		}
		if len(out) != 1 {
			t.Fatalf("want 1 warning, got %d", len(out))
		}
		w := out[0]
		if w["code"] != "dma_degraded" {
			t.Errorf("code: got %v, want dma_degraded", w["code"])
		}
		if w["channel_id"] != float64(49) {
			t.Errorf("channel_id: got %v, want 49", w["channel_id"])
		}
		msg, _ := w["message"].(string)
		if msg == "" {
			t.Fatal("message 不能为空 —— 前端原样显示，空串会渲染出空白行")
		}
		// 面向用户的文案必须说清三件事：是什么、会怎样、怎么办
		for _, want := range []string{"49", "DMA", "降级", "功能正常"} {
			if !strings.Contains(msg, want) {
				t.Errorf("message 应包含 %q，实际：%s", want, msg)
			}
		}
	})

	t.Run("多个降级通道逐条产出", func(t *testing.T) {
		var out []map[string]any
		if err := json.Unmarshal([]byte(buildConfigWarnings([]uint64{11, 22})), &out); err != nil {
			t.Fatalf("产出不是合法 JSON: %v", err)
		}
		if len(out) != 2 {
			t.Fatalf("want 2 warnings, got %d", len(out))
		}
		if out[0]["channel_id"] != float64(11) || out[1]["channel_id"] != float64(22) {
			t.Errorf("通道顺序应保持，得到 %v / %v", out[0]["channel_id"], out[1]["channel_id"])
		}
	})
}
