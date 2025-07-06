package swre

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/dgraph-io/badger/v4/options"
)

// ---------- Configuration ----------

// BadgerConfig provides comprehensive configuration for Badger v4 storage backend.
// This configuration is optimized for high-throughput DNS caching workloads
// with careful tuning of memory usage, compression, and garbage collection.
//
// Performance Considerations:
// - Memory tables and caches sized for DNS response patterns
// - Compression disabled by default for latency optimization
// - Sync writes disabled for maximum throughput (relies on OS buffering)
// - Value log GC tuned for write-heavy workloads
type BadgerConfig struct {
	// Storage paths
	Dir      string // Directory for LSM tree and metadata
	ValueDir string // Directory for value log files (can be same as Dir)

	// Write behavior
	SyncWrites bool // Whether to sync writes to disk immediately (impacts performance)

	// Compression settings
	Compression        options.CompressionType // Compression algorithm (None, Snappy, ZSTD)
	ZSTDCompressionLvl int                     // ZSTD compression level (1-22, higher = better compression, slower)

	// Conflict detection (expensive, typically disabled for cache workloads)
	DetectConflicts bool

	// Memory and storage tuning
	ValueThreshold int   // Size threshold for storing values in value log vs LSM tree
	MemTableSize   int64 // Size of each memtable before flushing to disk
	IndexCacheSize int64 // Size of block cache for index blocks (0 = disabled)
	BlockCacheSize int64 // Size of block cache for data blocks (0 = disabled)
	MaxTableSize   int64 // Maximum size of each SST file
	NumCompactors  int   // Number of concurrent compaction goroutines

	// Value log configuration
	ValueLogFileSize int64 // Maximum size of each value log file

	// Garbage collection settings
	GCInterval     time.Duration // How often to run value log GC
	GCDiscardRatio float64       // Minimum ratio of discardable data to trigger GC

	// Logging (optional)
	Logger badger.Logger // Custom logger for Badger operations (can be nil)
}

// DefaultBadgerConfig returns a production-ready configuration optimized for DNS caching.
// These defaults are tuned for high-throughput, low-latency cache workloads with
// typical DNS response sizes (mostly under 4KB).
//
// Configuration Rationale:
// - No compression: Prioritizes latency over storage efficiency
// - Large memtables (256MB): Reduces write amplification for high write rates
// - No block caching: DNS workloads typically don't benefit from read caching
// - Aggressive GC: Prevents value log bloat in write-heavy scenarios
// - Compactor count = CPU cores: Utilizes available parallelism for compaction
func DefaultBadgerConfig(dir string) BadgerConfig {
	return BadgerConfig{
		// Use single directory for simplicity
		Dir:      dir,
		ValueDir: dir,

		// Optimize for throughput over durability
		SyncWrites: false, // Rely on OS buffering for performance

		// No compression for lowest latency
		Compression: options.None,

		// Disable expensive conflict detection
		DetectConflicts: false,

		// 4KB threshold: Most DNS responses go to value log
		ValueThreshold: 4 << 10, // 4KB

		// Large memtable reduces write amplification
		MemTableSize: 256 << 20, // 256MB

		// Moderate SST file size for balanced compaction
		MaxTableSize: 128 << 20, // 128MB

		// Disable caching for write-heavy cache workloads
		IndexCacheSize: 0, // Disabled
		BlockCacheSize: 0, // Disabled

		// Use all available CPU cores for compaction
		NumCompactors: runtime.GOMAXPROCS(0),

		// Large value log files reduce overhead
		ValueLogFileSize: 1 << 30, // 1GB

		// Frequent GC prevents value log bloat
		GCInterval:     10 * time.Minute,
		GCDiscardRatio: 0.8, // Aggressive cleanup
	}
}

// ---------- Implementation ----------

type badgerStorage struct {
	db             *badger.DB
	gcInterval     time.Duration
	gcDiscardRatio float64

	closeOnce sync.Once
	wg        sync.WaitGroup
	closed    atomic.Bool
	doneCh    chan struct{}
}

var _ Storage = (*badgerStorage)(nil)

// --------------------------------------------------------------------------
// Construction
// --------------------------------------------------------------------------

func NewBadgerStorage(ctx context.Context, cfg BadgerConfig) (Storage, error) {
	opts := badger.
		DefaultOptions(cfg.Dir).
		WithValueDir(cfg.ValueDir).
		WithSyncWrites(cfg.SyncWrites).
		WithCompression(cfg.Compression).
		WithZSTDCompressionLevel(cfg.ZSTDCompressionLvl).
		WithDetectConflicts(cfg.DetectConflicts).
		WithMemTableSize(cfg.MemTableSize).
		WithIndexCacheSize(cfg.IndexCacheSize).
		WithBlockCacheSize(cfg.BlockCacheSize).
		WithBaseTableSize(cfg.MaxTableSize).
		WithNumCompactors(cfg.NumCompactors).
		WithValueLogFileSize(cfg.ValueLogFileSize).
		WithValueThreshold(int64(cfg.ValueThreshold)).
		WithChecksumVerificationMode(options.NoVerification)

	if cfg.Logger != nil {
		opts = opts.WithLogger(cfg.Logger)
	}

	// Badger.Open is blocking; perform it in a goroutine so ctx can cancel it.
	type openResult struct {
		db  *badger.DB
		err error
	}
	resCh := make(chan openResult, 1)
	go func() {
		db, err := badger.Open(opts)
		resCh <- openResult{db: db, err: err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-resCh:
		if r.err != nil {
			return nil, r.err
		}
		st := &badgerStorage{
			db:             r.db,
			gcInterval:     cfg.GCInterval,
			gcDiscardRatio: cfg.GCDiscardRatio,
			doneCh:         make(chan struct{}),
		}
		// Kick off value-log GC.
		st.wg.Add(1)
		go st.runValueLogGC()
		return st, nil
	}
}

// --------------------------------------------------------------------------
// Storage interface
// --------------------------------------------------------------------------

// Size-tiered buffer pools for different payload sizes
// This reduces memory fragmentation and improves utilization
var (
	// Small buffer pool for typical DNS responses (< 4KB)
	smallBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 4<<10) // 4KB
			return &b
		},
	}

	// Medium buffer pool for larger responses (< 32KB)
	mediumBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 32<<10) // 32KB
			return &b
		},
	}

	// Large buffer pool (used rarely, but important for big payloads)
	largeBufPool = sync.Pool{
		New: func() any {
			b := make([]byte, 0, 128<<10) // 128KB
			return &b
		},
	}
)

// getBufferFromPool returns an appropriately sized buffer based on size hint
func getBufferFromPool(sizeHint int) (*[]byte, func()) {
	var bufPtr *[]byte

	switch {
	case sizeHint <= 4<<10:
		bufPtr = smallBufPool.Get().(*[]byte)
		*bufPtr = (*bufPtr)[:0] // reset but preserve capacity
		return bufPtr, func() { smallBufPool.Put(bufPtr) }

	case sizeHint <= 32<<10:
		bufPtr = mediumBufPool.Get().(*[]byte)
		*bufPtr = (*bufPtr)[:0]
		return bufPtr, func() { mediumBufPool.Put(bufPtr) }

	default:
		bufPtr = largeBufPool.Get().(*[]byte)
		*bufPtr = (*bufPtr)[:0]
		return bufPtr, func() { largeBufPool.Put(bufPtr) }
	}
}

// Get retrieves a cache entry by key with high-performance optimizations.
// This method implements several optimization strategies for high-throughput scenarios:
//
// Performance Optimizations:
// - Early context cancellation check
// - Size-tiered buffer pooling to reduce GC pressure
// - Single read transaction to minimize lock contention
// - Efficient JSON deserialization with jsoniter
//
// Memory Management:
// - Reuses buffers from size-appropriate pools
// - Zero-copy value access where possible
// - Automatic buffer return to prevent leaks
func (b *badgerStorage) Get(ctx context.Context, key string) (*CacheEntry, error) {
	// Fast-fail if context is already cancelled
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	var entry *CacheEntry

	// Use read-only transaction for optimal concurrency
	err := b.db.View(func(txn *badger.Txn) error {
		// Attempt to retrieve the item
		item, err := txn.Get([]byte(key))
		if err != nil {
			if errors.Is(err, badger.ErrKeyNotFound) {
				return ErrNotFound // Standardized cache miss error
			}
			return err
		}

		// Get value size to select appropriate buffer pool
		valueSize := item.ValueSize()

		// Obtain size-appropriate buffer from pool to minimize allocations
		bufPtr, releaseBuffer := getBufferFromPool(int(valueSize))
		defer releaseBuffer()

		// Copy value data into our pooled buffer
		err = item.Value(func(v []byte) error {
			*bufPtr = append(*bufPtr, v...) // Safe copy to avoid data races
			return nil
		})
		if err != nil {
			return err
		}

		// Deserialize JSON data using fast jsoniter
		var e CacheEntry
		if err := e.Unmarshal(*bufPtr); err != nil {
			return err
		}
		entry = &e
		return nil
	})

	return entry, err
}

// Set stores a cache entry with advanced optimizations for write performance.
// This method supports both individual writes and batch operations for flexibility.
//
// Performance Features:
// - Early context cancellation detection
// - Efficient JSON serialization with pooled buffers
// - Smart TTL calculation using cached timestamps
// - Optional batch write support for bulk operations
// - Fallback TTL for expired entries to prevent immediate eviction
//
// Batch Operations:
// - Detects WriteBatch in context for bulk writes
// - Falls back to individual transactions if no batch present
// - Maintains consistency across both operation modes
func (b *badgerStorage) Set(ctx context.Context, key string, value *CacheEntry) error {
	// Quick exit if context is already cancelled
	if err := ctx.Err(); err != nil {
		return err
	}

	// Serialize cache entry to JSON bytes
	data, err := value.Marshal()
	if err != nil {
		return err
	}

	// Calculate storage-level TTL from cache entry metadata
	var ttl time.Duration
	if value.ExpiresAfter > 0 {
		// Use cached time for consistency and performance
		now := NowUnixMilli()
		if value.ExpiresAfter > now {
			// Entry is still valid, calculate remaining TTL
			ttl = time.Duration(value.ExpiresAfter-now) * time.Millisecond
		} else {
			// Entry already expired, but keep it briefly for fallback scenarios
			ttl = 30 * time.Second
		}
	}

	// Fast path: use batch operations if available in context
	if wb, ok := ctx.Value(batchCtxKey{}).(*badger.WriteBatch); ok && wb != nil {
		e := badger.NewEntry([]byte(key), data)
		if ttl > 0 {
			e.WithTTL(ttl)
		}
		return wb.SetEntry(e)
	}

	// Standard path: individual write transaction
	return b.db.Update(func(txn *badger.Txn) error {
		e := badger.NewEntry([]byte(key), data)
		if ttl > 0 {
			e.WithTTL(ttl)
		}
		return txn.SetEntry(e)
	})
}

func (b *badgerStorage) Delete(ctx context.Context, key string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return b.db.Update(func(txn *badger.Txn) error {
		return txn.Delete([]byte(key))
	})
}

func (b *badgerStorage) Close() error {
	var err error
	b.closeOnce.Do(func() {
		b.closed.Store(true)
		// Signal GC loop to stop
		close(b.doneCh)
		// Wait for GC loop to finish
		b.wg.Wait()
		err = b.db.Close()
	})
	return err
}

// --------------------------------------------------------------------------
// Helpers
// --------------------------------------------------------------------------

// runValueLogGC runs garbage collection on the Badger value log periodically
// with adaptive timing to optimize resource usage
func (b *badgerStorage) runValueLogGC() {
	defer b.wg.Done()

	ticker := time.NewTicker(b.gcInterval)
	defer ticker.Stop()

	// Memory pressure tracking to adjust GC pressure
	var (
		consecutiveSuccesses int
		consecutiveFailures  int
		backoffDelay         time.Duration
	)

	for {
		select {
		case <-ticker.C:
			// Try to run GC until it reports nothing to rewrite or encounters persistent errors
			successInRound := false
			gcAttempts := 0

			// Adaptive number of GC attempts based on success rate
			maxAttempts := 3
			if consecutiveSuccesses > 5 {
				maxAttempts = 1 // If we've been successful many times, do less work
			}

			for gcAttempts < maxAttempts {
				if err := b.db.RunValueLogGC(b.gcDiscardRatio); err != nil {
					if err == badger.ErrNoRewrite {
						// Nothing to GC - we're done for this round
						if gcAttempts > 0 {
							// We did some work successfully
							consecutiveSuccesses++
							consecutiveFailures = 0
						}
						break
					}

					// Error running GC - might be transient
					consecutiveFailures++
					consecutiveSuccesses = 0

					// If we keep failing, back off
					if consecutiveFailures > 3 {
						backoffDelay = time.Second * time.Duration(consecutiveFailures)
						if backoffDelay > 30*time.Second {
							backoffDelay = 30 * time.Second
						}
						time.Sleep(backoffDelay)
					}

					break
				}

				// Successful GC
				successInRound = true
				gcAttempts++

				// Small sleep between GC operations to reduce resource contention
				time.Sleep(100 * time.Millisecond)
			}

			if successInRound {
				consecutiveSuccesses++
				consecutiveFailures = 0
			}

		case <-b.doneCh:
			return
		}
	}
}

// ---- Optional batching API (100 % backward-compatible) ----

// Put a *badger.WriteBatch into ctx to enable extremely fast bulk writes.
type batchCtxKey struct{}

func WithWriteBatch(ctx context.Context, wb *badger.WriteBatch) context.Context {
	return context.WithValue(ctx, batchCtxKey{}, wb)
}
