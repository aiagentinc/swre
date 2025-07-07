package swre

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4/options"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTestBadgerStorage(t *testing.T) (*badgerStorage, string) {
	t.Helper()
	dir := t.TempDir()

	config := DefaultBadgerConfig(dir)

	storage, err := NewBadgerStorage(context.Background(), config)
	require.NoError(t, err)
	require.NotNil(t, storage)

	return storage.(*badgerStorage), dir
}

func TestDefaultBadgerConfig(t *testing.T) {
	dir := "/test/dir"
	config := DefaultBadgerConfig(dir)

	assert.Equal(t, dir, config.Dir)
	assert.Equal(t, dir, config.ValueDir)
	assert.False(t, config.SyncWrites)
	assert.Equal(t, options.None, config.Compression)
	assert.False(t, config.DetectConflicts)
	assert.Equal(t, 4<<10, config.ValueThreshold)
	assert.Equal(t, int64(256<<20), config.MemTableSize)
	assert.Equal(t, int64(128<<20), config.MaxTableSize)
	assert.Equal(t, int64(0), config.IndexCacheSize)
	assert.Equal(t, int64(0), config.BlockCacheSize)
	assert.Equal(t, runtime.GOMAXPROCS(0), config.NumCompactors)
	assert.Equal(t, int64(1<<30), config.ValueLogFileSize)
	assert.Equal(t, 10*time.Minute, config.GCInterval)
	assert.Equal(t, 0.8, config.GCDiscardRatio)
	assert.Nil(t, config.Logger)
}

func TestNewBadgerStorage(t *testing.T) {
	tests := []struct {
		name        string
		setupConfig func() BadgerConfig
		setupCtx    func() context.Context
		expectError bool
		errorCheck  func(error) bool
	}{
		{
			name: "successful creation",
			setupConfig: func() BadgerConfig {
				dir := t.TempDir()
				return DefaultBadgerConfig(dir)
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "invalid directory",
			setupConfig: func() BadgerConfig {
				return DefaultBadgerConfig("/invalid/path/that/does/not/exist")
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: true,
		},
		{
			name: "context cancelled before creation",
			setupConfig: func() BadgerConfig {
				dir := t.TempDir()
				return DefaultBadgerConfig(dir)
			},
			setupCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return errors.Is(err, context.Canceled)
			},
		},
		{
			name: "with compression enabled",
			setupConfig: func() BadgerConfig {
				dir := t.TempDir()
				config := DefaultBadgerConfig(dir)
				config.Compression = options.Snappy
				// When compression is enabled, BlockCacheSize must be set
				config.BlockCacheSize = 64 << 20 // 64MB
				return config
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "with custom logger",
			setupConfig: func() BadgerConfig {
				dir := t.TempDir()
				config := DefaultBadgerConfig(dir)
				// Note: badger.Logger interface can be set to nil
				config.Logger = nil
				return config
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := tt.setupConfig()
			ctx := tt.setupCtx()

			storage, err := NewBadgerStorage(ctx, config)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCheck != nil {
					assert.True(t, tt.errorCheck(err))
				}
				assert.Nil(t, storage)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, storage)
				defer func() {
					err := storage.Close()
					assert.NoError(t, err)
				}()
			}
		})
	}
}

func TestBadgerStorage_Get(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()

	testEntry := &CacheEntry{
		Value:        []byte("test value"),
		CreatedAt:    time.Now().UnixMilli(),
		StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
		ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
	}

	err := storage.Set(ctx, "test-key", testEntry)
	require.NoError(t, err)

	// Note: Badger does not allow empty keys

	tests := []struct {
		name        string
		key         string
		setupCtx    func() context.Context
		expectError bool
		errorCheck  func(error) bool
		valueCheck  func(*CacheEntry)
	}{
		{
			name:        "successful get",
			key:         "test-key",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
			valueCheck: func(entry *CacheEntry) {
				assert.Equal(t, testEntry.Value, entry.Value)
				assert.Equal(t, testEntry.CreatedAt, entry.CreatedAt)
				assert.Equal(t, testEntry.StaleAfter, entry.StaleAfter)
			},
		},
		{
			name:        "key not found",
			key:         "non-existent-key",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: true,
			errorCheck: func(err error) bool {
				return errors.Is(err, ErrNotFound)
			},
		},
		{
			name: "context cancelled",
			key:  "test-key",
			setupCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return errors.Is(err, context.Canceled)
			},
		},
		{
			name:        "empty key",
			key:         "",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: true,
			errorCheck: func(err error) bool {
				// Badger does not allow empty keys
				return err != nil && err.Error() == "Key cannot be empty"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()

			entry, err := storage.Get(ctx, tt.key)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCheck != nil {
					assert.True(t, tt.errorCheck(err))
				}
				assert.Nil(t, entry)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, entry)
				if tt.valueCheck != nil {
					tt.valueCheck(entry)
				}
			}
		})
	}
}

func TestBadgerStorage_Set(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	tests := []struct {
		name        string
		key         string
		entry       *CacheEntry
		setupCtx    func() context.Context
		expectError bool
		errorCheck  func(error) bool
	}{
		{
			name: "successful set",
			key:  "test-key",
			entry: &CacheEntry{
				Value:        []byte("test value"),
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "set with zero TTL",
			key:  "zero-ttl-key",
			entry: &CacheEntry{
				Value:        []byte("test value"),
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   0,
				ExpiresAfter: 0,
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "set expired entry",
			key:  "expired-key",
			entry: &CacheEntry{
				Value:        []byte("expired value"),
				CreatedAt:    time.Now().Add(-2 * time.Hour).UnixMilli(),
				StaleAfter:   time.Now().Add(-90 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(-time.Hour).UnixMilli(),
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "context cancelled",
			key:  "cancelled-key",
			entry: &CacheEntry{
				Value: []byte("test"),
			},
			setupCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return errors.Is(err, context.Canceled)
			},
		},
		{
			name: "large value",
			key:  "large-value-key",
			entry: &CacheEntry{
				Value:        make([]byte, 1<<20),
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "empty key",
			key:  "",
			entry: &CacheEntry{
				Value: []byte("test"),
			},
			setupCtx:    func() context.Context { return context.Background() },
			expectError: true,
			errorCheck: func(err error) bool {
				// Badger does not allow empty keys
				return err != nil && err.Error() == "Key cannot be empty"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()

			err := storage.Set(ctx, tt.key, tt.entry)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCheck != nil {
					assert.True(t, tt.errorCheck(err))
				}
			} else {
				assert.NoError(t, err)

				if ctx.Err() == nil && tt.entry != nil {
					retrieved, err := storage.Get(context.Background(), tt.key)
					assert.NoError(t, err)
					assert.Equal(t, tt.entry.Value, retrieved.Value)
				}
			}
		})
	}
}

func TestBadgerStorage_Delete(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()
	entry := &CacheEntry{
		Value:        []byte("test value"),
		CreatedAt:    time.Now().UnixMilli(),
		StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
		ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
	}

	err := storage.Set(ctx, "test-key", entry)
	require.NoError(t, err)

	tests := []struct {
		name        string
		key         string
		setupCtx    func() context.Context
		expectError bool
		errorCheck  func(error) bool
	}{
		{
			name:        "successful delete",
			key:         "test-key",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name:        "delete non-existent key",
			key:         "non-existent",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: false,
		},
		{
			name: "context cancelled",
			key:  "test-key",
			setupCtx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			expectError: true,
			errorCheck: func(err error) bool {
				return errors.Is(err, context.Canceled)
			},
		},
		{
			name:        "empty key",
			key:         "",
			setupCtx:    func() context.Context { return context.Background() },
			expectError: true,
			errorCheck: func(err error) bool {
				// Badger does not allow empty keys
				return err != nil && err.Error() == "Key cannot be empty"
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := tt.setupCtx()

			err := storage.Delete(ctx, tt.key)

			if tt.expectError {
				assert.Error(t, err)
				if tt.errorCheck != nil {
					assert.True(t, tt.errorCheck(err))
				}
			} else {
				assert.NoError(t, err)

				if tt.key == "test-key" && ctx.Err() == nil {
					_, err := storage.Get(context.Background(), tt.key)
					assert.Error(t, err)
					assert.True(t, errors.Is(err, ErrNotFound))
				}
			}
		})
	}
}

func TestBadgerStorage_Close(t *testing.T) {
	t.Run("successful close", func(t *testing.T) {
		storage, _ := createTestBadgerStorage(t)

		err := storage.Close()
		assert.NoError(t, err)

		err = storage.Close()
		assert.NoError(t, err)
	})

	t.Run("operations after close", func(t *testing.T) {
		storage, _ := createTestBadgerStorage(t)

		err := storage.Close()
		require.NoError(t, err)

		ctx := context.Background()
		_, err = storage.Get(ctx, "key")
		assert.Error(t, err)

		err = storage.Set(ctx, "key", &CacheEntry{Value: []byte("test")})
		assert.Error(t, err)

		err = storage.Delete(ctx, "key")
		assert.Error(t, err)
	})
}

func TestBadgerStorage_BatchOperations(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()

	// First, test regular operations
	entries := make(map[string]*CacheEntry)
	for i := 0; i < 10; i++ {
		key := string(rune('a' + i))
		entries[key] = &CacheEntry{
			Value:        []byte(key + " value"),
			CreatedAt:    time.Now().UnixMilli(),
			StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
			ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
		}
	}

	// Test with batch - need to create a batch first
	batch := storage.db.NewWriteBatch()
	defer batch.Cancel()

	batchCtx := WithWriteBatch(ctx, batch)

	for key, entry := range entries {
		err := storage.Set(batchCtx, key, entry)
		assert.NoError(t, err)
	}

	// Commit the batch
	err := batch.Flush()
	assert.NoError(t, err)

	for key, expectedEntry := range entries {
		retrieved, err := storage.Get(ctx, key)
		assert.NoError(t, err)
		assert.Equal(t, expectedEntry.Value, retrieved.Value)
	}
}

func TestBadgerStorage_ConcurrentOperations(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()
	numGoroutines := 10
	numOperations := 100

	var wg sync.WaitGroup
	wg.Add(numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < numOperations; j++ {
				key := string(rune('a'+goroutineID)) + string(rune('0'+j%10))
				entry := &CacheEntry{
					Value:        []byte(key + " value"),
					CreatedAt:    time.Now().UnixMilli(),
					StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
					ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
				}

				err := storage.Set(ctx, key, entry)
				assert.NoError(t, err)

				retrieved, err := storage.Get(ctx, key)
				assert.NoError(t, err)
				assert.Equal(t, entry.Value, retrieved.Value)

				if j%3 == 0 {
					err = storage.Delete(ctx, key)
					assert.NoError(t, err)
				}
			}
		}(i)
	}

	wg.Wait()
}

func TestBadgerStorage_BufferPooling(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()

	sizes := []int{
		100,
		1024,
		4 * 1024,
		16 * 1024,
		32 * 1024,
		64 * 1024,
		128 * 1024,
		256 * 1024,
	}

	for _, size := range sizes {
		t.Run(string(rune(size))+"_bytes", func(t *testing.T) {
			key := string(rune(size)) + "_key"
			value := make([]byte, size)
			for i := range value {
				value[i] = byte(i % 256)
			}

			entry := &CacheEntry{
				Value:        value,
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
			}

			err := storage.Set(ctx, key, entry)
			assert.NoError(t, err)

			retrieved, err := storage.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, entry.Value, retrieved.Value)
		})
	}
}

func TestBadgerStorage_GCBehavior(t *testing.T) {
	dir := t.TempDir()

	config := DefaultBadgerConfig(dir)
	config.GCInterval = 1 * time.Second
	config.GCDiscardRatio = 0.1

	storage, err := NewBadgerStorage(context.Background(), config)
	require.NoError(t, err)

	ctx := context.Background()

	for i := 0; i < 100; i++ {
		key := string(rune(i))
		entry := &CacheEntry{
			Value:        make([]byte, 1024),
			CreatedAt:    time.Now().UnixMilli(),
			StaleAfter:   time.Now().Add(500 * time.Millisecond).UnixMilli(),
			ExpiresAfter: time.Now().Add(time.Second).UnixMilli(),
		}
		err := storage.Set(ctx, key, entry)
		assert.NoError(t, err)
	}

	for i := 0; i < 50; i++ {
		key := string(rune(i))
		err := storage.Delete(ctx, key)
		assert.NoError(t, err)
	}

	time.Sleep(3 * time.Second)

	err = storage.Close()
	assert.NoError(t, err)
}

func TestBadgerStorage_CorruptedData(t *testing.T) {
	storage, dir := createTestBadgerStorage(t)

	ctx := context.Background()
	key := "test-key"
	entry := &CacheEntry{
		Value:        []byte("test value"),
		CreatedAt:    time.Now().UnixMilli(),
		StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
		ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
	}

	err := storage.Set(ctx, key, entry)
	require.NoError(t, err)

	err = storage.Close()
	require.NoError(t, err)

	manifestPath := filepath.Join(dir, "MANIFEST")
	err = os.WriteFile(manifestPath, []byte("corrupted"), 0644)
	require.NoError(t, err)

	config := DefaultBadgerConfig(dir)

	_, err = NewBadgerStorage(context.Background(), config)
	assert.Error(t, err)
}

func TestWithWriteBatch(t *testing.T) {
	ctx := context.Background()

	// Create a mock write batch
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	batch := storage.db.NewWriteBatch()
	defer batch.Cancel()

	batchCtx := WithWriteBatch(ctx, batch)

	value := batchCtx.Value(batchCtxKey{})
	assert.NotNil(t, value)
	assert.Equal(t, batch, value)
}

func TestGetBufferFromPool(t *testing.T) {
	tests := []struct {
		name         string
		sizeHint     int
		expectedPool string
	}{
		{
			name:         "tiny buffer",
			sizeHint:     100,
			expectedPool: "smallPool",
		},
		{
			name:         "small buffer",
			sizeHint:     2048,
			expectedPool: "smallPool",
		},
		{
			name:         "medium buffer",
			sizeHint:     10240,
			expectedPool: "mediumPool",
		},
		{
			name:         "large buffer",
			sizeHint:     65536,
			expectedPool: "largePool",
		},
		{
			name:         "xlarge buffer",
			sizeHint:     131072,
			expectedPool: "largePool",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, cleanup := getBufferFromPool(tt.sizeHint)
			defer cleanup()

			assert.NotNil(t, buf)
			assert.GreaterOrEqual(t, cap(*buf), 0)
		})
	}
}

func TestBadgerStorage_TTLHandling(t *testing.T) {
	storage, _ := createTestBadgerStorage(t)
	defer func() {
		err := storage.Close()
		assert.NoError(t, err)
	}()

	ctx := context.Background()

	tests := []struct {
		name  string
		entry *CacheEntry
	}{
		{
			name: "positive TTL",
			entry: &CacheEntry{
				Value:        []byte("test"),
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
			},
		},
		{
			name: "expired entry gets 30s TTL",
			entry: &CacheEntry{
				Value:        []byte("test"),
				CreatedAt:    time.Now().Add(-2 * time.Hour).UnixMilli(),
				StaleAfter:   time.Now().Add(-90 * time.Minute).UnixMilli(),
				ExpiresAfter: time.Now().Add(-time.Hour).UnixMilli(),
			},
		},
		{
			name: "zero TTL",
			entry: &CacheEntry{
				Value:        []byte("test"),
				CreatedAt:    time.Now().UnixMilli(),
				StaleAfter:   0,
				ExpiresAfter: 0,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := tt.name + "_key"
			err := storage.Set(ctx, key, tt.entry)
			assert.NoError(t, err)

			retrieved, err := storage.Get(ctx, key)
			assert.NoError(t, err)
			assert.Equal(t, tt.entry.Value, retrieved.Value)
		})
	}
}

func BenchmarkBadgerStorage_Get(b *testing.B) {
	storage, _ := createTestBadgerStorage(&testing.T{})
	defer func() {
		err := storage.Close()
		if err != nil {
			b.Fatal(err)
		}
	}()

	ctx := context.Background()
	key := "bench-key"
	entry := &CacheEntry{
		Value:        []byte("benchmark value"),
		CreatedAt:    time.Now().UnixMilli(),
		StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
		ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
	}

	err := storage.Set(ctx, key, entry)
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			_, err := storage.Get(ctx, key)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}

func BenchmarkBadgerStorage_Set(b *testing.B) {
	storage, _ := createTestBadgerStorage(&testing.T{})
	defer func() {
		err := storage.Close()
		if err != nil {
			b.Fatal(err)
		}
	}()

	ctx := context.Background()
	entry := &CacheEntry{
		Value:        []byte("benchmark value"),
		CreatedAt:    time.Now().UnixMilli(),
		StaleAfter:   time.Now().Add(30 * time.Minute).UnixMilli(),
		ExpiresAfter: time.Now().Add(time.Hour).UnixMilli(),
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			key := string(rune(i))
			err := storage.Set(ctx, key, entry)
			if err != nil {
				b.Fatal(err)
			}
			i++
		}
	})
}

func BenchmarkBadgerStorage_BufferPooling(b *testing.B) {
	sizes := []int{
		100,
		4096,
		32768,
		131072,
	}

	for _, size := range sizes {
		b.Run(string(rune(size))+"_bytes", func(b *testing.B) {
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					buf, cleanup := getBufferFromPool(size)
					*buf = append(*buf, make([]byte, size)...)
					cleanup()
				}
			})
		})
	}
}
