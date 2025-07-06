package swre

import (
	"context"
	"fmt"
	"runtime"
	"testing"
	"time"
)

// BenchmarkStaleEngine_MemoryAllocation tests memory allocation patterns of the SWR engine
// excluding storage layer (BadgerDB) allocations
func BenchmarkStaleEngine_MemoryAllocation(b *testing.B) {
	testCases := []struct {
		name      string
		valueSize int // Size of the cached value
	}{
		{"small_value_100B", 100},
		{"medium_value_1KB", 1024},
		{"large_value_10KB", 10 * 1024},
		{"xlarge_value_100KB", 100 * 1024},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Use in-memory storage to exclude BadgerDB allocations
			storage := NewMockStorage()
			logger := NewNoOpLogger()

			engine, _ := NewStaleEngineWithOptions(storage, logger,
				WithCacheTTL(&CacheTTL{
					FreshSeconds:   3600,
					StaleSeconds:   7200,
					ExpiredSeconds: 14400,
				}),
			)
			defer engine.Shutdown()

			ctx := context.Background()

			// Create test data of specified size
			testData := make([]byte, tc.valueSize)
			for i := range testData {
				testData[i] = byte(i % 256)
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				key := fmt.Sprintf("key-%d", i%1000) // Reuse keys to test cache hits
				_, err := engine.Execute(ctx, key, func() (interface{}, error) {
					// Return a copy to simulate real usage
					data := make([]byte, len(testData))
					copy(data, testData)
					return data, nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(tc.valueSize), "value_size_bytes")
		})
	}
}

// BenchmarkStaleEngine_CacheKeyGeneration tests the efficiency of cache key generation
func BenchmarkStaleEngine_CacheKeyGeneration(b *testing.B) {
	testCases := []struct {
		name   string
		keyLen int
	}{
		{"short_key_10", 10},
		{"medium_key_50", 50},
		{"long_key_200", 200},
		{"very_long_key_1000", 1000},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			storage := NewMockStorage()
			logger := NewNoOpLogger()

			engine, _ := NewStaleEngine(storage, logger)
			defer engine.Shutdown()

			ctx := context.Background()

			// Generate test key of specified length
			baseKey := ""
			for i := 0; i < tc.keyLen; i++ {
				baseKey += string(rune('a' + (i % 26)))
			}

			b.ResetTimer()
			b.ReportAllocs()

			for i := 0; i < b.N; i++ {
				key := fmt.Sprintf("%s-%d", baseKey, i)
				_, err := engine.Execute(ctx, key, func() (interface{}, error) {
					return "value", nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}

			b.ReportMetric(float64(tc.keyLen), "key_length")
		})
	}
}

// BenchmarkStaleEngine_GCPressure tests GC pressure under different workloads
func BenchmarkStaleEngine_GCPressure(b *testing.B) {
	scenarios := []struct {
		name        string
		numKeys     int
		valueSize   int
		churnRate   float64 // Percentage of new keys vs existing
		parallelism int
	}{
		{"low_pressure", 100, 1024, 0.1, 10},
		{"medium_pressure", 1000, 10240, 0.3, 50},
		{"high_pressure", 10000, 1024, 0.5, 100},
		{"extreme_pressure", 50000, 512, 0.8, 200},
	}

	for _, sc := range scenarios {
		b.Run(sc.name, func(b *testing.B) {
			storage := NewMockStorage()
			logger := NewNoOpLogger()

			engine, _ := NewStaleEngineWithOptions(storage, logger,
				WithCacheTTL(&CacheTTL{
					FreshSeconds:   10,
					StaleSeconds:   60,
					ExpiredSeconds: 300,
				}),
				WithMaxConcurrentRefreshes(sc.parallelism),
			)
			defer engine.Shutdown()

			ctx := context.Background()

			// Pre-populate cache
			testData := make([]byte, sc.valueSize)
			for i := 0; i < sc.numKeys; i++ {
				key := fmt.Sprintf("prepop-%d", i)
				_, _ = engine.Execute(ctx, key, func() (interface{}, error) {
					data := make([]byte, len(testData))
					copy(data, testData)
					return data, nil
				})
			}

			// Force GC to establish baseline
			runtime.GC()
			runtime.GC()

			var memStatsBefore runtime.MemStats
			runtime.ReadMemStats(&memStatsBefore)

			b.ResetTimer()

			// Run workload
			b.RunParallel(func(pb *testing.PB) {
				iter := 0
				for pb.Next() {
					iter++
					var key string
					if float64(iter%100)/100.0 < sc.churnRate {
						// New key (cache miss)
						key = fmt.Sprintf("new-%d-%d", iter, time.Now().UnixNano())
					} else {
						// Existing key
						key = fmt.Sprintf("prepop-%d", iter%sc.numKeys)
					}

					_, err := engine.Execute(ctx, key, func() (interface{}, error) {
						data := make([]byte, sc.valueSize)
						return data, nil
					})
					if err != nil {
						b.Error(err)
					}
				}
			})

			b.StopTimer()

			// Measure GC impact
			var memStatsAfter runtime.MemStats
			runtime.ReadMemStats(&memStatsAfter)

			gcPauses := memStatsAfter.PauseTotalNs - memStatsBefore.PauseTotalNs
			gcCount := memStatsAfter.NumGC - memStatsBefore.NumGC
			allocBytes := memStatsAfter.TotalAlloc - memStatsBefore.TotalAlloc

			b.ReportMetric(float64(gcPauses)/float64(b.N), "gc_pause_ns/op")
			b.ReportMetric(float64(gcCount), "gc_runs")
			b.ReportMetric(float64(allocBytes)/float64(b.N), "bytes_allocated/op")
			b.ReportMetric(sc.churnRate*100, "churn_rate_%")
		})
	}
}

// BenchmarkStaleEngine_HighCardinality tests performance with many unique keys
func BenchmarkStaleEngine_HighCardinality(b *testing.B) {
	storage := NewMockStorage()
	logger := NewNoOpLogger()

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   300,
			StaleSeconds:   3600,
			ExpiredSeconds: 7200,
		}),
	)
	defer engine.Shutdown()

	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()

	// Each operation uses a unique key
	for i := 0; i < b.N; i++ {
		key := fmt.Sprintf("unique-key-%d-%d", i, time.Now().UnixNano())
		_, err := engine.Execute(ctx, key, func() (interface{}, error) {
			return fmt.Sprintf("value-for-%s", key), nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}

	// Report memory usage
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	b.ReportMetric(float64(m.Alloc)/float64(b.N), "bytes_per_key")
	b.ReportMetric(float64(m.HeapObjects)/float64(b.N), "objects_per_key")
}

// BenchmarkStaleEngine_RefreshTrackerMemory specifically tests RefreshTracker memory usage
func BenchmarkStaleEngine_RefreshTrackerMemory(b *testing.B) {
	tracker := NewRefreshTracker(5 * time.Minute)

	testCases := []struct {
		name        string
		numKeys     int
		parallelism int
	}{
		{"1k_keys_10_parallel", 1000, 10},
		{"10k_keys_50_parallel", 10000, 50},
		{"100k_keys_100_parallel", 100000, 100},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			// Pre-populate tracker
			for i := 0; i < tc.numKeys; i++ {
				key := fmt.Sprintf("key-%d", i)
				tracker.TrySet(key)
			}

			b.ReportAllocs()
			b.ResetTimer()

			b.RunParallel(func(pb *testing.PB) {
				iter := 0
				for pb.Next() {
					iter++
					key := fmt.Sprintf("key-%d", iter%tc.numKeys)

					// Simulate refresh lifecycle
					if tracker.TrySet(key) {
						// Simulate some work
						time.Sleep(1 * time.Microsecond)
						tracker.Delete(key)
					}
				}
			})

			b.ReportMetric(float64(tc.numKeys), "tracked_keys")
			b.ReportMetric(float64(tc.parallelism), "parallelism")
		})
	}
}
