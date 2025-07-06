# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

This is a high-performance, production-ready SWR (Stale-While-Revalidate) cache implementation in Go. The library provides a generic caching solution for any use case requiring intelligent cache management with background refresh capabilities.

## Core Architecture

### Main Components

1. **StaleEngine** (`swr.go`): The core SWR cache engine that handles:
   - Single upstream call per key via singleflight
   - Non-blocking stale refresh with bounded goroutine lifetime
   - Lock-free hot path for fresh cache hits
   - Comprehensive TTL management with fresh/stale/expired phases

2. **Storage Layer** (`cache.go`): Pluggable storage interface with:
   - Context-aware operations
   - CacheEntry structure with SWR metadata
   - Efficient timestamp management and TTL calculations

3. **Serialization** (`serializers.go`): Multiple serialization strategies:
   - JSONSerializer: High-performance JSON using jsoniter
   - GobSerializer: Go-native binary serialization
   - CompressedSerializer: Decorator for gzip compression

4. **Circuit Breaker** (`circuit_breaker.go`): Fault tolerance with:
   - Global and per-key circuit breaking
   - LRU-based key breaker management
   - Configurable failure thresholds and recovery

5. **Refresh Tracking** (`refresh_tracker.go`): Prevents duplicate refreshes:
   - Atomic flag management per key
   - Automatic cleanup to prevent memory leaks
   - Configurable TTL for refresh flags

6. **Badger Storage** (`badger_storage.go`): Production storage backend:
   - Embedded key-value store
   - Compression and performance tuning
   - Graceful shutdown and resource management

7. **Logging System** (`logger.go`, `logger_*.go`): Flexible logging with:
   - Generic Logger interface for any logging library
   - Built-in adapters for zap and slog
   - Structured logging with type-safe fields
   - Test helpers and mock implementations

## Development Commands

### Building and Testing
```bash
# Build the project
go build ./...

# Run all tests
go test ./...

# Run tests with verbose output
go test -v ./...

# Run tests with coverage
go test -coverprofile=coverage.out ./...
go tool cover -html=coverage.out

# Run specific test patterns
go test -run TestStaleEngine ./...
go test -run TestCircuitBreaker ./...
```

### Benchmarking
```bash
# Run all benchmarks
go test -bench=. ./...

# Run specific benchmarks
go test -bench=BenchmarkStaleEngine ./...
go test -bench=BenchmarkSerialization ./...

# Memory profiling
go test -bench=. -memprofile=mem.prof ./...
go tool pprof mem.prof
```

### Code Quality
```bash
# Format code
go fmt ./...

# Vet for common issues
go vet ./...

# Run static analysis (if golangci-lint is installed)
golangci-lint run

# Generate documentation
go doc -all ./...
```

## Configuration Patterns

### Basic Usage
```go
// Create logger (any implementation of swr.Logger interface)
zapLogger, _ := zap.NewProduction()
logger, _ := swr.NewZapAdapter(zapLogger)

// Create with defaults
engine, _ := swr.NewStaleEngine(storage, logger)

// With options
engine, _ := swr.NewStaleEngineWithOptions(storage, logger,
    swr.WithCacheTTL(&swr.CacheTTL{
        FreshSeconds: 300,    // 5 minutes fresh
        StaleSeconds: 3600,   // 1 hour stale
        ExpiredSeconds: 3600, // 1 hour expired
    }),
    swr.WithMaxConcurrentRefreshes(100),
    swr.WithRefreshTimeout(10*time.Second),
)
```

### TTL Management
The library uses a three-phase TTL system:
- **Fresh Phase**: Immediate cache hits
- **Stale Phase**: Serve stale content + trigger background refresh
- **Expired Phase**: Optional fallback serving during refresh failures

### Performance Considerations
- Hot path (cache hit) is lock-free with only 2 atomic reads
- Background refreshes are bounded to prevent resource exhaustion
- Memory-efficient buffer pooling for serialization
- Intelligent time caching to reduce syscall overhead

## Testing Strategy

The codebase includes comprehensive test coverage:
- Unit tests for core functionality
- Integration tests for storage backends
- Performance benchmarks
- Memory leak detection
- Concurrency stress tests

When adding new features:
1. Add unit tests for new components
2. Update integration tests if storage interface changes
3. Add benchmarks for performance-critical paths
4. Test with race detection: `go test -race`

## Key Dependencies

- `github.com/dgraph-io/badger/v4`: Embedded key-value storage
- `github.com/hashicorp/golang-lru/v2`: LRU cache for circuit breaker
- `github.com/json-iterator/go`: High-performance JSON serialization
- `go.uber.org/zap`: Structured logging
- `golang.org/x/sync`: Singleflight for deduplication

## Module Information

- Module: `github.com/aiagentinc/swre`
- Go Version: 1.24
- Import path: Use `github.com/aiagentinc/swre` for external imports