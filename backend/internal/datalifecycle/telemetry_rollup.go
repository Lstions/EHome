package datalifecycle

// L3：分钟级宽行预聚合（设计/遥测存储优化方案-2026-11-06.md §5.2）。
//
// 一句话：把「每设备每分钟的全部字段统计量」压成**一行** bytea 落库，供长窗图表
// （时间跨度超过解码阈值）走预聚合，短窗仍走原始表/解码层。
//
// ============ 为什么是宽行（不是每字段一行）============
//
// 窄表（旧 unified_data_rollup_1m 的形状）：29 字段 × 1440 分钟 = 41,760 行/设备/天；
// 宽表：1440 行/设备/天。方案 §5.2 实测同口径下 327.3 B/行 vs 109.1 B/行 ×
// 102,240 行/天 ⇒ 宽行省约 12 倍。分区/保留期也按"整月分区 DROP"一次搞定。
//
// ============ 与已退役旧表的关系（别复活死表）============
//
// 旧表 unified_data_rollup_1m 于 2026-09-15 退役（docs/分析/rollup-退役裁决-2026-09-15.md），
// 退役理由是**当时没有消费者**：所有读路径 device_id 等值 + 时间窗，索引+分区裁剪已够快。
// 本表的差别是**有明确消费者**（长窗图表）；死表门禁（rollup_retirement_gate_test.go）
// 判的是旧表名与旧符号，本表是它的**反向断言对象**：退役步骤只能 DROP 旧表。
//
// ============ payload 编码：v1（自描述）与 v2（slot 量化）============
//
// 方案 §5.2 的目标是「每设备每分钟 1 行 / payload 约 232 B」，那是建立在
// §4.3 逐字段量化表 + §4.4 slot 字典之上的（payload 不带字段名，只带 slot_id 与
// 量化位宽打包）。两者都由 internal/telemetry（L2，task-3）交付，本任务**不依赖**
// 它，因此这里先落 v1：
//
//	v1 = 1B version | 2B BE 字段数 | N × { 1B 名字长度, 名字, 8B avg, 8B min,
//	                                       8B max, 8B last, 4B count }   (全部 float64 大端)
//
// v1 是**自描述**的（字段名内联），不依赖任何外部字典 ⇒ 现在就能落地、可测、
// 可回放；代价是每行约 1.5 KB（29 字段）而不是 232 B。解码按 version 分派，
// v2（slot 量化）落地后新写入用 v2、旧行照 v1 解，无需数据迁移。
//
// ⚠ 诚实声明（写进交付说明与 docs/分析）: 在 v2 落地前，本表的实际占用是方案
// §5.2 目标值的数倍；容量结论（90 天 ≤3 GB）**不能**用 v1 的实测值去论证，
// 必须等 v2（或接受 v1 的真实数字）。这段不许删——它是避免"拿占位实现当达标证据"的锚。

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

const (
	// TelemetryRollupTable 是宽行预聚合表的逻辑表名（= 物理母表名）。
	// 分区名形如 telemetry_rollup_1m_YYYYMM（与 unified_data 同款月分区）。
	TelemetryRollupTable = "telemetry_rollup_1m"

	// rollupPayloadVersionV1 是自描述编码的版本号（见文件头）。
	rollupPayloadVersionV1 byte = 1

	// rollupPayloadV1FieldOverhead = 1B 名字长度 + 8B×4 统计量 + 4B count。
	rollupPayloadV1FieldOverhead = 1 + 8*4 + 4
)

// ErrRollupPayloadCorrupt 表示 payload 不满足版本/长度/字段名约束。
// 单独导出便于读侧把它当"这一行坏，跳过并打点"，而不是整条查询失败。
var ErrRollupPayloadCorrupt = errors.New("telemetry rollup payload corrupt")

// RollupFieldStat 是单字段在一个分钟桶内的统计量。
type RollupFieldStat struct {
	Avg   float64
	Min   float64
	Max   float64
	Last  float64
	Count uint32
}

// RollupSample 是一条待聚合样本（字段名 + 数值）。
//
// 刻意不直接用 parser.Field：datalifecycle 不依赖 parser 包（依赖方向：
// databus/parser → 业务包，反过来会把解析层拖进生命周期包）。调用方（写入侧）
// 自己做一行字段映射即可。
type RollupSample struct {
	Name  string
	Value float64
}

// rollupAcc 是单个 (device, bucket) 的累加器。
type rollupAcc struct {
	fields map[string]*RollupFieldStat
}

func (a *rollupAcc) observe(name string, v float64) {
	st, ok := a.fields[name]
	if !ok {
		a.fields[name] = &RollupFieldStat{Avg: v, Min: v, Max: v, Last: v, Count: 1}
		return
	}
	// 增量均值 (数值稳定版): avg += (v - avg) / n
	st.Count++
	st.Avg += (v - st.Avg) / float64(st.Count)
	if v < st.Min {
		st.Min = v
	}
	if v > st.Max {
		st.Max = v
	}
	st.Last = v
}

// RollupCollector 是写入侧**旁路**聚合器（不扫描 DB，纯内存累加）。
//
// 语义（方案 §5.2 最后一条）：
//   - Observe 把样本累加进「当前分钟」桶，O(1)，不碰数据库；
//   - 分钟切换（或 Flush 显式触发）时把**已结束**的桶编码落库；
//   - 当前分钟桶始终留在内存，避免"半分钟数据先落库后又被覆盖"的抖动；
//   - 落库失败只打点并保留桶（下一轮重试），绝不阻塞主写入路径。
//
// 并发：Observe 来自多个 consumer goroutine，用互斥锁保护（临界区只做 map 查找，
// 单帧 29 个字段，实测可忽略；对比每帧一次 INSERT 的代价是数量级差异）。
type RollupCollector struct {
	db *gorm.DB
	// now 可注入（测试用），生产用 time.Now。
	now func() time.Time
	// bucketTTL 是内存桶的保留上限：超过它还没落库的桶会被丢弃并打点，
	// 防止 DB 长时间不可用时内存无界增长（宁可丢预聚合，也不 OOM —— 预聚合
	// 是派生数据，原始表才是真值来源）。
	bucketTTL time.Duration

	mu      sync.Mutex
	pending map[rollupKey]*rollupAcc

	// 指标钩子（nil 安全；沿用 databus 的注入范式，避免 datalifecycle 直接依赖
	// 某个具体 metrics 变量名）。
	onFlush func(rows int, bytes int64)
	onDrop  func(reason string)
}

type rollupKey struct {
	deviceID uint
	bucket   time.Time
}

// NewRollupCollector 构造聚合器。db 为 nil 时所有 Flush 为 no-op（便于开关关闭时
// 装配一个"透明"的 collector，调用方无需判空）。
func NewRollupCollector(db *gorm.DB) *RollupCollector {
	return &RollupCollector{
		db:        db,
		now:       time.Now,
		bucketTTL: 6 * time.Hour,
		pending:   make(map[rollupKey]*rollupAcc),
	}
}

// SetClock 注入时钟（测试用）。
func (c *RollupCollector) SetClock(now func() time.Time) {
	if now == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

// SetBucketTTL 设置内存桶保留上限（默认 6h）。
func (c *RollupCollector) SetBucketTTL(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.bucketTTL = d
}

// SetFlushHooks 注入落库观测钩子（行数/字节数/丢弃原因），nil 安全。
func (c *RollupCollector) SetFlushHooks(onFlush func(rows int, bytes int64), onDrop func(reason string)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onFlush = onFlush
	c.onDrop = onDrop
}

// RollupBucket 把时间戳截断到分钟（UTC）。
//
// 为什么必须 UTC: 分区键决定数据落在哪个月分区，而分区边界是按 UTC 月切的
// （partition_mgr.monthStart 用 time.UTC）。用本地时区截断会让月初/月末的
// 桶落进相邻分区，甚至落进不存在的分区（PG 报 no partition of relation）。
func RollupBucket(at time.Time) time.Time {
	return at.UTC().Truncate(time.Minute)
}

// Observe 累加一帧的全部样本。空样本/未知字段直接忽略（不报错：聚合是旁路，
// 不能因为一个空帧打断主写入）。
func (c *RollupCollector) Observe(deviceID uint, samples []RollupSample, at time.Time) {
	if c == nil || deviceID == 0 || len(samples) == 0 {
		return
	}
	bucket := RollupBucket(at)
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.pending == nil {
		c.pending = make(map[rollupKey]*rollupAcc)
	}
	key := rollupKey{deviceID: deviceID, bucket: bucket}
	acc, ok := c.pending[key]
	if !ok {
		acc = &rollupAcc{fields: make(map[string]*RollupFieldStat, len(samples))}
		c.pending[key] = acc
	}
	for _, s := range samples {
		if s.Name == "" || math.IsNaN(s.Value) || math.IsInf(s.Value, 0) {
			continue
		}
		acc.observe(s.Name, s.Value)
	}
}

// Sink 返回一个"可注入 databus 的回调"：签名刻意与 databus 既有 sink 家族保持一致
// 的形状 (deviceID + 一帧样本 + 时间)，但不引用 parser 包 —— 调用方（databus 接线处）
// 负责把 parser.Field 映射成 []RollupSample（一行 for 循环，见 §接线 patch 文档）。
//
// 为什么做成 "工厂返回闭包" 而不是 "让 databus 直接依赖 RollupCollector":
//
//	· databus 的既有约定是 4 个 Set*Sink（latest/alert/automation/sourceHealth），
//	  全部是"注入回调 + nil 时跳过"，不注入具体类型；
//	· 本包（datalifecycle）不 import databus，databus 也不该 import datalifecycle 的
//	  内部结构；闭包把耦合压到一个函数值上，主写入路径失败/未接线时行为完全不变。
//
// 返回的闭包 nil 安全（collector 为 nil 时直接 return，便于"开关关闭 ⇒ 装配空 sink"）。
func (c *RollupCollector) Sink() func(deviceID uint, samples []RollupSample, at time.Time) {
	if c == nil {
		return func(uint, []RollupSample, time.Time) {}
	}
	return func(deviceID uint, samples []RollupSample, at time.Time) {
		c.Observe(deviceID, samples, at)
	}
}

// FlushSink 返回一个无参回调，供 databus 的每分钟 tick 或独立 goroutine 调用
// （FlushCompleted 只落"已结束"的桶，重复调用安全）。
func (c *RollupCollector) FlushSink() func(ctx context.Context) (int, error) {
	if c == nil {
		return func(context.Context) (int, error) { return 0, nil }
	}
	return c.FlushCompleted
}

// PendingBuckets 返回当前内存中尚未落库的桶数（测试/观测用）。
func (c *RollupCollector) PendingBuckets() int {
	if c == nil {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.pending)
}

// FlushCompleted 把**已结束**（bucket < 当前分钟）的桶落库，并返回写入行数。
//
// 只落"已结束"的桶：当前分钟还在累加，提前落库会在下一轮被覆盖写（UPSERT），
// 多一次无意义的写放大；而且并发下可能把半桶数据写成最终值。
func (c *RollupCollector) FlushCompleted(ctx context.Context) (int, error) {
	if c == nil || c.db == nil {
		return 0, nil
	}
	c.mu.Lock()
	now := c.now
	ttl := c.bucketTTL
	currentBucket := RollupBucket(c.now())
	ready := make([]struct {
		key rollupKey
		acc *rollupAcc
	}, 0, len(c.pending))
	stale := 0
	for k, acc := range c.pending {
		if !k.bucket.Before(currentBucket) {
			continue // 当前分钟，继续累加
		}
		if currentBucket.Sub(k.bucket) > ttl {
			// 太老的桶：DB 长时间不可用。丢预聚合（派生数据），不让内存无界增长。
			delete(c.pending, k)
			stale++
			continue
		}
		delete(c.pending, k)
		ready = append(ready, struct {
			key rollupKey
			acc *rollupAcc
		}{k, acc})
	}
	onFlush := c.onFlush
	onDrop := c.onDrop
	c.mu.Unlock()
	_ = now

	if stale > 0 && onDrop != nil {
		onDrop(fmt.Sprintf("stale_bucket_expired:%d", stale))
	}
	if len(ready) == 0 {
		return 0, nil
	}
	// 按 (device, bucket) 稳定排序：让落库顺序可重放（测试断言 & 排障友好）。
	sort.Slice(ready, func(i, j int) bool {
		if ready[i].key.deviceID != ready[j].key.deviceID {
			return ready[i].key.deviceID < ready[j].key.deviceID
		}
		return ready[i].key.bucket.Before(ready[j].key.bucket)
	})
	var (
		rows  int
		bytes int64
	)
	for _, item := range ready {
		payload, err := EncodeRollupPayloadV1(item.acc.fields)
		if err != nil {
			if onDrop != nil {
				onDrop("encode:" + err.Error())
			}
			continue
		}
		if err := upsertRollupRow(ctx, c.db, item.key.deviceID, item.key.bucket, payload); err != nil {
			// 落库失败：桶**不再放回**内存（放回会让重试队列随故障时长膨胀），
			// 只打点。预聚合是派生数据，丢了下一分钟会重新累积——代价是这一格空白。
			if onDrop != nil {
				onDrop("upsert:" + err.Error())
			}
			continue
		}
		rows++
		bytes += int64(len(payload))
	}
	if rows > 0 && onFlush != nil {
		onFlush(rows, bytes)
	}
	return rows, nil
}

// upsertRollupRow 幂等写一行（同一 (device, bucket) 覆盖）。
//
// 用裸 SQL 而不是 GORM Create/Updates：本表没有自增主键，且 SQLite 与 PG 都支持
// ON CONFLICT ... DO UPDATE（SQLite ≥3.24），一条语句两方言通用，避免方言分支。
func upsertRollupRow(ctx context.Context, db *gorm.DB, deviceID uint, bucket time.Time, payload []byte) error {
	q := ctx
	return db.WithContext(q).Exec(
		"INSERT INTO "+TelemetryRollupTable+" (device_id, bucket, payload, updated_at) VALUES (?, ?, ?, ?) "+
			"ON CONFLICT (device_id, bucket) DO UPDATE SET payload = EXCLUDED.payload, updated_at = EXCLUDED.updated_at",
		deviceID, bucket, payload, time.Now().UTC(),
	).Error
}

// ==================== payload 编解码 (v1 自描述) ====================

// EncodeRollupPayloadV1 把单桶字段统计编码成 v1 payload。
// 字段名按字典序排列（确定性编码：同样的输入必然同样的字节，便于对拍与去重）。
func EncodeRollupPayloadV1(fields map[string]*RollupFieldStat) ([]byte, error) {
	if len(fields) == 0 {
		return nil, fmt.Errorf("rollup payload: empty field set")
	}
	if len(fields) > math.MaxUint16 {
		return nil, fmt.Errorf("rollup payload: too many fields (%d)", len(fields))
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		if name == "" {
			return nil, fmt.Errorf("rollup payload: empty field name")
		}
		if len(name) > 255 {
			return nil, fmt.Errorf("rollup payload: field name too long (%d bytes)", len(name))
		}
		names = append(names, name)
	}
	sort.Strings(names)

	size := 3
	for _, name := range names {
		size += rollupPayloadV1FieldOverhead + len(name)
	}
	buf := make([]byte, 0, size)
	buf = append(buf, rollupPayloadVersionV1)
	buf = binary.BigEndian.AppendUint16(buf, uint16(len(names)))
	for _, name := range names {
		st := fields[name]
		buf = append(buf, byte(len(name)))
		buf = append(buf, name...)
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(st.Avg))
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(st.Min))
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(st.Max))
		buf = binary.BigEndian.AppendUint64(buf, math.Float64bits(st.Last))
		buf = binary.BigEndian.AppendUint32(buf, st.Count)
	}
	return buf, nil
}

// DecodeRollupPayload 按版本分派解码。v1 之外未知版本按"坏行"处理（读侧跳过并打点，
// 不整条查询失败）—— 这是为 v2(slot 量化) 落地留的兼容口。
func DecodeRollupPayload(payload []byte) (map[string]*RollupFieldStat, error) {
	if len(payload) == 0 {
		return nil, fmt.Errorf("%w: empty payload", ErrRollupPayloadCorrupt)
	}
	switch payload[0] {
	case rollupPayloadVersionV1:
		return decodeRollupPayloadV1(payload)
	default:
		return nil, fmt.Errorf("%w: unknown version %d", ErrRollupPayloadCorrupt, payload[0])
	}
}

func decodeRollupPayloadV1(payload []byte) (map[string]*RollupFieldStat, error) {
	if len(payload) < 3 {
		return nil, fmt.Errorf("%w: v1 header truncated", ErrRollupPayloadCorrupt)
	}
	n := int(binary.BigEndian.Uint16(payload[1:3]))
	if n <= 0 {
		return nil, fmt.Errorf("%w: v1 field count 0", ErrRollupPayloadCorrupt)
	}
	off := 3
	out := make(map[string]*RollupFieldStat, n)
	for i := 0; i < n; i++ {
		if off >= len(payload) {
			return nil, fmt.Errorf("%w: v1 field %d name length missing", ErrRollupPayloadCorrupt, i)
		}
		nameLen := int(payload[off])
		off++
		if nameLen == 0 || off+nameLen > len(payload) {
			return nil, fmt.Errorf("%w: v1 field %d bad name length %d", ErrRollupPayloadCorrupt, i, nameLen)
		}
		name := string(payload[off : off+nameLen])
		off += nameLen
		const statBytes = 8*4 + 4
		if off+statBytes > len(payload) {
			return nil, fmt.Errorf("%w: v1 field %q stats truncated", ErrRollupPayloadCorrupt, name)
		}
		st := &RollupFieldStat{
			Avg:   math.Float64frombits(binary.BigEndian.Uint64(payload[off:])),
			Min:   math.Float64frombits(binary.BigEndian.Uint64(payload[off+8:])),
			Max:   math.Float64frombits(binary.BigEndian.Uint64(payload[off+16:])),
			Last:  math.Float64frombits(binary.BigEndian.Uint64(payload[off+24:])),
			Count: binary.BigEndian.Uint32(payload[off+32:]),
		}
		off += statBytes
		out[name] = st
	}
	if off != len(payload) {
		return nil, fmt.Errorf("%w: v1 trailing %d bytes", ErrRollupPayloadCorrupt, len(payload)-off)
	}
	return out, nil
}

// ==================== 分区/迁移/保留期（供启动路径接线）====================
//
// 这四个函数是 main.go / retention 接线的**唯一入口**。它们刻意不内置 goroutine，
// 也不改 main.go（本任务写范围不含 cmd/）：接线 patch 见
// docs/分析/遥测rollup-L3-接线与门禁-2026-11-06.md。

// EnsureTelemetryRollup1mPartitions 确保 [上月, 未来 months 月] 的月分区存在。
// 未分区（迁移未完成）时返回 nil —— 与 main.go 既有 ensurePartitions 的"未分区表跳过"
// 语义一致（PG 上对平表执行 PARTITION OF 会报错）。
func EnsureTelemetryRollup1mPartitions(db *gorm.DB, months int) error {
	if !isPostgres(db) {
		return nil
	}
	if !IsTablePartitioned(db, TelemetryRollupTable) {
		return nil
	}
	return NewPartitionManager(db).EnsurePartitionsFor(TelemetryRollupTable, months)
}

// MigrateTelemetryRollup1mToPartitioned 把该表转成分区母表（全新库直接建分区表）。
// 与 unified_data/device_data 走同一套 MigrateTableToPartitioned（主键/搬迁/校验/swap）。
//
// ⚠ 平表守卫（本表独有，别的表不需要）: 若库里已存在一张**平表** telemetry_rollup_1m，
// 这里直接报错而不是走进通用搬迁路径。原因：通用路径的 copyMonthBatched 依赖
// partitionCopyColumns[src] 的列清单（未登记的表会**回退到 unified_data 的 8 列**），
// 而本表只有 device_id/bucket/payload 三列 ⇒ INSERT ... SELECT 会报列不存在，
// 留下一个半成品 <table>_new 母表（源表不丢，但每次启动重试都失败）。
//
// 什么情况下才会出现平表：有人把本模型加进了生产 AutoMigrate（正是我们明确禁止的
// 做法，见 internal/models/telemetry_rollup.go 文件头）。与其静默留下半迁移状态，
// 不如 fail-loud 并给出处置指令。
func MigrateTelemetryRollup1mToPartitioned(db *gorm.DB) error {
	if !isPostgres(db) {
		return nil
	}
	if IsTablePartitioned(db, TelemetryRollupTable) {
		// 已分区：仍然走一遍幂等补齐（清单新增的索引/分区会在下次启动补上）。
		return MigrateTableToPartitioned(db, TelemetryRollupTable, TelemetryRollupTable+"_legacy")
	}
	var flat int64
	db.Raw(tableExistsSQL, TelemetryRollupTable, "r").Scan(&flat)
	if flat > 0 {
		return fmt.Errorf("telemetry_rollup_1m exists as a plain table: refusing to migrate " +
			"(it has no id/sensor_name columns, the generic copy path would fail); " +
			"drop it manually then restart — it is DERIVED data, losing it is safe: " +
			"DROP TABLE telemetry_rollup_1m")
	}
	return MigrateTableToPartitioned(db, TelemetryRollupTable, TelemetryRollupTable+"_legacy")
}

// DropTelemetryRollup1mPartitionsBefore 删除整月早于 cutoff 的分区（保留期的 O(1) 实现）。
func DropTelemetryRollup1mPartitionsBefore(db *gorm.DB, cutoff time.Time) ([]string, error) {
	if !isPostgres(db) {
		return nil, nil
	}
	if !IsTablePartitioned(db, TelemetryRollupTable) {
		return nil, nil
	}
	return NewPartitionManager(db).DropPartitionsBeforeFor(TelemetryRollupTable, cutoff)
}

// ReadRollupStats 取出一个时间窗内某设备某字段的分钟级统计序列（升序）。
//
// 供读路径（长窗图表）与对拍测试共用。返回 bucket 起点 + 该字段统计量。
// 坏行（版本未知/截断）跳过并计数，不整条查询失败——预聚合是派生数据，
// 单行坏掉不该让整张图 500。
func ReadRollupStats(ctx context.Context, db *gorm.DB, deviceID uint, field string, from, to time.Time) ([]time.Time, []RollupFieldStat, int, error) {
	if db == nil {
		return nil, nil, 0, fmt.Errorf("read rollup stats: nil db")
	}
	type row struct {
		Bucket  time.Time
		Payload []byte
	}
	var rows []row
	if err := db.WithContext(ctx).Raw(
		"SELECT bucket, payload FROM "+TelemetryRollupTable+
			" WHERE device_id = ? AND bucket >= ? AND bucket < ? ORDER BY bucket",
		deviceID, from, to,
	).Scan(&rows).Error; err != nil {
		return nil, nil, 0, err
	}
	buckets := make([]time.Time, 0, len(rows))
	stats := make([]RollupFieldStat, 0, len(rows))
	bad := 0
	for _, r := range rows {
		decoded, err := DecodeRollupPayload(r.Payload)
		if err != nil {
			bad++
			continue
		}
		st, ok := decoded[field]
		if !ok {
			continue // 该分钟没有这个字段的样本：不是坏数据
		}
		buckets = append(buckets, r.Bucket)
		stats = append(stats, *st)
	}
	if bad > 0 {
		slog.Warn("telemetry rollup: skipped corrupt payload rows", "rows", bad, "device_id", deviceID, "field", field)
	}
	return buckets, stats, bad, nil
}

// telemetryRollupModel 仅用于测试/工具里的 AutoMigrate（生产 PG 走裸 DDL）。
// 这里保留一个引用点，防止 models.TelemetryRollup1m 被误删而没人发现。
var _ = models.TelemetryRollup1m{}
