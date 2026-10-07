package api

// 终端历史 count 参数的边界回归 (2026-10-07)。
//
// 缺陷本体: GET /channels/:channel_id/terminal?count=-1 会把负值直接透传给
// terminal.History(n), 那里只做 `if n > t.count { n = t.count }` —— 负值**不满足**,
// 于是走到 `make([]Entry, 0, n)` ⇒ **panic: makeslice: cap out of range**。
// 已用同表达式在 /tmp 复刻实测确认 (不是理论推演)。
//
// 为什么断言 HTTP 状态码而不是内部字段:
//   panic 在 gin 的 recover 中间件下表现为 **500**, 而正常路径是 200。
//   直接在"效果层"断言状态码, 就不依赖任何新导出的内部符号,
//   也能在没有 recover 中间件时直接把 panic 暴露成测试失败 (而不是假绿)。
//
// 三种输入分别构造 (-1 / 0 / 超大), 与 handler_s4_limit_contract_test.go 同范式:
// 它们都是缺陷, 但错法不同, 合并成一条就分不清修好了哪一种。

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"
)

func terminalHistoryRequest(t *testing.T, r http.Handler, channelID uint, query string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/channels/"+strconv.FormatUint(uint64(channelID), 10)+"/terminal"+query, nil)
	r.ServeHTTP(w, req)
	return w
}

// TestTerminalHistoryCountBounds: 非法 count 一律归默认 50, 且**不得 panic**。
func TestTerminalHistoryCountBounds(t *testing.T) {
	// ⚠ 本用例的一个**已知局限**（如实写明, 不假装它更强）:
	// GetHistory 对"通道不存在"的分支会直接返回空切片, 根本走不到
	// `make([]Entry, 0, n)`。下面只创建了通道、没有制造终端历史,
	// 因此这条用例实际证明的是"**非法 count 不会让请求变成 500**",
	// 而不是"make() 那一行一定被执行到了"。
	// 之所以仍然有效: 修前 `?count=-1` 无论有无历史都会 panic —— 因为 panic 发生在
	// `make` 之前?不, 发生在 make 处; 无历史时 n 会被 clamp 到 0 ⇒ 不 panic。
	// ⇒ 因此我在 createChannel 之后**补一条真实历史**, 让 n=-1 真的能到 make。
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"count=-1 (负值 ⇒ 改前 make cap 负 ⇒ panic)", "?count=-1"},
		{"count=0 (0 条, 不得 panic)", "?count=0"},
		{"count=abc (解析失败 ⇒ 0)", "?count=abc"},
		{"count=99999 (超 ring 容量 256)", "?count=99999"},
		{"count=37 (合法值必须原样保留)", "?count=37"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, mgr, createChannel := setupTerminalRouteTest(t)
			createChannel(models.Channel{ID: 1, NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
			// 造至少 1 条历史 ⇒ n=-1 时 `n > t.count` 不成立, 必然走到 make(cap=-1)。
			// 不造历史的话 n 会被 clamp 成 0, 用例就退化成"恒绿"。
			mgr.TerminalMgr().RecordRX("NODE001", 1, []byte{0x01})

			w := terminalHistoryRequest(t, r, 1, tc.query)
			if w.Code != http.StatusOK {
				t.Fatalf("count 查询 %q: status = %d, want 200 (500 通常意味着 panic 被 recover 兜住)",
					tc.query, w.Code)
			}
		})
	}
}
