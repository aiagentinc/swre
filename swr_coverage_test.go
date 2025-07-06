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

// ============================================================================
// Consolidated test cases for uncovered code paths
// This file combines tests from swr_additional_test.go, swr_fallback_test.go,
// and swr_exact_fallback_test.go to reduce duplication
// ============================================================================

// --- Expired Entry Test ---

func TestStaleEngine_ExpiredEntryWithRefresh(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	engine, err := NewStaleEngine(storage, logger)
	require.NoError(t, err)
	defer engine.Shutdown()

	key := "expired-key"
	originalValue := "original-value"
	updatedValue := "updated-value"

	// Create an entry that is expired (all times in the past)
	now := time.Now()
	entry := &CacheEntry{
		Key:          key,
		Value:        []byte(fmt.Sprintf(`"%s"`, originalValue)),
		CreatedAt:    now.Add(-10 * time.Hour).UnixMilli(),
		ExpiresAfter: now.Add(-5 * time.Hour).UnixMilli(), // Expired 5 hours ago
		StaleAfter:   now.Add(-8 * time.Hour).UnixMilli(), // Became stale 8 hours ago
		Status:       "hit",
	}

	storage.mu.Lock()
	storage.data[key] = entry
	storage.mu.Unlock()

	// Track if async refresh was triggered
	refreshTriggered := atomic.Bool{}

	// Callback that will be called for refresh
	callback := func() (interface{}, error) {
		refreshTriggered.Store(true)
		return updatedValue, nil
	}

	// Execute should return expired entry immediately
	result, err := engine.Execute(context.Background(), key, callback)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "expired", result.Status)

	// Verify we got the original expired value
	var val string
	err = engine.serializer.Unmarshal(result.Value, &val)
	require.NoError(t, err)
	assert.Equal(t, originalValue, val)

	// Wait for async refresh to complete
	assert.Eventually(t, func() bool {
		return refreshTriggered.Load()
	}, 2*time.Second, 10*time.Millisecond, "Async refresh should have been triggered")
}

// --- Fallback Path Tests ---

func TestStaleEngine_StorageMissAfterSafeCall(t *testing.T) {
	// Use ConfigurableFailStorage to simulate Get failure after Set
	storage := &ConfigurableFailStorage{
		MockStorage:           NewMockStorage(),
		failGetAfterSetForKey: "test-key",
	}

	logger := NewTestLogger(t)
	engine, err := NewStaleEngine(storage, logger)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		return "test-value", nil
	}

	// This should trigger the fallback path with storage miss warning
	result, err := engine.Execute(context.Background(), "test-key", callback)
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "miss", result.Status)

	// Verify the value is correct despite storage Get failure
	var val string
	err = engine.serializer.Unmarshal(result.Value, &val)
	require.NoError(t, err)
	assert.Equal(t, "test-value", val)
}

func TestStaleEngine_FallbackMarshalError(t *testing.T) {
	storage := &ConfigurableFailStorage{
		MockStorage:           NewMockStorage(),
		failGetAfterSetForKey: "test-key",
	}
	logger := NewTestLogger(t)

	// Create a serializer that fails on second marshal (the fallback one)
	serializer := &ConfigurableFailSerializer{
		failOnMarshalCall: 2,
	}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithSerializer(serializer),
	)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		return "test-value", nil
	}

	// This should fail with marshal error in fallback path
	_, err = engine.Execute(context.Background(), "test-key", callback)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "marshal after storage miss")
}

func TestStaleEngine_FallbackTTLCalcError(t *testing.T) {
	storage := &ConfigurableFailStorage{
		MockStorage:           NewMockStorage(),
		failGetAfterSetForKey: "test-key",
	}
	logger := NewTestLogger(t)

	// Create a TTL calculator that fails on second call (the fallback one)
	ttlCalc := &ConfigurableFailTTLCalculator{
		failOnCall: 2,
	}

	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithTTLCalculator(ttlCalc),
	)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		return "test-value", nil
	}

	// This should fail with TTL calc error in fallback path
	_, err = engine.Execute(context.Background(), "test-key", callback)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "ttl calc failed")
}

// --- Value Transformation Failure Test ---

func TestStaleEngine_ValueTransformationFailure(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Create engine with a failing value transformer
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithValueTransformer(&AlwaysFailTransformer{}),
	)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		return "value", nil
	}

	// This should fail due to value transformation error
	_, err = engine.Execute(context.Background(), "test-key", callback)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "value transformation failed")
}

// --- Additional Tests for Edge Cases ---

// Test TTL calculation failure (not in fallback path)
func TestStaleEngine_TTLCalculationFailure(t *testing.T) {
	storage := NewMockStorage()
	// Force storage to always return not found to trigger primary path
	storage.getErr = ErrNotFound
	logger := NewTestLogger(t)

	// Create engine with a failing TTL calculator
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithTTLCalculator(&AlwaysFailTTLCalculator{}),
	)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		return "value", nil
	}

	// This should fail due to TTL calculation error
	_, err = engine.Execute(context.Background(), "test-key", callback)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "TTL calculation failed")
}

// Test marshaling failure (not in fallback path)
func TestStaleEngine_MarshalFailureInFallback(t *testing.T) {
	storage := NewMockStorage()
	// Force storage to always return not found
	storage.getErr = ErrNotFound
	logger := NewTestLogger(t)

	// Create engine with a serializer that fails on certain values
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithSerializer(&TypeBasedFailSerializer{}),
	)
	require.NoError(t, err)
	defer engine.Shutdown()

	callback := func() (interface{}, error) {
		// Return a value that will fail serialization
		return &UnserializableType{}, nil
	}

	// This should fail due to marshal error
	_, err = engine.Execute(context.Background(), "test-key", callback)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "serialization failed")
}

// --- Shutdown Timeout Test ---

func TestStaleEngine_ShutdownTimeout(t *testing.T) {
	storage := NewMockStorage()
	logger := NewTestLogger(t)

	// Create engine with very short refresh timeout
	engine, err := NewStaleEngineWithOptions(storage, logger,
		WithRefreshTimeout(50*time.Millisecond),
		WithMaxConcurrentRefreshes(2),
	)
	require.NoError(t, err)

	// Create a long-running refresh
	blockCh := make(chan struct{})
	defer close(blockCh)

	// Add stale entry to trigger refresh
	entry := &CacheEntry{
		Key:          "blocking-key",
		Value:        []byte(`"value"`),
		CreatedAt:    time.Now().Add(-2 * time.Hour).UnixMilli(),
		ExpiresAfter: time.Now().Add(1 * time.Hour).UnixMilli(),
		StaleAfter:   time.Now().Add(-1 * time.Hour).UnixMilli(),
	}
	storage.mu.Lock()
	storage.data["blocking-key"] = entry
	storage.mu.Unlock()

	// Start a goroutine that will block in refresh
	go func() {
		_, _ = engine.Execute(context.Background(), "blocking-key", func() (interface{}, error) {
			<-blockCh // Block until channel is closed
			return "new-value", nil
		})
	}()

	// Wait for refresh to start
	time.Sleep(100 * time.Millisecond)

	// Force the currentRefreshes count to be non-zero
	engine.currentRefreshes.Store(1)

	// Start shutdown - this should trigger the timeout warning path
	done := make(chan struct{})
	go func() {
		engine.Shutdown()
		close(done)
	}()

	// Wait for shutdown to complete
	select {
	case <-done:
		// Good, shutdown completed
	case <-time.After(6 * time.Second):
		t.Fatal("Shutdown took too long")
	}
}

// ============================================================================
// Consolidated Helper Types
// ============================================================================

// ConfigurableFailStorage can be configured to fail Get after Set for specific keys
type ConfigurableFailStorage struct {
	*MockStorage
	failGetAfterSetForKey string
	hasSet                sync.Map // map[string]bool
}

func (c *ConfigurableFailStorage) Set(ctx context.Context, key string, value *CacheEntry) error {
	err := c.MockStorage.Set(ctx, key, value)
	if key == c.failGetAfterSetForKey {
		c.hasSet.Store(key, true)
	}
	return err
}

func (c *ConfigurableFailStorage) Get(ctx context.Context, key string) (*CacheEntry, error) {
	// Only fail on second Get (after Set) for the specific key
	if key == c.failGetAfterSetForKey {
		if v, ok := c.hasSet.LoadAndDelete(key); ok && v.(bool) {
			return nil, ErrNotFound
		}
	}

	return c.MockStorage.Get(ctx, key)
}

// ConfigurableFailSerializer can be configured to fail on specific marshal calls
type ConfigurableFailSerializer struct {
	mu                sync.Mutex
	marshalCallCount  int
	failOnMarshalCall int // 0 means never fail
}

func (c *ConfigurableFailSerializer) Marshal(v interface{}) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.marshalCallCount++
	if c.failOnMarshalCall > 0 && c.marshalCallCount == c.failOnMarshalCall {
		return nil, errors.New("marshal failed in fallback")
	}

	return jsonFast.Marshal(v)
}

func (c *ConfigurableFailSerializer) Unmarshal(data []byte, v interface{}) error {
	return jsonFast.Unmarshal(data, v)
}

// ConfigurableFailTTLCalculator can be configured to fail on specific calls
type ConfigurableFailTTLCalculator struct {
	mu         sync.Mutex
	callCount  int
	failOnCall int // 0 means never fail
}

func (c *ConfigurableFailTTLCalculator) CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.callCount++
	if c.failOnCall > 0 && c.callCount == c.failOnCall {
		return 0, 0, errors.New("TTL calc failed in fallback")
	}

	return 15 * 24 * time.Hour, 15*24*time.Hour - 60*time.Second, nil
}

// AlwaysFailTransformer always fails transformation
type AlwaysFailTransformer struct{}

func (a *AlwaysFailTransformer) Transform(ctx context.Context, key string, value interface{}) (interface{}, error) {
	return nil, errors.New("transformation failed")
}

func (a *AlwaysFailTransformer) Restore(ctx context.Context, key string, data []byte) (interface{}, error) {
	return data, nil
}

// AlwaysFailTTLCalculator always fails TTL calculation
type AlwaysFailTTLCalculator struct{}

func (f *AlwaysFailTTLCalculator) CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error) {
	return 0, 0, errors.New("TTL calculation failed")
}

// TypeBasedFailSerializer fails for specific types
type TypeBasedFailSerializer struct{}

func (t *TypeBasedFailSerializer) Marshal(v interface{}) ([]byte, error) {
	if _, ok := v.(*UnserializableType); ok {
		return nil, errors.New("cannot serialize this type")
	}
	return jsonFast.Marshal(v)
}

func (t *TypeBasedFailSerializer) Unmarshal(data []byte, v interface{}) error {
	return jsonFast.Unmarshal(data, v)
}

// UnserializableType is a type that cannot be serialized
type UnserializableType struct {
	Channel chan int // Channels cannot be serialized
}
