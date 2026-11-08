package datalifecycle

// L3 rollup 的**真实数据验收测试**（默认 skip，需显式指定数据文件才跑）。
//
// 为什么必须用真实数据而不是造样本：方案 §5.2 的核心承诺是「写入侧内存聚合的
// 统计量 == 直接对原始表做 SQL 聚合」。造样本只能验证"我算的和我算的一样"；
// 真实数据才能暴露：时间戳精度（微秒）、同一分钟多帧、逐字段重复行、字段集合
// 随帧类型变化（device15 有 7 种）这些真实形状。
//
// 运行方式（真实数据在仓外的 .tmp-telemetry/realdata/）：
//
//	EHOME_ROLLUP_REALDATA_CSV=/ws/.tmp-telemetry/realdata/unified_data_202611.csv.gz \
//	GOW_WORKDIR=.agents/worktrees/telemetry-storage/backend scripts/gow.sh test \
//	  -run TestRollupRealData -v ./internal/datalifecycle/
//
// 判据（"误差 0"的精确定义，别含糊）：
//   · count / min / max / last   —— **严格相等**（0 误差）。这四个量是精确可复现的：
//     count 是整数计数，min/max 是选择，last 是"该分钟最后一行"（按 CSV 顺序 = 入库顺序）。
//   · avg                        —— 与 SQL 的 avg() 相对误差 <= 1e-12。
//     为什么不能要求严格相等：Go 侧是**增量均值**（数值稳定：avg += (v-avg)/n），
//     SQL 侧是 sum/count（SQLite 还带 Kahan 补偿求和），三者的浮点舍入路径不同。
//     这是**求和顺序差异**，不是语义差异 —— 用 1e-12 严格到远小于任何业务可感知量级。
//     测试里同时断言 avg ∈ [min, max]（这才是真正防"算错"的判据）。

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/csv"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

var _ = bufio.NewReaderSize

// realdataCSVEnv 指向 unified_data 的 CSV(.gz) 导出；未设置则 skip。
const realdataCSVEnv = "EHOME_ROLLUP_REALDATA_CSV"

// realdataPerDeviceLimit 是**每台设备**的读取上限（默认 15 万行）。
//
// 为什么按设备限额而不是按总行数：生产 CSV 是按 id（≈时间）顺序导出的，前几十万行
// **全是 device 14**（实测前 300 万行只有 device 14）。按总行数截断会让用例只覆盖
// 一台设备，而 device 15 恰好是多帧类型（7 种字段集合）的那台 —— 那正是聚合最容易
// 出错的地方。按设备限额逼着读取器扫到两种设备都有样本。
const realdataPerDeviceLimit = 150000

type realdataRawRow struct {
	deviceID   uint
	sensorName string
	value      float64
	timestamp  time.Time
}

// readRealdataCSV 读 CSV(.gz) 的前 limit 行。列序（生产导出）:
// id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id
func readRealdataCSV(t *testing.T, path string, limit int) []realdataRawRow {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open realdata csv: %v", err)
	}
	defer f.Close()

	var r *csv.Reader
	if strings.HasSuffix(path, ".gz") {
		gz, err := gzip.NewReader(bufio.NewReaderSize(f, 1<<20))
		if err != nil {
			t.Fatalf("gunzip: %v", err)
		}
		defer gz.Close()
		r = csv.NewReader(gz)
	} else {
		r = csv.NewReader(f)
	}
	r.FieldsPerRecord = -1
	r.ReuseRecord = false

	rows := make([]realdataRawRow, 0, limit*2)
	perDevice := make(map[uint]int, 4)
	for {
		rec, err := r.Read()
		if err != nil {
			break // EOF
		}
		if len(rec) < 6 {
			continue
		}
		deviceID64, err := strconv.ParseUint(strings.TrimSpace(rec[1]), 10, 32)
		if err != nil {
			continue
		}
		deviceID := uint(deviceID64)
		if perDevice[deviceID] >= limit {
			// 该设备已够，继续扫但不再存 —— 让其它设备也能凑够样本。
			continue
		}
		value, err := strconv.ParseFloat(strings.TrimSpace(rec[3]), 64)
		if err != nil {
			continue
		}
		ts, err := parseRealdataTimestamp(strings.TrimSpace(rec[5]))
		if err != nil {
			continue
		}
		perDevice[deviceID]++
		rows = append(rows, realdataRawRow{
			deviceID:   deviceID,
			sensorName: strings.TrimSpace(rec[2]),
			value:      value,
			timestamp:  ts,
		})
	}
	if len(rows) == 0 {
		t.Fatalf("realdata csv 没读到有效行: %s", path)
	}
	return rows
}

// parseRealdataTimestamp 解析生产导出的时间戳（带 6 位微秒 + 时区偏移）。
func parseRealdataTimestamp(s string) (time.Time, error) {
	layouts := []string{
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05.999999-07:00",
		"2006-01-02 15:04:05.999999+00",
		time.RFC3339Nano,
		"2006-01-02 15:04:05",
	}
	var lastErr error
	for _, layout := range layouts {
		if ts, err := time.Parse(layout, s); err == nil {
			return ts, nil
		} else {
			lastErr = err
		}
	}
	return time.Time{}, lastErr
}

// TestRollupRealDataMatchesSQLAggregation 是 L3 的无损性证据。
func TestRollupRealDataMatchesSQLAggregation(t *testing.T) {
	csvPath := os.Getenv(realdataCSVEnv)
	if csvPath == "" {
		t.Skipf("未设置 %s（真实数据验收需显式指定 CSV 路径）", realdataCSVEnv)
	}

	rows := readRealdataCSV(t, csvPath, realdataPerDeviceLimit)
	t.Logf("读取真实数据 %d 行（每设备上限 %d）", len(rows), realdataPerDeviceLimit)

	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1)
	}
	// 原始行表：只保留本用例需要的四列（模拟 unified_data 的最小形状）。
	if err := db.Exec(`CREATE TABLE raw_rows (
		device_id integer NOT NULL,
		sensor_name text NOT NULL,
		value real NOT NULL,
		"timestamp" TIMESTAMP NOT NULL
	)`).Error; err != nil {
		t.Fatalf("create raw_rows: %v", err)
	}
	if err := db.AutoMigrate(&modelsRollupProbe{}); err != nil {
		t.Fatalf("automigrate rollup probe: %v", err)
	}

	// ① 原始行入库（**CreateInBatches**，批次内一条多值 INSERT）+ ② 同一批数据喂给
	// 写入侧采集器（内存累加，不碰库）。
	//
	// 踩过的坑：最初用 sql.DB.Prepare + 逐行 Exec —— 拿的是**连接池**而不是那笔事务，
	// 于是每行一个隐式事务，40 万行烤了 4 分钟还没完（等价于"逐行 fsync"）。
	// CreateInBatches 每批 500 行，一次往返。
	type rawRow struct {
		DeviceID   uint      `gorm:"column:device_id"`
		SensorName string    `gorm:"column:sensor_name"`
		Value      float64   `gorm:"column:value"`
		Timestamp  time.Time `gorm:"column:timestamp;type:TIMESTAMP"`
	}
	raws := make([]rawRow, 0, len(rows))
	collector := NewRollupCollector(db)
	// ⚠ 关键：关掉内存桶 TTL（默认 6h）。TTL 是**生产保护**（DB 长时间不可用时
	// 丢预聚合防 OOM），不是统计语义；而本用例是一次性重放跨越数天的数据，
	// 一次性把时钟推到末尾再 flush ⇒ 默认 TTL 会把跨度 > 6h 的桶当作"过期"丢弃。
	// 实测踩到：device 15 的桶整段消失，表现为"rollup 缺桶 15078 vs 31965"。
	collector.SetBucketTTL(1000 * 24 * time.Hour)
	for _, row := range rows {
		raws = append(raws, rawRow{
			DeviceID:   row.deviceID,
			SensorName: row.sensorName,
			Value:      row.value,
			Timestamp:  row.timestamp.UTC(),
		})
		collector.Observe(row.deviceID, []RollupSample{{Name: row.sensorName, Value: row.value}}, row.timestamp)
	}
	startInsert := time.Now()
	if err := db.Table("raw_rows").CreateInBatches(raws, 500).Error; err != nil {
		t.Fatalf("insert raw rows: %v", err)
	}
	t.Logf("原始行入库 %d 行，耗时 %s", len(raws), time.Since(startInsert).Round(time.Millisecond))

	// ③ 落库 rollup：把时钟推到"最后一行之后一分钟"，让所有桶都判定为已结束。
	last := rows[len(rows)-1].timestamp
	collector.SetClock(func() time.Time { return RollupBucket(last).Add(2 * time.Minute) })
	written, err := collector.FlushCompleted(context.Background())
	if err != nil {
		t.Fatalf("flush rollup: %v", err)
	}
	if written == 0 {
		t.Fatal("没有写入任何 rollup 行 —— 采集器未生效")
	}
	t.Logf("rollup 落库 %d 行（每设备每分钟一行）", written)

	// ④ SQL 直接聚合（真值），与 ⑤ 解码 rollup 逐桶逐字段比对。
	// last = 该桶内最后一行（CSV 顺序 = 入库顺序，rowid 单调）。
	//
	// 为什么用 "GROUP BY 取 MAX(rid) + JOIN" 而不是相关子查询：
	// 相关子查询版本在 40 万行上实测 **93 秒**（每个桶线性回扫一次，O(n²) 形态）；
	// 这个版本是一次分组 + 两次等值 JOIN，同数据量下亚秒级。测试本身不该成为瓶颈
	// —— 跑 90 秒的用例会被人在 CI 里注释掉，那就等于没有这条证据。
	sqlText := `
		WITH ordered AS (
			SELECT device_id, sensor_name, value,
			       strftime('%Y-%m-%d %H:%M:00', "timestamp") AS bucket,
			       rowid AS rid
			FROM raw_rows
		), lastids AS (
			SELECT device_id, sensor_name, bucket, MAX(rid) AS max_rid
			FROM ordered GROUP BY device_id, sensor_name, bucket
		), agg AS (
			SELECT device_id, sensor_name, bucket,
			       count(*) AS cnt, min(value) AS min_v, max(value) AS max_v, avg(value) AS avg_v
			FROM ordered GROUP BY device_id, sensor_name, bucket
		)
		SELECT a.device_id, a.sensor_name, a.bucket, a.cnt, a.min_v, a.max_v, a.avg_v,
		       o.value AS last_v
		FROM agg a
		JOIN lastids l
		  ON l.device_id = a.device_id AND l.sensor_name = a.sensor_name AND l.bucket = a.bucket
		JOIN ordered o ON o.rid = l.max_rid`
	var expected []struct {
		DeviceID   uint
		SensorName string
		Bucket     string
		Cnt        int64
		MinV       float64
		MaxV       float64
		AvgV       float64
		LastV      float64
	}
	if err := db.Raw(sqlText).Scan(&expected).Error; err != nil {
		t.Fatalf("SQL 聚合: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("SQL 侧一个桶都没有 —— 造数失败")
	}

	// 把 rollup 行读回来，建索引 (device, bucket) → 字段统计。
	var storedRows []modelsRollupProbe
	if err := db.Find(&storedRows).Error; err != nil {
		t.Fatalf("读 rollup 行: %v", err)
	}
	type key struct {
		device uint
		bucket string
		field  string
	}
	actual := make(map[key]RollupFieldStat, len(storedRows)*8)
	for _, sr := range storedRows {
		stats, err := DecodeRollupPayload(sr.Payload)
		if err != nil {
			t.Fatalf("解码 rollup payload (device=%d bucket=%v): %v", sr.DeviceID, sr.Bucket, err)
		}
		bucketText := sr.Bucket.UTC().Format("2006-01-02 15:04:00")
		for name, st := range stats {
			actual[key{sr.DeviceID, bucketText, name}] = *st
		}
	}

	// ⑤ 逐条比对。
	var (
		minMaxLastMismatch int
		avgOutOfRange      int
		maxAvgRelErr       float64
		compared           int
	)
	for _, e := range expected {
		k := key{e.DeviceID, e.Bucket, e.SensorName}
		got, ok := actual[k]
		if !ok {
			t.Errorf("rollup 缺桶: device=%d bucket=%s field=%s", e.DeviceID, e.Bucket, e.SensorName)
			continue
		}
		compared++
		if int64(got.Count) != e.Cnt {
			minMaxLastMismatch++
			t.Errorf("count 不一致 device=%d bucket=%s field=%s: rollup=%d sql=%d",
				e.DeviceID, e.Bucket, e.SensorName, got.Count, e.Cnt)
			continue
		}
		if got.Min != e.MinV || got.Max != e.MaxV || got.Last != e.LastV {
			minMaxLastMismatch++
			t.Errorf("min/max/last 不一致 device=%d bucket=%s field=%s: rollup=(%v,%v,%v) sql=(%v,%v,%v)",
				e.DeviceID, e.Bucket, e.SensorName, got.Min, got.Max, got.Last, e.MinV, e.MaxV, e.LastV)
			continue
		}
		if got.Avg < got.Min || got.Avg > got.Max {
			avgOutOfRange++
			t.Errorf("avg 不在 [min,max] 内 device=%d bucket=%s field=%s: avg=%v min=%v max=%v",
				e.DeviceID, e.Bucket, e.SensorName, got.Avg, got.Min, got.Max)
			continue
		}
		if e.AvgV != 0 {
			relErr := absFloat(got.Avg-e.AvgV) / absFloat(e.AvgV)
			if relErr > maxAvgRelErr {
				maxAvgRelErr = relErr
			}
			if relErr > 1e-12 {
				t.Errorf("avg 相对误差 %.3e > 1e-12 device=%d bucket=%s field=%s: rollup=%v sql=%v",
					relErr, e.DeviceID, e.Bucket, e.SensorName, got.Avg, e.AvgV)
			}
		} else if got.Avg != 0 {
			t.Errorf("avg 应为 0 device=%d bucket=%s field=%s: rollup=%v", e.DeviceID, e.Bucket, e.SensorName, got.Avg)
		}
	}
	if minMaxLastMismatch != 0 || avgOutOfRange != 0 {
		t.Fatalf("统计量不一致: min/max/last=%d avg越界=%d", minMaxLastMismatch, avgOutOfRange)
	}
	if compared != len(expected) {
		t.Fatalf("比对条数 %d != SQL 桶字段数 %d", compared, len(expected))
	}
	t.Logf("✅ 逐桶逐字段比对通过: %d 条 (设备数=%d, 分钟桶数=%d)；"+
		"count/min/max/last 严格相等；avg 最大相对误差 %.3e (<=1e-12)",
		compared, countDistinctDevices(expected), countDistinctBuckets(expected), maxAvgRelErr)
}

// modelsRollupProbe 是 telemetry_rollup_1m 的最小模型（只给本用例建表用）。
// 与 models.TelemetryRollup1m 同形状；本包不 import models 的 rollup 类型，
// 避免测试用例与生产模型强耦合（模型改名不该弄红无损性证据）。
type modelsRollupProbe struct {
	DeviceID  uint      `gorm:"column:device_id;primaryKey;not null"`
	Bucket    time.Time `gorm:"column:bucket;primaryKey;not null"`
	Payload   []byte    `gorm:"column:payload;not null"`
	UpdatedAt time.Time
}

func (modelsRollupProbe) TableName() string { return TelemetryRollupTable }

func absFloat(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func countDistinctDevices(rows []struct {
	DeviceID   uint
	SensorName string
	Bucket     string
	Cnt        int64
	MinV       float64
	MaxV       float64
	AvgV       float64
	LastV      float64
}) int {
	seen := map[uint]struct{}{}
	for _, r := range rows {
		seen[r.DeviceID] = struct{}{}
	}
	return len(seen)
}

func countDistinctBuckets(rows []struct {
	DeviceID   uint
	SensorName string
	Bucket     string
	Cnt        int64
	MinV       float64
	MaxV       float64
	AvgV       float64
	LastV      float64
}) int {
	seen := map[[2]interface{}]struct{}{}
	for _, r := range rows {
		seen[[2]interface{}{r.DeviceID, r.Bucket}] = struct{}{}
	}
	return len(seen)
}
