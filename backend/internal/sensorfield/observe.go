package sensorfield

import (
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// obsKey 是观测维度：(device_id, sensor_name)（方案 §4.2 投影表主键）。
type obsKey struct {
	deviceID uint
	name     string
}

// obsCounter 是单个 (device, field) 的进程内观测计数。
//
// 热路径契约（方案 §4.2）：Observe 只做 sync.Map 命中 + atomic，无 DB、无锁竞争；
// 稳态零分配（Load 命中路径不分配，单位只存一次）。
type obsCounter struct {
	unit      atomic.Pointer[string]
	firstNano atomic.Int64 // 首次观测（事件时间，取最小）
	lastNano  atomic.Int64 // 末次观测（事件时间，取最大）
	total     atomic.Int64 // 进程内累计条数（诊断用）
	pending   atomic.Int64 // 自上次成功 Flush 以来的增量（落库用）
}

// obsStore 是观测计数器集合。
type obsStore struct {
	m sync.Map // map[obsKey]*obsCounter
}

// newObsStore 创建空的观测计数器集合。
func newObsStore() *obsStore { return &obsStore{} }

// storeMin 原子地写入“更小值”（0 表示未设置）。
func storeMin(addr *atomic.Int64, v int64) {
	for {
		old := addr.Load()
		if old != 0 && old <= v {
			return
		}
		if addr.CompareAndSwap(old, v) {
			return
		}
	}
}

// storeMax 原子地写入“更大值”。
func storeMax(addr *atomic.Int64, v int64) {
	for {
		old := addr.Load()
		if v <= old {
			return
		}
		if addr.CompareAndSwap(old, v) {
			return
		}
	}
}

// observe 记录一次观测（纯内存）。deviceID<=0 或 name 为空时静默忽略：
// unified_data.device_id 可能为空（DS-14 的另一面），这些行不能进投影。
func (s *obsStore) observe(deviceID uint, name, unit string, at time.Time) {
	if deviceID == 0 || name == "" {
		return
	}
	if at.IsZero() {
		at = time.Now()
	}
	k := obsKey{deviceID: deviceID, name: name}
	v, ok := s.m.Load(k)
	if !ok {
		v, _ = s.m.LoadOrStore(k, &obsCounter{})
	}
	c := v.(*obsCounter)
	ns := at.UnixNano()
	storeMin(&c.firstNano, ns)
	storeMax(&c.lastNano, ns)
	c.total.Add(1)
	c.pending.Add(1)
	if unit != "" && c.unit.Load() == nil {
		u := unit
		c.unit.CompareAndSwap(nil, &u)
	}
}

// flushEntry 是待落库观测（含计数器引用，成功后扣减 pending）。
type flushEntry struct {
	obs     Observation
	counter *obsCounter
}

// collectPending 收集自上次成功 Flush 以来的增量（按 device_id, name 稳定排序）。
func (s *obsStore) collectPending() []flushEntry {
	var out []flushEntry
	s.m.Range(func(k, v any) bool {
		key := k.(obsKey)
		c := v.(*obsCounter)
		pending := c.pending.Load()
		if pending <= 0 {
			return true
		}
		unit := ""
		if up := c.unit.Load(); up != nil {
			unit = *up
		}
		out = append(out, flushEntry{
			counter: c,
			obs: Observation{
				DeviceID:  key.deviceID,
				Name:      key.name,
				Unit:      unit,
				FirstSeen: time.Unix(0, c.firstNano.Load()),
				LastSeen:  time.Unix(0, c.lastNano.Load()),
				Samples:   pending,
			},
		})
		return true
	})
	sort.Slice(out, func(i, j int) bool {
		if out[i].obs.DeviceID != out[j].obs.DeviceID {
			return out[i].obs.DeviceID < out[j].obs.DeviceID
		}
		return out[i].obs.Name < out[j].obs.Name
	})
	return out
}

// payload 把 flushEntry 转成对外的 Observation 切片。
func flushEntriesToObservations(entries []flushEntry) []Observation {
	obs := make([]Observation, len(entries))
	for i := range entries {
		obs[i] = entries[i].obs
	}
	return obs
}

// ackPending 在 sink 成功后扣减已落库的增量（并发 Observe 的新增量不受影响）。
func ackPending(entries []flushEntry) {
	for i := range entries {
		entries[i].counter.pending.Add(-entries[i].obs.Samples)
	}
}
