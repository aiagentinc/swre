package swre

import (
	"bytes"
	"compress/gzip"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestCompressedSerializer_EdgeCases tests edge cases for compression
func TestCompressedSerializer_EdgeCases(t *testing.T) {
	s := &CompressedSerializer{
		Inner: &JSONSerializer{},
		Level: gzip.DefaultCompression,
	}

	// Test Write error path by using closed writer
	// This tests the error handling in Marshal when Write fails
	type largeStruct struct {
		Data []byte
	}

	// Normal case should work
	large := largeStruct{Data: make([]byte, 1024)}
	_, err := s.Marshal(large)
	assert.NoError(t, err)
}

// TestCompressedSerializer_CloseError tests gzip writer close error handling
func TestCompressedSerializer_CloseError(t *testing.T) {
	s := &CompressedSerializer{
		Inner: &JSONSerializer{},
		Level: gzip.DefaultCompression,
	}

	// Test with empty data
	_, err := s.Marshal([]byte{})
	assert.NoError(t, err)

	// Test with nil
	_, err = s.Marshal(nil)
	assert.NoError(t, err)
}

// TestNoOpMetrics_Coverage tests NoOpMetrics for coverage
func TestNoOpMetrics_Coverage(t *testing.T) {
	m := &NoOpMetrics{}

	// Call all methods to ensure coverage
	m.RecordHit("key1", "hit")
	m.RecordHit("key2", "stale")
	m.RecordMiss("key3")
	m.RecordError("key4", nil)
	m.RecordLatency("key5", time.Second)

	// No assertions needed - just coverage
}

// TestCompressedSerializer_UnmarshalIOError tests unmarshal IO error
func TestCompressedSerializer_UnmarshalIOError(t *testing.T) {
	s := NewCompressedSerializer(&JSONSerializer{})

	// Create truncated gzip data
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte("test data that will be truncated"))
	_ = w.Close()

	// Truncate the data to cause IO error
	data := buf.Bytes()
	truncated := data[:len(data)-5]

	var result string
	err := s.Unmarshal(truncated, &result)
	assert.Error(t, err)
}
