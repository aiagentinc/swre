// Package swre provides test helpers for logger testing.
package swre

import (
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest"
)

// NewTestLogger creates a logger suitable for testing.
// This provides a simple way to create loggers in tests that work with both
// the old zap-based API and the new Logger interface.
func NewTestLogger(t *testing.T) Logger {
	zapLogger := zaptest.NewLogger(t)
	adapter, _ := NewZapAdapter(zapLogger)
	return adapter
}

// NewSafeTestLogger creates a logger that safely handles logging after test completion.
// This is useful when dealing with background goroutines that might log after the test ends.
func NewSafeTestLogger(t *testing.T) Logger {
	// Use a NoOpLogger wrapped in a safe adapter
	// This prevents race conditions when background goroutines
	// try to log after the test has completed
	return &safeTestLogger{
		t:      t,
		logger: NewNoOpLogger(),
	}
}

// safeTestLogger wraps a logger to handle post-test logging safely
type safeTestLogger struct {
	t      *testing.T
	logger Logger
}

func (s *safeTestLogger) Debug(msg string, fields ...Field) {
	if s.t != nil {
		s.logger.Debug(msg, fields...)
	}
}

func (s *safeTestLogger) Info(msg string, fields ...Field) {
	if s.t != nil {
		s.logger.Info(msg, fields...)
	}
}

func (s *safeTestLogger) Warn(msg string, fields ...Field) {
	if s.t != nil {
		s.logger.Warn(msg, fields...)
	}
}

func (s *safeTestLogger) Error(msg string, fields ...Field) {
	if s.t != nil {
		s.logger.Error(msg, fields...)
	}
}

func (s *safeTestLogger) Named(name string) Logger {
	return &safeTestLogger{
		t:      s.t,
		logger: s.logger.Named(name),
	}
}

// NewBenchmarkLogger creates a no-op logger suitable for benchmarks.
// This avoids the overhead of logging during performance tests.
func NewBenchmarkLogger() Logger {
	return NewNoOpLogger()
}

// NewDevelopmentLogger creates a logger suitable for development and debugging.
// This logger outputs human-readable logs that are helpful during development.
func NewDevelopmentLogger() Logger {
	zapLogger, _ := zap.NewDevelopment()
	adapter, _ := NewZapAdapter(zapLogger)
	return adapter
}

// NewProductionLogger creates a logger suitable for production use.
// This logger outputs structured JSON logs optimized for production environments.
func NewProductionLogger() Logger {
	zapLogger, _ := zap.NewProduction()
	adapter, _ := NewZapAdapter(zapLogger)
	return adapter
}

// TestLoggerMock is a mock logger that captures log calls for testing.
// This is useful when you need to verify that specific log messages were generated.
type TestLoggerMock struct {
	DebugCalls []LogCall
	InfoCalls  []LogCall
	WarnCalls  []LogCall
	ErrorCalls []LogCall
	name       string
}

// LogCall represents a single log method call.
type LogCall struct {
	Message string
	Fields  []Field
}

// NewTestLoggerMock creates a new mock logger for testing.
func NewTestLoggerMock() *TestLoggerMock {
	return &TestLoggerMock{
		DebugCalls: make([]LogCall, 0),
		InfoCalls:  make([]LogCall, 0),
		WarnCalls:  make([]LogCall, 0),
		ErrorCalls: make([]LogCall, 0),
	}
}

// Debug captures debug log calls.
func (m *TestLoggerMock) Debug(msg string, fields ...Field) {
	m.DebugCalls = append(m.DebugCalls, LogCall{Message: msg, Fields: fields})
}

// Info captures info log calls.
func (m *TestLoggerMock) Info(msg string, fields ...Field) {
	m.InfoCalls = append(m.InfoCalls, LogCall{Message: msg, Fields: fields})
}

// Warn captures warn log calls.
func (m *TestLoggerMock) Warn(msg string, fields ...Field) {
	m.WarnCalls = append(m.WarnCalls, LogCall{Message: msg, Fields: fields})
}

// Error captures error log calls.
func (m *TestLoggerMock) Error(msg string, fields ...Field) {
	m.ErrorCalls = append(m.ErrorCalls, LogCall{Message: msg, Fields: fields})
}

// Named creates a new mock logger with the given name.
func (m *TestLoggerMock) Named(name string) Logger {
	// Return the same instance to share log captures
	// This is different from real loggers but useful for testing
	m.name = name
	return m
}

// Reset clears all captured log calls.
func (m *TestLoggerMock) Reset() {
	m.DebugCalls = m.DebugCalls[:0]
	m.InfoCalls = m.InfoCalls[:0]
	m.WarnCalls = m.WarnCalls[:0]
	m.ErrorCalls = m.ErrorCalls[:0]
}

// GetAllCalls returns all captured log calls across all levels.
func (m *TestLoggerMock) GetAllCalls() []LogCall {
	all := make([]LogCall, 0, len(m.DebugCalls)+len(m.InfoCalls)+len(m.WarnCalls)+len(m.ErrorCalls))
	all = append(all, m.DebugCalls...)
	all = append(all, m.InfoCalls...)
	all = append(all, m.WarnCalls...)
	all = append(all, m.ErrorCalls...)
	return all
}

// HasDebugMessage checks if a debug message was logged.
func (m *TestLoggerMock) HasDebugMessage(msg string) bool {
	for _, call := range m.DebugCalls {
		if call.Message == msg {
			return true
		}
	}
	return false
}

// HasInfoMessage checks if an info message was logged.
func (m *TestLoggerMock) HasInfoMessage(msg string) bool {
	for _, call := range m.InfoCalls {
		if call.Message == msg {
			return true
		}
	}
	return false
}

// HasWarnMessage checks if a warn message was logged.
func (m *TestLoggerMock) HasWarnMessage(msg string) bool {
	for _, call := range m.WarnCalls {
		if call.Message == msg {
			return true
		}
	}
	return false
}

// HasErrorMessage checks if an error message was logged.
func (m *TestLoggerMock) HasErrorMessage(msg string) bool {
	for _, call := range m.ErrorCalls {
		if call.Message == msg {
			return true
		}
	}
	return false
}
