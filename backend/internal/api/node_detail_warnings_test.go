package api

import (
	"encoding/json"
	"testing"

	"ehome/backend/internal/models"
)

// ⚠⚠ 2026-10-09（真机验证发现的缺陷，§215）：
// 节点详情必须把 config_warnings 作为**数组**返回，而不是 JSON 字符串。
//
// 真机现象：API 返回 "config_warnings": "[{\"code\":...}]"（字符串），
//
//	而前端写的是 Array.isArray(raw) 判断 ⇒ **永远不成立** ⇒ 提示永不显示。
//
// ⚠ 为什么前端单测没抓到：那个测试的 mock 里手写了**真数组**，
//
//	与后端真实响应的形状对不上 —— "mock 形状与真实响应对不上"的假绿。
//	⇒ 形状必须在**产生它的那一端**（后端）验证。
func TestNodeDetailParsesConfigWarnings(t *testing.T) {
	t.Run("JSON 字符串被解析成数组", func(t *testing.T) {
		node := models.Node{
			NodeID: "n1",
			// 与真实 DB 里存的形状一致（buildConfigWarnings 的产物）
			ConfigWarnings: `[{"code":"dma_degraded","channel_id":49,"message":"通道 49 降级"},` +
				`{"code":"dma_degraded","channel_id":50,"message":"通道 50 降级"}]`,
		}
		got := parseConfigWarnings(node)
		if len(got) != 2 {
			t.Fatalf("want 2 warnings, got %d", len(got))
		}
		first, ok := got[0].(map[string]any)
		if !ok {
			t.Fatalf("元素应是对象，实得 %T", got[0])
		}
		if first["code"] != "dma_degraded" {
			t.Errorf("code: got %v", first["code"])
		}
		if first["channel_id"] != float64(49) {
			t.Errorf("channel_id: got %v", first["channel_id"])
		}
	})

	t.Run("空字符串返回空数组而非 null", func(t *testing.T) {
		got := parseConfigWarnings(models.Node{NodeID: "n2"})
		if got == nil {
			t.Fatal("不能返回 nil —— JSON 会变成 null，前端 Array.isArray(null) 为 false")
		}
		if len(got) != 0 {
			t.Fatalf("want 0, got %d", len(got))
		}
		b, _ := json.Marshal(got)
		if string(b) != "[]" {
			t.Fatalf("应序列化为 []，实得 %s", b)
		}
	})

	t.Run("非法 JSON 不 panic，回退空数组", func(t *testing.T) {
		got := parseConfigWarnings(models.Node{NodeID: "n3", ConfigWarnings: "{不是 JSON"})
		if len(got) != 0 {
			t.Fatalf("非法 JSON 应回退空数组，实得 %d 项", len(got))
		}
	})

	t.Run("响应体整体序列化后 config_warnings 是数组", func(t *testing.T) {
		// 这是**形状**断言：模拟 Success(c, nodeDetailResponse{...}) 的产物。
		resp := nodeDetailResponse{
			Node:           models.Node{NodeID: "n4", ConfigWarnings: `[{"code":"dma_degraded","channel_id":7,"message":"m"}]`},
			ConfigWarnings: parseConfigWarnings(models.Node{ConfigWarnings: `[{"code":"dma_degraded","channel_id":7,"message":"m"}]`}),
		}
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(b, &decoded); err != nil {
			t.Fatal(err)
		}
		w, ok := decoded["config_warnings"].([]any)
		if !ok {
			t.Fatalf("config_warnings 必须是 JSON 数组，实得 %T（值：%v）—— "+
				"若是 string，前端 Array.isArray 判断会失败，提示永不显示",
				decoded["config_warnings"], decoded["config_warnings"])
		}
		if len(w) != 1 {
			t.Fatalf("want 1, got %d", len(w))
		}
		// 且不得出现两个 config_warnings 键（嵌入 + 外层同名会冲突）
		if n := countJSONKey(t, b, "config_warnings"); n != 1 {
			t.Fatalf("config_warnings 键出现 %d 次，应为 1", n)
		}
	})
}

func countJSONKey(t *testing.T, b []byte, key string) int {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	_, ok := m[key]
	if !ok {
		return 0
	}
	return 1
}
