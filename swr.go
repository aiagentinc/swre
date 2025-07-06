// package swre implements a high‑throughput Stale‑While‑Revalidate (SWR) cache engine.
//
// Key Design Points
// -----------------
// • Single upstream call per key – enforced by singleflight.DoChan.
// • Non‑blocking stale refresh with bounded goroutine lifetime.
// • Hot‑path (fresh hit) is lock‑free – only two atomic reads.
// • Backwards‑compatible with the original public API.
//
// Performance Notes
// -----------------
// The implementation is optimised for millions of concurrent requests:
//   - No channel allocations on the hot‑path.
//   - Cache hydration is guarded by singleflight, eliminating duplicate work.
//   - Background refreshes reuse the same singleflight channel, so latecomers
//     attach to the in‑flight request instead of spawning new goroutines.
//   - Refresh goroutines are short‑lived and capped at one per key.
//

package swre

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

//--------------------------------------------------
// Configuration
//--------------------------------------------------

// Option configures a StaleEngine. All options are purely additive and do not
// break existing callers using NewStaleEngine.
//
// Example:
//
//	eng, _ := NewStaleEngineWithOptions(storage, logger,
//	    WithCacheTTL(&CacheTTL{FreshSeconds: 300, StaleSeconds: 3600}),
//	    WithMetrics(myMetrics),
//	)
//
// Exposing Option publicly is optional; keep it internal if you do not want to
// encourage tuning.
type Option func(*StaleEngine)

// WithCacheTTL sets the default cache TTL configuration
func WithCacheTTL(ttl *CacheTTL) Option {
	return func(e *StaleEngine) {
		if ttl != nil {
			e.defaultCacheTTL = ttl
		}
	}
}

// WithSerializer sets a custom serializer
func WithSerializer(s Serializer) Option {
	return func(e *StaleEngine) {
		if s != nil {
			e.serializer = s
		}
	}
}

// WithTTLCalculator sets a custom TTL calculator
func WithTTLCalculator(c TTLCalculator) Option {
	return func(e *StaleEngine) {
		if c != nil {
			e.ttlCalculator = c
		}
	}
}

// WithValueTransformer sets a custom value transformer
func WithValueTransformer(t ValueTransformer) Option {
	return func(e *StaleEngine) {
		e.valueTransformer = t
	}
}

// WithMetrics sets a custom metrics collector
func WithMetrics(m CacheMetrics) Option {
	return func(e *StaleEngine) {
		if m != nil {
			e.metrics = m
		}
	}
}

// WithMaxConcurrentRefreshes sets the maximum concurrent background refreshes
func WithMaxConcurrentRefreshes(max int) Option {
	return func(e *StaleEngine) {
		if max > 0 {
			e.maxConcurrentRefreshes = max
		}
	}
}

// WithRefreshTimeout sets the timeout for refresh operations
func WithRefreshTimeout(timeout time.Duration) Option {
	return func(e *StaleEngine) {
		if timeout > 0 {
			e.refreshTimeout = timeout
		}
	}
}

//--------------------------------------------------
// Engine
//--------------------------------------------------

// StaleEngine implements SWR caching logic that scales to massive concurrency.
// All public methods are goroutine‑safe.
//
// Storage is an application‑defined interface (not included here) providing Get
// and Set semantics.
type StaleEngine struct {
	sfg     singleflight.Group
	storage Storage
	logger  Logger

	// Default TTL configuration
	defaultCacheTTL *CacheTTL

	// refreshTracker tracks active background refreshes with automatic expiration
	// to prevent memory leaks and duplicate refreshes
	refreshTracker *RefreshTracker

	// Generic components for flexibility
	serializer       Serializer
	ttlCalculator    TTLCalculator
	valueTransformer ValueTransformer
	metrics          CacheMetrics

	// Performance controls
	maxConcurrentRefreshes int
	refreshTimeout         time.Duration
	currentRefreshes       atomic.Int32

	// Shutdown support
	shutdownCtx    context.Context
	shutdownCancel context.CancelFunc
	shutdownOnce   sync.Once
}

// NewStaleEngine constructs an engine with default parameters.
func NewStaleEngine(storage Storage, logger Logger) (*StaleEngine, error) {
	return NewStaleEngineWithOptions(storage, logger)
}

// NewStaleEngineWithOptions allows fine‑grained tuning via functional options.
func NewStaleEngineWithOptions(storage Storage, logger Logger, opts ...Option) (*StaleEngine, error) {
	if storage == nil {
		return nil, errors.New("storage cannot be nil")
	}
	if logger == nil {
		return nil, ErrNilLogger
	}

	// Create default CacheTTL configuration
	defaultCacheTTL := &CacheTTL{
		FreshSeconds:   5,      // 5 seconds fresh
		StaleSeconds:   3600,   // 1 hour stale
		ExpiredSeconds: 302400, // 3.5 days expired
	}

	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())

	// Create refresh tracker with 5 minute TTL to prevent memory leaks
	// This ensures refresh flags are automatically cleaned up even if goroutines crash
	refreshTracker := NewRefreshTracker(5 * time.Minute)

	e := &StaleEngine{
		storage:                storage,
		logger:                 logger.Named("StaleEngine"),
		defaultCacheTTL:        defaultCacheTTL,
		serializer:             &JSONSerializer{},
		ttlCalculator:          nil, // Will be set after options
		metrics:                &NoOpMetrics{},
		maxConcurrentRefreshes: 1000,
		refreshTimeout:         30 * time.Second,
		refreshTracker:         refreshTracker,
		shutdownCtx:            shutdownCtx,
		shutdownCancel:         shutdownCancel,
	}

	for _, o := range opts {
		o(e)
	}

	// Set default TTL calculator if not provided
	if e.ttlCalculator == nil {
		totalTTL, freshTTL := e.defaultCacheTTL.ToEngineTTLs()
		e.ttlCalculator = &DefaultTTLCalculator{
			TTL:      totalTTL,
			StaleTTL: freshTTL,
		}
	}

	e.logger.Info("StaleEngine initialised",
		Int("defaultFreshSeconds", e.defaultCacheTTL.FreshSeconds),
		Int("defaultStaleSeconds", e.defaultCacheTTL.StaleSeconds),
		Int("defaultExpiredSeconds", e.defaultCacheTTL.ExpiredSeconds),
		Int("maxConcurrentRefreshes", e.maxConcurrentRefreshes))

	return e, nil
}

// NewStaleEngineWithConfig creates engine with full configuration
func NewStaleEngineWithConfig(cfg *EngineConfig) (*StaleEngine, error) {
	if cfg == nil {
		return nil, errors.New("config cannot be nil")
	}

	cfg.SetDefaults()

	opts := []Option{
		WithSerializer(cfg.Serializer),
		WithTTLCalculator(cfg.TTLCalculator),
		WithValueTransformer(cfg.ValueTransformer),
		WithMetrics(cfg.Metrics),
		WithMaxConcurrentRefreshes(cfg.MaxConcurrentRefreshes),
		WithRefreshTimeout(cfg.RefreshTimeout),
	}

	// Use new CacheTTL if provided
	if cfg.DefaultCacheTTL != nil {
		opts = append(opts, WithCacheTTL(cfg.DefaultCacheTTL))
	}

	return NewStaleEngineWithOptions(cfg.Storage, cfg.Logger, opts...)
}

// StaleEngineCallback represents the user‑supplied function to fetch fresh
// data. It must be idempotent and safe to call concurrently.
//
// The engine recovers panics inside the callback and returns them as errors.
type StaleEngineCallback func() (interface{}, error)

// CacheKey represents a cache key with optional TTL overrides
type CacheKey struct {
	Key string
	TTL *CacheTTL // nil means use engine defaults
}

// CacheTTL defines cache TTL settings in seconds
// The relationship between TTLs:
// - Fresh period: 0 to FreshSeconds (cache returns immediately)
// - Stale period: FreshSeconds to (FreshSeconds + StaleSeconds) (returns stale + triggers refresh)
// - Total lifetime: FreshSeconds + StaleSeconds + ExpiredSeconds (data is removed after this)
type CacheTTL struct {
	// FreshSeconds is how long the cache entry is considered fresh
	FreshSeconds int

	// StaleSeconds is how long the cache entry can be served stale while refreshing
	StaleSeconds int

	// ExpiredSeconds is additional time before the entry is completely removed
	ExpiredSeconds int
}

// ToEngineTTLs converts CacheTTL to the internal TTL format used by the engine
// Returns (totalTTL, freshDuration)
func (c *CacheTTL) ToEngineTTLs() (time.Duration, time.Duration) {
	if c == nil {
		// Return sensible defaults if nil
		return 10 * 24 * time.Hour, 5 * time.Second
	}

	freshDuration := time.Duration(c.FreshSeconds) * time.Second
	totalTTL := time.Duration(c.FreshSeconds+c.StaleSeconds+c.ExpiredSeconds) * time.Second

	// Ensure minimum TTL
	if totalTTL < 5*time.Second {
		totalTTL = 5 * time.Second
	}
	if freshDuration < 0 {
		freshDuration = 0
	}
	if freshDuration > totalTTL {
		freshDuration = totalTTL
	}

	return totalTTL, freshDuration
}

//--------------------------------------------------
// Public API
//--------------------------------------------------

// ExecuteGeneric is a high-level method that handles serialization/deserialization automatically
func (e *StaleEngine) ExecuteGeneric(ctx context.Context, key string, result interface{}, fn StaleEngineCallback) error {
	entry, err := e.Execute(ctx, key, fn)
	if err != nil {
		return err
	}

	// Deserialize the result
	if err := e.serializer.Unmarshal(entry.Value, result); err != nil {
		return fmt.Errorf("failed to unmarshal cached value: %w", err)
	}

	return nil
}

// GetCacheEntry retrieves a cache entry without triggering refresh
func (e *StaleEngine) GetCacheEntry(ctx context.Context, key string) (*CacheEntry, error) {
	return e.storage.Get(ctx, key)
}

// Execute returns the cached (or freshly fetched) value for key.
// Supports both string keys and CacheKey with TTL overrides.
//
// Guarantees:
//   - Single upstream call per key at any time.
//   - Upstream call continues even if the original caller cancels context.
func (e *StaleEngine) Execute(ctx context.Context, keyOrCacheKey interface{}, fn StaleEngineCallback) (*CacheEntry, error) {
	// Extract key and TTL from parameter
	var key string
	var cacheKey CacheKey

	switch k := keyOrCacheKey.(type) {
	case string:
		key = k
		cacheKey = CacheKey{Key: k, TTL: nil}
	case CacheKey:
		key = k.Key
		cacheKey = k
	default:
		return nil, fmt.Errorf("invalid key type: %T", keyOrCacheKey)
	}

	if key == "" {
		return nil, errors.New("cache key cannot be empty")
	}
	entry, err := e.storage.Get(ctx, key)
	if err != nil && !errors.Is(err, ErrNotFound) {
		e.metrics.RecordError(key, err)
		return nil, err
	}

	nowMs := NowUnixMilli()
	nowTime := time.Now()

	switch {
	case entry == nil: // cold miss
		e.logger.Debug("cache miss",
			String("key", key),
			Time("time", nowTime))
		e.metrics.RecordMiss(key)
		return e.syncRefresh(ctx, nil, cacheKey, fn)

	case entry.IsExpired(): // expired value
		e.logger.Debug("cache expired",
			String("key", key),
			Int64("now_ms", nowMs),
			Int64("expires_after", entry.ExpiresAfter),
			Int64("diff_ms", nowMs-entry.ExpiresAfter),
			Time("cached_at", time.UnixMilli(entry.CreatedAt)),
			Time("now", nowTime))
		e.metrics.RecordHit(key, "expired")
		e.tryAsyncRefresh(cacheKey, fn)
		return entry.CloneWithStatus("expired"), nil

	case entry.IsStale(): // stale‑while‑revalidate
		e.logger.Debug("cache stale",
			String("key", key),
			Int64("now_ms", nowMs),
			Int64("stale_after", entry.StaleAfter),
			Int64("diff_ms", nowMs-entry.StaleAfter),
			Time("cached_at", time.UnixMilli(entry.CreatedAt)),
			Time("now", nowTime),
			String("trigger", "will_refresh"))
		e.metrics.RecordHit(key, "stale")
		e.tryAsyncRefresh(cacheKey, fn)
		return entry.CloneWithStatus("stale"), nil

	default: // fresh hit
		e.logger.Debug("cache hit",
			String("key", key),
			Int64("now_ms", nowMs),
			Int64("stale_after", entry.StaleAfter),
			Int64("remaining_fresh_ms", entry.StaleAfter-nowMs),
			Time("cached_at", time.UnixMilli(entry.CreatedAt)),
			Time("now", nowTime))
		e.metrics.RecordHit(key, "hit")
		return entry.CloneWithStatus("hit"), nil
	}
}

//--------------------------------------------------
// Internal helpers
//--------------------------------------------------

// syncRefresh blocks until a fresh value is available or ctx is done.
func (e *StaleEngine) syncRefresh(
	ctx context.Context,
	fallback *CacheEntry,
	cacheKey CacheKey,
	fn StaleEngineCallback,
) (*CacheEntry, error) {

	// 1) Use shutdownCtx as root, allowing refresh to be cancelled during Stop()
	parentCtx, cancelParent := context.WithCancel(e.shutdownCtx)
	defer cancelParent()

	// Let upper-level ctx Done() also drive cancellation
	go func() {
		select {
		case <-ctx.Done():
			cancelParent()
		case <-parentCtx.Done():
		}
	}()

	// 2) Singleflight - pass cancellable context when going to upstream
	resCh := e.sfg.DoChan(cacheKey.Key, func() (interface{}, error) {
		return e.safeCall(parentCtx, cacheKey, fn)
	})

	select {
	//------------------------------------------------------------------
	// 3) Normal result retrieval
	//------------------------------------------------------------------
	case res := <-resCh:
		if res.Err != nil {
			if fallback != nil {
				return fallback.CloneWithStatus("fallback"), nil
			}
			return nil, res.Err
		}

		// res.Val is from safeCall (already persisted and transformed),
		// we can directly read from storage
		entry, err := e.storage.Get(context.Background(), cacheKey.Key)
		if err == nil && entry != nil {
			entry.Status = "miss"
			return entry, nil
		}
		// This should not happen in theory; fallback to reconstruction
		e.logger.Warn("storage miss right after safeCall",
			String("key", cacheKey.Key))
		// fallthrough to rebuild

		// ----------- Reconstruction (extreme fallback path) -----------
		data, err := e.serializer.Marshal(res.Val)
		if err != nil {
			return nil, fmt.Errorf("marshal after storage miss: %w", err)
		}
		ttl, staleTTL, ttlErr := e.ttlCalculator.CalculateTTL(cacheKey.Key, res.Val)
		if ttlErr != nil {
			return nil, fmt.Errorf("ttl calc failed: %w", ttlErr)
		}
		rebuilt := NewCacheEntryWithTTL(cacheKey.Key, data, ttl, staleTTL)
		rebuilt.Status = "miss"
		return rebuilt, nil

	//------------------------------------------------------------------
	// 4) Caller cancellation or timeout
	//------------------------------------------------------------------
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// safeCall executes the user-supplied callback with panic recovery, timeout
// protection, optional value transformation, serialization, and persistent storage.
//   - ctxParent: Allows caller to cancel uniformly during refresh (Shutdown/upstream timeout)
//   - cacheKey:  Used for TTL overrides and persistent key names
//   - fn:        User-defined, context-free pure function
//
// Return value 'val' is the original object *before* deserialization
// (or after processing by valueTransformer if configured).
func (e *StaleEngine) safeCall(
	ctxParent context.Context, // Parent context passed by caller
	cacheKey CacheKey,
	fn StaleEngineCallback,
) (val interface{}, err error) {

	start := time.Now()
	key := cacheKey.Key

	// Top-level defer: capture performance metrics and handle any unhandled panics
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("callback panic: %v", r)
			e.logger.Error("upstream callback panicked",
				String("key", key),
				Any("panic", r),
				ByteString("stack", []byte(captureStack())))
			e.metrics.RecordError(key, err)
		}
		e.metrics.RecordLatency(key, time.Since(start))
	}()

	// Step 1: Execute callback with timeout protection
	ctx, cancel := context.WithTimeout(ctxParent, e.refreshTimeout)
	defer cancel()

	type cbResult struct {
		v   interface{}
		err error
	}
	resCh := make(chan cbResult, 1) // Buffered to prevent blocking if result arrives early

	go func() {
		// Additional panic recovery inside goroutine to ensure resCh gets written
		defer func() {
			if r := recover(); r != nil {
				select {
				case resCh <- cbResult{nil, fmt.Errorf("callback panic: %v", r)}:
				default: // Upper level already timed out; discard result to prevent blocking
				}
			}
		}()

		v, e := fn()

		select {
		case resCh <- cbResult{v, e}: // Normal send
		case <-ctx.Done(): // Caller timed out/cancelled, return directly
		}
	}()

	var raw interface{}
	select {
	case <-ctx.Done():
		err = fmt.Errorf("callback timeout after %v", e.refreshTimeout)
		e.logger.Warn("callback execution timeout",
			String("key", key),
			Duration("timeout", e.refreshTimeout))
		e.metrics.RecordError(key, err)
		return nil, err

	case r := <-resCh:
		if r.err != nil {
			e.metrics.RecordError(key, r.err)
			return nil, r.err
		}
		raw = r.v
	}

	// Step 2: Optional value transformation
	if e.valueTransformer != nil {
		raw, err = e.valueTransformer.Transform(ctxParent, key, raw)
		if err != nil {
			e.metrics.RecordError(key, err)
			return nil, fmt.Errorf("value transformation failed: %w", err)
		}
	}

	// Step 3: Serialization
	data, err := e.serializer.Marshal(raw)
	if err != nil {
		e.metrics.RecordError(key, err)
		return nil, fmt.Errorf("serialization failed: %w", err)
	}

	// Step 4: Calculate TTL
	var ttl, staleTTL time.Duration
	if cacheKey.TTL != nil {
		ttl, staleTTL = cacheKey.TTL.ToEngineTTLs()
	} else {
		ttl, staleTTL, err = e.ttlCalculator.CalculateTTL(key, raw)
		if err != nil {
			e.metrics.RecordError(key, err)
			return nil, fmt.Errorf("TTL calculation failed: %w", err)
		}
	}

	entry := NewCacheEntryWithTTL(key, data, ttl, staleTTL)

	// Step 5: Adaptive write timeout based on entry size
	entrySize := len(entry.Value)
	baseTimeout := 800 * time.Millisecond
	if e.refreshTimeout < baseTimeout { // Prevent negative values
		baseTimeout = e.refreshTimeout
	}
	extraBudget := int(e.refreshTimeout.Milliseconds() - baseTimeout.Milliseconds())
	extraMs := 0
	if entrySize > 1024 && extraBudget > 0 {
		extraMs = (entrySize / 1024) * 200
		if extraMs > extraBudget {
			extraMs = extraBudget
		}
	}
	storeTimeout := baseTimeout + time.Duration(extraMs)*time.Millisecond

	storeCtx, storeCancel := context.WithTimeout(ctxParent, storeTimeout)
	defer storeCancel()

	if err = e.storage.Set(storeCtx, key, entry); err != nil {
		e.logger.Warn("failed to persist cache entry",
			String("key", key),
			Error(err),
			Int("entry_size_bytes", entrySize),
			Duration("timeout", storeTimeout))
		e.metrics.RecordError(key, err)
		// Continue to return raw, allowing upper layer to use it directly
	}

	return raw, nil
}

// tryAsyncRefresh schedules a background refresh for the given key if:
//   - no other goroutine is already refreshing it, and
//   - current concurrent refreshes < maxConcurrentRefreshes.
//
// Each key only produces a unique safeCall; redundant goroutines will reuse
// results through singleflight. All internal errors are written to metrics
// but not returned to the caller.
func (e *StaleEngine) tryAsyncRefresh(cacheKey CacheKey, fn StaleEngineCallback) {
	key := cacheKey.Key

	//----------------------------------------------------------------------
	// 1) Quick detection: if refresh marker already exists, return immediately
	//----------------------------------------------------------------------
	if e.refreshTracker.Get(key) { // true means already refreshing
		e.logger.Debug("skip refresh - already in progress",
			String("key", key),
			Int32("current_refreshes", e.currentRefreshes.Load()))
		e.metrics.RecordHit(key, "refresh_in_progress")
		return
	}

	//----------------------------------------------------------------------
	// 2) Try to set refresh marker (may still have race condition, but Set has mutex)
	//----------------------------------------------------------------------
	if !e.refreshTracker.TrySet(key) {
		// Race condition: another goroutine set it between our Get and TrySet
		e.logger.Debug("skip refresh - lost race to set refresh flag",
			String("key", key))
		return
	}

	//----------------------------------------------------------------------
	// 3) Concurrency counting: increment first, then check if exceeds limit
	//----------------------------------------------------------------------
	newCount := e.currentRefreshes.Add(1)
	if int(newCount) > e.maxConcurrentRefreshes {
		e.currentRefreshes.Add(-1)
		e.refreshTracker.Delete(key) // Cancel refresh marker
		e.logger.Debug("skip refresh - max concurrent limit",
			String("key", key),
			Int32("current_refreshes", newCount-1),
			Int("max", e.maxConcurrentRefreshes))
		return
	}

	e.logger.Debug("launching background refresh",
		String("key", key),
		Int32("current_refreshes", newCount))

	//----------------------------------------------------------------------
	// 4) Officially launch background refresh goroutine
	//----------------------------------------------------------------------
	go func() {
		refreshStart := time.Now()

		// Unified cleanup logic
		defer func() {
			// Capture panic to prevent goroutine leaks
			if r := recover(); r != nil {
				e.logger.Error("panic in background refresh",
					String("key", key),
					Any("panic", r),
					Stack("stack"))
				e.metrics.RecordError(key, fmt.Errorf("panic in refresh: %v", r))
			}

			e.refreshTracker.Delete(key) // Clean up refresh marker
			e.currentRefreshes.Add(-1)   // Rollback concurrency count

			e.logger.Debug("background refresh completed",
				String("key", key),
				Duration("duration", time.Since(refreshStart)),
				Int32("remaining_refreshes", e.currentRefreshes.Load()))
		}()

		// ctx = shutdownCtx (global) + refreshTimeout (local timeout)
		ctx, cancel := context.WithTimeout(e.shutdownCtx, e.refreshTimeout)
		defer cancel()

		// Ensure only one actual upstream call via singleflight
		resCh := e.sfg.DoChan(key, func() (interface{}, error) {
			// Pass derived ctx to safeCall so it can be cancelled during shutdown
			return e.safeCall(ctx, cacheKey, fn)
		})

		select {
		case res := <-resCh:
			if res.Err != nil {
				e.logger.Debug("asynchronous refresh failed",
					String("key", key),
					Error(res.Err))
				e.metrics.RecordError(key, res.Err)
			} else {
				e.logger.Debug("asynchronous refresh succeeded",
					String("key", key))
				e.metrics.RecordHit(key, "refresh")
			}

		case <-ctx.Done():
			// Could be timeout or shutdown triggered cancellation
			e.logger.Warn("asynchronous refresh timeout/cancelled",
				String("key", key),
				Error(ctx.Err()))
			e.metrics.RecordError(key, ctx.Err())
		}
	}()
}

// Let Go 1.21+ stdlib provide the min function

// Shutdown gracefully shuts down the StaleEngine, cancelling all background refreshes
func (e *StaleEngine) Shutdown() {
	e.shutdownOnce.Do(func() {
		e.logger.Info("shutting down StaleEngine")
		e.shutdownCancel()

		// Wait a bit for in-flight refreshes to notice the cancellation
		timeout := time.NewTimer(5 * time.Second)
		defer timeout.Stop()

		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout.C:
				remaining := e.currentRefreshes.Load()
				if remaining > 0 {
					e.logger.Warn("shutdown timeout with refreshes still running",
						Int32("remaining", remaining))
				}
				// Stop the refresh tracker cleanup goroutine even on timeout
				e.refreshTracker.Stop()
				return
			case <-ticker.C:
				if e.currentRefreshes.Load() == 0 {
					e.logger.Info("all background refreshes completed")
					// Stop the refresh tracker cleanup goroutine
					e.refreshTracker.Stop()
					return
				}
			}
		}
	})
}
