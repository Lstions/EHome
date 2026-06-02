package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

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
	L.Infow(msg, keysAndValues...)
}

func Error(msg string, keysAndValues ...interface{}) {
	L.Errorw(msg, keysAndValues...)
}

func Warn(msg string, keysAndValues ...interface{}) {
	L.Warnw(msg, keysAndValues...)
}

func Debug(msg string, keysAndValues ...interface{}) {
	L.Debugw(msg, keysAndValues...)
}

func Fatal(msg string, keysAndValues ...interface{}) {
	L.Fatalw(msg, keysAndValues...)
}

// Printf-style convenience functions
func Infof(template string, args ...interface{}) {
	L.Infof(template, args...)
}

func Errorf(template string, args ...interface{}) {
	L.Errorf(template, args...)
}

func Warnf(template string, args ...interface{}) {
	L.Warnf(template, args...)
}

func Debugf(template string, args ...interface{}) {
	L.Debugf(template, args...)
}

func Fatalf(template string, args ...interface{}) {
	L.Fatalf(template, args...)
}