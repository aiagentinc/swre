package swre

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockStorage implements Storage interface for testing
type MockStorage struct {
	mu     sync.RWMutex
	data   map[string]*CacheEntry
	getErr error
	setErr error
	calls  atomic.Int32
}

func NewMockStorage() *MockStorage {
	return &MockStorage{
		data: make(map[string]*CacheEntry),
	}
}

func (m *MockStorage) Get(ctx context.Context, key string) (*CacheEntry, error) {
	m.calls.Add(1)
	if m.getErr != nil {
		return nil, m.getErr
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.data[key]
	if !ok {
		return nil, ErrNotFound
	}

	return entry.CloneWithStatus(entry.Status), nil
}

func (m *MockStorage) Set(ctx context.Context, key string, value *CacheEntry) error {
	m.calls.Add(1)
	if m.setErr != nil {
		return m.setErr
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.data[key] = value
	return nil
}

func (m *MockStorage) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.data, key)
	return nil
}

func (m *MockStorage) Close() error {
	return nil
}

func (m *MockStorage) GetCallCount() int32 {
	return m.calls.Load()
}

// TestMetrics implements CacheMetrics for testing
type TestMetrics struct {
	hits   sync.Map // map[string]int
	misses atomic.Int32
	errors atomic.Int32
}

func (t *TestMetrics) RecordHit(key string, status string) {
	val, _ := t.hits.LoadOrStore(status, &atomic.Int32{})
	val.(*atomic.Int32).Add(1)
}

func (t *TestMetrics) RecordMiss(key string) {
	t.misses.Add(1)
}

func (t *TestMetrics) RecordError(key string, err error) {
	t.errors.Add(1)
}

func (t *TestMetrics) RecordLatency(key string, duration time.Duration) {}

func (t *TestMetrics) GetHits(status string) int32 {
	val, ok := t.hits.Load(status)
	if !ok {
		return 0
	}
	return val.(*atomic.Int32).Load()
}

// Tests

func TestStaleEngine_CacheMiss(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   3000, // 50 minutes fresh
			StaleSeconds:   600,  // 10 minutes stale
			ExpiredSeconds: 0,    // no additional expired time
		}))
	require.NoError(t, err)

	ctx := context.Background()
	callCount := 0

	entry, err := engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount++
		return map[string]string{"data": "test"}, nil
	})

	require.NoError(t, err)
	assert.Equal(t, "miss", entry.Status)
	assert.Equal(t, 1, callCount)
	assert.Equal(t, int32(1), metrics.misses.Load())
}

func TestStaleEngine_CacheHit(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   3540, // 59 minutes fresh
			StaleSeconds:   60,   // 1 minute stale
			ExpiredSeconds: 0,    // no additional expired time
		}))
	require.NoError(t, err)

	ctx := context.Background()
	callCount := 0

	// First call - miss
	_, err = engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount++
		return "test-data", nil
	})
	require.NoError(t, err)

	// Second call - hit
	entry, err := engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount++
		return "test-data", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "hit", entry.Status)
	assert.Equal(t, 1, callCount) // Callback should only be called once
	assert.Equal(t, int32(1), metrics.GetHits("hit"))
}

func TestStaleEngine_StaleWhileRevalidate(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	// Use very short TTL for testing
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0, // 0 seconds fresh
			StaleSeconds:   1, // 1 second stale
			ExpiredSeconds: 0, // 0 seconds additional
		}))
	require.NoError(t, err)

	ctx := context.Background()
	callCount := atomic.Int32{}

	// First call - miss
	_, err = engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount.Add(1)
		return "test-data", nil
	})
	require.NoError(t, err)

	// Wait for entry to become stale
	time.Sleep(100 * time.Millisecond)

	// Second call - stale (should trigger background refresh)
	entry, err := engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // Simulate slow refresh
		return "refreshed-data", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "stale", entry.Status)

	// Wait for background refresh to complete
	time.Sleep(100 * time.Millisecond)
	finalCount := callCount.Load()
	// Should be 2 (initial + background refresh)
	assert.GreaterOrEqual(t, finalCount, int32(2))

	// Third call - should get refreshed data
	entry, err = engine.Execute(ctx, "test-key", func() (interface{}, error) {
		callCount.Add(1)
		return "should-not-be-called", nil
	})

	require.NoError(t, err)
	// Should be hit or stale depending on timing
	assert.Contains(t, []string{"hit", "stale"}, entry.Status)
	// Count could be 2 or 3 depending on whether another background refresh was triggered
	assert.Contains(t, []int32{2, 3}, callCount.Load())
}

func TestStaleEngine_ConcurrentRequests(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMaxConcurrentRefreshes(5))
	require.NoError(t, err)

	ctx := context.Background()
	callCount := atomic.Int32{}

	// Launch 100 concurrent requests for the same key
	var wg sync.WaitGroup
	errors := make(chan error, 100)

	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := engine.Execute(ctx, "concurrent-key", func() (interface{}, error) {
				callCount.Add(1)
				time.Sleep(10 * time.Millisecond) // Simulate work
				return "concurrent-data", nil
			})

			if err != nil {
				errors <- err
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check no errors
	for err := range errors {
		t.Errorf("Unexpected error: %v", err)
	}

	// Should only call the callback once due to singleflight
	assert.Equal(t, int32(1), callCount.Load())
}

func TestStaleEngine_CallbackPanic(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics))
	require.NoError(t, err)

	ctx := context.Background()

	_, err = engine.Execute(ctx, "panic-key", func() (interface{}, error) {
		panic("test panic")
	})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "callback panic")
	assert.Equal(t, int32(1), metrics.errors.Load())
}

func TestStaleEngine_CustomSerializer(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Use Gob serializer instead of JSON
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithSerializer(&GobSerializer{}))
	require.NoError(t, err)

	ctx := context.Background()

	type TestStruct struct {
		Name  string
		Value int
	}

	// Store complex struct
	_, err = engine.Execute(ctx, "gob-key", func() (interface{}, error) {
		return TestStruct{Name: "test", Value: 42}, nil
	})
	require.NoError(t, err)

	// Retrieve and verify
	var result TestStruct
	err = engine.ExecuteGeneric(ctx, "gob-key", &result, func() (interface{}, error) {
		return TestStruct{}, errors.New("should not be called")
	})

	require.NoError(t, err)
	assert.Equal(t, "test", result.Name)
	assert.Equal(t, 42, result.Value)
}

func TestStaleEngine_DynamicTTL(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithTTLCalculator(&DynamicTTLCalculator{
			Calculator: func(key string, value interface{}) (ttl, staleTTL time.Duration, err error) {
				// Different TTL based on value
				if str, ok := value.(string); ok && str == "short-lived" {
					return 200 * time.Millisecond, 50 * time.Millisecond, nil
				}
				return 1 * time.Hour, 50 * time.Minute, nil
			},
		}))
	require.NoError(t, err)

	ctx := context.Background()

	// Store short-lived entry
	_, err = engine.Execute(ctx, "ttl-key", func() (interface{}, error) {
		return "short-lived", nil
	})
	require.NoError(t, err)

	// Wait for entry to become stale (staleTTL is 50ms)
	time.Sleep(100 * time.Millisecond)

	// Should serve stale and trigger background refresh
	var callCount atomic.Int32
	entry, err := engine.Execute(ctx, "ttl-key", func() (interface{}, error) {
		callCount.Add(1)
		return "refreshed", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "stale", entry.Status) // Should be stale

	// Wait for background refresh
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load()) // Background refresh should have triggered
}

func TestCircuitBreaker_OpenOnFailures(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	cb := NewCircuitBreaker(CircuitBreakerConfig{
		Engine:           engine,
		FailureThreshold: 3,
		Timeout:          100 * time.Millisecond,
	})

	ctx := context.Background()

	// Cause failures
	for i := 0; i < 3; i++ {
		_, err := cb.Execute(ctx, fmt.Sprintf("fail-%d", i), func() (interface{}, error) {
			return nil, errors.New("simulated failure")
		})
		assert.Error(t, err)
	}

	// Circuit should be open
	assert.Equal(t, "open", cb.GetState())

	// Subsequent calls should fail immediately
	_, err := cb.Execute(ctx, "test", func() (interface{}, error) {
		return "should not be called", nil
	})
	assert.ErrorIs(t, err, ErrCircuitOpen)

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Circuit should allow half-open
	_, err = cb.Execute(ctx, "recovery", func() (interface{}, error) {
		return "recovered", nil
	})
	assert.NoError(t, err)
	assert.Equal(t, "half-open", cb.GetState())
}

func TestStaleEngine_MaxConcurrentRefreshes(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithMaxConcurrentRefreshes(2),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0, // 0 seconds fresh
			StaleSeconds:   1, // 1 second stale
			ExpiredSeconds: 0, // no additional expired time
		}))
	require.NoError(t, err)
	defer engine.Shutdown() // Ensure cleanup

	ctx := context.Background()
	refreshStarted := make(chan string, 10)
	refreshCompleted := make(chan string, 10)

	// Prepare multiple keys
	for i := 0; i < 5; i++ {
		key := fmt.Sprintf("key-%d", i)
		_, err := engine.Execute(ctx, key, func() (interface{}, error) {
			return fmt.Sprintf("data-%d", i), nil
		})
		require.NoError(t, err)
	}

	// Wait for entries to become stale
	time.Sleep(60 * time.Millisecond)

	// Trigger refreshes for all keys
	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		key := fmt.Sprintf("key-%d", i)
		go func(k string) {
			defer wg.Done()
			engine.Execute(ctx, k, func() (interface{}, error) {
				select {
				case refreshStarted <- k:
				default:
				}
				time.Sleep(50 * time.Millisecond)
				select {
				case refreshCompleted <- k:
				default:
				}
				return k + "-refreshed", nil
			})
		}(key)
	}

	// Verify max concurrent refreshes is respected
	time.Sleep(10 * time.Millisecond)
	assert.LessOrEqual(t, len(refreshStarted), 2)

	// Wait for all goroutines to complete
	wg.Wait()
}

// Benchmarks

func BenchmarkStaleEngine_CacheHit(b *testing.B) {
	storage := NewMockStorage()
	logger := NewNoOpLogger()

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	// Warm up cache
	engine.Execute(ctx, "bench-key", func() (interface{}, error) {
		return "benchmark-data", nil
	})

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := engine.Execute(ctx, "bench-key", func() (interface{}, error) {
				return "should-not-be-called", nil
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkStaleEngine_ConcurrentMiss(b *testing.B) {
	storage := NewMockStorage()
	logger := NewNoOpLogger()

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			key := fmt.Sprintf("miss-key-%d", i)
			_, err := engine.Execute(ctx, key, func() (interface{}, error) {
				return "data", nil
			})
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
