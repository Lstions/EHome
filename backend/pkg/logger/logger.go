package logger

import (
	"sync/atomic"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

/* L 是全局 logger 实例。
 *
 * 并发契约（2026-10-04 修复）：**必须通过 atomic 读写**。
 *
 * 原实现是裸的 `var L *zap.SugaredLogger`，Init() 直接赋值、各日志函数直接读。
 * 这在测试里会真实触发数据竞争（CI 的 -race 抓到过两次，每次命中不同用例）：
 *
 *   WARNING: DATA RACE
 *   Write at ...: logger.Init()      logger.go:51  <- setupTestRouter
 *   Previous read: logger.sugared()  logger.go:9   <- 后台 consumer 的 Warnf
 *
 * 成因：每个测试各自调 setupTestRouter->logger.Init 重新赋值 L，而 databus 注册的
 * 后台 goroutine（db_persist / sensor_parser / logstream…）在同一时刻正在打日志。
 * 生产路径下 Init 只在启动时调用一次，所以只在测试里暴露 —— 但那正是 CI 红的
 * 原因，而且掩盖了真实回归（本次 SPI 修复的两次 CI 都栽在它上面）。
 *
 * 用 atomic.Pointer 而不是 sync.RWMutex：日志在热路径上，每次调用加锁不值得；
 * 这里只需要"要么看到旧 logger、要么看到新 logger"，不需要互斥。
 * 注意不能用 atomic.Value 直接存 *zap.SugaredLogger 的空接口（会 panic 于
 * 类型不一致），所以显式用 atomic.Pointer[zap.SugaredLogger]。
 */
var L atomic.Pointer[zap.SugaredLogger]

func sugared() *zap.SugaredLogger {
	if l := L.Load(); l != nil {
		return l
	}
	return zap.NewNop().Sugar()
}

// Current 返回当前 logger，未初始化时返回 Nop logger（与 sugared 同语义）。
// 供需要持有 logger 引用的地方使用，避免绕过 atomic 契约直接读 L。
func Current() *zap.SugaredLogger { return sugared() }

// Swap 原子替换全局 logger 并返回旧值，供测试临时替换输出目标。
//
// 为什么必须用它而不是直接赋值 logger.L：L 已是 atomic.Pointer，直接赋值既编译
// 不过、也会重新引入数据竞争。测试里临时换 logger（捕获日志断言内容）是合理需求，
// 所以提供一个显式的原子入口，而不是逼测试去碰内部字段。
//
// 用法（典型）：
//
//	previous := logger.Swap(zap.New(core).Sugar())
//	t.Cleanup(func() { logger.Swap(previous) })
func Swap(l *zap.SugaredLogger) *zap.SugaredLogger {
	return L.Swap(l)
}

// Init initializes the global logger
func Init(level string) error {
	var zapLevel zapcore.Level
	if err := zapLevel.UnmarshalText([]byte(level)); err != nil {
		zapLevel = zapcore.InfoLevel
	}

	config := zap.Config{
		Level:       zap.NewAtomicLevelAt(zapLevel),
		Development: false,
		Encoding:    "console",
		EncoderConfig: zapcore.EncoderConfig{
			TimeKey:        "time",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.CapitalLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.MillisDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		},
		OutputPaths:      []string{"stdout"},
		ErrorOutputPaths: []string{"stderr"},
	}

	l, err := config.Build()
	if err != nil {
		return err
	}

	L.Store(l.Sugar())
	return nil
}

// Sync flushes any buffered log entries
func Sync() {
	if l := L.Load(); l != nil {
		_ = l.Sync()
	}
}

// Convenience functions using structured logging (msg + key/value pairs)
func Info(msg string, keysAndValues ...interface{}) {
	sugared().Infow(msg, keysAndValues...)
}

func Error(msg string, keysAndValues ...interface{}) {
	sugared().Errorw(msg, keysAndValues...)
}

func Warn(msg string, keysAndValues ...interface{}) {
	sugared().Warnw(msg, keysAndValues...)
}

func Debug(msg string, keysAndValues ...interface{}) {
	sugared().Debugw(msg, keysAndValues...)
}

func Fatal(msg string, keysAndValues ...interface{}) {
	sugared().Fatalw(msg, keysAndValues...)
}

// Printf-style convenience functions
func Infof(template string, args ...interface{}) {
	sugared().Infof(template, args...)
}

func Errorf(template string, args ...interface{}) {
	sugared().Errorf(template, args...)
}

func Warnf(template string, args ...interface{}) {
	sugared().Warnf(template, args...)
}

func Debugf(template string, args ...interface{}) {
	sugared().Debugf(template, args...)
}

func Fatalf(template string, args ...interface{}) {
	sugared().Fatalf(template, args...)
}
