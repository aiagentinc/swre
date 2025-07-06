// Package swre provides a high-performance, production-ready SWR cache implementation in Go.
package swre

import (
	"time"
)

// Logger defines a structured logging interface that can be implemented by various logging libraries.
// This interface is designed to provide structured logging capabilities while maintaining flexibility
// for users to choose their preferred logging implementation.
//
// The interface follows common logging patterns and supports the most frequently used log levels
// and field types required by the SWR cache engine.
type Logger interface {
	// Debug logs a message at debug level with optional structured fields.
	// Debug messages are typically used for detailed troubleshooting information.
	Debug(msg string, fields ...Field)

	// Info logs a message at info level with optional structured fields.
	// Info messages are used for general informational messages.
	Info(msg string, fields ...Field)

	// Warn logs a message at warning level with optional structured fields.
	// Warning messages indicate potentially harmful situations.
	Warn(msg string, fields ...Field)

	// Error logs a message at error level with optional structured fields.
	// Error messages indicate error conditions that should be investigated.
	Error(msg string, fields ...Field)

	// Named creates a new Logger instance with the specified name.
	// This is useful for creating subsystem-specific loggers with a common prefix.
	// The behavior of naming is implementation-specific (e.g., dot-separated, slash-separated).
	Named(name string) Logger
}

// Field represents a structured logging field with a key-value pair.
// Implementations of this interface can optimize storage and serialization
// based on the specific logging library being used.
type Field interface {
	// Key returns the field's key/name.
	Key() string

	// Value returns the field's value.
	// The type of the value depends on the field type.
	Value() interface{}

	// Type returns the field type for optimized handling.
	Type() FieldType
}

// FieldType represents the type of a logging field.
// This allows logging adapters to optimize serialization.
type FieldType int

const (
	// FieldTypeUnknown indicates an unknown field type.
	FieldTypeUnknown FieldType = iota
	// FieldTypeString indicates a string field.
	FieldTypeString
	// FieldTypeInt indicates an integer field.
	FieldTypeInt
	// FieldTypeInt32 indicates a 32-bit integer field.
	FieldTypeInt32
	// FieldTypeInt64 indicates a 64-bit integer field.
	FieldTypeInt64
	// FieldTypeDuration indicates a time.Duration field.
	FieldTypeDuration
	// FieldTypeTime indicates a time.Time field.
	FieldTypeTime
	// FieldTypeError indicates an error field.
	FieldTypeError
	// FieldTypeAny indicates a field with any type.
	FieldTypeAny
	// FieldTypeByteString indicates a byte string field.
	FieldTypeByteString
	// FieldTypeStack indicates a stack trace field.
	FieldTypeStack
)

// field is the internal implementation of the Field interface.
type field struct {
	key       string
	value     interface{}
	fieldType FieldType
}

// Key returns the field's key.
func (f field) Key() string {
	return f.key
}

// Value returns the field's value.
func (f field) Value() interface{} {
	return f.value
}

// Type returns the field's type.
func (f field) Type() FieldType {
	return f.fieldType
}

// String creates a string field.
func String(key, val string) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeString,
	}
}

// Int creates an integer field.
func Int(key string, val int) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeInt,
	}
}

// Int32 creates a 32-bit integer field.
func Int32(key string, val int32) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeInt32,
	}
}

// Int64 creates a 64-bit integer field.
func Int64(key string, val int64) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeInt64,
	}
}

// Duration creates a time.Duration field.
func Duration(key string, val time.Duration) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeDuration,
	}
}

// Time creates a time.Time field.
func Time(key string, val time.Time) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeTime,
	}
}

// Error creates an error field with the key "error".
// If err is nil, it returns a field with a nil value.
func Error(err error) Field {
	return field{
		key:       "error",
		value:     err,
		fieldType: FieldTypeError,
	}
}

// ErrorKey creates an error field with a custom key.
func ErrorKey(key string, err error) Field {
	return field{
		key:       key,
		value:     err,
		fieldType: FieldTypeError,
	}
}

// Any creates a field with any type of value.
// This should be used sparingly as it may be less efficient than typed fields.
func Any(key string, val interface{}) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeAny,
	}
}

// ByteString creates a byte string field.
// This is useful for logging binary data that should be treated as a string.
func ByteString(key string, val []byte) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeByteString,
	}
}

// Stack creates a stack trace field with the key "stacktrace".
// The value should be a string representation of the stack trace.
func Stack(val string) Field {
	return field{
		key:       "stacktrace",
		value:     val,
		fieldType: FieldTypeStack,
	}
}

// StackKey creates a stack trace field with a custom key.
func StackKey(key string, val string) Field {
	return field{
		key:       key,
		value:     val,
		fieldType: FieldTypeStack,
	}
}

// NoOpLogger is a logger implementation that discards all log messages.
// It's useful for testing or when logging needs to be disabled.
type NoOpLogger struct{}

// Debug implements Logger.Debug.
func (n NoOpLogger) Debug(msg string, fields ...Field) {}

// Info implements Logger.Info.
func (n NoOpLogger) Info(msg string, fields ...Field) {}

// Warn implements Logger.Warn.
func (n NoOpLogger) Warn(msg string, fields ...Field) {}

// Error implements Logger.Error.
func (n NoOpLogger) Error(msg string, fields ...Field) {}

// Named implements Logger.Named.
func (n NoOpLogger) Named(name string) Logger {
	return n
}

// NewNoOpLogger creates a new no-op logger.
func NewNoOpLogger() Logger {
	return NoOpLogger{}
}
