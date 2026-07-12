package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func sugared() *zap.SugaredLogger {
	if L != nil {
		return L
	}
	return zap.NewNop().Sugar()
}

// L is the global logger instance
var L *zap.SugaredLogger

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

	L = l.Sugar()
	return nil
}

// Sync flushes any buffered log entries
func Sync() {
	if L != nil {
		_ = L.Sync()
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
