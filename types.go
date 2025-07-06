package swre

import (
	"context"
	"errors"
	"time"
)

// Common errors
var (
	// ErrNilLogger is returned when a nil logger is provided
	ErrNilLogger = errors.New("logger cannot be nil")
)

// Serializer defines the interface for cache value serialization.
// This abstraction allows pluggable serialization strategies optimized
// for different data types and performance requirements.
//
// Implementation Guidelines:
// - Should be thread-safe for concurrent use
// - Marshal should handle nil values gracefully
// - Unmarshal should validate input data
// - Consider compression for large payloads
type Serializer interface {
	// Marshal converts a Go value to bytes for storage
	Marshal(v interface{}) ([]byte, error)

	// Unmarshal converts stored bytes back to a Go value
	Unmarshal(data []byte, v interface{}) error
}

// TTLCalculator defines the interface for dynamic TTL calculation.
// This allows cache TTL to be determined based on the actual data content,
// key patterns, or external factors like DNS record types.
//
// TTL Relationship:
// - totalTTL = fresh period + stale period + expired period
// - staleTTL = fresh period (when background refresh starts)
// - The difference (totalTTL - staleTTL) is the stale serving window
//
// Use Cases:
// - DNS records with varying TTLs based on record type
// - Content-based TTL (e.g., larger responses cached longer)
// - Time-of-day based TTL adjustments
type TTLCalculator interface {
	// CalculateTTL computes TTL values for a cache entry.
	// Returns (totalTTL, staleTTL, error) where:
	// - totalTTL: Complete lifetime of the cache entry
	// - staleTTL: Fresh period before stale-while-revalidate kicks in
	// - error: Calculation failure (uses defaults)
	CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error)
}

// ValueTransformer defines the interface for value preprocessing.
// This abstraction enables custom transformations like compression,
// encryption, or format conversion before caching.
//
// Common Use Cases:
// - Compression for large DNS responses
// - Encryption for sensitive data
// - Format normalization (e.g., DNS wire format)
// - Content filtering or sanitization
//
// Implementation Notes:
// - Transform operates on pre-serialization values
// - Restore works with post-deserialization data
// - Both methods should be idempotent
// - Must handle context cancellation gracefully
type ValueTransformer interface {
	// Transform processes a value before caching.
	// Called after upstream fetch but before serialization.
	Transform(ctx context.Context, key string, value interface{}) (interface{}, error)

	// Restore converts cached data back to usable form.
	// Called after deserialization but before returning to caller.
	Restore(ctx context.Context, key string, data []byte) (interface{}, error)
}

// CacheMetrics defines the interface for cache observability.
// This abstraction enables pluggable metrics collection for monitoring
// cache performance, hit rates, and error patterns.
//
// Metrics Categories:
// - Hit/Miss ratios for cache effectiveness
// - Latency distributions for performance monitoring
// - Error rates and types for reliability tracking
// - Per-key patterns for hotspot identification
//
// Implementation Guidelines:
// - Methods should be non-blocking and fast
// - Consider sampling for high-frequency operations
// - Include relevant labels/tags for aggregation
// - Handle nil/invalid inputs gracefully
type CacheMetrics interface {
	// RecordHit tracks cache hit events with status details.
	// Status values: "hit", "stale", "expired", "refresh", "fallback"
	RecordHit(key string, status string)

	// RecordMiss tracks cache miss events requiring upstream fetch.
	RecordMiss(key string)

	// RecordError tracks error events for reliability monitoring.
	RecordError(key string, err error)

	// RecordLatency tracks operation timing for performance analysis.
	RecordLatency(key string, duration time.Duration)
}

// EngineConfig provides comprehensive configuration for StaleEngine initialization.
// This structure centralizes all tunable parameters for cache behavior,
// performance characteristics, and operational features.
//
// Configuration Categories:
// - Core: Required components (Storage, Logger)
// - TTL: Cache lifetime management
// - Extensibility: Pluggable components for customization
// - Performance: Concurrency and timeout controls
//
// Usage Patterns:
// - Use SetDefaults() to apply sensible defaults
// - Override specific fields for custom behavior
// - Validate configuration before engine creation
type EngineConfig struct {
	// Core required components
	Storage Storage // Backend storage implementation
	Logger  Logger  // Structured logger for operations

	// Legacy TTL configuration (deprecated, use DefaultCacheTTL)
	DefaultTTL         time.Duration // Total cache lifetime
	DefaultStaleOffset time.Duration // Offset from total TTL to stale time

	// Modern TTL configuration with explicit phases
	DefaultCacheTTL *CacheTTL // Structured TTL with fresh/stale/expired periods

	// Pluggable components for extensibility
	Serializer       Serializer       // Value serialization strategy
	TTLCalculator    TTLCalculator    // Dynamic TTL calculation
	ValueTransformer ValueTransformer // Value preprocessing pipeline
	Metrics          CacheMetrics     // Observability and monitoring

	// Performance and concurrency controls
	MaxConcurrentRefreshes int           // Limit for background refresh goroutines
	RefreshTimeout         time.Duration // Individual refresh operation timeout
}

// SetDefaults applies default values to config
func (c *EngineConfig) SetDefaults() {
	// If DefaultCacheTTL is provided, use it to set DefaultTTL and DefaultStaleOffset
	if c.DefaultCacheTTL != nil {
		totalTTL := time.Duration(c.DefaultCacheTTL.FreshSeconds+c.DefaultCacheTTL.StaleSeconds+c.DefaultCacheTTL.ExpiredSeconds) * time.Second
		freshTTL := time.Duration(c.DefaultCacheTTL.FreshSeconds) * time.Second

		c.DefaultTTL = totalTTL
		c.DefaultStaleOffset = totalTTL - freshTTL

		// Set TTL calculator if not already set
		if c.TTLCalculator == nil {
			c.TTLCalculator = &DefaultTTLCalculator{
				TTL:      totalTTL,
				StaleTTL: freshTTL,
			}
		}
	} else {
		// Use legacy defaults
		if c.DefaultTTL == 0 {
			c.DefaultTTL = 15 * 24 * time.Hour // 15 days
		}
		if c.DefaultStaleOffset == 0 {
			c.DefaultStaleOffset = 60 * time.Second // 60 seconds
		}
		if c.TTLCalculator == nil {
			c.TTLCalculator = &DefaultTTLCalculator{
				TTL:      c.DefaultTTL,
				StaleTTL: c.DefaultTTL - c.DefaultStaleOffset,
			}
		}
	}

	if c.Serializer == nil {
		c.Serializer = &JSONSerializer{}
	}
	if c.MaxConcurrentRefreshes == 0 {
		c.MaxConcurrentRefreshes = 1000
	}
	if c.RefreshTimeout == 0 {
		c.RefreshTimeout = 30 * time.Second
	}
}
