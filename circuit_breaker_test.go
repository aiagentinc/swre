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

func createTestCircuitBreaker(t *testing.T, cfg *CircuitBreakerConfig) *CircuitBreaker {
	storage := NewMockStorage()
	logger := NewTestLogger(t)
	engine, err := NewStaleEngine(storage, logger)
	require.NoError(t, err)

	if cfg == nil {
		cfg = &CircuitBreakerConfig{
			Engine: engine,
		}
	} else {
		cfg.Engine = engine
	}

	return NewCircuitBreaker(*cfg)
}

func TestNewCircuitBreaker(t *testing.T) {
	t.Run("default configuration", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		assert.NotNil(t, cb)
		assert.Equal(t, int32(5), cb.failureThreshold)
		assert.Equal(t, int32(2), cb.successThreshold)
		assert.Equal(t, 30*time.Second, cb.timeout)
		assert.Equal(t, int32(3), cb.maxHalfOpenReqs)
		assert.Equal(t, 10000, cb.maxKeyBreakers)
		assert.NotNil(t, cb.keyBreakerLRU)
	})

	t.Run("custom configuration", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 10,
			SuccessThreshold: 5,
			Timeout:          1 * time.Minute,
			MaxHalfOpenReqs:  10,
			MaxKeyBreakers:   5000,
		}
		cb := createTestCircuitBreaker(t, cfg)
		assert.Equal(t, int32(10), cb.failureThreshold)
		assert.Equal(t, int32(5), cb.successThreshold)
		assert.Equal(t, 1*time.Minute, cb.timeout)
		assert.Equal(t, int32(10), cb.maxHalfOpenReqs)
		assert.Equal(t, 5000, cb.maxKeyBreakers)
	})

	t.Run("zero values get defaults", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 0,
			SuccessThreshold: 0,
			Timeout:          0,
			MaxHalfOpenReqs:  0,
			MaxKeyBreakers:   0,
		}
		cb := createTestCircuitBreaker(t, cfg)
		assert.Equal(t, int32(5), cb.failureThreshold)
		assert.Equal(t, int32(2), cb.successThreshold)
		assert.Equal(t, 30*time.Second, cb.timeout)
		assert.Equal(t, int32(3), cb.maxHalfOpenReqs)
		assert.Equal(t, 10000, cb.maxKeyBreakers)
	})
}

func TestCircuitBreakerExecute(t *testing.T) {
	t.Run("successful execution in closed state", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		ctx := context.Background()

		entry, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return "test-value", nil
		})

		require.NoError(t, err)
		assert.NotNil(t, entry)
		assert.Equal(t, "closed", cb.GetState())
	})

	t.Run("failure tracking opens circuit", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 3,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Simulate failures
		for i := 0; i < 3; i++ {
			_, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
			assert.Error(t, err)
		}

		assert.Equal(t, "open", cb.GetState())

		// Circuit should be open, requests should fail immediately
		_, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return "should not execute", nil
		})
		assert.Equal(t, ErrCircuitOpen, err)
	})

	t.Run("circuit transitions to half-open after timeout", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 2,
			Timeout:          100 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Open the circuit
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}
		assert.Equal(t, "open", cb.GetState())

		// Wait for timeout
		time.Sleep(150 * time.Millisecond)

		// Should be able to execute in half-open state
		_, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return "test-value", nil
		})
		assert.NoError(t, err)
		assert.Equal(t, "half-open", cb.GetState())
	})

	t.Run("successful requests in half-open close circuit", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 2,
			SuccessThreshold: 2,
			Timeout:          100 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Open the circuit
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Wait for timeout
		time.Sleep(150 * time.Millisecond)

		// Execute successful requests in half-open state
		for i := 0; i < 2; i++ {
			_, err := cb.Execute(ctx, fmt.Sprintf("test-key-%d", i), func() (interface{}, error) {
				return "success", nil
			})
			require.NoError(t, err)
		}

		assert.Equal(t, "closed", cb.GetState())
	})

	t.Run("failure in half-open reopens circuit", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 2,
			Timeout:          100 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Open the circuit
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Wait for timeout
		time.Sleep(150 * time.Millisecond)

		// Fail in half-open state
		_, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return nil, errors.New("test error")
		})
		assert.Error(t, err)
		assert.Equal(t, "open", cb.GetState())
	})

	t.Run("max half-open requests limit", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 2,
			MaxHalfOpenReqs:  2,
			Timeout:          100 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Open the circuit
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Wait for timeout
		time.Sleep(150 * time.Millisecond)

		// Run concurrent requests in half-open state
		var wg sync.WaitGroup
		var successCount atomic.Int32
		var openErrorCount atomic.Int32

		for i := 0; i < 5; i++ {
			wg.Add(1)
			go func(idx int) {
				defer wg.Done()
				_, err := cb.Execute(ctx, fmt.Sprintf("key-%d", idx), func() (interface{}, error) {
					time.Sleep(50 * time.Millisecond) // Simulate slow operation
					return "success", nil
				})
				switch err {
				case nil:
					successCount.Add(1)
				case ErrCircuitOpen:
					openErrorCount.Add(1)
				}
			}(i)
		}

		wg.Wait()
		// Should allow max 2 requests, others should get circuit open error
		assert.LessOrEqual(t, int(successCount.Load()), 2)
		assert.Greater(t, int(openErrorCount.Load()), 0)
	})
}

func TestCircuitBreakerExecuteGeneric(t *testing.T) {
	t.Run("successful execution", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		ctx := context.Background()

		type TestData struct {
			Value string
		}

		var result TestData
		err := cb.ExecuteGeneric(ctx, "test-key", &result, func() (interface{}, error) {
			return &TestData{Value: "test-value"}, nil
		})

		require.NoError(t, err)
		assert.Equal(t, "test-value", result.Value)
	})

	t.Run("circuit open error", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 1,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Open the circuit
		_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return nil, errors.New("test error")
		})

		var result string
		err := cb.ExecuteGeneric(ctx, "test-key", &result, func() (interface{}, error) {
			return "should not execute", nil
		})

		assert.Equal(t, ErrCircuitOpen, err)
	})

	t.Run("unmarshal error", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		ctx := context.Background()

		var result int
		err := cb.ExecuteGeneric(ctx, "test-key", &result, func() (interface{}, error) {
			return "not an int", nil
		})

		assert.Error(t, err)
	})
}

func TestPerKeyCircuitBreaker(t *testing.T) {
	t.Run("per-key failure tracking", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 3,
			Timeout:          100 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Fail key1 enough to trigger per-key breaker
		for i := 0; i < 3; i++ {
			_, _ = cb.Execute(ctx, "key1", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// key1 should be blocked by per-key breaker
		_, err := cb.Execute(ctx, "key1", func() (interface{}, error) {
			return "should not execute", nil
		})
		assert.Equal(t, ErrCircuitOpen, err)

		// Reset global circuit breaker to ensure key2 test works
		cb.setState(CircuitClosed)
		cb.failures.Store(0)

		// key2 should still work
		_, err = cb.Execute(ctx, "key2", func() (interface{}, error) {
			return "success", nil
		})
		assert.NoError(t, err)

		// Wait for timeout
		time.Sleep(150 * time.Millisecond)

		// key1 should work again
		_, err = cb.Execute(ctx, "key1", func() (interface{}, error) {
			return "success", nil
		})
		assert.NoError(t, err)
	})

	t.Run("per-key success resets failures", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 3,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Two failures
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "key1", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Success should reset
		_, err := cb.Execute(ctx, "key1", func() (interface{}, error) {
			return "success", nil
		})
		assert.NoError(t, err)

		// Two more failures should not trigger breaker (counter was reset)
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "key1", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Should still work
		_, err = cb.Execute(ctx, "key1", func() (interface{}, error) {
			return "success", nil
		})
		assert.NoError(t, err)
	})

	t.Run("LRU eviction of key breakers", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 5, // High threshold to avoid global circuit opening
			MaxKeyBreakers:   3, // Small LRU size for testing
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Create failures for multiple keys
		keys := []string{"key1", "key2", "key3", "key4"}
		for _, key := range keys {
			_, _ = cb.Execute(ctx, key, func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}

		// Verify LRU size
		cb.keyBreakerMutex.Lock()
		lruLen := cb.keyBreakerLRU.Len()
		cb.keyBreakerMutex.Unlock()
		assert.Equal(t, 3, lruLen) // Should be capped at maxKeyBreakers
	})
}

func TestCircuitBreakerGetState(t *testing.T) {
	cb := createTestCircuitBreaker(t, nil)

	t.Run("closed state", func(t *testing.T) {
		cb.setState(CircuitClosed)
		assert.Equal(t, "closed", cb.GetState())
	})

	t.Run("open state", func(t *testing.T) {
		cb.setState(CircuitOpen)
		assert.Equal(t, "open", cb.GetState())
	})

	t.Run("half-open state", func(t *testing.T) {
		cb.setState(CircuitHalfOpen)
		assert.Equal(t, "half-open", cb.GetState())
	})

	t.Run("unknown state", func(t *testing.T) {
		cb.state.Store(999) // Invalid state
		assert.Equal(t, "unknown", cb.GetState())
	})
}

func TestCircuitBreakerReset(t *testing.T) {
	cfg := &CircuitBreakerConfig{
		FailureThreshold: 2,
	}
	cb := createTestCircuitBreaker(t, cfg)
	ctx := context.Background()

	// Open the circuit and create some per-key breakers
	for i := 0; i < 2; i++ {
		_, _ = cb.Execute(ctx, "key1", func() (interface{}, error) {
			return nil, errors.New("test error")
		})
		_, _ = cb.Execute(ctx, "key2", func() (interface{}, error) {
			return nil, errors.New("test error")
		})
	}

	// Verify circuit is open
	assert.Equal(t, "open", cb.GetState())
	assert.Greater(t, int(cb.failures.Load()), 0)

	// Verify per-key breakers exist
	_, ok1 := cb.keyBreakers.Load("key1")
	_, ok2 := cb.keyBreakers.Load("key2")
	assert.True(t, ok1)
	assert.True(t, ok2)

	// Reset
	cb.Reset()

	// Verify everything is reset
	assert.Equal(t, "closed", cb.GetState())
	assert.Equal(t, int32(0), cb.failures.Load())
	assert.Equal(t, int32(0), cb.successes.Load())
	assert.Equal(t, int32(0), cb.halfOpenRequests.Load())

	// Verify per-key breakers are cleared
	_, ok1 = cb.keyBreakers.Load("key1")
	_, ok2 = cb.keyBreakers.Load("key2")
	assert.False(t, ok1)
	assert.False(t, ok2)

	// Verify LRU is empty
	cb.keyBreakerMutex.Lock()
	lruLen := cb.keyBreakerLRU.Len()
	cb.keyBreakerMutex.Unlock()
	assert.Equal(t, 0, lruLen)

	// Circuit should work normally after reset
	_, err := cb.Execute(ctx, "key1", func() (interface{}, error) {
		return "success", nil
	})
	assert.NoError(t, err)
}

func TestCircuitBreakerConcurrency(t *testing.T) {
	t.Run("concurrent operations", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 10,
			SuccessThreshold: 5,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		var wg sync.WaitGroup
		numGoroutines := 50
		numOperations := 100

		for i := 0; i < numGoroutines; i++ {
			wg.Add(1)
			go func(routineID int) {
				defer wg.Done()
				for j := 0; j < numOperations; j++ {
					key := fmt.Sprintf("key-%d-%d", routineID, j%10)
					_, _ = cb.Execute(ctx, key, func() (interface{}, error) {
						if routineID%2 == 0 && j%3 == 0 {
							return nil, errors.New("test error")
						}
						return "success", nil
					})
				}
			}(i)
		}

		wg.Wait()
		// Should not panic or have race conditions
	})

	t.Run("concurrent state transitions", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 5,
			SuccessThreshold: 3,
			Timeout:          50 * time.Millisecond,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		var wg sync.WaitGroup
		stop := make(chan bool)

		// Goroutine to cause failures
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = cb.Execute(ctx, "fail-key", func() (interface{}, error) {
						return nil, errors.New("test error")
					})
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()

		// Goroutine to cause successes
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					_, _ = cb.Execute(ctx, "success-key", func() (interface{}, error) {
						return "success", nil
					})
					time.Sleep(10 * time.Millisecond)
				}
			}
		}()

		// Goroutine to reset
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
					cb.Reset()
					time.Sleep(100 * time.Millisecond)
				}
			}
		}()

		// Let it run for a bit
		time.Sleep(500 * time.Millisecond)
		close(stop)
		wg.Wait()
	})
}

func TestCircuitBreakerEdgeCases(t *testing.T) {
	t.Run("canExecute with invalid state", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		// Set an invalid state
		cb.state.Store(999)
		assert.False(t, cb.canExecute())
	})

	t.Run("success in closed state resets failure count", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 3,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Two failures
		for i := 0; i < 2; i++ {
			_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
				return nil, errors.New("test error")
			})
		}
		assert.Equal(t, int32(2), cb.failures.Load())

		// Success should reset failures
		_, err := cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return "success", nil
		})
		assert.NoError(t, err)
		assert.Equal(t, int32(0), cb.failures.Load())
	})

	t.Run("non-existent key in canExecuteKey", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		// Should return true for non-existent key
		assert.True(t, cb.canExecuteKey("non-existent-key"))
	})

	t.Run("existing key breaker update in LRU", func(t *testing.T) {
		cfg := &CircuitBreakerConfig{
			FailureThreshold: 5,
		}
		cb := createTestCircuitBreaker(t, cfg)
		ctx := context.Background()

		// Create initial failure
		_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return nil, errors.New("test error")
		})

		// Verify key exists in LRU
		cb.keyBreakerMutex.Lock()
		kb, exists := cb.keyBreakerLRU.Get("test-key")
		cb.keyBreakerMutex.Unlock()
		assert.True(t, exists)
		assert.Equal(t, int32(1), kb.failures.Load())

		// Another failure should update existing entry
		_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return nil, errors.New("test error")
		})

		cb.keyBreakerMutex.Lock()
		kb, exists = cb.keyBreakerLRU.Get("test-key")
		cb.keyBreakerMutex.Unlock()
		assert.True(t, exists)
		assert.Equal(t, int32(2), kb.failures.Load())
	})

	t.Run("resetKeyBreaker with LRU access", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		ctx := context.Background()

		// Create a key breaker
		_, _ = cb.Execute(ctx, "test-key", func() (interface{}, error) {
			return nil, errors.New("test error")
		})

		// Reset it
		cb.resetKeyBreaker("test-key")

		// Verify it was reset
		val, ok := cb.keyBreakers.Load("test-key")
		assert.True(t, ok)
		kb := val.(*keyCircuitBreaker)
		assert.Equal(t, int32(0), kb.failures.Load())
	})

	t.Run("resetKeyBreaker for non-existent key", func(t *testing.T) {
		cb := createTestCircuitBreaker(t, nil)
		// Should not panic
		cb.resetKeyBreaker("non-existent-key")
	})
}
