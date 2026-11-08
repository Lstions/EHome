package api

// rollup_oracle_fixture_test.go — rollup 重建结果对**生产 rollup payload** 的跨路径对拍。
//
// ============ 这个文件替代了什么 ============
//
// 原 TestRollupMatchesUnifiedForLongWindows 用 queryHistoryPoints（生产 SQL）做基准。
// unified_data 即将 DROP，且 queryHistoryPoints 本身要被删 —— 那条对拍会**跑不了**，
// 而跑不了在 CI 里通常是 skip，**skip 是绿的**。
//
// ============ 新基准为什么是**真独立**的 ============
//
// fixture 里的 oracle_rollup_1m 是**生产 telemetry_rollup_1m 的真实 payload**，
// 由 live collector（内存累加 60s flush）或 unified_data 回填写入。
//
// 三条路径互不依赖：
//
//	live collector  读解析器的实时样本，不读帧表
//	历史回填        读 unified_data，不读帧表
//	帧表重建        读 telemetry_frame 解码，不读前两者
//
// 所以「帧表重建的分钟统计 == 生产 rollup payload 的分钟统计」是一个**跨路径**证明，
// 而不是自洽。
//
// ============ ⚠ 为什么按**日期**筛而不按 source 列筛 ============
//
// telemetry_rollup_coverage.source 列**不可信**：写入有两个函数
// （MarkRollupCoverage 不写 source、MarkRollupCoverageWithSource 写），
// 实测 55 行 complete 里只有 2 行有 source，其余是空串。
//
// 而 2026-11-05 早于 L1 生效（2026-11-07），那天无论哪条写入路径，
// 样本都是**全字段**的 ⇒ 两条来源口径相同 ⇒ 可以混用。
//
// ============ 容差：按字段的 Step/2 ============
//
// 帧是量化存储（code = round((v-Min)/Step)），解码回来最多差 Step/2。
// 而生产 rollup 的统计量是用**原始值**算的。所以逐值比较的容差必须是该字段的 Step/2，
// 不是固定常数 —— 用固定 1e-9 会对 rsoc（Step=1）误报，用 1.0 会对 cell_voltage_1
// （Step=0.001）失去鉴别力。
//
// count 是**精确**的（样本数不受量化影响），所以单独断言。

import (
	"context"
	"encoding/base64"
	"sort"
	"testing"
	"time"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
)

// buildFrameDict 用 fixture 的 slot 定义 + 帧行 slot_id 构造字典。
//
// 见 history_oracle_fixture_test.go 的 fixtureDictionary（同一个理由：
// payload 的位流布局由生产那一版 slot 决定，不能伪造 ID，也不能用
// make(...,0,n)+copy 那种零长度拷贝）。
func buildFrameDict(t *testing.T) telemetry.SlotDictionary {
	t.Helper()
	f := loadOracleFixture(t)
	return buildFixtureDictionary(t, f)
}

// TestRollupMatchesOracleFixture20261105 用生产 rollup payload 验帧表重建。
func TestRollupMatchesOracleFixture20261105(t *testing.T) {
	f := loadOracleFixture(t)
	if len(f.OracleRollup) == 0 {
		t.Fatal("fixture 里没有 oracle_rollup_1m —— 生成脚本没导 rollup_win.csv，" +
			"或导出了但为空。不要改成 skip：这条用例是 rollup 唯一的跨路径证明")
	}

	db := setupOracleDB(t)
	dict := buildFixtureDictionary(t, f)
	seedOracleFrames(t, db, f)
	start, end := fixtureWindow(t, f)

	// 解码窗口内全部字段（一次解码服务所有字段，与生产批请求同路径）。
	points, _, err := telemetry.DecodeRangeAnchoredBounded(
		context.Background(), db, dict, f.DeviceID, start, end, nil, 24*time.Hour, 4_000_000)
	if err != nil {
		t.Fatalf("帧表解码失败: %v", err)
	}
	if len(points) == 0 {
		t.Fatal("帧表解码 0 点 —— fixture 缺锚点帧？(见 history_oracle_fixture_test.go 文件头)")
	}

	// 按 1 分钟桶聚合：avg/min/max/last/count（**生产 rollup 的口径**）。
	type stat struct {
		sum    float64
		min    float64
		max    float64
		last   float64
		lastTS time.Time
		count  int
	}
	byBucket := make(map[int64]map[string]*stat)
	for i := range points {
		bucket := points[i].TS.Truncate(time.Minute).Unix()
		m := byBucket[bucket]
		if m == nil {
			m = make(map[string]*stat)
			byBucket[bucket] = m
		}
		st := m[points[i].Name]
		if st == nil {
			st = &stat{min: points[i].Value, max: points[i].Value}
			m[points[i].Name] = st
		}
		st.sum += points[i].Value
		st.count++
		if points[i].Value < st.min {
			st.min = points[i].Value
		}
		if points[i].Value > st.max {
			st.max = points[i].Value
		}
		// 解码结果已按时间升序 ⇒ 后到者即桶内最后值。
		if !points[i].TS.Before(st.lastTS) {
			st.last = points[i].Value
			st.lastTS = points[i].TS
		}
	}

	// 逐分钟与生产 payload 比对。
	checked, worstRatio := 0, 0.0
	for _, or := range f.OracleRollup {
		bucketTS, err := time.Parse(time.RFC3339Nano, or.Bucket)
		if err != nil {
			t.Fatalf("fixture 桶时间解析失败 %q: %v", or.Bucket, err)
		}
		payload, err := base64.StdEncoding.DecodeString(or.Payload)
		if err != nil {
			t.Fatalf("%s payload base64 解码失败: %v", or.Bucket, err)
		}
		want, err := datalifecycle.DecodeRollupPayload(payload)
		if err != nil {
			t.Fatalf("%s RollupPayload 解码失败: %v", or.Bucket, err)
		}
		got := byBucket[bucketTS.Unix()]
		if len(got) != len(want) {
			t.Fatalf("%s 字段数 重建=%d 生产=%d", or.Bucket, len(got), len(want))
		}
		names := make([]string, 0, len(want))
		for n := range want {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, name := range names {
			w, g := want[name], got[name]
			sp, _ := telemetry.SpecFor(name)
			tol := sp.Step / 2
			if g == nil {
				t.Fatalf("%s 字段 %q 重建结果缺失（生产有）", or.Bucket, name)
			}
			if g.count != int(w.Count) {
				t.Fatalf("%s/%s count 重建=%d 生产=%d", or.Bucket, name, g.count, w.Count)
			}
			avg := g.sum / float64(g.count)
			for _, c := range []struct {
				label     string
				got, want float64
			}{
				{"avg", avg, w.Avg},
				{"min", g.min, w.Min},
				{"max", g.max, w.Max},
				{"last", g.last, w.Last},
			} {
				d := c.got - c.want
				if d < 0 {
					d = -d
				}
				if d > tol {
					t.Fatalf("%s/%s %s 重建=%v 生产=%v 差=%v 超过 Step/2=%v（量化容差）",
						or.Bucket, name, c.label, c.got, c.want, d, tol)
				}
				if tol > 0 && d/tol > worstRatio {
					worstRatio = d / tol
				}
			}
			checked++
		}
	}

	// 覆盖度自检：不能只比对了 1 个字段就绿。
	wantChecks := len(f.OracleRollup) * len(f.OracleAvg)
	if checked < wantChecks/2 {
		t.Fatalf("只比对了 %d 个 (分钟, 字段) 组合，期望接近 %d —— 比对范围不足", checked, wantChecks)
	}
	t.Logf("rollup 跨路径对拍通过：%d 分钟 × %d 字段 = %d 个组合，"+
		"最大偏差占量化容差(Step/2) 的 %.1f%%", len(f.OracleRollup), len(f.OracleAvg), checked, worstRatio*100)
}
