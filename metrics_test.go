package swre

import (
	"errors"
	"testing"
	"time"
)

// TestNoOpMetrics_AllMethods tests all NoOpMetrics methods for coverage
func TestNoOpMetrics_AllMethods(t *testing.T) {
	metrics := &NoOpMetrics{}

	// Test all methods - they should not panic
	metrics.RecordHit("test-key", "hit")
	metrics.RecordHit("test-key", "stale")
	metrics.RecordHit("test-key", "expired")

	metrics.RecordMiss("test-key")
	metrics.RecordMiss("another-key")

	metrics.RecordError("test-key", errors.New("test error"))
	metrics.RecordError("test-key", nil)

	metrics.RecordLatency("test-key", 100*time.Millisecond)
	metrics.RecordLatency("test-key", 0)
	metrics.RecordLatency("test-key", -1*time.Second)

	// No assertions needed - just ensure methods complete without panic
}
