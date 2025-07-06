package swre

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkStaleEngine_StaleServing benchmarks the critical path of serving stale data
// while triggering background refresh - this is the core SWR functionality
func BenchmarkStaleEngine_StaleServing(b *testing.B) {
	storage := NewMockStorage()
	logger := NewNoOpLogger()

	// Create engine with very short fresh TTL to force stale serving
	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   0, // Immediately stale
			StaleSeconds:   3600,
			ExpiredSeconds: 7200,
		}),
		WithMaxConcurrentRefreshes(100),
	)
	defer engine.Shutdown()

	ctx := context.Background()
	key := "bench-stale-key"

	// Pre-populate cache with an entry
	_, err := engine.Execute(ctx, key, func() (interface{}, error) {
		return "initial-value", nil
	})
	if err != nil {
		b.Fatal(err)
	}

	// Wait a moment to ensure entry is stale
	time.Sleep(10 * time.Millisecond)

	var refreshCount atomic.Int64
	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			entry, err := engine.Execute(ctx, key, func() (interface{}, error) {
				refreshCount.Add(1)
				// Simulate a high-cost operation (20ms+)
				time.Sleep(20 * time.Millisecond)
				return fmt.Sprintf("refreshed-%d", time.Now().UnixNano()), nil
			})
			if err != nil {
				b.Fatal(err)
			}
			// Verify we're getting stale data (fast path)
			if entry.Status != "stale" && entry.Status != "hit" {
				b.Fatalf("unexpected status: %s", entry.Status)
			}
		}
	})

	b.ReportMetric(float64(refreshCount.Load()), "refreshes")
	b.ReportMetric(float64(b.N)/float64(refreshCount.Load()), "stale_serves_per_refresh")
}

// BenchmarkStaleEngine_BackgroundRefresh benchmarks the background refresh mechanism
func BenchmarkStaleEngine_BackgroundRefresh(b *testing.B) {
	testCases := []struct {
		name                string
		concurrentRefreshes int
		numKeys             int
	}{
		{"1key_10concurrent", 10, 1},
		{"10keys_10concurrent", 10, 10},
		{"100keys_50concurrent", 50, 100},
		{"1000keys_100concurrent", 100, 1000},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			storage := NewMockStorage()
			logger := NewNoOpLogger()

			engine, _ := NewStaleEngineWithOptions(storage, logger,
				WithCacheTTL(&CacheTTL{
					FreshSeconds:   0, // Immediately stale
					StaleSeconds:   3600,
					ExpiredSeconds: 7200,
				}),
				WithMaxConcurrentRefreshes(tc.concurrentRefreshes),
			)
			defer engine.Shutdown()

			ctx := context.Background()

			// Pre-populate cache
			for i := 0; i < tc.numKeys; i++ {
				key := fmt.Sprintf("key-%d", i)
				_, err := engine.Execute(ctx, key, func() (interface{}, error) {
					return fmt.Sprintf("value-%d", i), nil
				})
				if err != nil {
					b.Fatal(err)
				}
			}

			// Wait to ensure all entries are stale
			time.Sleep(10 * time.Millisecond)

			var refreshCount atomic.Int64
			var wg sync.WaitGroup

			b.ResetTimer()

			// Simulate concurrent access to stale entries
			for i := 0; i < b.N; i++ {
				wg.Add(1)
				go func(iter int) {
					defer wg.Done()
					key := fmt.Sprintf("key-%d", iter%tc.numKeys)
					_, err := engine.Execute(ctx, key, func() (interface{}, error) {
						refreshCount.Add(1)
						// Simulate high-cost operation
						time.Sleep(20 * time.Millisecond)
						return fmt.Sprintf("refreshed-%d-%d", iter, time.Now().UnixNano()), nil
					})
					if err != nil {
						b.Error(err)
					}
				}(i)
			}

			wg.Wait()

			b.ReportMetric(float64(refreshCount.Load()), "total_refreshes")
			b.ReportMetric(float64(tc.concurrentRefreshes), "max_concurrent")
			b.ReportMetric(float64(tc.numKeys), "num_keys")
		})
	}
}

// BenchmarkStaleEngine_MixedWorkload simulates a realistic workload with hits, misses, and stale serves
func BenchmarkStaleEngine_MixedWorkload(b *testing.B) {
	storage := NewMockStorage()
	logger := NewNoOpLogger()

	engine, _ := NewStaleEngineWithOptions(storage, logger,
		WithCacheTTL(&CacheTTL{
			FreshSeconds:   1,    // 1 second fresh
			StaleSeconds:   3600, // 1 hour stale
			ExpiredSeconds: 7200, // 2 hours expired
		}),
		WithMaxConcurrentRefreshes(50),
	)
	defer engine.Shutdown()

	ctx := context.Background()

	// Pre-populate some cache entries
	numPrePopulated := 100
	for i := 0; i < numPrePopulated; i++ {
		key := fmt.Sprintf("prepop-key-%d", i)
		_, err := engine.Execute(ctx, key, func() (interface{}, error) {
			return fmt.Sprintf("value-%d", i), nil
		})
		if err != nil {
			b.Fatal(err)
		}
	}

	// Stats tracking
	var cacheHits, cacheMisses, staleServes, refreshes atomic.Int64

	b.ResetTimer()

	b.RunParallel(func(pb *testing.PB) {
		iter := 0
		for pb.Next() {
			iter++
			// Mix of access patterns:
			// 60% existing keys (will be hits or stale)
			// 40% new keys (cache misses)
			var key string
			if iter%10 < 6 {
				key = fmt.Sprintf("prepop-key-%d", iter%numPrePopulated)
			} else {
				key = fmt.Sprintf("new-key-%d", iter)
			}

			entry, err := engine.Execute(ctx, key, func() (interface{}, error) {
				refreshes.Add(1)
				// Simulate high-cost operation
				time.Sleep(20 * time.Millisecond)
				return fmt.Sprintf("computed-%s-%d", key, time.Now().UnixNano()), nil
			})

			if err != nil {
				b.Error(err)
				continue
			}

			// Track stats
			switch entry.Status {
			case "fresh":
				cacheHits.Add(1)
			case "miss":
				cacheMisses.Add(1)
			case "stale":
				staleServes.Add(1)
			}
		}
	})

	// Report detailed metrics
	total := float64(b.N)
	b.ReportMetric(float64(cacheHits.Load())/total*100, "hit_rate_%")
	b.ReportMetric(float64(cacheMisses.Load())/total*100, "miss_rate_%")
	b.ReportMetric(float64(staleServes.Load())/total*100, "stale_rate_%")
	b.ReportMetric(float64(refreshes.Load()), "total_refreshes")
	b.ReportMetric(total/float64(refreshes.Load()), "requests_per_refresh")
}

// BenchmarkStaleEngine_ConcurrentStaleAccess tests performance when many goroutines
// access the same stale key simultaneously
func BenchmarkStaleEngine_ConcurrentStaleAccess(b *testing.B) {
	concurrencyLevels := []int{10, 50, 100, 500}

	for _, concurrency := range concurrencyLevels {
		b.Run(fmt.Sprintf("concurrency_%d", concurrency), func(b *testing.B) {
			storage := NewMockStorage()
			logger := NewNoOpLogger()

			engine, _ := NewStaleEngineWithOptions(storage, logger,
				WithCacheTTL(&CacheTTL{
					FreshSeconds:   0, // Immediately stale
					StaleSeconds:   3600,
					ExpiredSeconds: 7200,
				}),
				WithMaxConcurrentRefreshes(10), // Limited to test queuing
			)
			defer engine.Shutdown()

			ctx := context.Background()
			key := "hot-key"

			// Pre-populate
			_, err := engine.Execute(ctx, key, func() (interface{}, error) {
				return "initial", nil
			})
			if err != nil {
				b.Fatal(err)
			}

			// Ensure stale
			time.Sleep(10 * time.Millisecond)

			var refreshCount atomic.Int64
			var staleCount atomic.Int64

			b.ResetTimer()

			// Launch concurrent goroutines
			var wg sync.WaitGroup
			start := make(chan struct{})

			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start // Wait for signal to start

					iterations := b.N / concurrency
					for j := 0; j < iterations; j++ {
						entry, err := engine.Execute(ctx, key, func() (interface{}, error) {
							refreshCount.Add(1)
							time.Sleep(50 * time.Millisecond) // Expensive operation
							return "refreshed", nil
						})
						if err != nil {
							b.Error(err)
							continue
						}
						if entry.Status == "stale" {
							staleCount.Add(1)
						}
					}
				}()
			}

			// Start all goroutines simultaneously
			close(start)
			wg.Wait()

			b.ReportMetric(float64(concurrency), "goroutines")
			b.ReportMetric(float64(refreshCount.Load()), "total_refreshes")
			b.ReportMetric(float64(staleCount.Load()), "stale_serves")
			b.ReportMetric(float64(staleCount.Load())/float64(refreshCount.Load()), "stale_to_refresh_ratio")
		})
	}
}
