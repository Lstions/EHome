package backfill

// config.go — 回填引擎的参数与默认值。

import (
	"os"
	"strconv"
	"time"
)

// 环境变量名。
//
// 两个都是**参数**不是开关：EHOME_BACKFILL_ENABLED 默认为 true（契约 §8 把回填
// 定为必备能力），设成 false 只是给运维一个「这台机器先别跑」的应急阀。
const (
	// EnvEnabled 开关：默认 true。
	EnvEnabled = "EHOME_BACKFILL_ENABLED"
	// EnvBatchFrames 每批处理的帧数，默认 2000。
	EnvBatchFrames = "EHOME_BACKFILL_BATCH_FRAMES"
)

const (
	// DefaultBatchFrames 是每批帧数默认值。
	//
	// 2000 的来由（两道约束取小）：
	//
	//	-- 上界：一批要在内存里持有「全部帧的 readings」，device14 一帧 29 字段，
	//	--   2000 帧 × 29 × 约 40 B（string header + float）≈ 2.3 MB，可接受；
	//	-- 下界：批太小会让断点推进过密（每批一次 UPDATE），342 批 → 3420 批只是
	//	--   把 40 分钟里的 342 次写放大成 3420 次，收益不抵成本。
	DefaultBatchFrames = 2000

	// DefaultKeyFrameInterval 是关键帧间隔，默认 60s（与线上 L2 同口径）。
	//
	// 为什么不复用 telemetry.DefaultKeyFrameInterval 的值而另立常量：回填是一个
	// **可以独立调**的场景（例如历史数据稀疏时可以放宽到 5 分钟以进一步压缩）。
	// 但默认值必须与线上一致 —— 两套默认值不一致会让「回填数据」与「线上数据」
	// 的解码成本出现无法解释的差异。
	DefaultKeyFrameInterval = 60 * time.Second
)

// Config 是回填引擎参数。
type Config struct {
	// BatchFrames 每批处理的帧数（<=0 用默认值）。
	BatchFrames int
	// KeyFrameInterval 强制关键帧间隔（<=0 用默认值）。
	KeyFrameInterval time.Duration
	// DeviceFilter 非空时只回填这些设备（排障/灰度用；空 = 全部）。
	DeviceFilter []uint
	// ResetEncoderPerBatch 每批开始时清空编码器状态（默认 true）。
	//
	// 为什么默认 true（重要，见 runner.go 的详细论证）：编码器的 seq 记账 map
	// 按 (device_id, timestamp) 无界增长，3.42M 帧会累计到 3.42M 个 entry
	// （Go map 约 160 MB）。每批重置把它钉在 BatchFrames 量级。
	//
	// 代价：每批首帧变成关键帧。342 批 × 约 35 B ≈ 12 KB 额外 payload，
	// 相对 463 MB 可忽略。
	ResetEncoderPerBatch bool
}

// normalize 填默认值。
func (c Config) normalize() Config {
	if c.BatchFrames <= 0 {
		c.BatchFrames = DefaultBatchFrames
	}
	if c.KeyFrameInterval <= 0 {
		c.KeyFrameInterval = DefaultKeyFrameInterval
	}
	return c
}

// ConfigFromEnv 从环境变量组装参数（Enabled 单独返回，因为它是开关不是参数）。
//
// 非法值**回落到默认并继续**，不报错：一个打错的 batch 数字不该让回填停摆。
// 但会在 Runner 的 Errors 里留一条，运维能在进度接口看到 —— 静默忽略配置错误
// 会让「我明明设了 5000，怎么还是 2000」变成无解之谜。
func ConfigFromEnv() (Config, bool, []string) {
	// 先 normalize：缺失/非法的 env 必须回落到默认值，而不是留 0。
	// 留 0 会让日志与进度里显示 BatchFrames=0，运维看不出实际用的是 2000。
	cfg := Config{ResetEncoderPerBatch: true}.normalize()
	var warnings []string

	enabled := true
	if v, ok := os.LookupEnv(EnvEnabled); ok {
		b, err := strconv.ParseBool(v)
		if err != nil {
			warnings = append(warnings, "忽略非法的 "+EnvEnabled+"="+v+"（按 true 处理）")
		} else {
			enabled = b
		}
	}

	if v, ok := os.LookupEnv(EnvBatchFrames); ok {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			warnings = append(warnings, "忽略非法的 "+EnvBatchFrames+"="+v+"（按默认 "+strconv.Itoa(DefaultBatchFrames)+" 处理）")
		case n <= 0:
			warnings = append(warnings, "忽略非正的 "+EnvBatchFrames+"="+v+"（按默认 "+strconv.Itoa(DefaultBatchFrames)+" 处理）")
		case n > 20000:
			warnings = append(warnings, "钳制过大的 "+EnvBatchFrames+"="+v+" 到 20000（单批内存上界保护）")
			cfg.BatchFrames = 20000
		default:
			cfg.BatchFrames = n
		}
	}

	return cfg, enabled, warnings
}
