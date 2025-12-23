[![GoDoc](https://godoc.org/github.com/KarpelesLab/mwsr?status.svg)](https://godoc.org/github.com/KarpelesLab/mwsr)
[![Go Report Card](https://goreportcard.com/badge/github.com/KarpelesLab/mwsr)](https://goreportcard.com/report/github.com/KarpelesLab/mwsr)
[![Build Status](https://github.com/KarpelesLab/mwsr/actions/workflows/test.yml/badge.svg)](https://github.com/KarpelesLab/mwsr/actions/workflows/test.yml)
[![Coverage Status](https://coveralls.io/repos/github/KarpelesLab/mwsr/badge.svg?branch=master)](https://coveralls.io/github/KarpelesLab/mwsr?branch=master)

# mwsr

Multiple writers, single reader.

This is a simple lib that allows multiple threads to write in parallel to a
single queue, and the queue to be flushed as soon as possible, possibly with
multiple values inside.

## Installation

```bash
go get github.com/KarpelesLab/mwsr
```

Requires Go 1.23 or later.

## Types

The library provides two buffer types:

- **`Buffer[T]`** - Synchronous buffer that flushes inline during writes. Simpler, no goroutine, no cleanup needed. Compatible with `io.Writer` when `T=byte`.
- **`AsyncBuffer[T]`** - Asynchronous buffer with a background goroutine for flushing. Requires `Close()` to stop the goroutine.

## Usage

### Buffer (synchronous)

```go
buf := mwsr.NewBuffer(128, func(v []byte) error {
    _, err := conn.Write(v)
    return err
})

// Writes are batched and flushed synchronously
buf.Write([]byte("hello"))
buf.Write([]byte("world"))

// Force flush remaining data
buf.Flush()
```

### WriteV for scatter-gather I/O

`WriteV` writes multiple slices atomically, guaranteeing order without interleaving from concurrent writers. This is ideal for protocols where headers and body must stay together:

```go
buf := mwsr.NewBuffer[byte](4096, func(v []byte) error {
    _, err := conn.Write(v)
    return err
})

// Write HTTP response atomically - headers and body stay together
buf.WriteV([][]byte{
    []byte("HTTP/1.1 200 OK\r\n"),
    []byte("Content-Type: text/plain\r\n"),
    []byte("Content-Length: 13\r\n"),
    []byte("\r\n"),
    []byte("Hello, World!"),
})
```

`WriteV` is designed for zero additional allocations in the fast path when all data fits in the buffer.

### AsyncBuffer (asynchronous)

```go
q := mwsr.NewAsyncBuffer(128, func(v []int) error {
    log.Printf("got values: %+v", v)
    return nil
})
defer q.Close() // Always close to prevent goroutine leaks

// This will typically result in less than 10 calls to log.Printf
for i := 0; i < 10; i++ {
    go q.Write(i)
}

// Optionally force a flush
q.Flush()
```

### Generic Types

The library uses Go generics, so you can use any type:

```go
// With strings
q := mwsr.NewAsyncBuffer(100, func(v []string) error {
    for _, s := range v {
        fmt.Println(s)
    }
    return nil
})

// With custom structs
type LogEntry struct {
    Level   string
    Message string
}

q := mwsr.NewAsyncBuffer(100, func(v []LogEntry) error {
    for _, entry := range v {
        log.Printf("[%s] %s", entry.Level, entry.Message)
    }
    return nil
})
```

## API

### Buffer

- `NewBuffer[T any](size uint32, cb func([]T) error) *Buffer[T]` - Create a new synchronous buffer
- `Write(p []T) (int, error)` - Write values to the buffer (thread-safe, copies data)
- `WriteV(p [][]T) (int, error)` - Write multiple slices atomically with guaranteed ordering (zero-alloc fast path)
- `Flush() error` - Force flush the buffer

### AsyncBuffer

- `NewAsyncBuffer[T any](size int, cb func([]T) error) *AsyncBuffer[T]` - Create a new async buffer with background goroutine
- `Write(v T) error` - Write a single value to the queue (thread-safe)
- `Flush() error` - Force flush the queue and wait for completion
- `Close() error` - Flush remaining items and stop the background goroutine

### Deprecated Aliases

For backwards compatibility, `Mwsr` is an alias for `AsyncBuffer` and `New` is an alias for `NewAsyncBuffer`.

## Design

The code uses `sync.RWMutex` in an unconventional way:

- Multiple `RLock` can be acquired at the same time - used during `Write()`
- Only one routine can hold `Lock` - used during flush

This allows multiple concurrent writes while ensuring exclusive access during flush operations.
