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

## Improvements

### Allow writes while flush is running

This would be actually fairly simple, by allocating a new buffer and unlocking
before running the callback. This would remove a lock and add a new allocation
instead. If the write is fast enough, locking is likely going to be better.

