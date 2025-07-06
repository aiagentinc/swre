# SWR Cache

[![CI](https://github.com/aiagentinc/swre/actions/workflows/test.yml/badge.svg)](https://github.com/aiagentinc/swre/actions/workflows/test.yml)
[![codecov](https://codecov.io/gh/aiagentinc/swre/graph/badge.svg)](https://codecov.io/gh/aiagentinc/swre)
[![Go Reference](https://pkg.go.dev/badge/github.com/aiagentinc/swre.svg)](https://pkg.go.dev/github.com/aiagentinc/swre)
[![Go Report Card](https://goreportcard.com/badge/github.com/aiagentinc/swre)](https://goreportcard.com/report/github.com/aiagentinc/swre)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A high-performance, production-ready **Stale-While-Revalidate (SWR)** cache implementation in Go. Perfect for applications that need intelligent caching with background refresh capabilities.

## 🚀 Why SWR Cache?

**Stale-While-Revalidate** is a powerful caching strategy that:
- **Serves stale content instantly** while refreshing in the background
- **Eliminates cache stampedes** through intelligent request deduplication  
- **Provides predictable performance** with sub-millisecond cache hits
- **Handles failures gracefully** with expired content fallbacks

Perfect for APIs, microservices, and any application where **low latency** and **high availability** matter.

## ✨ Features

- 🏃‍♂️ **Ultra-Fast**: Lock-free hot path with only 2 atomic reads
- 🔄 **True SWR Pattern**: Background refresh while serving stale content
- 🛡️ **Circuit Breaker**: Built-in protection against cascading failures
- 📊 **Pluggable Metrics**: Monitor cache performance and hit rates
- 🔧 **Flexible Serialization**: JSON, Gob, custom serializers, with compression
- ⚡ **High Concurrency**: Optimized for millions of concurrent requests
- 💾 **Multiple Storage Backends**: Badger (embedded), Redis, or custom
- 🎯 **Dynamic TTL**: Configure cache lifetime based on content or patterns
- 🧠 **Memory Efficient**: Smart buffer pooling and optimized allocations

## 📦 Installation

```bash
go get github.com/aiagentinc/swre
```

## 🚀 Quick Start

### Basic Usage

```go
package main

import (
    "context"
    "fmt"
    "log"
    "time"
    
    "github.com/aiagentinc/swre"
    "go.uber.org/zap"
)

func main() {
    // Create logger (you can use any logger that implements swre.Logger)
    zapLogger, _ := zap.NewProduction()
    logger, _ := swre.NewZapAdapter(zapLogger)
    
    // Create storage backend
    storage, err := swre.NewBadgerStorage(context.Background(), 
        swre.DefaultBadgerConfig("/tmp/cache"))
    if err != nil {
        log.Fatal(err)
    }
    defer storage.Close()
    
    // Create SWR engine
    engine, err := swre.NewStaleEngine(storage, logger)
    if err != nil {
        log.Fatal(err)
    }
    defer engine.Shutdown()
    
    // Use the cache
    ctx := context.Background()
    entry, err := engine.Execute(ctx, "user:123", func() (interface{}, error) {
        // Simulate expensive operation (database call, API request, etc.)
        time.Sleep(100 * time.Millisecond)
        return map[string]interface{}{
            "id":   123,
            "name": "John Doe",
            "email": "john@example.com",
        }, nil
    })
    
    if err != nil {
        log.Fatal(err)
    }
    
    fmt.Printf("Cache Status: %s\n", entry.Status)
    fmt.Printf("Data: %s\n", string(entry.Value))
}
```

### Type-Safe Usage

```go
type User struct {
    ID    int    `json:"id"`
    Name  string `json:"name"`
    Email string `json:"email"`
}

var user User
err := engine.ExecuteGeneric(ctx, "user:123", &user, func() (interface{}, error) {
    return fetchUserFromDatabase(123)
})

if err != nil {
    log.Printf("Cache error: %v", err)
    return
}

fmt.Printf("User: %+v\n", user)
```

## 🔧 Advanced Configuration

### Custom TTL Configuration

```go
// Create logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Configure cache behavior with fine-grained TTL control
engine, err := swre.NewStaleEngineWithOptions(storage, logger,
    swre.WithCacheTTL(&swre.CacheTTL{
        FreshSeconds:   300,  // 5 minutes fresh
        StaleSeconds:   3600, // 1 hour stale (background refresh)
        ExpiredSeconds: 3600, // 1 hour expired (fallback)
    }),
    swre.WithMaxConcurrentRefreshes(100),
    swre.WithRefreshTimeout(10*time.Second),
)
```

### Dynamic TTL Based on Content

```go
// Create logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Customize TTL based on data characteristics
ttlCalc := &swre.DynamicTTLCalculator{
    Calculator: func(key string, value interface{}) (ttl, staleTTL time.Duration, err error) {
        switch v := value.(type) {
        case User:
            if v.IsActive {
                return 1 * time.Hour, 50 * time.Minute, nil
            }
            return 24 * time.Hour, 23 * time.Hour, nil
        case APIResponse:
            // Cache API responses based on their type
            if v.StatusCode == 200 {
                return 30 * time.Minute, 25 * time.Minute, nil
            }
            return 5 * time.Minute, 4 * time.Minute, nil
        default:
            return 15 * time.Minute, 10 * time.Minute, nil
        }
    },
}

engine, err := swre.NewStaleEngineWithOptions(storage, logger,
    swre.WithTTLCalculator(ttlCalc),
)
```

### Circuit Breaker Protection

```go
// Add fault tolerance with circuit breaker
cb := swre.NewCircuitBreaker(swre.CircuitBreakerConfig{
    Engine:           engine,
    FailureThreshold: 5,  // Open after 5 failures
    SuccessThreshold: 2,  // Close after 2 successes
    Timeout:          30 * time.Second,
    MaxHalfOpenReqs:  3,  // Allow 3 test requests in half-open state
})

// Use circuit breaker instead of engine directly
entry, err := cb.Execute(ctx, "api:weather", func() (interface{}, error) {
    return callExternalAPI()
})

if errors.Is(err, swre.ErrCircuitOpen) {
    // Circuit is open, use fallback data
    return getFallbackWeatherData()
}
```

### Compression for Large Payloads

```go
// Create logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Compress large responses to save memory and storage
compressedSerializer := swre.NewCompressedSerializer(&swre.JSONSerializer{})

engine, err := swre.NewStaleEngineWithOptions(storage, logger,
    swre.WithSerializer(compressedSerializer),
)
```

### Custom Serialization

```go
// Use Protocol Buffers or other serialization formats
type ProtobufSerializer struct{}

func (p *ProtobufSerializer) Marshal(v interface{}) ([]byte, error) {
    if pb, ok := v.(proto.Message); ok {
        return proto.Marshal(pb)
    }
    return nil, fmt.Errorf("value is not a protobuf message")
}

func (p *ProtobufSerializer) Unmarshal(data []byte, v interface{}) error {
    if pb, ok := v.(proto.Message); ok {
        return proto.Unmarshal(data, pb)
    }
    return fmt.Errorf("value is not a protobuf message")
}
```

## 📝 Flexible Logging

SWR Cache uses a generic `Logger` interface, allowing you to use any logging library:

### Using Zap (Popular Choice)

```go
import (
    "go.uber.org/zap"
    "github.com/aiagentinc/swre"
)

// Create zap logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Create engine with zap logger
engine, _ := swre.NewStaleEngine(storage, logger)
```

### Using Standard Library slog

```go
import (
    "log/slog"
    "github.com/aiagentinc/swre"
)

// Create slog logger
slogLogger := slog.Default()
logger, _ := swre.NewSlogAdapter(slogLogger)

// Create engine with slog logger
engine, _ := swre.NewStaleEngine(storage, logger)
```

### Custom Logger Implementation

```go
// Implement the Logger interface for any logging library
type MyLogger struct {
    // your logger implementation
}

func (l *MyLogger) Debug(msg string, fields ...swre.Field) { /* ... */ }
func (l *MyLogger) Info(msg string, fields ...swre.Field) { /* ... */ }
func (l *MyLogger) Warn(msg string, fields ...swre.Field) { /* ... */ }
func (l *MyLogger) Error(msg string, fields ...swre.Field) { /* ... */ }
func (l *MyLogger) Named(name string) swre.Logger { /* ... */ }

// Use your custom logger
logger := &MyLogger{}
engine, _ := swre.NewStaleEngine(storage, logger)
```

### Testing with Mock Logger

```go
// Use the provided test helpers
func TestMyCache(t *testing.T) {
    logger := swre.NewTestLogger(t)
    engine, _ := swre.NewStaleEngine(storage, logger)
    // ... your tests
}

// Or use a no-op logger for benchmarks
func BenchmarkCache(b *testing.B) {
    logger := swre.NewNoOpLogger()
    engine, _ := swre.NewStaleEngine(storage, logger)
    // ... your benchmark
}
```

## 📊 Cache Status Types

Understanding cache entry statuses helps with debugging and monitoring:

- **`hit`**: Fresh cache hit (optimal performance)
- **`stale`**: Stale content served, background refresh triggered
- **`miss`**: Cache miss, fetched from source synchronously  
- **`expired`**: Expired content served while refreshing
- **`fallback`**: Error occurred, serving expired content as last resort

## 🏗️ Storage Backends

### Badger (Recommended)

High-performance embedded key-value store, perfect for single-node deployments:

```go
// Production-optimized configuration
cfg := swre.DefaultBadgerConfig("/var/lib/myapp/cache")
cfg.Compression = options.ZSTD        // Enable compression
cfg.MemTableSize = 512 << 20          // 512MB memory tables
cfg.ValueLogFileSize = 2 << 30        // 2GB value log files
cfg.GCInterval = 5 * time.Minute      // Frequent garbage collection

storage, err := swre.NewBadgerStorage(ctx, cfg)
```

### Custom Storage Backend

Implement the `Storage` interface for Redis, MongoDB, or any other backend:

```go
type Storage interface {
    Get(ctx context.Context, key string) (*CacheEntry, error)
    Set(ctx context.Context, key string, value *CacheEntry) error
    Delete(ctx context.Context, key string) error
    Close() error
}

// Example Redis storage implementation
type RedisStorage struct {
    client *redis.Client
}

func (r *RedisStorage) Get(ctx context.Context, key string) (*CacheEntry, error) {
    data, err := r.client.Get(ctx, key).Result()
    if err == redis.Nil {
        return nil, swre.ErrNotFound
    }
    if err != nil {
        return nil, err
    }
    
    var entry swre.CacheEntry
    err = json.Unmarshal([]byte(data), &entry)
    return &entry, err
}
```

## 📈 Monitoring & Metrics

### Prometheus Integration

```go
type PrometheusMetrics struct {
    hits    *prometheus.CounterVec
    misses  prometheus.Counter
    errors  prometheus.Counter
    latency prometheus.Histogram
}

func (p *PrometheusMetrics) RecordHit(key string, status string) {
    p.hits.WithLabelValues(status).Inc()
}

func (p *PrometheusMetrics) RecordMiss(key string) {
    p.misses.Inc()
}

func (p *PrometheusMetrics) RecordError(key string, err error) {
    p.errors.Inc()
}

func (p *PrometheusMetrics) RecordLatency(key string, duration time.Duration) {
    p.latency.Observe(duration.Seconds())
}

// Create logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Use with engine
engine, err := swre.NewStaleEngineWithOptions(storage, logger,
    swre.WithMetrics(prometheusMetrics),
)
```

## 🎯 Use Cases

### API Response Caching
```go
// Cache external API responses with smart TTL
engine.Execute(ctx, "weather:london", func() (interface{}, error) {
    return callWeatherAPI("London")
})
```

### Database Query Caching
```go
// Cache expensive database queries
engine.ExecuteGeneric(ctx, "users:active", &users, func() (interface{}, error) {
    return db.Query("SELECT * FROM users WHERE active = true")
})
```

### Microservice Communication
```go
// Cache service-to-service calls
engine.Execute(ctx, "inventory:product:123", func() (interface{}, error) {
    return inventoryService.GetProduct(123)
})
```

### Static Content Caching
```go
// Cache generated content (reports, images, etc.)
engine.Execute(ctx, "report:monthly:2024-01", func() (interface{}, error) {
    return generateMonthlyReport("2024-01")
})
```

## ⚡ Performance

### Benchmarks

Our benchmarks show exceptional performance for cache operations:

```
BenchmarkStaleEngine_CacheHit-8           20000000    105 ns/op      0 B/op     0 allocs/op
BenchmarkStaleEngine_ConcurrentMiss-8      2000000   1053 ns/op    512 B/op     8 allocs/op
BenchmarkStaleEngine_StaleServing-8       15000000    120 ns/op      0 B/op     0 allocs/op
```

### Performance Characteristics

- **🏃‍♂️ Hot Path**: Lock-free cache hits with only 2 atomic reads
- **🔄 Concurrent Protection**: Single upstream call per key via singleflight
- **🧠 Memory Efficient**: Smart buffer pooling reduces GC pressure
- **⚖️ Load Balancing**: Background refreshes prevent request clustering

## 💡 Best Practices

### 1. Key Design
```go
// ✅ Good: Hierarchical keys
"user:profile:123"
"api:weather:london:current"
"db:products:category:electronics"

// ❌ Avoid: Flat keys without structure
"user123profile"
"weatherlondon"
```

### 2. TTL Strategy
```go
// ✅ Match TTL to data characteristics
type TTLCalculator struct{}

func (t *TTLCalculator) CalculateTTL(key string, value interface{}) (time.Duration, time.Duration, error) {
    switch {
    case strings.HasPrefix(key, "user:session:"):
        return 30 * time.Minute, 25 * time.Minute, nil  // Short-lived
    case strings.HasPrefix(key, "config:"):
        return 24 * time.Hour, 23 * time.Hour, nil      // Stable data
    case strings.HasPrefix(key, "api:stock:"):
        return 1 * time.Minute, 50 * time.Second, nil   // High volatility
    default:
        return 15 * time.Minute, 10 * time.Minute, nil
    }
}
```

### 3. Error Handling
```go
// ✅ Always handle errors gracefully
entry, err := engine.Execute(ctx, key, fetchFunc)
switch {
case err == nil:
    // Use cached data
    return entry.Value
case errors.Is(err, swre.ErrCircuitOpen):
    // Circuit breaker is open, use fallback
    return getFallbackData()
default:
    // Other errors, decide based on criticality
    log.Printf("Cache error: %v", err)
    return nil, err
}
```

### 4. Resource Management
```go
// ✅ Always clean up resources
defer engine.Shutdown()  // Graceful shutdown
defer storage.Close()    // Close storage connections
```

### 5. Monitoring
```go
// ✅ Track important metrics
type Metrics struct {
    HitRate    float64
    MissRate   float64
    ErrorRate  float64
    P99Latency time.Duration
}

// Monitor cache effectiveness
if hitRate < 0.8 {
    log.Warn("Low cache hit rate, consider tuning TTL")
}
```

## 🚀 Production Configuration

```go
// Create logger
zapLogger, _ := zap.NewProduction()
logger, _ := swre.NewZapAdapter(zapLogger)

// Production-ready configuration
cfg := &swre.EngineConfig{
    Storage: storage,
    Logger:  logger.Named("cache"),
    
    // Configure for your workload
    DefaultCacheTTL: &swre.CacheTTL{
        FreshSeconds:   1800,  // 30 minutes fresh
        StaleSeconds:   3600,  // 1 hour stale window  
        ExpiredSeconds: 1800,  // 30 minutes expired buffer
    },
    
    // Performance tuning
    MaxConcurrentRefreshes: 100,
    RefreshTimeout:         10 * time.Second,
    
    // Enable observability
    Metrics: prometheusMetrics,
    
    // Optimize for your data
    Serializer: swre.NewCompressedSerializer(&swre.JSONSerializer{}),
}

engine, err := swre.NewStaleEngineWithConfig(cfg)
```

## 🤝 Contributing

We welcome contributions! Please:

1. **Fork** the repository
2. **Create** a feature branch (`git checkout -b feature/amazing-feature`)
3. **Add tests** for new functionality
4. **Run tests**: `go test ./...`
5. **Run benchmarks**: `go test -bench=. ./...`
6. **Commit** changes (`git commit -m 'Add amazing feature'`)
7. **Push** to branch (`git push origin feature/amazing-feature`)
8. **Open** a Pull Request

### Development Commands

```bash
# Run tests with race detection
go test -race ./...

# Run benchmarks
go test -bench=. -benchmem ./...

# Check coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out
```

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 🙋‍♂️ Support

- 📚 **Documentation**: Check our [examples](examples/) directory
- 🐛 **Issues**: [GitHub Issues](https://github.com/aiagentinc/swre/issues)
- 💬 **Discussions**: [GitHub Discussions](https://github.com/aiagentinc/swre/discussions)
- ⭐ **Star us** on GitHub if this project helps you!