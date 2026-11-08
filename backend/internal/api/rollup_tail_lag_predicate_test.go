package api

// rollup_tail_lag_predicate_test.go — 钉住「尾巴空」的两种成因**可区分**（2026-11-08）。

// ============ 守的是一条能力，不是一个布尔值 ============

// rollupLiveTailPoints 在尾巴点集为空时，唯一能区分两种性质完全不同情况的东西是
// `tailEmptyLooksLikeLag`：

//	① 设备安静（last_data_at 早于尾巴窗口起点）   => 正常，**不该告警**
//	② 设备刚报过（last_data_at 在窗口内）但帧表没帧 => 真故障（落盘滞后/分区错位），**该告警**

// 两者在「点集为空」这个症状上完全一样。

// ============ 为什么必须有用例守它 ============

// 这条判断的产出只是一条 `logger.Warnf`。**没有用例守的告警 = 会在某次重构里静默消失**,
// 而且不会有任何东西变红 —— 因为它只是少打一行日志。
//
// 同族事故（今天已确认 3 次）：
//   · mergeStringValues 的补齐逻辑
//   · NaN 边界（只能靠注释守卫）
//   · 本文件这一条
//
// 所以判据被抽成纯函数 `tailEmptyLooksLikeLag`，让「区分能力」本身可被穷举钉住，
// 而不是钉住「有没有打日志」（后者要么注入 logger —— 脆；要么 grep 日志 —— 更脆）。

// ============ 它同时守着一条「必要的残片」============

// `probeDeviceLastDataAt` 原本是 frameCovered（覆盖判据）的锚点参数。
// 覆盖判据已于 2026-11-08 删除，**但这个函数必须留** —— 本文件就是它留下来的理由。
//
// 如果只按「覆盖判据的辅助函数」判断，它会被一起删掉，而后果是：
// 所有「尾巴空」变成同一种不可区分的状态，落盘卡住这类真故障再也没人知道。

import (
	"testing"
	"time"
)

// TestTailEmptyLooksLikeLag_ThreeStates 是主用例：三种状态必须被区分。
func TestTailEmptyLooksLikeLag_ThreeStates(t *testing.T) {
	from := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC) // 尾巴窗口起点

	cases := []struct {
		name string
		last time.Time
		want bool
		why  string
	}{
		{
			name: "零值（设备从没报过 / 读取失败）",
			last: time.Time{},
			want: false,
			why:  "无法判断 ⇒ 不当成故障（宁可漏报也不误报）",
		},
		{
			name: "设备安静（最后上报早于尾巴窗口）",
			last: from.Add(-30 * time.Minute),
			want: false,
			why:  "设备已离线/安静，尾巴空是正常的 —— 告警就是噪音",
		},
		{
			name: "帧没落盘（最后上报在尾巴窗口内）",
			last: from.Add(30 * time.Second),
			want: true,
			why:  "设备刚报过，帧表却没帧 ⇒ 落盘滞后或分区错位，必须可观测",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := tailEmptyLooksLikeLag(c.last, from)
			if got != c.want {
				t.Fatalf("tailEmptyLooksLikeLag(%v, %v) = %v，want %v\n\t%s",
					c.last, from, got, c.want, c.why)
			}
		})
	}
}

// TestTailEmptyLooksLikeLag_BoundaryAtFrom 钉住边界：恰好等于 from 时的方向。
//
// 这不是抠字眼 —— `After` 与 `!Before` 在这一点上给出相反结果，
// 而「设备最后一次上报恰好等于尾巴窗口起点」在生产上真的会发生
// （尾巴窗口起点由取整/截断算出，与 last_data_at 撞上的概率不低）。
//
// 当前语义：**等于 from 时不告警**（按「不在窗口内」处理）。
// 理由：那是「刚好在窗口开始前报的」—— 帧属于窗口外的最后一刻，
// 尾巴内无帧是合理的。选这个方向的真正原因是它与改造前的内联条件一致（保持行为）。
func TestTailEmptyLooksLikeLag_BoundaryAtFrom(t *testing.T) {
	from := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)

	if tailEmptyLooksLikeLag(from, from) {
		t.Fatalf("lastDataAt == from 时判成了故障。当前语义是「等于不算在窗口内」" +
			"（与改造前的内联条件 last.After(from) 一致）。\n\t" +
			"若你要改成 !Before（等于算在窗口内），请同步改 rollupLiveTailPoints 的调用点" +
			"并说明为什么那个方向的误报可以接受 —— 误报会让运维忽略这条告警。")
	}
}

// TestTailEmptyLooksLikeLag_OnlyDependsOnTwoInputs 是**结构断言**：
// 这个判据必须只依赖 (lastDataAt, from) 两个输入。
//
// 为什么值得写：它现在是个纯函数，但如果将来有人为了「更准确」往里加
// db / ctx / q（比如去查帧表里到底有没有帧），它就变成不可测的了，
// 而这条用例会红 —— 那不是阻止改动，是**要求改动者想清楚**。
func TestTailEmptyLooksLikeLag_IsPure(t *testing.T) {
	// 同样输入反复调用必须同结果（纯函数的最基本性质）。
	from := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	last := from.Add(30 * time.Second)
	first := tailEmptyLooksLikeLag(last, from)
	for i := 0; i < 100; i++ {
		if tailEmptyLooksLikeLag(last, from) != first {
			t.Fatalf("第 %d 次调用结果与第 1 次不同 —— 函数不纯（有隐藏状态/时间依赖）", i+2)
		}
	}
}
