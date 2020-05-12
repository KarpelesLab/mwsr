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
