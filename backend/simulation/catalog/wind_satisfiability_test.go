//go:build simulation

// 本文件**长期**守护 SIM-WIND-005 依赖的可满足性不变量（不是临时探针）。
//
// 被守护的契约（构造式实现在 wind.go 的 windFindCrossWindow / windCrossWindow*）：
//  1. 「含当前时刻的跨零点窗口」对 1440 个分钟点**全部可构造**，一个都不能失败；
//  2. 「不含当前时刻的跨零点窗口」**仅** now == 23:59 数学上无解
//     （不含 ⇔ end <= now < start <= 1439 ⇒ now <= 1438），其余 1439 分钟必须可构造。
//     ⇒ 场景里任何「构造失败就跳过」都是缺陷；唯一允许的跳过点由判据精确给出；
//  3. 构造出的窗口必须真的跨零点（start > end），且按 windContains 的**独立语义**
//     判定结果与请求的 inside 一致（不是拿构造器自己的输出当期望值）；
//  4. 运行期稳定性：场景创建规则后要等真实 60s ticker 才可能求值，窗口不能在下一个
//     tick 就翻转 —— 含 now 的窗口必须至少再含 60 分钟；不含 now 的窗口在 23:57 之前
//     必须至少再排除 1 分钟（23:58/23:59 的排除期天然 <= 1 分钟，见下）。
//
// 回归价值（本文件存在的直接原因）：旧实现把 startOffset 与 width 都以 60 分钟步进，
// 在 11:00–11:59 构造不出「含 now」的窗口、在 23:00–23:59 构造不出「不含 now」的窗口，
// 于是 SIM-WIND-005 每天这两个小时必然变红。本文件会直接抓住该缺陷（穷举到分钟）。
//
// 为什么必须穷举而不是抽样：该缺陷是**按小时周期性**出现的，一天里绝大多数时刻抽样
// 都会通过 —— 2026-09-15 11:11 的那次全量仿真正是踩在无解小时里才暴露出来。
package catalog

import (
	"testing"
	"time"
)

// windMinutesPerDay 是一天的分钟数（跨零点窗口的判定域）。
const windMinutesPerDay = 1440

// windAtMinute 构造当天第 nowMin 分钟的时刻（只用到时/分，与 windMinutes 对应）。
func windAtMinute(nowMin int) time.Time {
	return time.Date(2026, 9, 15, nowMin/60, nowMin%60, 0, 0, time.Local)
}

// windAssertConstructedWindow 用**独立语义**校验一个构造出的窗口：
// 必须真的跨零点（start > end），且对 now 的判定等于请求的 inside。
func windAssertConstructedWindow(t *testing.T, nowMin int, inside bool, start, end string, now time.Time) {
	t.Helper()
	startAt, err := time.Parse("15:04", start)
	if err != nil {
		t.Errorf("now=%s inside=%v：窗口起点 %q 不是 HH:MM: %v", windHHMM(nowMin), inside, start, err)
		return
	}
	endAt, err := time.Parse("15:04", end)
	if err != nil {
		t.Errorf("now=%s inside=%v：窗口终点 %q 不是 HH:MM: %v", windHHMM(nowMin), inside, end, err)
		return
	}
	if !startAt.After(endAt) {
		t.Errorf("now=%s inside=%v：构造出的窗口 %s–%s 不跨零点（起点必须晚于终点）",
			windHHMM(nowMin), inside, start, end)
		return
	}
	got, err := windContains(start, end, now)
	if err != nil {
		t.Errorf("now=%s inside=%v：windContains(%s, %s) 报错: %v", windHHMM(nowMin), inside, start, end, err)
		return
	}
	if got != inside {
		t.Errorf("now=%s inside=%v：窗口 %s–%s 的实际判定是 %v（应为 %v）",
			windHHMM(nowMin), inside, start, end, got, inside)
	}
}

// TestWindCrossWindowSatisfiability 穷举 1440 分钟 × 两个 inside 取值，钉死
// 「可构造性 + 语义正确 + 运行期稳定性」三件事。
func TestWindCrossWindowSatisfiability(t *testing.T) {
	var unsolvableIn, unsolvableOut []int
	for nowMin := 0; nowMin < windMinutesPerDay; nowMin++ {
		now := windAtMinute(nowMin)

		// ---- inside=true：恒有解 ----
		start, end, ok := windFindCrossWindow(now, true)
		if !ok {
			unsolvableIn = append(unsolvableIn, nowMin)
		} else {
			windAssertConstructedWindow(t, nowMin, true, start, end, now)
			// 稳定性：含 now 的窗口必须至少再含 60 分钟（场景要等一个真实 ticker）。
			// 23:5x 起跑时靠晨间段接续，因此这里对 1440 分钟全部成立。
			next := windAtMinute((nowMin + 60) % windMinutesPerDay)
			if got, err := windContains(start, end, next); err != nil || !got {
				t.Errorf("now=%s：窗口 %s–%s 在 60 分钟后（%s）已不含 now（got=%v err=%v）—— "+
					"场景等待 ticker 时会假红", windHHMM(nowMin), start, end,
					windHHMM((nowMin+60)%windMinutesPerDay), got, err)
			}
		}

		// ---- inside=false：仅 23:59 无解 ----
		start, end, ok = windFindCrossWindow(now, false)
		wantUnsolvable := windCrossWindowUnsolvable(nowMin)
		if ok == wantUnsolvable {
			t.Errorf("now=%s：构造成功=%v 与无解判据=%v 不一致（两者必须互为否定）",
				windHHMM(nowMin), ok, wantUnsolvable)
		}
		if !ok {
			unsolvableOut = append(unsolvableOut, nowMin)
			continue
		}
		windAssertConstructedWindow(t, nowMin, false, start, end, now)
		// 稳定性：不含 now 的窗口必须至少再排除 1 分钟。
		// 例外：now ∈ {23:58, 23:59} 时排除期天然 <= 1 分钟 —— 当天剩不下更多分钟，
		// 任何「不含该分钟」的空档上边界都不可能晚于 23:59。
		if nowMin <= windMinutesPerDay-3 {
			next := windAtMinute(nowMin + 1)
			if got, err := windContains(start, end, next); err != nil || got {
				t.Errorf("now=%s：窗口 %s–%s 在 1 分钟后（%s）就已含 now（got=%v err=%v）—— "+
					"场景断言 out 规则不触发时会假红", windHHMM(nowMin), start, end,
					windHHMM(nowMin+1), got, err)
			}
		}
	}

	// 无解集合必须**精确**等于判据给出的那一个点：多一个就说明构造器漏了分支，
	// 少一个就说明判据过宽（会让场景在该跳过的时刻硬跑）。
	if len(unsolvableIn) != 0 {
		t.Errorf("「含当前时刻的跨零点窗口」应当对 1440 分钟全部可构造，实际无解 %d 个: %v",
			len(unsolvableIn), windHHMMList(unsolvableIn))
	}
	if len(unsolvableOut) != 1 || unsolvableOut[0] != windMinutesPerDay-1 {
		t.Errorf("「不含当前时刻的跨零点窗口」的无解分钟应当恰为 [23:59]，实际 %d 个: %v",
			len(unsolvableOut), windHHMMList(unsolvableOut))
	}
}

// TestWindCrossWindowGroundTruth 是**独立于构造器**的真值：
// 直接穷举全部分钟粒度跨零点窗口（start > end，共 1440×1439/2 ≈ 103 万个），
// 用区间差分标记算出「每个分钟点是否存在含它 / 不含它的跨零点窗口」。
//
// 为什么必须有它：只测构造器会陷入自证 —— 构造器漏掉一个分支时，"它自己说无解"
// 与"真的无解"无法区分，跳过逻辑就有了伪装成合理边界的空间。本测试给出与构造器
// 无关的真值，再断言构造器的可构造集合与真值**逐分钟相等**。
//
// 复杂度：标记是 O(1) 的区间差分，总计 O(1440²) ≈ 100 万次操作，毫秒级。
func TestWindCrossWindowGroundTruth(t *testing.T) {
	// diffIn / diffOut 是长度 1441 的差分数组（下标 1440 是哨兵）。
	// 对窗口 (s, e)（s > e）：含 now 的区间是 [0,e) ∪ [s,1440)，不含 now 的是 [e,s)。
	var diffIn, diffOut [windMinutesPerDay + 1]int
	for s := 1; s < windMinutesPerDay; s++ {
		for e := 0; e < s; e++ {
			diffIn[0]++
			diffIn[e]--
			diffIn[s]++
			diffIn[windMinutesPerDay]--
			diffOut[e]++
			diffOut[s]--
		}
	}

	var missingIn, missingOut []int
	runIn, runOut := 0, 0
	for minute := 0; minute < windMinutesPerDay; minute++ {
		runIn += diffIn[minute]
		runOut += diffOut[minute]
		if runIn <= 0 {
			missingIn = append(missingIn, minute)
		}
		if runOut <= 0 {
			missingOut = append(missingOut, minute)
		}
	}

	// 真值 1：任何分钟都落在**某个**跨零点窗口里（00:01–23:59 尤其显然）。
	if len(missingIn) != 0 {
		t.Errorf("真值异常：这些分钟不含在任何跨零点窗口中 %v（预期为空）", windHHMMList(missingIn))
	}
	// 真值 2：只有 23:59 落在**所有**跨零点窗口里。
	if len(missingOut) != 1 || missingOut[0] != windMinutesPerDay-1 {
		t.Errorf("真值异常：不存在「不含该分钟」的跨零点窗口的分钟应为 [23:59]，实际 %v",
			windHHMMList(missingOut))
	}

	// 构造器必须与真值逐分钟一致 —— 这是"跳过 23:59"这个裁决的唯一依据。
	for nowMin := 0; nowMin < windMinutesPerDay; nowMin++ {
		now := windAtMinute(nowMin)
		_, _, okIn := windFindCrossWindow(now, true)
		if okIn != (len(missingIn) == 0) {
			t.Errorf("now=%s：含 now 的窗口可构造=%v，与真值（存在=%v）不符",
				windHHMM(nowMin), okIn, len(missingIn) == 0)
			break
		}
		_, _, okOut := windFindCrossWindow(now, false)
		wantSolvable := nowMin != windMinutesPerDay-1
		if okOut != wantSolvable {
			t.Errorf("now=%s：不含 now 的窗口可构造=%v，但真值说该分钟%s存在这样的窗口",
				windHHMM(nowMin), okOut, map[bool]string{true: "", false: "不"}[wantSolvable])
			break
		}
	}
}

// windHHMMList 把分钟数列表渲染成 "HH:MM" 便于失败信息定位。
func windHHMMList(minutes []int) []string {
	out := make([]string, 0, len(minutes))
	for _, minute := range minutes {
		out = append(out, windHHMM(minute))
	}
	return out
}
