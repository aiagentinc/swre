package swre

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestNewSlogAdapter(t *testing.T) {
	t.Run("success with valid logger", func(t *testing.T) {
		logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
		adapter, err := NewSlogAdapter(logger)
		if err != nil {
			t.Fatalf("expected no error, got %v", err)
		}
		if adapter == nil {
			t.Fatal("expected adapter to be non-nil")
		}
		if adapter.logger != logger {
			t.Fatal("adapter logger should match input logger")
		}
	})

	t.Run("error with nil logger", func(t *testing.T) {
		adapter, err := NewSlogAdapter(nil)
		if err != ErrNilLogger {
			t.Fatalf("expected ErrNilLogger, got %v", err)
		}
		if adapter != nil {
			t.Fatal("expected adapter to be nil")
		}
	})
}

func TestSlogAdapter_LoggingMethods(t *testing.T) {
	tests := []struct {
		name     string
		logFunc  func(adapter *SlogAdapter, msg string, fields ...Field)
		level    slog.Level
		message  string
		fields   []Field
	}{
		{
			name:    "Debug with no fields",
			logFunc: (*SlogAdapter).Debug,
			level:   slog.LevelDebug,
			message: "debug message",
			fields:  nil,
		},
		{
			name:    "Info with string field",
			logFunc: (*SlogAdapter).Info,
			level:   slog.LevelInfo,
			message: "info message",
			fields:  []Field{String("key", "value")},
		},
		{
			name:    "Warn with int field",
			logFunc: (*SlogAdapter).Warn,
			level:   slog.LevelWarn,
			message: "warn message",
			fields:  []Field{Int("count", 42)},
		},
		{
			name:    "Error with error field",
			logFunc: (*SlogAdapter).Error,
			level:   slog.LevelError,
			message: "error message",
			fields:  []Field{ErrorKey("err", errors.New("test error"))},
		},
		{
			name:    "Debug with multiple fields",
			logFunc: (*SlogAdapter).Debug,
			level:   slog.LevelDebug,
			message: "multi-field message",
			fields: []Field{
				String("str", "value"),
				Int("num", 123),
				Any("flag", true),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			opts := &slog.HandlerOptions{Level: slog.LevelDebug}
			handler := slog.NewJSONHandler(&buf, opts)
			logger := slog.New(handler)
			adapter, _ := NewSlogAdapter(logger)

			tt.logFunc(adapter, tt.message, tt.fields...)

			var logEntry map[string]interface{}
			if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
				t.Fatalf("failed to unmarshal log entry: %v", err)
			}

			if msg, ok := logEntry["msg"].(string); !ok || msg != tt.message {
				t.Errorf("expected message %q, got %q", tt.message, msg)
			}

			if level, ok := logEntry["level"].(string); !ok || level != tt.level.String() {
				t.Errorf("expected level %q, got %q", tt.level.String(), level)
			}

			for _, field := range tt.fields {
				if field == nil {
					continue
				}
				key := field.Key()
				if _, exists := logEntry[key]; !exists {
					t.Errorf("expected field %q to exist in log entry", key)
				}
			}
		})
	}
}

func TestSlogAdapter_Named(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	logger := slog.New(handler)
	adapter, _ := NewSlogAdapter(logger)

	namedAdapter := adapter.Named("test-component")
	namedAdapter.Info("named logger message")

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	if component, ok := logEntry["component"].(string); !ok || component != "test-component" {
		t.Errorf("expected component name %q, got %q", "test-component", component)
	}
}

func TestSlogAdapter_convertFieldsToAttrs(t *testing.T) {
	adapter := &SlogAdapter{}
	now := time.Now()
	testError := errors.New("test error")
	stackTrace := "goroutine 1 [running]:\nmain.main()\n\t/tmp/main.go:5 +0x20"

	tests := []struct {
		name           string
		fields         []Field
		expectedCount  int
		validateResult func(t *testing.T, attrs []any)
	}{
		{
			name:          "nil fields slice",
			fields:        nil,
			expectedCount: 0,
		},
		{
			name:          "empty fields slice",
			fields:        []Field{},
			expectedCount: 0,
		},
		{
			name:          "fields with nil element",
			fields:        []Field{nil, String("key", "value"), nil},
			expectedCount: 1,
		},
		{
			name: "string field",
			fields: []Field{
				String("str", "hello"),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "str" || attr.Value.String() != "hello" {
					t.Errorf("unexpected string attribute: %v", attr)
				}
			},
		},
		{
			name: "int field",
			fields: []Field{
				Int("num", 42),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "num" || attr.Value.Int64() != 42 {
					t.Errorf("unexpected int attribute: %v", attr)
				}
			},
		},
		{
			name: "int32 field",
			fields: []Field{
				Int32("num32", 32),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "num32" || attr.Value.Int64() != 32 {
					t.Errorf("unexpected int32 attribute: %v", attr)
				}
			},
		},
		{
			name: "int64 field",
			fields: []Field{
				Int64("num64", 64),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "num64" || attr.Value.Int64() != 64 {
					t.Errorf("unexpected int64 attribute: %v", attr)
				}
			},
		},
		{
			name: "duration field",
			fields: []Field{
				Duration("dur", 5*time.Second),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "dur" || attr.Value.Duration() != 5*time.Second {
					t.Errorf("unexpected duration attribute: %v", attr)
				}
			},
		},
		{
			name: "time field",
			fields: []Field{
				Time("timestamp", now),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "timestamp" || !attr.Value.Time().Equal(now) {
					t.Errorf("unexpected time attribute: %v", attr)
				}
			},
		},
		{
			name: "error field",
			fields: []Field{
				ErrorKey("err", testError),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "err" || attr.Value.String() != testError.Error() {
					t.Errorf("unexpected error attribute: %v", attr)
				}
			},
		},
		{
			name: "nil error field",
			fields: []Field{
				ErrorKey("err", nil),
			},
			expectedCount: 0,
		},
		{
			name: "byte string field",
			fields: []Field{
				ByteString("bytes", []byte("hello bytes")),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "bytes" || attr.Value.String() != "hello bytes" {
					t.Errorf("unexpected byte string attribute: %v", attr)
				}
			},
		},
		{
			name: "stack field",
			fields: []Field{
				StackKey("stack", stackTrace),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "stack" || attr.Value.String() != stackTrace {
					t.Errorf("unexpected stack attribute: %v", attr)
				}
			},
		},
		{
			name: "any field",
			fields: []Field{
				Any("custom", map[string]int{"a": 1, "b": 2}),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "custom" {
					t.Errorf("unexpected any attribute key: %v", attr.Key)
				}
			},
		},
		{
			name: "bool field (via Any)",
			fields: []Field{
				Any("flag", true),
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "flag" || attr.Value.Bool() != true {
					t.Errorf("unexpected bool attribute: %v", attr)
				}
			},
		},
		{
			name: "multiple fields of different types",
			fields: []Field{
				String("str", "value"),
				Int("num", 100),
				Duration("dur", time.Minute),
				ErrorKey("err", testError),
				Any("flag", false),
			},
			expectedCount: 5,
		},
		{
			name: "field with wrong type assertion",
			fields: []Field{
				&fieldImpl{
					key:       "test",
					value:     123,
					fieldType: FieldTypeString,
				},
			},
			expectedCount: 0,
		},
		{
			name: "field with unknown type",
			fields: []Field{
				&fieldImpl{
					key:       "unknown",
					value:     "unknown value",
					fieldType: FieldType(999),
				},
			},
			expectedCount: 1,
			validateResult: func(t *testing.T, attrs []any) {
				attr := attrs[0].(slog.Attr)
				if attr.Key != "unknown" {
					t.Errorf("unexpected attribute key for unknown type: %v", attr.Key)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			attrs := adapter.convertFieldsToAttrs(tt.fields)
			
			if len(attrs) != tt.expectedCount {
				t.Errorf("expected %d attributes, got %d", tt.expectedCount, len(attrs))
			}

			if tt.validateResult != nil && len(attrs) > 0 {
				tt.validateResult(t, attrs)
			}
		})
	}
}

func TestSlogAdapter_IntegrationWithRealLogger(t *testing.T) {
	var buf bytes.Buffer
	opts := &slog.HandlerOptions{
		Level: slog.LevelDebug,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}
	handler := slog.NewJSONHandler(&buf, opts)
	logger := slog.New(handler)
	adapter, _ := NewSlogAdapter(logger)

	adapter.Debug("debug", String("level", "debug"))
	adapter.Info("info", Int("count", 1))
	adapter.Warn("warn", Duration("elapsed", time.Second))
	adapter.Error("error", ErrorKey("err", errors.New("failed")))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 log lines, got %d", len(lines))
	}

	expectedMessages := []string{"debug", "info", "warn", "error"}
	for i, line := range lines {
		var entry map[string]interface{}
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("failed to unmarshal line %d: %v", i, err)
		}
		if msg, ok := entry["msg"].(string); !ok || msg != expectedMessages[i] {
			t.Errorf("line %d: expected message %q, got %q", i, expectedMessages[i], msg)
		}
	}
}

func TestSlogAdapter_ContextLogging(t *testing.T) {
	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, nil)
	baseLogger := slog.New(handler)
	
	contextLogger := baseLogger.With("request-id", "12345")
	
	adapter, _ := NewSlogAdapter(contextLogger)
	adapter.Info("context message", String("key", "value"))

	var logEntry map[string]interface{}
	if err := json.Unmarshal(buf.Bytes(), &logEntry); err != nil {
		t.Fatalf("failed to unmarshal log entry: %v", err)
	}

	if msg, ok := logEntry["msg"].(string); !ok || msg != "context message" {
		t.Errorf("expected message %q, got %q", "context message", msg)
	}
	
	if reqID, ok := logEntry["request-id"].(string); !ok || reqID != "12345" {
		t.Errorf("expected request-id %q, got %q", "12345", reqID)
	}
}

type fieldImpl struct {
	key       string
	value     interface{}
	fieldType FieldType
}

func (f *fieldImpl) Key() string          { return f.key }
func (f *fieldImpl) Value() interface{}   { return f.value }
func (f *fieldImpl) Type() FieldType      { return f.fieldType }