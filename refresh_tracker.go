// package swre implements a high-throughput Stale-While-Revalidate (SWR) cache engine.
//

package swre

import (
	"sync"
	"time"
)

// RefreshTracker prevents duplicate background refresh operations through
// time-based tracking with automatic memory leak prevention.
//
// Key Features:
// - Thread-safe tracking of active refresh operations
// - Automatic cleanup of expired tracking entries
// - Memory-efficient with bounded growth
// - Graceful shutdown support
//
// Design Rationale:
// - Uses time-based expiration instead of explicit cleanup
// - Background cleanup goroutine prevents unbounded memory growth
// - Read-write mutex optimizes for frequent read operations
// - Separate cleanup context allows independent lifecycle management
type RefreshTracker struct {
	mu         sync.RWMutex         // Protects concurrent access to entries map
	entries    map[string]time.Time // Maps cache keys to expiration timestamps
	ttl        time.Duration        // How long to track each refresh operation
	cleanupCtx chan struct{}        // Signals cleanup goroutine to stop
	wg         sync.WaitGroup       // Waits for cleanup goroutine during shutdown
}

// NewRefreshTracker creates a new refresh tracker with automatic cleanup.
// The TTL parameter controls how long refresh operations are tracked,
// which affects both memory usage and the precision of duplicate detection.
//
// TTL Guidelines:
// - Too short: May allow duplicate refreshes for slow operations
// - Too long: Increases memory usage and cleanup overhead
// - Recommended: 2-5x your typical refresh timeout
// - Default: 5 minutes for most DNS caching scenarios
func NewRefreshTracker(ttl time.Duration) *RefreshTracker {
	rt := &RefreshTracker{
		entries:    make(map[string]time.Time),
		ttl:        ttl,
		cleanupCtx: make(chan struct{}),
	}

	// Launch background cleanup goroutine
	rt.wg.Add(1)
	go rt.cleanup()

	return rt
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
// - Uses exclusive locking to prevent race conditions
// - Automatically handles expired entries without external cleanup
// - Provides strong consistency guarantees for refresh coordination
func (rt *RefreshTracker) TrySet(key string) bool {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	now := time.Now()

	// Check if key is already being tracked
	if expireTime, exists := rt.entries[key]; exists {
		if now.Before(expireTime) {
			return false // Still active, refresh already in progress
		}
		// Entry expired, safe to proceed with new refresh
	}

	// Mark key as being refreshed with expiration time
	rt.entries[key] = now.Add(rt.ttl)
	return true
}

// Delete removes a key from the refresh tracker.
func (rt *RefreshTracker) Delete(key string) {
	rt.mu.Lock()
	defer rt.mu.Unlock()
	delete(rt.entries, key)
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
	rt.mu.RLock()
	defer rt.mu.RUnlock()

	// Check if key exists and is still valid
	if expireTime, exists := rt.entries[key]; exists {
		return time.Now().Before(expireTime)
	}
	return false
}

// cleanup runs background maintenance to prevent memory leaks.
// This goroutine periodically removes expired tracking entries,
// ensuring bounded memory usage even under high refresh rates.
//
// Cleanup Strategy:
// - Runs every 30 seconds (balances memory vs CPU overhead)
// - Removes entries that have exceeded their TTL
// - Uses efficient map iteration for batch cleanup
// - Responds to shutdown signals for clean termination
//
// Memory Management:
// - Prevents unbounded growth under high refresh rates
// - Handles edge cases like long-running refresh operations
// - Maintains efficiency even with thousands of tracked keys
func (rt *RefreshTracker) cleanup() {
	defer rt.wg.Done()

	// Clean up every 30 seconds - balance between memory usage and CPU overhead
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rt.cleanupExpired()
		case <-rt.cleanupCtx:
			// Shutdown requested
			return
		}
	}
}

// cleanupExpired performs batch removal of expired tracking entries.
// This method efficiently prunes the entries map to prevent memory bloat
// while maintaining thread safety through exclusive locking.
//
// Performance Considerations:
// - Uses exclusive lock to prevent interference with active operations
// - Batch processes all expired entries in single critical section
// - Map deletions are O(1) operations
// - Lock duration scales with number of expired entries, not total entries
func (rt *RefreshTracker) cleanupExpired() {
	rt.mu.Lock()
	defer rt.mu.Unlock()

	// Snapshot current time for consistent expiration check
	now := time.Now()

	// Remove all entries that have exceeded their TTL
	for key, expireTime := range rt.entries {
		if now.After(expireTime) {
			delete(rt.entries, key)
		}
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
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.entries)
}
