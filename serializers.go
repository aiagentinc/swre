package swre

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"fmt"
	"io"
	"time"
)

// JSONSerializer implements high-performance JSON serialization using jsoniter.
// This serializer provides 2-3x better performance than the standard library
// while maintaining full compatibility with encoding/json.
//
// Performance Characteristics:
// - Optimized for speed over memory usage
// - Uses jsoniter.ConfigFastest for maximum throughput
// - Suitable for high-frequency cache operations
// - Thread-safe for concurrent use
type JSONSerializer struct{}

func (j *JSONSerializer) Marshal(v interface{}) ([]byte, error) {
	return jsonFast.Marshal(v)
}

func (j *JSONSerializer) Unmarshal(data []byte, v interface{}) error {
	return jsonFast.Unmarshal(data, v)
}

// GobSerializer implements Go-specific binary serialization using encoding/gob.
// This serializer is more efficient than JSON for Go-native types but
// sacrifices cross-language compatibility.
//
// Trade-offs:
// - Faster serialization for complex Go structures
// - More compact binary representation
// - Go-specific format (not interoperable with other languages)
// - Requires type registration for complex interfaces
//
// Best suited for:
// - Internal Go-to-Go communication
// - Complex nested structures
// - When binary size matters more than interoperability
type GobSerializer struct{}

func (g *GobSerializer) Marshal(v interface{}) ([]byte, error) {
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func (g *GobSerializer) Unmarshal(data []byte, v interface{}) error {
	dec := gob.NewDecoder(bytes.NewReader(data))
	return dec.Decode(v)
}

// CompressedSerializer adds gzip compression to any underlying serializer.
// This decorator pattern allows compression to be layered onto existing
// serialization strategies for size-sensitive scenarios.
//
// Use Cases:
// - Large DNS responses (e.g., TXT records with certificates)
// - Memory-constrained environments
// - Network bandwidth optimization
// - Long-term storage of large cache entries
//
// Performance Trade-offs:
// - Significant CPU overhead during serialization/deserialization
// - Reduced memory usage and network transfer
// - Compression ratio varies by data type (JSON compresses well)
type CompressedSerializer struct {
	Inner Serializer // Underlying serializer to compress
	Level int        // gzip compression level (1=fast, 9=best compression)
}

func NewCompressedSerializer(inner Serializer) *CompressedSerializer {
	return &CompressedSerializer{
		Inner: inner,
		Level: gzip.DefaultCompression,
	}
}

func (c *CompressedSerializer) Marshal(v interface{}) ([]byte, error) {
	// First serialize with inner serializer
	data, err := c.Inner.Marshal(v)
	if err != nil {
		return nil, err
	}

	// Then compress
	var buf bytes.Buffer
	w, err := gzip.NewWriterLevel(&buf, c.Level)
	if err != nil {
		return nil, err
	}

	if _, err := w.Write(data); err != nil {
		w.Close()
		return nil, err
	}

	if err := w.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

func (c *CompressedSerializer) Unmarshal(data []byte, v interface{}) error {
	// First decompress
	r, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return err
	}
	defer r.Close()

	decompressed, err := io.ReadAll(r)
	if err != nil {
		return err
	}

	// Then deserialize with inner serializer
	return c.Inner.Unmarshal(decompressed, v)
}

// DefaultTTLCalculator provides static TTL values for all cache entries.
// This is the simplest TTL strategy, using fixed durations regardless
// of key patterns or content characteristics.
//
// Configuration:
// - TTL: Total cache entry lifetime
// - StaleTTL: Fresh period before stale-while-revalidate
//
// Suitable for:
// - Uniform caching policies
// - Simple deployment scenarios
// - When content characteristics don't vary significantly
type DefaultTTLCalculator struct {
	TTL      time.Duration // Total time-to-live for cache entries
	StaleTTL time.Duration // Fresh period before background refresh
}

func (d *DefaultTTLCalculator) CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error) {
	return d.TTL, d.StaleTTL, nil
}

// DynamicTTLCalculator enables custom TTL logic via a user-provided function.
// This flexible approach allows TTL decisions based on content analysis,
// key patterns, external factors, or complex business logic.
//
// Function Signature:
// - Input: cache key and value for context
// - Output: total TTL, stale TTL, and optional error
// - Error triggers fallback to default TTL values
//
// Example Use Cases:
// - DNS record type-specific TTLs (A=1h, MX=24h, TXT=10m)
// - Size-based TTL (larger responses cached longer)
// - Time-of-day adjustments (shorter TTL during peak hours)
// - External API-driven TTL decisions
type DynamicTTLCalculator struct {
	// Calculator function implementing custom TTL logic
	Calculator func(key string, value interface{}) (ttl time.Duration, staleTTL time.Duration, err error)
}

func (d *DynamicTTLCalculator) CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error) {
	if d.Calculator == nil {
		return 0, 0, fmt.Errorf("calculator function not set")
	}
	return d.Calculator(key, value)
}

// NoOpTransformer provides a pass-through implementation of ValueTransformer.
// This null object pattern allows the transformer interface to be optional
// while maintaining a consistent API.
//
// Behavior:
// - Transform: Returns input value unchanged
// - Restore: Returns input data unchanged
// - No processing overhead
// - Useful as a default or placeholder implementation
type NoOpTransformer struct{}

func (n *NoOpTransformer) Transform(ctx context.Context, key string, value interface{}) (interface{}, error) {
	return value, nil
}

func (n *NoOpTransformer) Restore(ctx context.Context, key string, data []byte) (interface{}, error) {
	// For NoOp, we assume data is already the value
	return data, nil
}

// NoOpMetrics provides a null implementation of CacheMetrics.
// This allows metrics collection to be optional while maintaining
// a consistent interface throughout the caching system.
//
// Behavior:
// - All metric recording methods are no-ops
// - Zero performance overhead
// - Useful for testing or when metrics aren't needed
// - Can be replaced with real metrics implementation later
type NoOpMetrics struct{}

func (n *NoOpMetrics) RecordHit(key string, status string)              {}
func (n *NoOpMetrics) RecordMiss(key string)                            {}
func (n *NoOpMetrics) RecordError(key string, err error)                {}
func (n *NoOpMetrics) RecordLatency(key string, duration time.Duration) {}
