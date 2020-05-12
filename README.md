[![GoDoc](https://godoc.org/github.com/KarpelesLab/mwsr?status.svg)](https://godoc.org/github.com/KarpelesLab/mwsr)

# mwsr

Multiple writers, single reader.

This is a simple lib that allows multiple threads to write in parallel to a
single queue, and the queue to be flushed as soon as possible, possibly with
multiple values inside.

Example use:

```go
	q := mwsr.New(128, func(v []interface{}) error {
		log.Printf("got values: %+v", v)
		return nil
	})

	// this will typically result in less than 10 calls to log.Printf
	for i := 0; i < 10; i++ {
		go q.Write(i)
	}
```

The code is meant to be simple enough to be copied and adapted to any project
so it can run without using `interface{}`, which would likely help a lot in
terms of performance. Think of this as a proof of concept showing that it is
possible to use `sync.RWMutex` the other way around.

## Improvements

### Allow writes while flush is running

This would be actually fairly simple, by allocating a new buffer and unlocking
before running the callback. This would remove a lock and add a new allocation
instead. If the callback is fast enough however, it will likely be better to
not duplicate memory.

### Flush pointers after write

Right now, after the callback is called values are kept in the buffer. A
simple loop setting all entries to nil could fix that, but would require more
CPU to run. The flush can actually be done from within the callback, so it
shouldn't be an issue.
