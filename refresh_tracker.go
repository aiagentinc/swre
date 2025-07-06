// package swre implements a high-throughput Stale-While-Revalidate (SWR) cache engine.
//

package swre

import (
	"hash/fnv"
	"sync"
	"time"
)

const (
	// Number of shards - must be power of 2 for efficient modulo
	numShards = 16
	shardMask = numShards - 1
)

// RefreshTracker prevents duplicate background refresh operations through
// time-based tracking with automatic memory leak prevention.
//
// Key Features:
// - Thread-safe tracking of active refresh operations
// - Automatic cleanup of expired tracking entries
// - Memory-efficient with bounded growth
// - Graceful shutdown support
// - Sharded design for high concurrency performance
//
// Performance Optimizations:
// - Uses 16 shards to reduce lock contention
// - Incremental cleanup to avoid blocking all operations
// - Read-write mutex per shard for optimal read performance
//
// Design Rationale:
// - Uses time-based expiration instead of explicit cleanup
// - Background cleanup goroutine prevents unbounded memory growth
// - Sharding reduces lock contention by 16x
// - Incremental cleanup processes one shard at a time
type RefreshTracker struct {
	shards     [numShards]*refreshShard
	ttl        time.Duration
	cleanupCtx chan struct{}
	wg         sync.WaitGroup
}

// refreshShard represents a single shard with its own lock
type refreshShard struct {
	mu      sync.RWMutex
	entries map[string]time.Time
}

// NewRefreshTracker creates a new refresh tracker with automatic cleanup.
// The TTL parameter controls how long refresh operations are tracked,
// which affects both memory usage and the precision of duplicate detection.
//
// TTL Guidelines:
// - Too short: May allow duplicate refreshes for slow operations
// - Too long: Increases memory usage and cleanup overhead
// - Recommended: 2-5x your typical refresh timeout
// - Default: 5 minutes for most caching scenarios
func NewRefreshTracker(ttl time.Duration) *RefreshTracker {
	rt := &RefreshTracker{
		ttl:        ttl,
		cleanupCtx: make(chan struct{}),
	}

	// Initialize all shards
	for i := 0; i < numShards; i++ {
		rt.shards[i] = &refreshShard{
			entries: make(map[string]time.Time),
		}
	}

	// Launch background cleanup goroutine
	rt.wg.Add(1)
	go rt.cleanup()

	return rt
}

// getShard returns the shard for a given key using FNV-1a hash
func (rt *RefreshTracker) getShard(key string) *refreshShard {
	h := fnv.New32a()
	// FNV-1a hash Write never returns an error, but we handle it to satisfy gosec
	_, _ = h.Write([]byte(key))
	return rt.shards[h.Sum32()&shardMask]
}

// TrySet atomically attempts to mark a key as being refreshed.
// This method implements optimistic concurrency control to prevent
// duplicate refresh operations while allowing expired entries to be reused.
//
// Return Values:
// - true: Successfully marked key for refresh (caller should proceed)
// - false: Key already being refreshed by another goroutine (caller should skip)
//
// Race Condition Handling:
// - Uses per-shard locking to minimize contention
// - Automatically handles expired entries without external cleanup
// - Provides strong consistency guarantees for refresh coordination
func (rt *RefreshTracker) TrySet(key string) bool {
	shard := rt.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()

	// Check if key is already being tracked
	if expireTime, exists := shard.entries[key]; exists {
		if now.Before(expireTime) {
			return false // Still active, refresh already in progress
		}
		// Entry expired, safe to proceed with new refresh
	}

	// Mark key as being refreshed with expiration time
	shard.entries[key] = now.Add(rt.ttl)
	return true
}

// Delete removes a key from the refresh tracker.
func (rt *RefreshTracker) Delete(key string) {
	shard := rt.getShard(key)
	shard.mu.Lock()
	defer shard.mu.Unlock()
	delete(shard.entries, key)
}

// Get checks if a key is currently being refreshed without side effects.
// This read-only operation uses shared locking for optimal concurrency
// in scenarios with frequent refresh status checks.
//
// Performance Characteristics:
// - Uses read lock for minimal contention with other readers
// - O(1) lookup time with map-based storage
// - Automatic expiration check without cleanup overhead
// - Safe for high-frequency polling
//
// Use Cases:
// - Quick duplicate refresh detection
// - Monitoring and debugging refresh patterns
// - Load balancing decisions based on refresh status
func (rt *RefreshTracker) Get(key string) bool {
	shard := rt.getShard(key)
	shard.mu.RLock()
	defer shard.mu.RUnlock()

	// Check if key exists and is still valid
	if expireTime, exists := shard.entries[key]; exists {
		return time.Now().Before(expireTime)
	}
	return false
}

// cleanup runs background maintenance with incremental shard processing.
// This goroutine periodically removes expired tracking entries,
// ensuring bounded memory usage even under high refresh rates.
//
// Cleanup Strategy:
// - Runs every 2 seconds per shard (32 seconds for full cycle)
// - Processes one shard at a time to minimize lock duration
// - Uses efficient map iteration for batch cleanup
// - Responds to shutdown signals for clean termination
//
// Memory Management:
// - Prevents unbounded growth under high refresh rates
// - Handles edge cases like long-running refresh operations
// - Maintains efficiency even with millions of tracked keys
func (rt *RefreshTracker) cleanup() {
	defer rt.wg.Done()

	// Clean up more frequently but process less each time
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	shardIndex := 0

	for {
		select {
		case <-ticker.C:
			// Clean up one shard at a time (round-robin)
			rt.cleanupShard(shardIndex)
			shardIndex = (shardIndex + 1) % numShards
		case <-rt.cleanupCtx:
			// Shutdown requested
			return
		}
	}
}

// cleanupShard cleans expired entries from a single shard.
// This is the key optimization - lock contention is reduced by 16x
// compared to cleaning all entries at once.
func (rt *RefreshTracker) cleanupShard(shardIndex int) {
	shard := rt.shards[shardIndex]
	shard.mu.Lock()
	defer shard.mu.Unlock()

	now := time.Now()

	// Remove expired entries from this shard only
	for key, expireTime := range shard.entries {
		if now.After(expireTime) {
			delete(shard.entries, key)
		}
	}
}

// cleanupExpired performs batch removal of all expired tracking entries.
// This method efficiently prunes all shards to prevent memory bloat
// while maintaining thread safety through per-shard locking.
//
// Performance Considerations:
// - Processes shards sequentially to avoid overwhelming the system
// - Each shard lock is held only for its cleanup duration
// - Total cleanup time is sum of individual shard cleanup times
func (rt *RefreshTracker) cleanupExpired() {
	for i := 0; i < numShards; i++ {
		rt.cleanupShard(i)
	}
}

// Stop gracefully shuts down the refresh tracker and its cleanup goroutine.
func (rt *RefreshTracker) Stop() {
	close(rt.cleanupCtx)
	rt.wg.Wait()
}

// Size returns the current number of tracked refresh operations.
// This method is primarily intended for monitoring, debugging, and
// capacity planning purposes.
//
// Monitoring Uses:
// - Track refresh concurrency patterns
// - Detect potential memory leaks
// - Monitor system load and refresh rates
// - Validate cleanup effectiveness
//
// Note: The returned value is a snapshot and may change immediately
// after the call returns due to concurrent operations.
func (rt *RefreshTracker) Size() int {
	total := 0
	for i := 0; i < numShards; i++ {
		rt.shards[i].mu.RLock()
		total += len(rt.shards[i].entries)
		rt.shards[i].mu.RUnlock()
	}
	return total
}
