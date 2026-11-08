package api

// device_history_downsample.go — max_points 的**下推降采样**（2026-11-08）。
//
// ============ 为什么做这个 ============
//
// 生产实测（device 14，24h）：
//
//	无 max_points     7.08 s / 439.7 MB   ← 439.7 MB 是**物化出来的切片**
//	max_points=100    3.22 s /    19 KB
//
// 同样的解码量，多花的 3.9 s 全在「把 2.46M 行组装成 models.UnifiedData 再序列化」。
// 而 max_points=100 的请求把这 439.7 MB 造出来之后立刻扔掉 99.996%。
//
// 本文件把抽样**推到组装之前**：只组装被抽中的那 ~100 行。
//
// ============ 为什么不是「每字段各自抽样」============
//
// 那样会改变抽样结果（每个字段各取自己的第 N 个点），而对外表现是「点数/首尾点变了」。
// **必须与现有 downsampleUnifiedData 的输出逐点一致**，所以这里精确复刻它的规则：
//
//	step = len(data)/maxPoints；若 step < 2 则 step = 2
//	保留 data[0]，然后每隔 step 取一个（i < len(data)-1），最后保留 data[len(data)-1]
//
// 区别只在于：**用 k 路归并逐点前进，命中选中下标时才物化**，而不是先把 2.46M 行全造出来。
//
// ============ 为什么需要归并而不是简单切片 ============
//
// 最终行序是 (timestamp, sensor_name)，而解码结果是**按字段分组**的 map。
// 全局第 i 行必须按合并后的顺序数出来，所以要对各字段的有序序列做 k 路归并
// （k = 字段数，约 29；每步 O(log k)）。

import (
	"container/heap"
	"context"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// sensorPoint 指向 byName 里某个字段的第 idx 个点。
type sensorPoint struct {
	name string
	idx  int
}

// mergeHeap 是 k 路归并的最小堆，键为 (timestamp, name) —— 与最终排序键一致。
type mergeHeap struct {
	byName map[string][]HistoryPoint
	items  []sensorPoint
}

func (h *mergeHeap) Len() int { return len(h.items) }

func (h *mergeHeap) Less(i, j int) bool {
	a, b := h.items[i], h.items[j]
	ta, tb := h.byName[a.name][a.idx].Timestamp, h.byName[b.name][b.idx].Timestamp
	if !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return a.name < b.name
}

func (h *mergeHeap) Swap(i, j int) { h.items[i], h.items[j] = h.items[j], h.items[i] }

func (h *mergeHeap) Push(x any) { h.items = append(h.items, x.(sensorPoint)) }

func (h *mergeHeap) Pop() any {
	old := h.items
	n := len(old)
	it := old[n-1]
	h.items = old[:n-1]
	return it
}

// frameHistoryRowsDownsampled 与 frameHistoryRows 语义相同，但当 maxPoints > 0
// 且总点数超过它时，**只组装被抽中的行**。
//
// 返回 (rows, ok)；ok=false 的语义与 frameHistoryRows 完全一致（交回裸 SQL）。
func frameHistoryRowsDownsampled(
	ctx context.Context, db *gorm.DB, deviceID uint, sensorName string,
	start, end time.Time, maxPoints int,
) ([]models.UnifiedData, bool) {
	byName, units, ok := decodeHistoryByName(ctx, db, deviceID, sensorName, start, end)
	if !ok {
		return nil, false
	}

	total := 0
	for _, pts := range byName {
		total += len(pts)
	}
	// 与 downsampleUnifiedData 保持同一判定：不需要抽样时，完整物化（行为完全不变）。
	if maxPoints <= 0 || total <= maxPoints {
		return assembleAllRows(byName, units, deviceID), true
	}

	// 精确复刻 downsampleUnifiedData 的下标规则。
	step := total / maxPoints
	if step < 2 {
		step = 2
	}
	selected := make(map[int]struct{}, maxPoints+2)
	selected[0] = struct{}{}
	for i := step; i < total-1; i += step {
		selected[i] = struct{}{}
	}
	selected[total-1] = struct{}{}

	out := make([]models.UnifiedData, 0, len(selected))
	h := &mergeHeap{byName: byName}
	for name := range byName {
		if len(byName[name]) > 0 {
			h.items = append(h.items, sensorPoint{name: name, idx: 0})
		}
	}
	heap.Init(h)

	for i := 0; h.Len() > 0; i++ {
		it := heap.Pop(h).(sensorPoint)
		if err := ctx.Err(); err != nil {
			return nil, false
		}
		if _, want := selected[i]; want {
			out = append(out, buildHistoryRow(byName, units, deviceID, it))
		}
		if next := it.idx + 1; next < len(byName[it.name]) {
			heap.Push(h, sensorPoint{name: it.name, idx: next})
		}
	}
	return out, true
}
