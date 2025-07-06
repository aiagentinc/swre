// Package swre provides a high-performance, production-ready SWR cache implementation in Go.
package swre

import (
	"runtime/debug"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// ZapAdapter adapts a zap.Logger to implement the Logger interface.
// This adapter provides a bridge between the generic Logger interface
// and zap's specific implementation, allowing existing zap users to
// continue using their configured loggers seamlessly.
type ZapAdapter struct {
	logger *zap.Logger
}

// NewZapAdapter creates a new ZapAdapter from an existing zap.Logger.
// If the provided logger is nil, it returns an error.
func NewZapAdapter(logger *zap.Logger) (*ZapAdapter, error) {
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &ZapAdapter{logger: logger}, nil
}

// Debug logs a message at debug level with optional structured fields.
func (z *ZapAdapter) Debug(msg string, fields ...Field) {
	if ce := z.logger.Check(zapcore.DebugLevel, msg); ce != nil {
		ce.Write(z.convertFields(fields)...)
	}
}

// Info logs a message at info level with optional structured fields.
func (z *ZapAdapter) Info(msg string, fields ...Field) {
	if ce := z.logger.Check(zapcore.InfoLevel, msg); ce != nil {
		ce.Write(z.convertFields(fields)...)
	}
}

// Warn logs a message at warning level with optional structured fields.
func (z *ZapAdapter) Warn(msg string, fields ...Field) {
	if ce := z.logger.Check(zapcore.WarnLevel, msg); ce != nil {
		ce.Write(z.convertFields(fields)...)
	}
}

// Error logs a message at error level with optional structured fields.
func (z *ZapAdapter) Error(msg string, fields ...Field) {
	if ce := z.logger.Check(zapcore.ErrorLevel, msg); ce != nil {
		ce.Write(z.convertFields(fields)...)
	}
}

// Named creates a new Logger instance with the specified name.
func (z *ZapAdapter) Named(name string) Logger {
	return &ZapAdapter{logger: z.logger.Named(name)}
}

// convertFields converts generic Fields to zap.Fields.
// This method optimizes field conversion based on field types to maintain
// the performance characteristics of the underlying zap logger.
func (z *ZapAdapter) convertFields(fields []Field) []zap.Field {
	if len(fields) == 0 {
		return nil
	}

	zapFields := make([]zap.Field, 0, len(fields))
	for _, f := range fields {
		if f == nil {
			continue
		}

		switch f.Type() {
		case FieldTypeString:
			if v, ok := f.Value().(string); ok {
				zapFields = append(zapFields, zap.String(f.Key(), v))
			}
		case FieldTypeInt:
			if v, ok := f.Value().(int); ok {
				zapFields = append(zapFields, zap.Int(f.Key(), v))
			}
		case FieldTypeInt32:
			if v, ok := f.Value().(int32); ok {
				zapFields = append(zapFields, zap.Int32(f.Key(), v))
			}
		case FieldTypeInt64:
			if v, ok := f.Value().(int64); ok {
				zapFields = append(zapFields, zap.Int64(f.Key(), v))
			}
		case FieldTypeDuration:
			if v, ok := f.Value().(time.Duration); ok {
				zapFields = append(zapFields, zap.Duration(f.Key(), v))
			}
		case FieldTypeTime:
			if v, ok := f.Value().(time.Time); ok {
				zapFields = append(zapFields, zap.Time(f.Key(), v))
			}
		case FieldTypeError:
			if err, ok := f.Value().(error); ok {
				if err != nil {
					zapFields = append(zapFields, zap.Error(err))
				}
			}
		case FieldTypeByteString:
			if v, ok := f.Value().([]byte); ok {
				zapFields = append(zapFields, zap.ByteString(f.Key(), v))
			}
		case FieldTypeStack:
			if v, ok := f.Value().(string); ok {
				zapFields = append(zapFields, zap.String(f.Key(), v))
			}
		case FieldTypeAny:
			zapFields = append(zapFields, zap.Any(f.Key(), f.Value()))
		default:
			// For unknown types, use Any
			zapFields = append(zapFields, zap.Any(f.Key(), f.Value()))
		}
	}

	return zapFields
}

// zapToLoggerAdapter is a helper function that automatically wraps a *zap.Logger
// into a Logger interface. This is used internally for backward compatibility.
func zapToLoggerAdapter(zapLogger *zap.Logger) Logger {
	if zapLogger == nil {
		return NewNoOpLogger()
	}
	adapter, _ := NewZapAdapter(zapLogger)
	return adapter
}

// Helper function to handle stack traces for backward compatibility
func captureStack() string {
	return string(debug.Stack())
}
