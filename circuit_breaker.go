package swre

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// CircuitState represents the state of a circuit breaker
type CircuitState int32

const (
	CircuitClosed CircuitState = iota
	CircuitOpen
	CircuitHalfOpen
)

var (
	ErrCircuitOpen = errors.New("circuit breaker is open")
)

// CircuitBreaker wraps StaleEngine with circuit breaker pattern
type CircuitBreaker struct {
	engine *StaleEngine

	// Circuit breaker configuration
	failureThreshold int32
	successThreshold int32
	timeout          time.Duration

	// State tracking
	state            atomic.Int32
	failures         atomic.Int32
	successes        atomic.Int32
	lastFailureTime  atomic.Int64
	halfOpenRequests atomic.Int32
	maxHalfOpenReqs  int32

	// Per-key circuit breakers with LRU eviction
	keyBreakers     sync.Map // map[string]*keyCircuitBreaker
	keyBreakerMutex sync.Mutex
	keyBreakerLRU   *lru.Cache[string, *keyCircuitBreaker] // Thread-safe LRU cache
	maxKeyBreakers  int                                    // Maximum number of key breakers to track
}

type keyCircuitBreaker struct {
	key             string
	failures        atomic.Int32
	lastFailureTime atomic.Int64
}

// CircuitBreakerConfig holds circuit breaker configuration
type CircuitBreakerConfig struct {
	Engine           *StaleEngine
	FailureThreshold int32         // Failures before opening circuit
	SuccessThreshold int32         // Successes in half-open before closing
	Timeout          time.Duration // Time before trying half-open
	MaxHalfOpenReqs  int32         // Max concurrent requests in half-open state
	MaxKeyBreakers   int           // Maximum number of per-key circuit breakers (default: 10000)
}

// NewCircuitBreaker creates a new circuit breaker wrapper
func NewCircuitBreaker(cfg CircuitBreakerConfig) *CircuitBreaker {
	if cfg.FailureThreshold == 0 {
		cfg.FailureThreshold = 5
	}
	if cfg.SuccessThreshold == 0 {
		cfg.SuccessThreshold = 2
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxHalfOpenReqs == 0 {
		cfg.MaxHalfOpenReqs = 3
	}
	if cfg.MaxKeyBreakers == 0 {
		cfg.MaxKeyBreakers = 10000 // Default to 10k keys
	}

	// Create LRU cache for key breakers
	lruCache, _ := lru.New[string, *keyCircuitBreaker](cfg.MaxKeyBreakers)

	return &CircuitBreaker{
		engine:           cfg.Engine,
		failureThreshold: cfg.FailureThreshold,
		successThreshold: cfg.SuccessThreshold,
		timeout:          cfg.Timeout,
		maxHalfOpenReqs:  cfg.MaxHalfOpenReqs,
		maxKeyBreakers:   cfg.MaxKeyBreakers,
		keyBreakerLRU:    lruCache,
	}
}

// Execute wraps StaleEngine.Execute with circuit breaker logic
func (cb *CircuitBreaker) Execute(ctx context.Context, key string, fn StaleEngineCallback) (*CacheEntry, error) {
	// Check global circuit state
	if !cb.canExecute() {
		return nil, ErrCircuitOpen
	}

	// Check per-key circuit breaker
	if !cb.canExecuteKey(key) {
		return nil, ErrCircuitOpen
	}

	// Track half-open requests
	if cb.getState() == CircuitHalfOpen {
		current := cb.halfOpenRequests.Add(1)
		if current > cb.maxHalfOpenReqs {
			cb.halfOpenRequests.Add(-1)
			return nil, ErrCircuitOpen
		}
		defer cb.halfOpenRequests.Add(-1)
	}

	// Execute with circuit breaker protection
	entry, err := cb.engine.Execute(ctx, key, fn)

	if err != nil {
		cb.recordFailure(key)
	} else {
		cb.recordSuccess(key)
	}

	return entry, err
}

// ExecuteGeneric wraps StaleEngine.ExecuteGeneric with circuit breaker logic
func (cb *CircuitBreaker) ExecuteGeneric(ctx context.Context, key string, result interface{}, fn StaleEngineCallback) error {
	entry, err := cb.Execute(ctx, key, fn)
	if err != nil {
		return err
	}

	return cb.engine.serializer.Unmarshal(entry.Value, result)
}

func (cb *CircuitBreaker) getState() CircuitState {
	return CircuitState(cb.state.Load())
}

func (cb *CircuitBreaker) setState(state CircuitState) {
	cb.state.Store(int32(state))
}

func (cb *CircuitBreaker) canExecute() bool {
	state := cb.getState()

	switch state {
	case CircuitClosed:
		return true

	case CircuitOpen:
		// Check if timeout has passed
		lastFailure := cb.lastFailureTime.Load()
		if time.Since(time.Unix(0, lastFailure)) > cb.timeout {
			cb.setState(CircuitHalfOpen)
			cb.successes.Store(0)
			return true
		}
		return false

	case CircuitHalfOpen:
		return true

	default:
		return false
	}
}

func (cb *CircuitBreaker) canExecuteKey(key string) bool {
	val, ok := cb.keyBreakers.Load(key)
	if !ok {
		return true
	}

	kb := val.(*keyCircuitBreaker)
	failures := kb.failures.Load()

	if failures >= cb.failureThreshold {
		lastFailure := kb.lastFailureTime.Load()
		if time.Since(time.Unix(0, lastFailure)) > cb.timeout {
			// Reset the key breaker
			kb.failures.Store(0)
			return true
		}
		return false
	}

	return true
}

func (cb *CircuitBreaker) recordFailure(key string) {
	// Update global state
	failures := cb.failures.Add(1)
	cb.lastFailureTime.Store(time.Now().UnixNano())

	state := cb.getState()
	if state == CircuitClosed && failures >= cb.failureThreshold {
		cb.setState(CircuitOpen)
	} else if state == CircuitHalfOpen {
		cb.setState(CircuitOpen)
		cb.failures.Store(0)
	}

	// Update per-key state with LRU eviction
	cb.recordKeyFailure(key)
}

func (cb *CircuitBreaker) recordSuccess(key string) {
	// Reset per-key breaker on success
	cb.resetKeyBreaker(key)

	// Update global state
	state := cb.getState()
	switch state {
	case CircuitHalfOpen:
		successes := cb.successes.Add(1)
		if successes >= cb.successThreshold {
			cb.setState(CircuitClosed)
			cb.failures.Store(0)
			cb.successes.Store(0)
		}
	case CircuitClosed:
		// Reset failure count on success in closed state
		cb.failures.Store(0)
	}
}

// GetState returns the current circuit breaker state
func (cb *CircuitBreaker) GetState() string {
	switch cb.getState() {
	case CircuitClosed:
		return "closed"
	case CircuitOpen:
		return "open"
	case CircuitHalfOpen:
		return "half-open"
	default:
		return "unknown"
	}
}

// Reset resets the circuit breaker to closed state
func (cb *CircuitBreaker) Reset() {
	cb.setState(CircuitClosed)
	cb.failures.Store(0)
	cb.successes.Store(0)
	cb.halfOpenRequests.Store(0)

	// Clear all key breakers
	cb.keyBreakerMutex.Lock()
	cb.keyBreakers.Range(func(key, value interface{}) bool {
		cb.keyBreakers.Delete(key)
		return true
	})
	// Clear the LRU cache
	cb.keyBreakerLRU.Purge()
	cb.keyBreakerMutex.Unlock()
}

// recordKeyFailure updates per-key failure state with LRU eviction
func (cb *CircuitBreaker) recordKeyFailure(key string) {
	cb.keyBreakerMutex.Lock()
	defer cb.keyBreakerMutex.Unlock()

	// Check if key already exists in LRU
	if kb, exists := cb.keyBreakerLRU.Get(key); exists {
		// Update failure count
		kb.failures.Add(1)
		kb.lastFailureTime.Store(time.Now().UnixNano())
		return
	}

	// Create new key breaker
	kb := &keyCircuitBreaker{
		key: key,
	}
	kb.failures.Store(1)
	kb.lastFailureTime.Store(time.Now().UnixNano())

	// Add to LRU (automatically handles eviction)
	cb.keyBreakerLRU.Add(key, kb)
	cb.keyBreakers.Store(key, kb)
}

// resetKeyBreaker resets the failure count for a key
func (cb *CircuitBreaker) resetKeyBreaker(key string) {
	if val, ok := cb.keyBreakers.Load(key); ok {
		kb := val.(*keyCircuitBreaker)
		kb.failures.Store(0)

		// Update LRU access time
		cb.keyBreakerMutex.Lock()
		if _, exists := cb.keyBreakerLRU.Get(key); exists {
			_ = exists
			// Get operation already moves to front
		}
		cb.keyBreakerMutex.Unlock()
	}
}
