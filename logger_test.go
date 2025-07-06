package swre

import (
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// TestLoggerInterface verifies the Logger interface implementation.
func TestLoggerInterface(t *testing.T) {
	tests := []struct {
		name   string
		logger Logger
	}{
		{
			name:   "NoOpLogger",
			logger: NewNoOpLogger(),
		},
		{
			name:   "ZapAdapter",
			logger: NewTestLogger(t),
		},
		{
			name:   "TestLoggerMock",
			logger: NewTestLoggerMock(),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Test all log levels
			tt.logger.Debug("debug message", String("key", "value"))
			tt.logger.Info("info message", Int("count", 42))
			tt.logger.Warn("warn message", Duration("elapsed", time.Second))
			tt.logger.Error("error message", Error(errors.New("test error")))

			// Test Named logger
			named := tt.logger.Named("subsystem")
			named.Info("named logger message", Time("timestamp", time.Now()))
		})
	}
}

// TestZapAdapter verifies the ZapAdapter implementation.
func TestZapAdapter(t *testing.T) {
	zapLogger := zaptest.NewLogger(t)
	adapter, err := NewZapAdapter(zapLogger)
	if err != nil {
		t.Fatalf("NewZapAdapter failed: %v", err)
	}

	// Test nil logger
	_, err = NewZapAdapter(nil)
	if err != ErrNilLogger {
		t.Errorf("Expected ErrNilLogger for nil logger, got %v", err)
	}

	// Test all field types
	now := time.Now()
	testError := errors.New("test error")

	adapter.Debug("test all field types",
		String("string", "value"),
		Int("int", 123),
		Int32("int32", int32(456)),
		Int64("int64", int64(789)),
		Duration("duration", 5*time.Second),
		Time("time", now),
		Error(testError),
		ErrorKey("custom_error", testError),
		Any("any", map[string]int{"a": 1}),
		ByteString("bytes", []byte("hello")),
		Stack("stack trace"),
		StackKey("custom_stack", "custom trace"),
	)

	// Test Named functionality
	named := adapter.Named("component")
	named.Info("named component log")
}

// TestFieldCreation verifies field creation functions.
func TestFieldCreation(t *testing.T) {
	// Test String field
	f := String("key", "value")
	if f.Key() != "key" || f.Value() != "value" || f.Type() != FieldTypeString {
		t.Errorf("String field creation failed")
	}

	// Test Int field
	f = Int("count", 42)
	if f.Key() != "count" || f.Value() != 42 || f.Type() != FieldTypeInt {
		t.Errorf("Int field creation failed")
	}

	// Test Duration field
	d := 5 * time.Second
	f = Duration("elapsed", d)
	if f.Key() != "elapsed" || f.Value() != d || f.Type() != FieldTypeDuration {
		t.Errorf("Duration field creation failed")
	}

	// Test Error field
	err := errors.New("test error")
	f = Error(err)
	if f.Key() != "error" || f.Value() != err || f.Type() != FieldTypeError {
		t.Errorf("Error field creation failed")
	}

	// Test Error field with nil
	f = Error(nil)
	if f.Key() != "error" || f.Value() != nil || f.Type() != FieldTypeError {
		t.Errorf("Error field with nil failed")
	}

	// Test Time field
	now := time.Now()
	f = Time("timestamp", now)
	if f.Key() != "timestamp" || f.Value() != now || f.Type() != FieldTypeTime {
		t.Errorf("Time field creation failed")
	}

	// Test Any field
	data := map[string]int{"a": 1, "b": 2}
	f = Any("data", data)
	if f.Key() != "data" || f.Type() != FieldTypeAny {
		t.Errorf("Any field creation failed")
	}

	// Test ByteString field
	bytes := []byte("hello world")
	f = ByteString("payload", bytes)
	if f.Key() != "payload" || f.Type() != FieldTypeByteString {
		t.Errorf("ByteString field creation failed")
	}

	// Test Stack field
	f = Stack("trace")
	if f.Key() != "stacktrace" || f.Value() != "trace" || f.Type() != FieldTypeStack {
		t.Errorf("Stack field creation failed")
	}

	// Test custom key stack
	f = StackKey("custom_trace", "trace")
	if f.Key() != "custom_trace" || f.Value() != "trace" || f.Type() != FieldTypeStack {
		t.Errorf("StackKey field creation failed")
	}
}


// TestNewLoggerAPI verifies the new Logger interface API.
func TestNewLoggerAPI(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Test new constructor
	engine, err := NewStaleEngine(storage, logger)
	if err != nil {
		t.Fatalf("NewStaleEngine failed: %v", err)
	}
	if engine == nil {
		t.Fatal("Expected engine, got nil")
	}

	// Test new constructor with options
	engine2, err := NewStaleEngineWithOptions(storage, logger,
		WithMaxConcurrentRefreshes(100),
		WithRefreshTimeout(10*time.Second),
	)
	if err != nil {
		t.Fatalf("NewStaleEngineWithOptions failed: %v", err)
	}
	if engine2 == nil {
		t.Fatal("Expected engine, got nil")
	}

	// Test EngineConfig with Logger interface
	cfg := &EngineConfig{
		Storage: storage,
		Logger:  logger,
	}
	cfg.SetDefaults()
	engine3, err := NewStaleEngineWithConfig(cfg)
	if err != nil {
		t.Fatalf("NewStaleEngineWithConfig failed: %v", err)
	}
	if engine3 == nil {
		t.Fatal("Expected engine, got nil")
	}
}

// TestTestLoggerMock verifies the mock logger functionality.
func TestTestLoggerMock(t *testing.T) {
	mock := NewTestLoggerMock()

	// Log some messages
	mock.Debug("debug msg", String("key", "value"))
	mock.Info("info msg", Int("count", 42))
	mock.Warn("warn msg")
	mock.Error("error msg", Error(errors.New("test")))

	// Verify captures
	if len(mock.DebugCalls) != 1 || mock.DebugCalls[0].Message != "debug msg" {
		t.Errorf("Debug call not captured correctly")
	}

	if len(mock.InfoCalls) != 1 || mock.InfoCalls[0].Message != "info msg" {
		t.Errorf("Info call not captured correctly")
	}

	if len(mock.WarnCalls) != 1 || mock.WarnCalls[0].Message != "warn msg" {
		t.Errorf("Warn call not captured correctly")
	}

	if len(mock.ErrorCalls) != 1 || mock.ErrorCalls[0].Message != "error msg" {
		t.Errorf("Error call not captured correctly")
	}

	// Test helper methods
	if !mock.HasDebugMessage("debug msg") {
		t.Errorf("HasDebugMessage failed")
	}

	if !mock.HasInfoMessage("info msg") {
		t.Errorf("HasInfoMessage failed")
	}

	if !mock.HasWarnMessage("warn msg") {
		t.Errorf("HasWarnMessage failed")
	}

	if !mock.HasErrorMessage("error msg") {
		t.Errorf("HasErrorMessage failed")
	}

	// Test GetAllCalls
	all := mock.GetAllCalls()
	if len(all) != 4 {
		t.Errorf("Expected 4 total calls, got %d", len(all))
	}

	// Test Reset
	mock.Reset()
	if len(mock.GetAllCalls()) != 0 {
		t.Errorf("Reset failed")
	}

	// Test Named
	named := mock.Named("subsystem")
	named.Info("named message")
	if !mock.HasInfoMessage("named message") {
		t.Errorf("Named logger failed")
	}
}

// BenchmarkLogging compares logging performance.
func BenchmarkLogging(b *testing.B) {
	storage := NewMockStorage()
	
	// Benchmark with NoOp logger
	b.Run("NoOpLogger", func(b *testing.B) {
		logger := NewNoOpLogger()
		engine, _ := NewStaleEngine(storage, logger)
		
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			engine.logger.Debug("test message",
				String("key", "value"),
				Int("count", i),
			)
		}
	})

	// Benchmark with Zap NOP logger
	b.Run("ZapNopLogger", func(b *testing.B) {
		zapLogger := zap.NewNop()
		adapter, _ := NewZapAdapter(zapLogger)
		engine, _ := NewStaleEngine(storage, adapter)
		
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			engine.logger.Debug("test message",
				String("key", "value"),
				Int("count", i),
			)
		}
	})
}