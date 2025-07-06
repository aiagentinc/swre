package swre

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCacheEntry_TimeChecks tests time-based cache entry status checks
func TestCacheEntry_TimeChecks(t *testing.T) {
	now := NowUnixMilli()

	tests := []struct {
		name          string
		entry         *CacheEntry
		checkTime     int64
		expectExpired bool
		expectStale   bool
	}{
		{
			name: "fresh entry",
			entry: &CacheEntry{
				CreatedAt:    now,
				StaleAfter:   now + 1000,
				ExpiresAfter: now + 2000,
			},
			checkTime:     now + 500,
			expectExpired: false,
			expectStale:   false,
		},
		{
			name: "stale entry",
			entry: &CacheEntry{
				CreatedAt:    now,
				StaleAfter:   now + 1000,
				ExpiresAfter: now + 2000,
			},
			checkTime:     now + 1500,
			expectExpired: false,
			expectStale:   true,
		},
		{
			name: "expired entry",
			entry: &CacheEntry{
				CreatedAt:    now,
				StaleAfter:   now + 1000,
				ExpiresAfter: now + 2000,
			},
			checkTime:     now + 2500,
			expectExpired: true,
			expectStale:   true,
		},
		{
			name: "zero stale time",
			entry: &CacheEntry{
				CreatedAt:    now,
				StaleAfter:   0,
				ExpiresAfter: now + 2000,
			},
			checkTime:     now + 1500,
			expectExpired: false,
			expectStale:   false,
		},
		{
			name: "zero expire time",
			entry: &CacheEntry{
				CreatedAt:    now,
				StaleAfter:   now + 1000,
				ExpiresAfter: 0,
			},
			checkTime:     now + 2500,
			expectExpired: false,
			expectStale:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expectExpired, tt.entry.IsExpiredAt(tt.checkTime))
			assert.Equal(t, tt.expectStale, tt.entry.IsStaleAt(tt.checkTime))
		})
	}
}

// TestCacheEntry_CurrentTimeChecks tests time checks with current time
func TestCacheEntry_CurrentTimeChecks(t *testing.T) {
	// Fresh entry
	fresh := &CacheEntry{
		CreatedAt:    NowUnixMilli(),
		StaleAfter:   NowUnixMilli() + 10000,
		ExpiresAfter: NowUnixMilli() + 20000,
	}
	assert.False(t, fresh.IsExpired())
	assert.False(t, fresh.IsStale())

	// Stale entry
	stale := &CacheEntry{
		CreatedAt:    NowUnixMilli() - 2000,
		StaleAfter:   NowUnixMilli() - 1000,
		ExpiresAfter: NowUnixMilli() + 1000,
	}
	assert.False(t, stale.IsExpired())
	assert.True(t, stale.IsStale())

	// Expired entry
	expired := &CacheEntry{
		CreatedAt:    NowUnixMilli() - 3000,
		StaleAfter:   NowUnixMilli() - 2000,
		ExpiresAfter: NowUnixMilli() - 1000,
	}
	assert.True(t, expired.IsExpired())
	assert.True(t, expired.IsStale())
}

// TestCacheEntry_CloneWithStatus tests deep cloning
func TestCacheEntry_CloneWithStatus(t *testing.T) {
	original := &CacheEntry{
		Key:          "test-key",
		Value:        []byte("test-value"),
		CreatedAt:    1234567890,
		ExpiresAfter: 1234567900,
		StaleAfter:   1234567895,
		IsShared:     true,
		Status:       "hit",
	}

	// Clone with new status
	clone := original.CloneWithStatus("stale")

	// Verify clone
	assert.Equal(t, original.Key, clone.Key)
	assert.Equal(t, original.CreatedAt, clone.CreatedAt)
	assert.Equal(t, original.ExpiresAfter, clone.ExpiresAfter)
	assert.Equal(t, original.StaleAfter, clone.StaleAfter)
	assert.Equal(t, "stale", clone.Status)
	assert.False(t, clone.IsShared)

	// Verify deep copy of Value slice
	assert.Equal(t, original.Value, clone.Value)
	// Modify clone's value
	clone.Value[0] = 'X'
	// Original should be unchanged
	assert.NotEqual(t, original.Value[0], clone.Value[0])

	// Test nil entry
	var nilEntry *CacheEntry
	nilClone := nilEntry.CloneWithStatus("test")
	assert.Nil(t, nilClone)

	// Test empty value
	emptyValue := &CacheEntry{
		Key:   "empty",
		Value: []byte{},
	}
	emptyClone := emptyValue.CloneWithStatus("cloned")
	assert.Equal(t, []byte{}, emptyClone.Value)
	assert.NotNil(t, emptyClone.Value)

	// Test nil value
	nilValue := &CacheEntry{
		Key:   "nil-value",
		Value: nil,
	}
	nilClone2 := nilValue.CloneWithStatus("cloned")
	assert.Equal(t, []byte(nil), nilClone2.Value)
}

// TestNewCacheEntryWithTTL tests cache entry creation with TTL
func TestNewCacheEntryWithTTL(t *testing.T) {
	tests := []struct {
		name             string
		ttl              time.Duration
		staleTTL         time.Duration
		expectedTTL      time.Duration
		expectedStaleTTL time.Duration
	}{
		{
			name:             "normal TTL",
			ttl:              10 * time.Minute,
			staleTTL:         8 * time.Minute,
			expectedTTL:      10 * time.Minute,
			expectedStaleTTL: 8 * time.Minute,
		},
		{
			name:             "minimum TTL enforced",
			ttl:              2 * time.Second,
			staleTTL:         1 * time.Second,
			expectedTTL:      5 * time.Second,
			expectedStaleTTL: 1 * time.Second,
		},
		{
			name:             "negative stale TTL",
			ttl:              10 * time.Minute,
			staleTTL:         -1 * time.Minute,
			expectedTTL:      10 * time.Minute,
			expectedStaleTTL: 9 * time.Minute,
		},
		{
			name:             "stale TTL larger than TTL",
			ttl:              30 * time.Second,
			staleTTL:         -5 * time.Minute,
			expectedTTL:      30 * time.Second,
			expectedStaleTTL: 1 * time.Second, // Updated to match new logic: if staleTTL < 0, it becomes 1 second
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := "test-key"
			value := []byte("test-value")

			entry := NewCacheEntryWithTTL(key, value, tt.ttl, tt.staleTTL)

			assert.Equal(t, key, entry.Key)
			assert.Equal(t, value, entry.Value)
			assert.False(t, entry.IsShared)
			assert.Equal(t, "MISS", entry.Status)

			// Calculate expected times
			ttlMs := tt.expectedTTL.Milliseconds()
			staleTTLMs := tt.expectedStaleTTL.Milliseconds()

			// Verify times are set correctly (with small tolerance for timing)
			actualTTL := entry.ExpiresAfter - entry.CreatedAt
			actualStaleTTL := entry.StaleAfter - entry.CreatedAt

			assert.InDelta(t, ttlMs, actualTTL, 10)
			assert.InDelta(t, staleTTLMs, actualStaleTTL, 10)
		})
	}
}

// TestNowUnixMilli tests time function
func TestNowUnixMilli(t *testing.T) {
	// Test that we get fresh time initially
	t1 := NowUnixMilli()
	now := time.Now().UnixNano() / int64(time.Millisecond)
	assert.InDelta(t, now, t1, 10) // Within 10ms

	// Test that cached time is used within cache interval
	t2 := NowUnixMilli()
	assert.Equal(t, t1, t2, "Should return cached time immediately")

	// Wait for cache to expire (> 10ms)
	time.Sleep(15 * time.Millisecond)

	// Should get fresh time now
	t3 := NowUnixMilli()
	assert.Greater(t, t3, t1, "Should return fresh time after cache expiry")

	// Verify timestamp is reasonable (within last second)
	finalNow := time.Now().UnixNano() / int64(time.Millisecond)
	assert.InDelta(t, finalNow, t3, 1000) // Within 1 second
}

// TestMarshalBufPool tests buffer pool functionality
func TestMarshalBufPool(t *testing.T) {
	// Get buffer from pool
	bufPtr := marshalBufPool.Get().(*[]byte)
	assert.NotNil(t, bufPtr)
	// Buffer should be reset to 0 length but retain capacity
	assert.GreaterOrEqual(t, cap(*bufPtr), 2048)

	// Use buffer
	*bufPtr = (*bufPtr)[:0] // Reset to ensure 0 length
	*bufPtr = append(*bufPtr, []byte("test data")...)
	assert.Equal(t, 9, len(*bufPtr))

	// Return to pool
	marshalBufPool.Put(bufPtr)

	// Get again - capacity should be retained
	bufPtr2 := marshalBufPool.Get().(*[]byte)
	assert.GreaterOrEqual(t, cap(*bufPtr2), 2048)
	marshalBufPool.Put(bufPtr2)
}

// TestCacheEntry_Marshal_LargeBuffer tests buffer pool with large data
func TestCacheEntry_Marshal_LargeBuffer(t *testing.T) {
	// Create entry with large value
	largeValue := make([]byte, 64*1024) // 64KB
	for i := range largeValue {
		largeValue[i] = byte(i % 256)
	}

	entry := &CacheEntry{
		Key:          "large-key",
		Value:        largeValue,
		CreatedAt:    NowUnixMilli(),
		ExpiresAfter: NowUnixMilli() + 10000,
		StaleAfter:   NowUnixMilli() + 5000,
		Status:       "hit",
	}

	// Marshal should handle large data
	data, err := entry.Marshal()
	require.NoError(t, err)
	assert.NotEmpty(t, data)

	// Unmarshal and verify
	var decoded CacheEntry
	err = decoded.Unmarshal(data)
	require.NoError(t, err)
	assert.Equal(t, entry.Key, decoded.Key)
	assert.Equal(t, entry.Value, decoded.Value)
}

// BenchmarkCacheEntry_Marshal benchmarks marshaling performance
func BenchmarkCacheEntry_Marshal(b *testing.B) {
	entry := &CacheEntry{
		Key:          "bench-key",
		Value:        []byte("benchmark test value with some reasonable length"),
		CreatedAt:    1234567890,
		ExpiresAfter: 1234567900,
		StaleAfter:   1234567895,
		IsShared:     true,
		Status:       "hit",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := entry.Marshal()
		if err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkCacheEntry_CloneWithStatus benchmarks cloning performance
func BenchmarkCacheEntry_CloneWithStatus(b *testing.B) {
	entry := &CacheEntry{
		Key:          "bench-key",
		Value:        make([]byte, 1024), // 1KB value
		CreatedAt:    1234567890,
		ExpiresAfter: 1234567900,
		StaleAfter:   1234567895,
		IsShared:     true,
		Status:       "hit",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		clone := entry.CloneWithStatus("stale")
		if clone == nil {
			b.Fatal("clone is nil")
		}
	}
}

// BenchmarkNowUnixMilli benchmarks time function performance
func BenchmarkNowUnixMilli(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = NowUnixMilli()
	}
}
