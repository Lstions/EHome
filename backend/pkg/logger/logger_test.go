package logger

import (
	"testing"
)

func TestInit(t *testing.T) {
	err := Init("warn")
	if err != nil {
		t.Fatalf("Init(warn) failed: %v", err)
	}
	if L == nil {
		t.Fatal("L should be initialized after Init()")
	}
}

func TestInitDebug(t *testing.T) {
	err := Init("debug")
	if err != nil {
		t.Fatalf("Init(debug) failed: %v", err)
	}
}

func TestInitInvalidLevel(t *testing.T) {
	// Invalid level should default to info, not error
	err := Init("invalid_level")
	if err != nil {
		t.Fatalf("Init(invalid_level) should not error, got: %v", err)
	}
}

func TestLoggingFunctions(t *testing.T) {
	// Init with warn so debug/info won't output much
	_ = Init("warn")

	// These should not panic
	Info("test info message")
	Error("test error message")
	Warn("test warn message")
	Debug("test debug message")

	// Printf-style
	Infof("test %s", "infof")
	Errorf("test %s", "errorf")
	Warnf("test %s", "warnf")
	Debugf("test %s", "debugf")
}

func TestSync(t *testing.T) {
	_ = Init("warn")
	// Should not panic
	Sync()
}

func TestLoggerWithKeysAndValues(t *testing.T) {
	_ = Init("warn")

	// Structured logging with key-value pairs should not panic
	Info("structured message", "key1", "value1", "key2", 42)
	Error("structured error", "err", "something failed")
}
