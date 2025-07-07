package swre

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/gob"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestJSONSerializer tests JSON serialization
func TestJSONSerializer(t *testing.T) {
	s := &JSONSerializer{}

	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{
			name:     "string",
			input:    "test string",
			expected: "test string",
		},
		{
			name:     "number",
			input:    42,
			expected: float64(42), // JSON unmarshals numbers as float64
		},
		{
			name:     "bool",
			input:    true,
			expected: true,
		},
		{
			name: "struct",
			input: struct {
				Name  string `json:"name"`
				Value int    `json:"value"`
			}{Name: "test", Value: 100},
			expected: map[string]interface{}{"name": "test", "value": float64(100)},
		},
		{
			name:     "slice",
			input:    []string{"a", "b", "c"},
			expected: []interface{}{"a", "b", "c"},
		},
		{
			name:     "map",
			input:    map[string]int{"a": 1, "b": 2},
			expected: map[string]interface{}{"a": float64(1), "b": float64(2)},
		},
		{
			name:     "nil",
			input:    nil,
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := s.Marshal(tt.input)
			require.NoError(t, err)
			assert.NotNil(t, data)

			// Unmarshal
			var result interface{}
			err = s.Unmarshal(data, &result)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestJSONSerializer_Errors tests JSON serialization errors
func TestJSONSerializer_Errors(t *testing.T) {
	s := &JSONSerializer{}

	// Marshal error - channel cannot be marshaled
	_, err := s.Marshal(make(chan int))
	assert.Error(t, err)

	// Unmarshal error - invalid JSON
	err = s.Unmarshal([]byte("invalid json"), &struct{}{})
	assert.Error(t, err)
}

// TestGobSerializer tests Gob serialization
func TestGobSerializer(t *testing.T) {
	s := &GobSerializer{}

	// Register type for gob
	gob.Register(TestStruct{})

	tests := []struct {
		name  string
		input interface{}
	}{
		{
			name:  "string",
			input: "test string",
		},
		{
			name:  "int",
			input: 42,
		},
		{
			name:  "struct",
			input: TestStruct{Name: "test", Value: 100},
		},
		{
			name:  "slice",
			input: []int{1, 2, 3, 4, 5},
		},
		{
			name:  "map",
			input: map[string]string{"key": "value"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Marshal
			data, err := s.Marshal(tt.input)
			require.NoError(t, err)
			assert.NotNil(t, data)

			// Create appropriate container for unmarshaling
			var result interface{}
			switch tt.input.(type) {
			case string:
				var str string
				err = s.Unmarshal(data, &str)
				result = str
			case int:
				var num int
				err = s.Unmarshal(data, &num)
				result = num
			case TestStruct:
				var ts TestStruct
				err = s.Unmarshal(data, &ts)
				result = ts
			case []int:
				var slice []int
				err = s.Unmarshal(data, &slice)
				result = slice
			case map[string]string:
				var m map[string]string
				err = s.Unmarshal(data, &m)
				result = m
			}

			require.NoError(t, err)
			assert.Equal(t, tt.input, result)
		})
	}
}

type TestStruct struct {
	Name  string
	Value int
}

// TestGobSerializer_Errors tests Gob serialization errors
func TestGobSerializer_Errors(t *testing.T) {
	s := &GobSerializer{}

	// Unmarshal error - invalid data
	err := s.Unmarshal([]byte("invalid gob data"), &struct{}{})
	assert.Error(t, err)
}

// TestCompressedSerializer tests compression wrapper
func TestCompressedSerializer(t *testing.T) {
	inner := &JSONSerializer{}

	tests := []struct {
		name             string
		compressionLevel int
		input            interface{}
	}{
		{
			name:             "default compression",
			compressionLevel: gzip.DefaultCompression,
			input:            "This is a test string that should be compressed",
		},
		{
			name:             "best speed",
			compressionLevel: gzip.BestSpeed,
			input:            map[string]string{"key": "value", "foo": "bar"},
		},
		{
			name:             "best compression",
			compressionLevel: gzip.BestCompression,
			input:            []string{"item1", "item2", "item3", "item4", "item5"},
		},
		{
			name:             "no compression",
			compressionLevel: gzip.NoCompression,
			input:            42,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &CompressedSerializer{
				Inner: inner,
				Level: tt.compressionLevel,
			}

			// Marshal
			compressed, err := s.Marshal(tt.input)
			require.NoError(t, err)
			assert.NotNil(t, compressed)

			// Verify it's actually compressed (has gzip header)
			if tt.compressionLevel != gzip.NoCompression {
				assert.True(t, bytes.HasPrefix(compressed, []byte{0x1f, 0x8b}))
			}

			// Unmarshal
			var result interface{}
			err = s.Unmarshal(compressed, &result)
			require.NoError(t, err)

			// Compare based on type (JSON unmarshaling quirks)
			switch v := tt.input.(type) {
			case string:
				assert.Equal(t, v, result)
			case int:
				assert.Equal(t, float64(v), result)
			case map[string]string:
				resultMap := result.(map[string]interface{})
				for k, v := range v {
					assert.Equal(t, v, resultMap[k])
				}
			case []string:
				resultSlice := result.([]interface{})
				for i, v := range v {
					assert.Equal(t, v, resultSlice[i])
				}
			}
		})
	}
}

// TestCompressedSerializer_Constructor tests constructor
func TestCompressedSerializer_Constructor(t *testing.T) {
	inner := &JSONSerializer{}
	s := NewCompressedSerializer(inner)

	assert.Equal(t, inner, s.Inner)
	assert.Equal(t, gzip.DefaultCompression, s.Level)
}

// TestCompressedSerializer_Errors tests compression errors
func TestCompressedSerializer_Errors(t *testing.T) {
	// Test with inner serializer that fails
	failingSer := &failingSerializer{err: errors.New("marshal error")}
	s := &CompressedSerializer{
		Inner: failingSer,
		Level: gzip.DefaultCompression,
	}

	// Marshal error from inner serializer
	_, err := s.Marshal("test")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal error")

	// Test invalid compression level
	s2 := &CompressedSerializer{
		Inner: &JSONSerializer{},
		Level: 999, // Invalid level
	}
	_, err = s2.Marshal("test")
	assert.Error(t, err)

	// Unmarshal error - not gzipped data
	s3 := NewCompressedSerializer(&JSONSerializer{})
	err = s3.Unmarshal([]byte("not compressed"), &struct{}{})
	assert.Error(t, err)

	// Test unmarshal with failing inner serializer
	s4 := &CompressedSerializer{
		Inner: &failingSerializer{err: errors.New("unmarshal error")},
		Level: gzip.DefaultCompression,
	}
	// Create valid gzip data
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write([]byte("test"))
	_ = w.Close()

	err = s4.Unmarshal(buf.Bytes(), &struct{}{})
	assert.Error(t, err)
}

type failingSerializer struct {
	err error
}

func (f *failingSerializer) Marshal(v interface{}) ([]byte, error) {
	return nil, f.err
}

func (f *failingSerializer) Unmarshal(data []byte, v interface{}) error {
	return f.err
}

// TestDefaultTTLCalculator tests default TTL calculation
func TestDefaultTTLCalculator(t *testing.T) {
	calc := &DefaultTTLCalculator{
		TTL:      5 * time.Minute,
		StaleTTL: 4 * time.Minute,
	}

	ttl, staleTTL, err := calc.CalculateTTL("test-key", "test-value")
	assert.NoError(t, err)
	assert.Equal(t, 5*time.Minute, ttl)
	assert.Equal(t, 4*time.Minute, staleTTL)
}

// TestDynamicTTLCalculator tests dynamic TTL calculation
func TestDynamicTTLCalculator(t *testing.T) {
	tests := []struct {
		name          string
		calculator    func(string, interface{}) (time.Duration, time.Duration, error)
		key           string
		value         interface{}
		expectedTTL   time.Duration
		expectedStale time.Duration
		expectedErr   bool
	}{
		{
			name: "based on key prefix",
			calculator: func(key string, value interface{}) (time.Duration, time.Duration, error) {
				if len(key) > 5 && key[:5] == "short" {
					return 1 * time.Minute, 30 * time.Second, nil
				}
				return 10 * time.Minute, 8 * time.Minute, nil
			},
			key:           "short-lived",
			value:         "data",
			expectedTTL:   1 * time.Minute,
			expectedStale: 30 * time.Second,
		},
		{
			name: "based on value type",
			calculator: func(key string, value interface{}) (time.Duration, time.Duration, error) {
				switch value.(type) {
				case string:
					return 5 * time.Minute, 4 * time.Minute, nil
				case int:
					return 10 * time.Minute, 9 * time.Minute, nil
				default:
					return 1 * time.Hour, 50 * time.Minute, nil
				}
			},
			key:           "test",
			value:         42,
			expectedTTL:   10 * time.Minute,
			expectedStale: 9 * time.Minute,
		},
		{
			name: "error case",
			calculator: func(key string, value interface{}) (time.Duration, time.Duration, error) {
				return 0, 0, errors.New("ttl calculation error")
			},
			key:         "error",
			value:       "data",
			expectedErr: true,
		},
		{
			name:        "nil calculator",
			calculator:  nil,
			key:         "test",
			value:       "data",
			expectedErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calc := &DynamicTTLCalculator{
				Calculator: tt.calculator,
			}

			ttl, staleTTL, err := calc.CalculateTTL(tt.key, tt.value)

			if tt.expectedErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expectedTTL, ttl)
				assert.Equal(t, tt.expectedStale, staleTTL)
			}
		})
	}
}

// TestNoOpTransformer tests no-op transformer
func TestNoOpTransformer(t *testing.T) {
	transformer := &NoOpTransformer{}
	ctx := context.Background()

	// Transform - should return same value
	value := "test-value"
	result, err := transformer.Transform(ctx, "key", value)
	assert.NoError(t, err)
	assert.Equal(t, value, result)

	// Restore - should return data as-is
	data := []byte("test-data")
	restored, err := transformer.Restore(ctx, "key", data)
	assert.NoError(t, err)
	assert.Equal(t, data, restored)
}

// TestGobSerializer_EdgeCases tests edge cases for Gob serialization
func TestGobSerializer_EdgeCases(t *testing.T) {
	s := &GobSerializer{}

	// Test encoding error with invalid type (channels can't be encoded)
	_, err := s.Marshal(make(chan int))
	assert.Error(t, err)
}

// TestCompressedSerializer_LargeData tests compression with large data
func TestCompressedSerializer_LargeData(t *testing.T) {
	s := NewCompressedSerializer(&JSONSerializer{})

	// Create large repetitive data (highly compressible)
	largeData := make([]string, 10000)
	for i := range largeData {
		largeData[i] = "This is a repetitive string that should compress well"
	}

	// Marshal
	compressed, err := s.Marshal(largeData)
	require.NoError(t, err)

	// Original JSON size
	jsonData, _ := jsonFast.Marshal(largeData)
	originalSize := len(jsonData)
	compressedSize := len(compressed)

	// Should achieve significant compression
	compressionRatio := float64(compressedSize) / float64(originalSize)
	assert.Less(t, compressionRatio, 0.1) // Should compress to less than 10% of original

	// Unmarshal
	var result []string
	err = s.Unmarshal(compressed, &result)
	require.NoError(t, err)
	assert.Equal(t, largeData, result)
}

// TestCacheEntry_Marshal tests cache entry marshaling
func TestCacheEntry_Marshal(t *testing.T) {
	entry := &CacheEntry{
		Key:          "test-key",
		Value:        []byte("test-value"),
		CreatedAt:    1234567890,
		ExpiresAfter: 1234567900,
		StaleAfter:   1234567895,
		IsShared:     true,
		Status:       "hit",
	}

	data, err := entry.Marshal()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal and verify
	var decoded CacheEntry
	err = decoded.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, entry.Key, decoded.Key)
	assert.Equal(t, entry.Value, decoded.Value)
	assert.Equal(t, entry.CreatedAt, decoded.CreatedAt)
	assert.Equal(t, entry.ExpiresAfter, decoded.ExpiresAfter)
	assert.Equal(t, entry.StaleAfter, decoded.StaleAfter)
	assert.Equal(t, entry.IsShared, decoded.IsShared)
	assert.Equal(t, entry.Status, decoded.Status)
}

// TestCacheEntry_Unmarshal_Invalid tests unmarshaling invalid data
func TestCacheEntry_Unmarshal_Invalid(t *testing.T) {
	entry := &CacheEntry{}
	err := entry.Unmarshal([]byte("invalid json"))
	assert.Error(t, err)
}
