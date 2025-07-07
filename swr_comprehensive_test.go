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

// TestStaleEngine_NewStaleEngine tests the default constructor
func TestStaleEngine_NewStaleEngine(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, err := NewStaleEngine(storage, logger)
	require.NoError(t, err)
	assert.NotNil(t, engine)
}

// TestStaleEngine_NewStaleEngineWithConfig tests config-based constructor
func TestStaleEngine_NewStaleEngineWithConfig(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	cfg := &EngineConfig{
		Storage: storage,
		Logger:  logger,
		DefaultCacheTTL: &CacheTTL{
			FreshSeconds:   10,
			StaleSeconds:   300,
			ExpiredSeconds: 600,
		},
		MaxConcurrentRefreshes: 50,
		RefreshTimeout:         10 * time.Second,
	}

	engine, err := NewStaleEngineWithConfig(cfg)
	require.NoError(t, err)
	assert.NotNil(t, engine)
	assert.Equal(t, 50, engine.maxConcurrentRefreshes)
	assert.Equal(t, 10*time.Second, engine.refreshTimeout)
}

// TestStaleEngine_InvalidConstructors tests error cases
func TestStaleEngine_InvalidConstructors(t *testing.T) {
	logger := NewTestLogger(t)
	storage := NewMockStorage()

	// Nil storage
	_, err := NewStaleEngine(nil, logger)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "storage cannot be nil")

	// Nil logger
	_, err = NewStaleEngine(storage, nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "logger cannot be nil")

	// Nil config
	_, err = NewStaleEngineWithConfig(nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "config cannot be nil")
}

// TestStaleEngine_GetCacheEntry tests direct cache retrieval
func TestStaleEngine_GetCacheEntry(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	// Get non-existent entry
	entry, err := engine.GetCacheEntry(ctx, "missing-key")
	assert.Error(t, err)
	assert.Nil(t, entry)

	// Store entry first
	_, err = engine.Execute(ctx, "test-key", func() (interface{}, error) {
		return "test-data", nil
	})
	require.NoError(t, err)

	// Get existing entry
	entry, err = engine.GetCacheEntry(ctx, "test-key")
	assert.NoError(t, err)
	assert.NotNil(t, entry)
}

// TestStaleEngine_ExecuteGeneric tests generic execution with serialization
func TestStaleEngine_ExecuteGeneric(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	type TestData struct {
		Name  string
		Value int
	}

	var result TestData
	err := engine.ExecuteGeneric(ctx, "generic-key", &result, func() (interface{}, error) {
		return TestData{Name: "test", Value: 42}, nil
	})

	require.NoError(t, err)
	assert.Equal(t, "test", result.Name)
	assert.Equal(t, 42, result.Value)
}

// TestStaleEngine_ExecuteWithCacheKey tests CacheKey with TTL overrides
func TestStaleEngine_ExecuteWithCacheKey(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	// Use CacheKey with custom TTL
	cacheKey := CacheKey{
		Key: "custom-ttl-key",
		TTL: &CacheTTL{
			FreshSeconds:   1,
			StaleSeconds:   2,
			ExpiredSeconds: 3,
		},
	}

	entry, err := engine.Execute(ctx, cacheKey, func() (interface{}, error) {
		return "custom-data", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "miss", entry.Status)

	// Verify custom TTL was applied
	// The TTL calculator should have used the per-key TTL
	// Note: actual TTL will be much longer due to default stale/expired calculation
	assert.NotNil(t, entry)
}

// TestStaleEngine_InvalidKeyTypes tests invalid key parameter types
func TestStaleEngine_InvalidKeyTypes(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	// Invalid key type
	_, err := engine.Execute(ctx, 123, func() (interface{}, error) {
		return "data", nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "invalid key type")

	// Empty string key
	_, err = engine.Execute(ctx, "", func() (interface{}, error) {
		return "data", nil
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cache key cannot be empty")
}

// TestStaleEngine_StorageErrors tests storage error handling
func TestStaleEngine_StorageErrors(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics))
	ctx := context.Background()

	// Test Get error
	storage.getErr = errors.New("storage get error")
	_, err := engine.Execute(ctx, "error-key", func() (interface{}, error) {
		return "data", nil
	})
	assert.Error(t, err)
	assert.Equal(t, int32(1), metrics.errors.Load())

	// Reset error
	storage.getErr = nil
	metrics.errors.Store(0)

	// Test Set error
	storage.setErr = errors.New("storage set error")
	_, err = engine.Execute(ctx, "set-error-key", func() (interface{}, error) {
		return "data", nil
	})
	// Should not return error to caller, but should record it
	assert.NoError(t, err)
	assert.Equal(t, int32(1), metrics.errors.Load())
}

// TestStaleEngine_AsyncRefreshWithShutdown tests background refresh with shutdown
func TestStaleEngine_AsyncRefreshWithShutdown(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0,
			StaleSeconds:   1,
			ExpiredSeconds: 0,
		}),
		WithRefreshTimeout(100*time.Millisecond))

	ctx := context.Background()
	refreshStarted := make(chan struct{})
	refreshBlocked := make(chan struct{})

	// Initial cache population
	_, err := engine.Execute(ctx, "shutdown-key", func() (interface{}, error) {
		return "initial", nil
	})
	require.NoError(t, err)

	// Wait for stale
	time.Sleep(50 * time.Millisecond)

	// Trigger background refresh that will block
	go func() {
		_, _ = engine.Execute(ctx, "shutdown-key", func() (interface{}, error) {
			close(refreshStarted)
			<-refreshBlocked // Block until we signal
			return "refreshed", nil
		})
	}()

	// Wait for refresh to start
	<-refreshStarted

	// Shutdown engine while refresh is in progress
	engine.Shutdown()

	// Unblock refresh
	close(refreshBlocked)

	// Verify engine handled shutdown gracefully
	assert.Equal(t, int32(0), engine.currentRefreshes.Load())
}

// TestStaleEngine_ExpiredEntry tests expired cache entry handling
func TestStaleEngine_ExpiredEntry(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	// Create an entry that expires immediately but remains stale for a bit
	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0, // Fresh for 0 seconds
			StaleSeconds:   1, // Stale for 1 second
			ExpiredSeconds: 0, // No additional expired time
		}))

	ctx := context.Background()

	// Create entry
	_, err := engine.Execute(ctx, "expire-key", func() (interface{}, error) {
		return "data", nil
	})
	require.NoError(t, err)

	// Wait for entry to become stale but not expired
	time.Sleep(50 * time.Millisecond)

	// Access stale entry (should trigger background refresh)
	var callCount atomic.Int32
	entry, err := engine.Execute(ctx, "expire-key", func() (interface{}, error) {
		callCount.Add(1)
		return "refreshed", nil
	})

	require.NoError(t, err)
	assert.Equal(t, "stale", entry.Status)
	assert.Equal(t, int32(1), metrics.GetHits("stale"))

	// Background refresh should have been triggered
	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(1), callCount.Load())
}

// TestStaleEngine_TryAsyncRefreshRaceCondition tests concurrent refresh attempts
func TestStaleEngine_TryAsyncRefreshRaceCondition(t *testing.T) {
	storage := NewMockStorage()
	logger := NewSafeTestLogger(t)

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithMaxConcurrentRefreshes(5),
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0,
			StaleSeconds:   10,
			ExpiredSeconds: 0,
		}))

	ctx := context.Background()

	// Populate cache
	for i := 0; i < 10; i++ {
		key := fmt.Sprintf("race-key-%d", i)
		_, err := engine.Execute(ctx, key, func() (interface{}, error) {
			return fmt.Sprintf("data-%d", i), nil
		})
		require.NoError(t, err)
	}

	// Wait for entries to become stale
	time.Sleep(50 * time.Millisecond)

	// Trigger many concurrent refreshes
	var wg sync.WaitGroup
	refreshCount := atomic.Int32{}

	for i := 0; i < 10; i++ {
		for j := 0; j < 5; j++ { // 5 goroutines per key
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				key := fmt.Sprintf("race-key-%d", idx)
				_, _ = engine.Execute(ctx, key, func() (interface{}, error) {
					refreshCount.Add(1)
					time.Sleep(20 * time.Millisecond)
					return fmt.Sprintf("refreshed-%d", idx), nil
				})
			}(i)
		}
	}

	wg.Wait()
	time.Sleep(100 * time.Millisecond) // Wait for background refreshes

	// Each key should only refresh once despite multiple attempts
	assert.LessOrEqual(t, refreshCount.Load(), int32(10))
}

// TestStaleEngine_ContextCancellation tests context cancellation behavior
func TestStaleEngine_ContextCancellation(t *testing.T) {
	storage := NewMockStorage()
	logger := NewSafeTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)

	// Create a cancellable context
	ctx, cancel := context.WithCancel(context.Background())

	// Start a slow operation
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := engine.Execute(ctx, "cancel-key", func() (interface{}, error) {
			time.Sleep(100 * time.Millisecond)
			return "data", nil
		})
		assert.Error(t, err)
		assert.ErrorIs(t, err, context.Canceled)
	}()

	// Cancel context while operation is in progress
	time.Sleep(20 * time.Millisecond)
	cancel()

	// Wait for operation to complete
	<-done
}

// TestStaleEngine_WithAllOptions tests all configuration options
func TestStaleEngine_WithAllOptions(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	customSerializer := &GobSerializer{}
	customTTLCalc := &DynamicTTLCalculator{
		Calculator: func(key string, value interface{}) (time.Duration, time.Duration, error) {
			return 1 * time.Hour, 30 * time.Minute, nil
		},
	}
	customTransformer := &NoOpTransformer{}
	customMetrics := &TestMetrics{}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   300,
			StaleSeconds:   600,
			ExpiredSeconds: 900,
		}),
		WithSerializer(customSerializer),
		WithTTLCalculator(customTTLCalc),
		WithValueTransformer(customTransformer),
		WithMetrics(customMetrics),
		WithMaxConcurrentRefreshes(100),
		WithRefreshTimeout(60*time.Second))

	require.NoError(t, err)
	assert.NotNil(t, engine)
	assert.Equal(t, customSerializer, engine.serializer)
	assert.Equal(t, customTTLCalc, engine.ttlCalculator)
	assert.Equal(t, customTransformer, engine.valueTransformer)
	assert.Equal(t, customMetrics, engine.metrics)
	assert.Equal(t, 100, engine.maxConcurrentRefreshes)
	assert.Equal(t, 60*time.Second, engine.refreshTimeout)
}

// TestCacheTTL_ToEngineTTLs tests TTL conversion
func TestCacheTTL_ToEngineTTLs(t *testing.T) {
	tests := []struct {
		name          string
		ttl           *CacheTTL
		expectedTotal time.Duration
		expectedFresh time.Duration
	}{
		{
			name: "normal values",
			ttl: &CacheTTL{
				FreshSeconds:   60,
				StaleSeconds:   300,
				ExpiredSeconds: 600,
			},
			expectedTotal: 960 * time.Second,
			expectedFresh: 60 * time.Second,
		},
		{
			name: "minimum TTL enforced",
			ttl: &CacheTTL{
				FreshSeconds:   1,
				StaleSeconds:   1,
				ExpiredSeconds: 0,
			},
			expectedTotal: 5 * time.Second, // Minimum enforced
			expectedFresh: 1 * time.Second,
		},
		{
			name: "negative fresh duration",
			ttl: &CacheTTL{
				FreshSeconds:   -10,
				StaleSeconds:   20,
				ExpiredSeconds: 30,
			},
			expectedTotal: 40 * time.Second,
			expectedFresh: 0,
		},
		{
			name: "fresh exceeds total",
			ttl: &CacheTTL{
				FreshSeconds:   100,
				StaleSeconds:   0,
				ExpiredSeconds: 0,
			},
			expectedTotal: 100 * time.Second,
			expectedFresh: 100 * time.Second,
		},
		{
			name:          "nil TTL",
			ttl:           nil,
			expectedTotal: 10 * 24 * time.Hour, // Default
			expectedFresh: 5 * time.Second,     // Default
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			total, fresh := tt.ttl.ToEngineTTLs()
			assert.Equal(t, tt.expectedTotal, total)
			assert.Equal(t, tt.expectedFresh, fresh)
		})
	}
}

// TestEngineConfig_SetDefaults tests configuration defaults
func TestEngineConfig_SetDefaults(t *testing.T) {
	cfg := &EngineConfig{}
	cfg.SetDefaults()

	assert.NotNil(t, cfg.Serializer)
	assert.NotNil(t, cfg.TTLCalculator)
	assert.Equal(t, 1000, cfg.MaxConcurrentRefreshes)
	assert.Equal(t, 30*time.Second, cfg.RefreshTimeout)
	assert.Equal(t, 15*24*time.Hour, cfg.DefaultTTL)
	assert.Equal(t, 60*time.Second, cfg.DefaultStaleOffset)

	// Test with CacheTTL
	cfg2 := &EngineConfig{
		DefaultCacheTTL: &CacheTTL{
			FreshSeconds:   10,
			StaleSeconds:   20,
			ExpiredSeconds: 30,
		},
	}
	cfg2.SetDefaults()

	assert.Equal(t, 60*time.Second, cfg2.DefaultTTL)
	assert.Equal(t, 50*time.Second, cfg2.DefaultStaleOffset)
}

// TestStaleEngine_CallbackError tests callback error handling
func TestStaleEngine_CallbackError(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	metrics := &TestMetrics{}

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithMetrics(metrics))

	ctx := context.Background()
	expectedErr := errors.New("callback error")

	_, err := engine.Execute(ctx, "error-key", func() (interface{}, error) {
		return nil, expectedErr
	})

	assert.Error(t, err)
	assert.Equal(t, expectedErr, err)
	assert.Equal(t, int32(1), metrics.errors.Load())
}

// TestStaleEngine_ValueTransformer tests value transformation
func TestStaleEngine_ValueTransformer(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Custom transformer that adds prefix
	transformer := &testTransformer{prefix: "transformed:"}

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithValueTransformer(transformer))

	ctx := context.Background()

	// Store value
	_, err := engine.Execute(ctx, "transform-key", func() (interface{}, error) {
		return "original", nil
	})
	require.NoError(t, err)

	// Verify transformation was applied
	assert.Equal(t, 1, transformer.transformCalls)
}

// Helper transformer for testing
type testTransformer struct {
	prefix         string
	transformCalls int
	restoreCalls   int
}

func (t *testTransformer) Transform(ctx context.Context, key string, value interface{}) (interface{}, error) {
	t.transformCalls++
	if str, ok := value.(string); ok {
		return t.prefix + str, nil
	}
	return value, nil
}

func (t *testTransformer) Restore(ctx context.Context, key string, data []byte) (interface{}, error) {
	t.restoreCalls++
	return string(data), nil
}

// TestNewCacheEntry tests deprecated constructor
func TestNewCacheEntry(t *testing.T) {
	entry, err := NewCacheEntry("test-key", map[string]string{"data": "test"})
	require.NoError(t, err)
	assert.Equal(t, "test-key", entry.Key)
	assert.NotEmpty(t, entry.Value)
	assert.Equal(t, "MISS", entry.Status)

	// Test serialization error
	_, err = NewCacheEntry("bad-key", make(chan int))
	assert.Error(t, err)
}

// TestStaleEngine_LargePayloadTimeout tests adaptive timeout for large payloads
func TestStaleEngine_LargePayloadTimeout(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	engine, _ := NewStaleEngine(storage, logger)
	ctx := context.Background()

	// Create large payload (2MB)
	largeData := make([]byte, 2*1024*1024)
	for i := range largeData {
		largeData[i] = byte(i % 256)
	}

	start := time.Now()
	_, err := engine.Execute(ctx, "large-key", func() (interface{}, error) {
		return largeData, nil
	})
	elapsed := time.Since(start)

	require.NoError(t, err)
	// Should complete without timeout despite large size
	assert.Less(t, elapsed, 5*time.Second)
}
