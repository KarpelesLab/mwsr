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

Requires Go 1.21 or later.

## Usage

```go
q := mwsr.New(128, func(v []int) error {
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
q := mwsr.New(100, func(v []string) error {
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

q := mwsr.New(100, func(v []LogEntry) error {
    for _, entry := range v {
        log.Printf("[%s] %s", entry.Level, entry.Message)
    }
    return nil
})
```

## API

- `New[T any](size int, cb func([]T) error) *Mwsr[T]` - Create a new queue with the given buffer size and callback
- `Write(v T) error` - Write a value to the queue (thread-safe)
- `Flush() error` - Force flush the queue and wait for completion
- `Close() error` - Flush remaining items and stop the background goroutine

## Design

The code uses `sync.RWMutex` in an unconventional way:

- Multiple `RLock` can be acquired at the same time - used during `Write()`
- Only one routine can hold `Lock` - used during buffer swap

Writes can continue while the flush callback is executing. When a flush starts,
the current buffer is swapped out and a new buffer is allocated for incoming
writes. This maximizes throughput by not blocking writers during potentially
slow callback operations.
