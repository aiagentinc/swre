// Package swre provides a high-performance, production-ready SWR cache implementation in Go.
package swre

import (
	"log/slog"
	"time"
)

// SlogAdapter adapts Go's standard slog.Logger to implement the Logger interface.
// This adapter enables using the standard library's structured logger with SWR cache.
type SlogAdapter struct {
	logger *slog.Logger
}

// NewSlogAdapter creates a new SlogAdapter from an existing slog.Logger.
func NewSlogAdapter(logger *slog.Logger) (*SlogAdapter, error) {
	if logger == nil {
		return nil, ErrNilLogger
	}
	return &SlogAdapter{logger: logger}, nil
}

// Debug logs a message at debug level with optional structured fields.
func (s *SlogAdapter) Debug(msg string, fields ...Field) {
	s.logger.Debug(msg, s.convertFieldsToAttrs(fields)...)
}

// Info logs a message at info level with optional structured fields.
func (s *SlogAdapter) Info(msg string, fields ...Field) {
	s.logger.Info(msg, s.convertFieldsToAttrs(fields)...)
}

// Warn logs a message at warning level with optional structured fields.
func (s *SlogAdapter) Warn(msg string, fields ...Field) {
	s.logger.Warn(msg, s.convertFieldsToAttrs(fields)...)
}

// Error logs a message at error level with optional structured fields.
func (s *SlogAdapter) Error(msg string, fields ...Field) {
	s.logger.Error(msg, s.convertFieldsToAttrs(fields)...)
}

// Named creates a new Logger instance with the specified name.
func (s *SlogAdapter) Named(name string) Logger {
	// slog doesn't have built-in support for named loggers,
	// so we add it as a persistent attribute
	return &SlogAdapter{
		logger: s.logger.With("component", name),
	}
}

// convertFieldsToAttrs converts generic Fields to slog attributes.
func (s *SlogAdapter) convertFieldsToAttrs(fields []Field) []any {
	if len(fields) == 0 {
		return nil
	}

	attrs := make([]any, 0, len(fields)*2)
	for _, f := range fields {
		if f == nil {
			continue
		}

		switch f.Type() {
		case FieldTypeString:
			if v, ok := f.Value().(string); ok {
				attrs = append(attrs, slog.String(f.Key(), v))
			}
		case FieldTypeInt:
			if v, ok := f.Value().(int); ok {
				attrs = append(attrs, slog.Int(f.Key(), v))
			}
		case FieldTypeInt32:
			if v, ok := f.Value().(int32); ok {
				attrs = append(attrs, slog.Int(f.Key(), int(v)))
			}
		case FieldTypeInt64:
			if v, ok := f.Value().(int64); ok {
				attrs = append(attrs, slog.Int64(f.Key(), v))
			}
		case FieldTypeDuration:
			if v, ok := f.Value().(time.Duration); ok {
				attrs = append(attrs, slog.Duration(f.Key(), v))
			}
		case FieldTypeTime:
			if v, ok := f.Value().(time.Time); ok {
				attrs = append(attrs, slog.Time(f.Key(), v))
			}
		case FieldTypeError:
			if err, ok := f.Value().(error); ok && err != nil {
				attrs = append(attrs, slog.String(f.Key(), err.Error()))
			}
		case FieldTypeByteString:
			if v, ok := f.Value().([]byte); ok {
				attrs = append(attrs, slog.String(f.Key(), string(v)))
			}
		case FieldTypeStack:
			if v, ok := f.Value().(string); ok {
				attrs = append(attrs, slog.String(f.Key(), v))
			}
		case FieldTypeAny:
			attrs = append(attrs, slog.Any(f.Key(), f.Value()))
		default:
			attrs = append(attrs, slog.Any(f.Key(), f.Value()))
		}
	}

	return attrs
}