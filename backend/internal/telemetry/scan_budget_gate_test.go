package telemetry

// scan_budget_gate_test.go — 2026-11-11 生产事故的防复发门禁。
//
// 事故回顾：cmd/server/main.go 的 rollup 回填把 DecodeRangeAnchoredBounded 的
// 第 8 个参数（**扫描预算**）传成了 MaxDecodeFrames（32768，语义是「一次性解码
// 单批上限」）。生产 dev14 在 2026-11-08 15:57 ~ 11-10 05:21 之间没有关键帧，
// 回填 11-10 的每一段都要从 32 小时前的锚点预热 ≈ 118,922 帧 ⇒ 超预算 ⇒
// FramesTruncated>0 ⇒ 该单元标 partial ⇒ 长窗图表（24h/7d/30d）全部报「超过帧表
// 单次解码上限」，而底层 rollup 数据其实是齐的。
//
// 这两个常量只差一个后缀，选错在代码 review 时几乎看不出来，但后果是
// **用户可见的图表坏掉**。所以用断言把语义差异钉死。

import (
	"testing"
	"time"
)

// TestScanBudgetCoversWorstCaseAnchorSpan 锁住「扫描预算必须覆盖生产最坏锚点跨度」。
//
// 现场最坏形态：37 小时无关键帧（store.go 的注释里记录过）+ 6 小时请求窗口。
// 按 1 Hz 算需要 (37+6)*3600 = 154,800 帧。
func TestScanBudgetCoversWorstCaseAnchorSpan(t *testing.T) {
	const worstAnchorSpan = 43 * time.Hour             // 37h 无关键帧 + 6h 窗口
	needFrames := int64(worstAnchorSpan / time.Second) // @1 Hz
	if int64(MaxScanFrames) < needFrames {
		t.Fatalf("MaxScanFrames=%d 不足：最坏锚点跨度 %s @1Hz 需要 %d 帧；"+
			"调小它会让 rollup 回填在「关键帧稀疏」的设备上静默标 partial，"+
			"进而让长窗图表全部报错（2026-11-11 事故）",
			MaxScanFrames, worstAnchorSpan, needFrames)
	}
	t.Logf("MaxScanFrames=%d 覆盖 %s @1Hz（最坏形态 %s，余量 %.1fx）",
		MaxScanFrames, time.Duration(MaxScanFrames)*time.Second, worstAnchorSpan,
		float64(MaxScanFrames)/float64(needFrames))
}

// TestMaxDecodeFramesIsNotAScanBudget 钉住两个常量的语义边界：
// MaxDecodeFrames **不得**被当作扫描预算使用（它覆盖不了最坏锚点跨度）。
//
// 这条断言的作用不是「检查数值」，而是**把这次的错误用法写进测试** ——
// 下一次有人想「统一成一个常量」时，会先看到这里为什么不能。
func TestMaxDecodeFramesIsNotAScanBudget(t *testing.T) {
	const worstAnchorSpan = 43 * time.Hour
	needFrames := int64(worstAnchorSpan / time.Second)
	if int64(MaxDecodeFrames) >= needFrames {
		t.Fatalf("MaxDecodeFrames=%d 已能覆盖最坏锚点跨度 %s —— "+
			"如果这是有意扩容，请同步复核 main.go 的回填预算与本文档的事故记录",
			MaxDecodeFrames, worstAnchorSpan)
	}
	t.Logf("MaxDecodeFrames=%d 只覆盖 %s @1Hz（刻意小于最坏锚点跨度 %s）—— "+
		"它是单批解码上限，不是扫描预算",
		MaxDecodeFrames, time.Duration(MaxDecodeFrames)*time.Second, worstAnchorSpan)
}

// TestMaxScanFramesIsEightTimesDecodeFrames 锁住两者关系（store.go 的论证前提）。
func TestMaxScanFramesIsEightTimesDecodeFrames(t *testing.T) {
	if MaxScanFrames != 8*MaxDecodeFrames {
		t.Fatalf("MaxScanFrames=%d != 8*MaxDecodeFrames=%d；"+
			"store.go 的性能论证（72.8h vs 9.1h）依赖这个比例，改了要同步改注释",
			MaxScanFrames, 8*MaxDecodeFrames)
	}
}
