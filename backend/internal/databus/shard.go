package databus

import (
	"hash/fnv"
	"strconv"
)

// shardConsumer fans one logical consumer out into N independent bus workers.
//
// Why: heavy consumers (sensor_parser, db_persist) run one bus worker per
// registered consumer, so a single registration processes events strictly
// serially. That capped the whole ingest pipeline at one core regardless of
// machine size. Registering N shardConsumer instances keeps the bus core
// unchanged: each shard gets its own mailbox and worker goroutine, and the
// shard key guarantees per-device ordering.
//
// Ordering contract: all events for one DeviceID hash to the same shard, so
// a device is still processed strictly in fanout (arrival) order. Cross-device
// ordering was never guaranteed and still is not.
//
// The shard count is fixed for the process lifetime: changing N reassigns
// devices between shards, which would break in-flight per-device ordering.
type shardConsumer struct {
	inner  DataConsumer
	shard  int
	shards int
}

// NewShardConsumer wraps inner as shard i of n. Names must be unique per bus
// (bus.Register silently ignores duplicate names), hence the _shardN suffix.
func NewShardConsumer(inner DataConsumer, shard, shards int) DataConsumer {
	return &shardConsumer{
		inner:  inner,
		shard:  shard,
		shards: shards,
	}
}

func (s *shardConsumer) Name() string { return s.inner.Name() + "_shard" + strconv.Itoa(s.shard) }

func (s *shardConsumer) ShouldHandle(evt DataEvent) bool {
	if !s.inner.ShouldHandle(evt) {
		return false
	}
	return shardForDevice(evt.DeviceID, s.shards) == s.shard
}

func (s *shardConsumer) Handle(evt DataEvent) { s.inner.Handle(evt) }

// shardForDevice maps a node ID to a stable shard index via FNV-1a.
func shardForDevice(deviceID string, shards int) int {
	if shards <= 1 {
		return 0
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(deviceID))
	return int(h.Sum32() % uint32(shards))
}
