package firmwarepkg

// Limits 是包解析的大小与安全上限（方案 §4.5）。
//
// 零值字段由 normalized() 补成生产默认值，因此 `Limits{MaxEntries: 4}` 只改一项、
// 其余仍是默认；否则「零值上限 = 全拒」会让调用方写出一个永远失败的解析器（评审 B-1）。
type Limits struct {
	MaxEntries      int   // 条目数上限，默认 16（实际恒为 2）
	MaxUncompressed int64 // 解压总量上限，默认 16 MiB
	MaxManifest     int64 // 清单大小上限，默认 64 KiB
	MaxRatio        int   // 单条目压缩比上限，默认 100:1
}

const (
	defaultMaxEntries      = 16
	defaultMaxUncompressed = 16 << 20
	defaultMaxManifest     = 64 << 10
	defaultMaxRatio        = 100
)

// DefaultLimits 返回生产默认值。
func DefaultLimits() Limits {
	return Limits{
		MaxEntries:      defaultMaxEntries,
		MaxUncompressed: defaultMaxUncompressed,
		MaxManifest:     defaultMaxManifest,
		MaxRatio:        defaultMaxRatio,
	}
}

// normalized 把零值/负值字段补成默认值，其余原样保留。
func (l Limits) normalized() Limits {
	if l.MaxEntries <= 0 {
		l.MaxEntries = defaultMaxEntries
	}
	if l.MaxUncompressed <= 0 {
		l.MaxUncompressed = defaultMaxUncompressed
	}
	if l.MaxManifest <= 0 {
		l.MaxManifest = defaultMaxManifest
	}
	if l.MaxRatio <= 0 {
		l.MaxRatio = defaultMaxRatio
	}
	return l
}
