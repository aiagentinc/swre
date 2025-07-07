# SWR Cache Architecture

This document provides a detailed architectural overview of the SWR (Stale-While-Revalidate) cache implementation, including visual representations of key components and processes.

## Table of Contents

1. [Architecture Overview](#architecture-overview)
2. [Core Components](#core-components)
3. [SWR Cache Lifecycle](#swr-cache-lifecycle)
4. [Singleflight Mechanism](#singleflight-mechanism)
5. [Circuit Breaker Pattern](#circuit-breaker-pattern)
6. [Dynamic TTL Strategy](#dynamic-ttl-strategy)
7. [Performance Characteristics](#performance-characteristics)

## Architecture Overview

```mermaid
graph TB
    subgraph "Client Applications"
        A1[Application 1]
        A2[Application 2]
        A3[Application N]
    end

    subgraph "SWR Cache Engine"
        SE[StaleEngine]
        SF[Singleflight]
        CB[Circuit Breaker]
        RT[RefreshTracker]
        
        SE --> SF
        SE --> CB
        SE --> RT
    end

    subgraph "Storage Layer"
        SI[Storage Interface]
        BS[Badger Storage]
        RS[Redis Storage]
        MS[Memory Storage]
        
        SI --> BS
        SI --> RS
        SI --> MS
    end

    subgraph "Serialization"
        SER[Serializer Interface]
        JSON[JSON Serializer]
        GOB[Gob Serializer]
        COMP[Compressed Serializer]
        
        SER --> JSON
        SER --> GOB
        SER --> COMP
    end

    subgraph "Observability"
        LOG[Logger Interface]
        MET[Metrics]
        
        ZAP[Zap Logger]
        SLOG[Slog Logger]
        
        LOG --> ZAP
        LOG --> SLOG
    end

    A1 --> SE
    A2 --> SE
    A3 --> SE
    
    SE --> SI
    SE --> SER
    SE --> LOG
    SE --> MET

    style SE fill:#f9f,stroke:#333,stroke-width:4px
    style SF fill:#bbf,stroke:#333,stroke-width:2px
    style CB fill:#fbf,stroke:#333,stroke-width:2px
    style RT fill:#bfb,stroke:#333,stroke-width:2px
```

## Core Components

### StaleEngine
The heart of the SWR cache that orchestrates all caching operations:

```mermaid
classDiagram
    class StaleEngine {
        -storage Storage
        -logger Logger
        -metrics Metrics
        -circuitBreaker CircuitBreaker
        -refreshTracker RefreshTracker
        -singleflight Group
        +Get(ctx, key, fetch) (value, error)
        +Set(ctx, key, value, ttl) error
        +Delete(ctx, key) error
        +refreshInBackground(key, fetch)
    }
    
    class Storage {
        <<interface>>
        +Get(ctx, key) (CacheEntry, error)
        +Set(ctx, key, entry) error
        +Delete(ctx, key) error
        +Close() error
    }
    
    class CircuitBreaker {
        -globalBreaker Breaker
        -keyBreakers LRU
        +Execute(key, fn) error
        +RecordSuccess(key)
        +RecordFailure(key)
    }
    
    class RefreshTracker {
        -shards []shard
        -numShards int
        +MarkRefreshing(key) bool
        +UnmarkRefreshing(key)
        +IsRefreshing(key) bool
    }
    
    StaleEngine --> Storage
    StaleEngine --> CircuitBreaker
    StaleEngine --> RefreshTracker
```

## SWR Cache Lifecycle

The three-phase TTL system ensures optimal performance and availability:

```mermaid
sequenceDiagram
    participant Client
    participant StaleEngine
    participant Storage
    participant Upstream
    participant Background

    Note over Storage: Cache Entry States
    Note over Storage: 1. Fresh (0 to FreshSeconds)
    Note over Storage: 2. Stale (FreshSeconds to StaleSeconds)
    Note over Storage: 3. Expired (StaleSeconds to ExpiredSeconds)
    Note over Storage: 4. Deleted (after ExpiredSeconds)

    %% Fresh Hit
    Client->>StaleEngine: Get(key)
    StaleEngine->>Storage: Get(key)
    Storage-->>StaleEngine: Entry (Fresh)
    StaleEngine-->>Client: Value (immediate)
    Note right of Client: Fresh hit - no refresh needed

    %% Stale Hit
    Client->>StaleEngine: Get(key)
    StaleEngine->>Storage: Get(key)
    Storage-->>StaleEngine: Entry (Stale)
    StaleEngine-->>Client: Stale Value (immediate)
    StaleEngine->>Background: Trigger refresh
    Background->>Upstream: Fetch new data
    Upstream-->>Background: New data
    Background->>Storage: Update entry
    Note right of Client: Stale hit - background refresh

    %% Cache Miss
    Client->>StaleEngine: Get(key)
    StaleEngine->>Storage: Get(key)
    Storage-->>StaleEngine: Not found
    StaleEngine->>Upstream: Fetch data (blocking)
    Upstream-->>StaleEngine: Data
    StaleEngine->>Storage: Set(key, data)
    StaleEngine-->>Client: Value
    Note right of Client: Cache miss - blocking fetch
```

### TTL Phase Transitions

```mermaid
stateDiagram-v2
    [*] --> Fresh: New Entry
    Fresh --> Stale: After FreshSeconds
    Stale --> Expired: After StaleSeconds
    Expired --> [*]: After ExpiredSeconds
    
    Fresh --> [*]: Delete()
    Stale --> [*]: Delete()
    Expired --> [*]: Delete()
    
    Stale --> Fresh: Successful Refresh
    Expired --> Fresh: Successful Refresh
    
    note right of Fresh: Immediate return\nNo refresh
    note right of Stale: Immediate return\nBackground refresh
    note right of Expired: Return if available\nBlocking refresh
```

## Singleflight Mechanism

Prevents cache stampede by ensuring only one upstream request per key:

```mermaid
sequenceDiagram
    participant Client1
    participant Client2
    participant Client3
    participant Singleflight
    participant Upstream

    Note over Singleflight: Key "user:123" not in flight

    Client1->>Singleflight: Request "user:123"
    Singleflight->>Singleflight: Mark key in flight
    Singleflight->>Upstream: Fetch data
    
    Client2->>Singleflight: Request "user:123"
    Note over Singleflight: Key already in flight
    Singleflight-->>Client2: Wait...
    
    Client3->>Singleflight: Request "user:123"
    Singleflight-->>Client3: Wait...
    
    Upstream-->>Singleflight: Data
    Singleflight->>Singleflight: Unmark key
    Singleflight-->>Client1: Data
    Singleflight-->>Client2: Data (shared)
    Singleflight-->>Client3: Data (shared)
    
    Note over Singleflight: Single upstream call\nShared result
```

### Singleflight Benefits

```mermaid
graph LR
    subgraph "Without Singleflight"
        C1[Client 1] --> U1[Upstream]
        C2[Client 2] --> U2[Upstream]
        C3[Client 3] --> U3[Upstream]
        C4[Client 4] --> U4[Upstream]
        
        U1 --> DB1[(Database)]
        U2 --> DB1
        U3 --> DB1
        U4 --> DB1
    end
    
    subgraph "With Singleflight"
        D1[Client 1] --> SF[Singleflight]
        D2[Client 2] --> SF
        D3[Client 3] --> SF
        D4[Client 4] --> SF
        
        SF --> U5[Upstream]
        U5 --> DB2[(Database)]
    end
    
    style DB1 fill:#f66,stroke:#333,stroke-width:2px
    style DB2 fill:#6f6,stroke:#333,stroke-width:2px
```

## Circuit Breaker Pattern

Protects the system from cascading failures:

```mermaid
stateDiagram-v2
    [*] --> Closed: Initial State
    
    Closed --> Open: Failure Threshold Reached
    Open --> HalfOpen: After Recovery Time
    HalfOpen --> Closed: Success
    HalfOpen --> Open: Failure
    
    note right of Closed: Normal operation\nRequests pass through
    note right of Open: Circuit broken\nFast fail all requests
    note right of HalfOpen: Testing recovery\nLimited requests allowed
```

### Circuit Breaker Flow

```mermaid
flowchart TD
    A[Request] --> B{Circuit State?}
    
    B -->|Closed| C[Execute Request]
    C --> D{Success?}
    D -->|Yes| E[Record Success]
    D -->|No| F[Record Failure]
    F --> G{Threshold Reached?}
    G -->|Yes| H[Open Circuit]
    G -->|No| I[Return Error]
    
    B -->|Open| J{Recovery Time Passed?}
    J -->|No| K[Fast Fail]
    J -->|Yes| L[Half-Open State]
    
    B -->|Half-Open| M[Execute Test Request]
    M --> N{Success?}
    N -->|Yes| O[Close Circuit]
    N -->|No| P[Re-open Circuit]
    
    style H fill:#f66,stroke:#333,stroke-width:2px
    style K fill:#f66,stroke:#333,stroke-width:2px
    style O fill:#6f6,stroke:#333,stroke-width:2px
```

### Per-Key Circuit Breaking

```mermaid
graph TB
    subgraph "Global Circuit Breaker"
        GCB[Global State]
    end
    
    subgraph "Key-Level Circuit Breakers (LRU)"
        K1[Key: user:123<br/>State: Closed]
        K2[Key: product:456<br/>State: Open]
        K3[Key: order:789<br/>State: Half-Open]
        K4[Key: ...<br/>State: ...]
    end
    
    subgraph "Request Flow"
        R1[Request user:123] --> K1
        R2[Request product:456] --> K2
        R3[Request order:789] --> K3
    end
    
    K1 --> GCB
    K2 --> GCB
    K3 --> GCB
    
    style K2 fill:#f66,stroke:#333,stroke-width:2px
    style K3 fill:#ff6,stroke:#333,stroke-width:2px
```

## Dynamic TTL Strategy

Flexible TTL configuration based on content or patterns:

```mermaid
flowchart TD
    A[Cache Entry] --> B{TTL Strategy}
    
    B --> C[Default TTL]
    B --> D[Pattern-Based TTL]
    B --> E[Content-Based TTL]
    B --> F[Dynamic TTL Function]
    
    C --> G[Fixed Duration<br/>Fresh: 5m<br/>Stale: 1h<br/>Expired: 1h]
    
    D --> H{Key Pattern}
    H -->|user:*| I[Short TTL<br/>Fresh: 1m<br/>Stale: 5m]
    H -->|config:*| J[Long TTL<br/>Fresh: 1h<br/>Stale: 24h]
    H -->|session:*| K[Medium TTL<br/>Fresh: 15m<br/>Stale: 30m]
    
    E --> L{Content Type}
    L -->|Large Data| M[Extended TTL]
    L -->|Frequently Updated| N[Short TTL]
    L -->|Static Content| O[Long TTL]
    
    F --> P[Custom Function<br/>f(key, value) → TTL]
```

### TTL Configuration Examples

```mermaid
graph LR
    subgraph "Configuration"
        TTL[CacheTTL Config]
        TTL --> F[FreshSeconds: 300]
        TTL --> S[StaleSeconds: 3600]
        TTL --> E[ExpiredSeconds: 3600]
    end
    
    subgraph "Timeline"
        T0[0s<br/>Set] --> T1[5min<br/>Fresh→Stale]
        T1 --> T2[1h<br/>Stale→Expired]
        T2 --> T3[2h<br/>Expired→Deleted]
    end
    
    style T0 fill:#6f6,stroke:#333,stroke-width:2px
    style T1 fill:#ff6,stroke:#333,stroke-width:2px
    style T2 fill:#f66,stroke:#333,stroke-width:2px
```

## Performance Characteristics

### Hot Path Optimization

```mermaid
flowchart LR
    A[Cache Get Request] --> B[Atomic Read: Key State]
    B --> C{Is Fresh?}
    C -->|Yes| D[Storage Get]
    D --> E[Return Value]
    
    C -->|No| F[Check Stale/Expired]
    
    style B fill:#6f6,stroke:#333,stroke-width:2px
    style D fill:#6f6,stroke:#333,stroke-width:2px
    
    Note1[Lock-Free Operation]
    Note2[2 Atomic Reads Only]
```

### Concurrent Request Handling

```mermaid
graph TB
    subgraph "Incoming Requests"
        R1[Request 1]
        R2[Request 2]
        R3[Request 3]
        RN[Request N]
    end
    
    subgraph "Concurrency Control"
        SF[Singleflight<br/>Deduplication]
        RT[RefreshTracker<br/>Sharded Locks]
        CB[Circuit Breaker<br/>Fast Fail]
    end
    
    subgraph "Resource Management"
        GP[Goroutine Pool<br/>MaxConcurrentRefreshes]
        BP[Buffer Pool<br/>Serialization]
        TO[Timeouts<br/>RefreshTimeout]
    end
    
    R1 --> SF
    R2 --> SF
    R3 --> SF
    RN --> SF
    
    SF --> RT
    RT --> CB
    CB --> GP
    GP --> BP
    BP --> TO
```

### Memory Efficiency

```mermaid
pie title "Memory Usage Distribution"
    "Cache Entries" : 45
    "Refresh Tracker (Sharded)" : 15
    "Circuit Breaker (LRU)" : 10
    "Singleflight Groups" : 5
    "Buffer Pools" : 10
    "Metadata & Indices" : 15
```

## Summary

The SWR Cache architecture is designed for:

1. **High Performance**: Lock-free hot path, minimal atomic operations
2. **Resilience**: Circuit breakers, graceful degradation
3. **Efficiency**: Singleflight deduplication, sharded locks
4. **Flexibility**: Pluggable storage, serializers, and metrics
5. **Observability**: Comprehensive logging and metrics

This architecture enables applications to achieve:
- Sub-millisecond cache hits
- Automatic background refresh
- Protection from cache stampedes
- Graceful handling of failures
- Scalability to millions of requests