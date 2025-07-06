package swre

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

// BenchmarkRefreshTracker tests the performance of RefreshTracker
func BenchmarkRefreshTracker(b *testing.B) {
	scenarios := []struct {
		name       string
		numKeys    int
		numWorkers int
	}{
		{"LowContention", 100, 4},
		{"MediumContention", 1000, 16},
		{"HighContention", 10000, 64},
		{"VeryHighContention", 100000, 128},
	}

	for _, scenario := range scenarios {
		b.Run(scenario.name, func(b *testing.B) {
			rt := NewRefreshTracker(5 * time.Minute)
			defer rt.Stop()

			// Pre-populate some entries
			for i := 0; i < scenario.numKeys/2; i++ {
				rt.TrySet(fmt.Sprintf("key-%d", i))
			}

			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				id := 0
				for pb.Next() {
					id++
					key := fmt.Sprintf("key-%d", id%scenario.numKeys)

					// Mix of operations to simulate real usage
					switch id % 3 {
					case 0:
						rt.TrySet(key)
					case 1:
						rt.Get(key)
					case 2:
						rt.Delete(key)
					}
				}
			})
		})
	}
}

// BenchmarkRefreshTrackerTrySet focuses on TrySet operation
func BenchmarkRefreshTrackerTrySet(b *testing.B) {
	rt := NewRefreshTracker(5 * time.Minute)
	defer rt.Stop()

	b.RunParallel(func(pb *testing.PB) {
		id := 0
		for pb.Next() {
			id++
			rt.TrySet(fmt.Sprintf("key-%d", id%10000))
		}
	})
}

// BenchmarkRefreshTrackerGet focuses on Get operation
func BenchmarkRefreshTrackerGet(b *testing.B) {
	rt := NewRefreshTracker(5 * time.Minute)
	defer rt.Stop()

	// Pre-populate entries
	for i := 0; i < 10000; i++ {
		rt.TrySet(fmt.Sprintf("key-%d", i))
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		id := 0
		for pb.Next() {
			id++
			rt.Get(fmt.Sprintf("key-%d", id%10000))
		}
	})
}

// BenchmarkRefreshTrackerCleanup tests cleanup performance
func BenchmarkRefreshTrackerCleanup(b *testing.B) {
	// Use very short TTL to force many expirations
	rt := NewRefreshTracker(100 * time.Millisecond)
	defer rt.Stop()

	// Add many entries that will expire
	numEntries := 100000
	for i := 0; i < numEntries; i++ {
		rt.TrySet(fmt.Sprintf("cleanup-key-%d", i))
	}

	// Wait for entries to expire
	time.Sleep(150 * time.Millisecond)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rt.cleanupExpired()
	}
}

// BenchmarkRefreshTrackerConcurrentMixed tests mixed operations under high concurrency
func BenchmarkRefreshTrackerConcurrentMixed(b *testing.B) {
	rt := NewRefreshTracker(1 * time.Second)
	defer rt.Stop()

	numWorkers := 100
	keysPerWorker := 1000

	b.ResetTimer()

	var wg sync.WaitGroup
	wg.Add(numWorkers)

	start := time.Now()

	for w := 0; w < numWorkers; w++ {
		go func(workerID int) {
			defer wg.Done()

			for i := 0; i < keysPerWorker; i++ {
				key := fmt.Sprintf("worker-%d-key-%d", workerID, i)

				// Simulate realistic usage pattern
				if rt.TrySet(key) {
					// Simulate some work
					time.Sleep(time.Microsecond)
					rt.Delete(key)
				}

				// Check some other keys
				for j := 0; j < 3; j++ {
					checkKey := fmt.Sprintf("worker-%d-key-%d", (workerID+j)%numWorkers, i)
					rt.Get(checkKey)
				}
			}
		}(w)
	}

	wg.Wait()

	elapsed := time.Since(start)
	totalOps := numWorkers * keysPerWorker * 5 // TrySet + Delete + 3 Gets
	opsPerSec := float64(totalOps) / elapsed.Seconds()

	b.ReportMetric(opsPerSec, "ops/s")
	b.ReportMetric(float64(rt.Size()), "final_size")
}

// BenchmarkRefreshTrackerMemory tests memory usage patterns
func BenchmarkRefreshTrackerMemory(b *testing.B) {
	for _, numKeys := range []int{1000, 10000, 100000} {
		b.Run(fmt.Sprintf("Keys_%d", numKeys), func(b *testing.B) {
			b.ReportAllocs()

			rt := NewRefreshTracker(5 * time.Minute)
			defer rt.Stop()

			for i := 0; i < b.N; i++ {
				for j := 0; j < numKeys; j++ {
					rt.TrySet(fmt.Sprintf("mem-key-%d", j))
				}

				// Let cleanup run
				time.Sleep(10 * time.Millisecond)

				// Clear all
				for j := 0; j < numKeys; j++ {
					rt.Delete(fmt.Sprintf("mem-key-%d", j))
				}
			}
		})
	}
}
