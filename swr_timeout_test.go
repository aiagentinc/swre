package swre

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStaleEngine_CallbackTimeout tests that slow callbacks are properly timed out
func TestStaleEngine_CallbackTimeout(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Create engine with 1 second refresh timeout
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithRefreshTimeout(1*time.Second))
	require.NoError(t, err)

	ctx := context.Background()

	// Test 1: Callback that takes 2 seconds (should timeout)
	t.Run("SlowCallbackTimeout", func(t *testing.T) {
		start := time.Now()
		_, err := engine.Execute(ctx, "slow-key", func() (interface{}, error) {
			// This callback takes 2 seconds, longer than the 1 second timeout
			time.Sleep(2 * time.Second)
			return "should-not-be-returned", nil
		})

		elapsed := time.Since(start)

		// Should get timeout error
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "callback timeout after 1s")

		// Should timeout around 1 second, not wait for full 2 seconds
		assert.Less(t, elapsed, 1500*time.Millisecond)
		assert.GreaterOrEqual(t, elapsed, 1*time.Second)

		// Verify nothing was written to cache
		entry, err := storage.Get(ctx, "slow-key")
		assert.True(t, errors.Is(err, ErrNotFound))
		assert.Nil(t, entry)
	})

	// Test 2: Fast callback completes before timeout
	t.Run("FastCallbackSuccess", func(t *testing.T) {
		entry, err := engine.Execute(ctx, "fast-key", func() (interface{}, error) {
			// This callback takes 100ms, well within the 1 second timeout
			time.Sleep(100 * time.Millisecond)
			return "fast-data", nil
		})

		// Should succeed
		assert.NoError(t, err)
		assert.NotNil(t, entry)
		assert.Equal(t, "miss", entry.Status)

		// Verify data was written to cache
		cachedEntry, err := storage.Get(ctx, "fast-key")
		assert.NoError(t, err)
		assert.NotNil(t, cachedEntry)
	})

	// Test 3: Callback that panics should still be caught
	t.Run("PanicCallback", func(t *testing.T) {
		_, err := engine.Execute(ctx, "panic-key", func() (interface{}, error) {
			panic("test panic")
		})

		// Should get panic error
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "callback panic: test panic")

		// Verify nothing was written to cache
		entry, err := storage.Get(ctx, "panic-key")
		assert.True(t, errors.Is(err, ErrNotFound))
		assert.Nil(t, entry)
	})

	// Test 4: Background refresh with timeout
	t.Run("BackgroundRefreshTimeout", func(t *testing.T) {
		// First, seed the cache with fresh data
		_, err := engine.Execute(ctx, "bg-key", func() (interface{}, error) {
			return "initial-data", nil
		})
		require.NoError(t, err)

		// Make the entry stale by manipulating the cache directly
		entry, _ := storage.Get(ctx, "bg-key")
		entry.StaleAfter = time.Now().UnixMilli() - 1000 // Make it stale
		_ = storage.Set(ctx, "bg-key", entry)

		// Execute with a slow callback - should return stale data immediately
		// and trigger background refresh that will timeout
		staleEntry, err := engine.Execute(ctx, "bg-key", func() (interface{}, error) {
			// This callback takes 2 seconds in background
			time.Sleep(2 * time.Second)
			return "refreshed-data", nil
		})

		// Should return stale data immediately
		assert.NoError(t, err)
		assert.Equal(t, "stale", staleEntry.Status)

		// Wait a bit to ensure background refresh has started
		time.Sleep(100 * time.Millisecond)

		// Wait for timeout plus buffer
		time.Sleep(1200 * time.Millisecond)

		// Check that the cache still has the old data (refresh timed out)
		cachedEntry, err := storage.Get(ctx, "bg-key")
		assert.NoError(t, err)
		assert.Equal(t, entry.Value, cachedEntry.Value) // Should still be initial data
	})

	// Test 5: Per-key timeout override
	t.Run("PerKeyTimeout", func(t *testing.T) {
		// Create a custom cache key with very short timeout
		customKey := CacheKey{
			Key: "custom-timeout-key",
			TTL: &CacheTTL{
				FreshSeconds:   1,
				StaleSeconds:   1,
				ExpiredSeconds: 1,
			},
		}

		// Even though engine has 1 second timeout, this should respect the callback timeout
		start := time.Now()
		_, err := engine.Execute(ctx, customKey, func() (interface{}, error) {
			// Sleep for 500ms - within engine timeout but we're testing engine behavior
			time.Sleep(500 * time.Millisecond)
			return "custom-data", nil
		})
		elapsed := time.Since(start)

		// Should succeed as it's within the engine's refresh timeout
		assert.NoError(t, err)
		assert.Less(t, elapsed, 1*time.Second)
	})
}

// TestStaleEngine_ConcurrentTimeouts tests timeout behavior with concurrent requests
func TestStaleEngine_ConcurrentTimeouts(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Create engine with 500ms refresh timeout
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithRefreshTimeout(500*time.Millisecond))
	require.NoError(t, err)

	ctx := context.Background()

	// Launch multiple concurrent requests for the same slow key
	const numRequests = 5
	errCh := make(chan error, numRequests)

	for i := 0; i < numRequests; i++ {
		go func() {
			_, err := engine.Execute(ctx, "concurrent-slow-key", func() (interface{}, error) {
				// This callback takes 1 second, longer than the 500ms timeout
				time.Sleep(1 * time.Second)
				return "slow-data", nil
			})
			errCh <- err
		}()
	}

	// Collect all results
	var timeoutCount int
	for i := 0; i < numRequests; i++ {
		err := <-errCh
		if err != nil && contains(err.Error(), "callback timeout") {
			timeoutCount++
		}
	}

	// All requests should timeout due to singleflight
	assert.Equal(t, numRequests, timeoutCount)

	// Verify nothing was written to cache
	entry, err := storage.Get(ctx, "concurrent-slow-key")
	assert.True(t, errors.Is(err, ErrNotFound))
	assert.Nil(t, entry)
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[:len(substr)] == substr || len(s) > len(substr) && contains(s[1:], substr)
}
