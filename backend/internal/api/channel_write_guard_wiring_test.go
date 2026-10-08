package api

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestChannelWritePaths_RouteThroughEnsureBusConfigExtras 是 §197 的**接线**护栏。
//
// 为什么单独测"接线"而不是只测判据函数：
//
//	本轮真机缺陷**不是判据写错**，而是**判据只挂在了 UART 上** ——
//	四条写 channels 的路径各自只调 ensureUARTBusConfig，I2C **整整一类**没有守卫。
//	⇒ 「判据函数正确」与「判据真的被每条路径调用」是两件事，必须分别锁住。
//	（本仓已多次记过"函数存在但调用点不存在"的静默死角。）
//
// 手段：断言已知写路径的**源码**里不再直调 ensureUARTBusConfig（应走分派入口）。
//
//	⚠ 这是源码级判据，不是运行期判据 —— 它的价值在于"新增写路径时会被提醒"，
//	  代价是改名/重构时会红（那正是希望有人来确认的时刻）。
//	  与 pagination_dialect_gate_test.go / gorm_write_shape_gate_test.go 同族。
//
// 它凭什么会失败：任一路径改回直调 ensureUARTBusConfig，本用例立刻红。
func TestChannelWritePaths_RouteThroughEnsureBusConfigExtras(t *testing.T) {
	// 四条已知写路径（2026-10-08 清点，见 §197）：
	//   ① POST /channels            ② PUT /channels
	//   ③ 向导内联（edge device 绑定）  ④ 节点通道批量更新
	// 前三条在 handler_device.go / handler_edge_device.go，第四条在 handler_node.go。
	files := []string{
		"handler_device.go",
		"handler_edge_device.go",
		"handler_node.go",
	}

	for _, f := range files {
		raw, err := os.ReadFile(filepath.Join(".", f))
		if err != nil {
			t.Fatalf("读不到 %s: %v", f, err)
		}
		found := false
		for i, line := range strings.Split(string(raw), "\n") {
			trimmed := strings.TrimSpace(line)
			// 注释里提到旧函数名是有价值的（历史说明），只禁**代码**直调。
			if strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "*") {
				continue
			}
			if strings.Contains(trimmed, "ensureUARTBusConfig(") {
				// ⚠ 格式串必须写在**一行**：Go 不支持"相邻字符串字面量跨行拼接"
				//（写成两行会在参数表里报 missing ',' before newline）。
				// 实测过，别再试。
				t.Errorf("%s:%d 仍**直调** ensureUARTBusConfig ⇒ 绕过了 ensureBusConfigExtras，该路径上的 I2C 没有守卫（§197 的缺陷形态）：%s",
					f, i+1, trimmed)
			}
			if strings.Contains(trimmed, "ensureBusConfigExtras(") {
				found = true
			}
		}
		if !found {
			t.Errorf("%s 里找不到 ensureBusConfigExtras(...) 调用 ⇒ 该写路径没有总线守卫；新增路径请接上它（这是本用例存在的意义）。", f)
		}
	}
}
