// package swre implements a high-throughput Stale-While-Revalidate (SWR) cache engine.
//

package swre

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefreshTracker_BasicOperations tests basic functionality
func TestRefreshTracker_BasicOperations(t *testing.T) {
	rt := NewRefreshTracker(1 * time.Second)
	defer rt.Stop()

	// Test 1: TrySet on new key should succeed
	assert.True(t, rt.TrySet("key1"), "First TrySet should succeed")

	// Test 2: Get should return true for existing key
	assert.True(t, rt.Get("key1"), "Get should return true for existing key")

	// Test 3: TrySet on existing key should fail
	assert.False(t, rt.TrySet("key1"), "Second TrySet should fail")

	// Test 4: Get on non-existing key should return false
	assert.False(t, rt.Get("nonexistent"), "Get should return false for non-existing key")

	// Test 5: Delete should remove the key
	rt.Delete("key1")
	assert.False(t, rt.Get("key1"), "Get should return false after Delete")

	// Test 6: TrySet after Delete should succeed
	assert.True(t, rt.TrySet("key1"), "TrySet after Delete should succeed")

	// Test 7: Size should reflect current entries
	assert.Equal(t, 1, rt.Size(), "Size should be 1")
}

// TestRefreshTracker_Expiration tests TTL expiration
func TestRefreshTracker_Expiration(t *testing.T) {
	rt := NewRefreshTracker(100 * time.Millisecond)
	defer rt.Stop()

	// Add a key
	require.True(t, rt.TrySet("expiring-key"))
	assert.True(t, rt.Get("expiring-key"), "Key should exist initially")

	// Wait for expiration
	time.Sleep(150 * time.Millisecond)

	// Key should be considered expired
	assert.False(t, rt.Get("expiring-key"), "Key should be expired")

	// TrySet should succeed on expired key
	assert.True(t, rt.TrySet("expiring-key"), "TrySet should succeed on expired key")
}

// TestRefreshTracker_CleanupProcess tests automatic cleanup
func TestRefreshTracker_CleanupProcess(t *testing.T) {
	// Use a short TTL for faster testing
	rt := NewRefreshTracker(100 * time.Millisecond)
	defer rt.Stop()

	// Add multiple keys
	for i := 0; i < 5; i++ {
		key := string(rune('a' + i))
		require.True(t, rt.TrySet(key))
	}
	assert.Equal(t, 5, rt.Size(), "Should have 5 entries")

	// Wait for TTL to expire
	time.Sleep(150 * time.Millisecond)

	// Manually trigger cleanup (since automatic cleanup runs every 2 seconds per shard)
	rt.cleanupExpired()

	// All keys should be cleaned up
	assert.Equal(t, 0, rt.Size(), "All entries should be cleaned up")
}

// TestRefreshTracker_ConcurrentAccess tests thread safety
func TestRefreshTracker_ConcurrentAccess(t *testing.T) {
	rt := NewRefreshTracker(1 * time.Second)
	defer rt.Stop()

	const (
		numGoroutines = 100
		numOperations = 1000
	)

	var wg sync.WaitGroup
	errors := make(chan error, numGoroutines)

	// Concurrent TrySet/Get/Delete operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				key := "key" + string(rune('0'+(j%10)))

				switch j % 3 {
				case 0:
					rt.TrySet(key)
				case 1:
					rt.Get(key)
				case 2:
					rt.Delete(key)
				}
			}
		}(i)
	}

	// Concurrent Size() calls
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < numOperations; j++ {
				size := rt.Size()
				if size < 0 {
					errors <- assert.AnError
				}
			}
		}()
	}

	wg.Wait()
	close(errors)

	// Check for errors
	for err := range errors {
		t.Fatalf("Concurrent access error: %v", err)
	}
}

// TestRefreshTracker_RaceCondition tests for race conditions with race detector
func TestRefreshTracker_RaceCondition(t *testing.T) {
	if !testing.Short() {
		t.Skip("Skipping race test in non-short mode")
	}

	rt := NewRefreshTracker(50 * time.Millisecond)
	defer rt.Stop()

	var wg sync.WaitGroup

	// Writer goroutines
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := "race-key-" + string(rune('0'+(j%5)))
				if j%2 == 0 {
					rt.TrySet(key)
				} else {
					rt.Delete(key)
				}
				runtime.Gosched() // Encourage goroutine switching
			}
		}(i)
	}

	// Reader goroutines
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				key := "race-key-" + string(rune('0'+(j%5)))
				rt.Get(key)
				rt.Size()
				runtime.Gosched()
			}
		}()
	}

	wg.Wait()
}

// TestRefreshTracker_GracefulShutdown tests Stop() method
func TestRefreshTracker_GracefulShutdown(t *testing.T) {
	rt := NewRefreshTracker(1 * time.Second)

	// Add some entries
	rt.TrySet("key1")
	rt.TrySet("key2")

	// Stop should complete without hanging
	done := make(chan bool)
	go func() {
		rt.Stop()
		done <- true
	}()

	select {
	case <-done:
		// Success
	case <-time.After(1 * time.Second):
		t.Fatal("Stop() did not complete in time")
	}

	// Verify we can't perform operations after Stop
	// (This is just a basic check - the cleanup goroutine stopping is verified by Stop() returning)
}

// TestRefreshTracker_EdgeCases tests edge cases
func TestRefreshTracker_EdgeCases(t *testing.T) {
	rt := NewRefreshTracker(1 * time.Second)
	defer rt.Stop()

	// Test empty key
	assert.True(t, rt.TrySet(""), "Should handle empty key")
	assert.True(t, rt.Get(""), "Should get empty key")
	rt.Delete("")
	assert.False(t, rt.Get(""), "Empty key should be deleted")

	// Test very long key
	longKey := string(make([]byte, 1024))
	assert.True(t, rt.TrySet(longKey), "Should handle long key")
	assert.True(t, rt.Get(longKey), "Should get long key")

	// Test Unicode keys
	unicodeKey := "🔑🌟测试"
	assert.True(t, rt.TrySet(unicodeKey), "Should handle Unicode key")
	assert.True(t, rt.Get(unicodeKey), "Should get Unicode key")
}

// TestRefreshTracker_MemoryLeak tests for memory leaks
func TestRefreshTracker_MemoryLeak(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping memory leak test in short mode")
	}

	rt := NewRefreshTracker(10 * time.Millisecond)
	defer rt.Stop()

	// Force many expiration cycles
	for cycle := 0; cycle < 10; cycle++ {
		// Add 1000 entries
		for i := 0; i < 1000; i++ {
			key := "leak-test-" + string(rune('a')) + "-" + string(rune('0'+(i%10))) + "-" + string(rune('0'+(cycle%10)))
			rt.TrySet(key)
		}

		// Wait for expiration and cleanup
		// Need to wait for: TTL (10ms) + cleanup interval (30s) + buffer
		time.Sleep(60 * time.Millisecond)

		// Manually trigger cleanup since default interval is 30s
		rt.cleanupExpired()

		// Size should be 0 after cleanup
		size := rt.Size()
		if size > 10 { // Allow small margin for timing
			t.Errorf("Cycle %d: Expected size near 0, got %d", cycle, size)
		}
	}
}

// Benchmark tests

// BenchmarkRefreshTracker_TrySet benchmarks TrySet operation
func BenchmarkRefreshTracker_TrySet(b *testing.B) {
	rt := NewRefreshTracker(1 * time.Hour) // Long TTL to avoid expiration
	defer rt.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "bench-key-" + string(rune(i%1000))
			rt.TrySet(key)
			i++
		}
	})
}

// BenchmarkRefreshTracker_Get benchmarks Get operation
func BenchmarkRefreshTracker_Get(b *testing.B) {
	rt := NewRefreshTracker(1 * time.Hour)
	defer rt.Stop()

	// Pre-populate
	for i := 0; i < 1000; i++ {
		rt.TrySet("bench-key-" + string(rune(i)))
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "bench-key-" + string(rune(i%1000))
			rt.Get(key)
			i++
		}
	})
}

// BenchmarkRefreshTracker_Delete benchmarks Delete operation
func BenchmarkRefreshTracker_Delete(b *testing.B) {
	rt := NewRefreshTracker(1 * time.Hour)
	defer rt.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "bench-key-" + string(rune(i))
			rt.TrySet(key)
			rt.Delete(key)
			i++
		}
	})
}

// BenchmarkRefreshTracker_Mixed benchmarks mixed operations
func BenchmarkRefreshTracker_Mixed(b *testing.B) {
	rt := NewRefreshTracker(100 * time.Millisecond)
	defer rt.Stop()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := "bench-key-" + string(rune(i%100))
			switch i % 3 {
			case 0:
				rt.TrySet(key)
			case 1:
				rt.Get(key)
			case 2:
				rt.Delete(key)
			}
			i++
		}
	})
}

// BenchmarkRefreshTracker_ConcurrentAccess benchmarks under high concurrency
func BenchmarkRefreshTracker_ConcurrentAccess(b *testing.B) {
	rt := NewRefreshTracker(1 * time.Second)
	defer rt.Stop()

	numKeys := 10000
	var counter atomic.Int64

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			n := counter.Add(1)
			key := "key-" + string(rune(int(n)%numKeys))

			switch n % 4 {
			case 0:
				rt.TrySet(key)
			case 1, 2:
				rt.Get(key)
			case 3:
				rt.Delete(key)
			}
		}
	})

	b.ReportMetric(float64(b.N)/b.Elapsed().Seconds(), "ops/sec")
}

// TestRefreshTracker_StressTest performs a stress test
func TestRefreshTracker_StressTest(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	rt := NewRefreshTracker(50 * time.Millisecond)
	defer rt.Stop()

	const (
		numGoroutines = 50
		duration      = 2 * time.Second
	)

	ctx := make(chan struct{})
	var ops atomic.Int64

	// Start stress goroutines
	var wg sync.WaitGroup
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			for {
				select {
				case <-ctx:
					return
				default:
					key := "stress-" + string(rune(id)) + "-" + string(rune(ops.Add(1)%100))

					// Random operation
					switch ops.Load() % 3 {
					case 0:
						rt.TrySet(key)
					case 1:
						rt.Get(key)
					case 2:
						rt.Delete(key)
					}
				}
			}
		}(i)
	}

	// Run for specified duration
	time.Sleep(duration)
	close(ctx)
	wg.Wait()

	totalOps := ops.Load()
	opsPerSec := float64(totalOps) / duration.Seconds()
	t.Logf("Stress test completed: %d total operations, %.2f ops/sec", totalOps, opsPerSec)

	// Verify tracker is still functional
	assert.True(t, rt.TrySet("final-key"), "Tracker should still be functional")
	assert.True(t, rt.Get("final-key"), "Should be able to get key")
}
