package mwsr

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBufferBasic(t *testing.T) {
	var result []int
	var mu sync.Mutex

	buf := NewBuffer(128, func(v []int) error {
		mu.Lock()
		result = append(result, v...)
		mu.Unlock()
		return nil
	})

	buf.Write([]int{1, 2, 3})
	buf.Write([]int{4, 5})
	buf.Flush()

	if len(result) != 5 {
		t.Errorf("expected 5 items, got %d", len(result))
	}

	expected := []int{1, 2, 3, 4, 5}
	for i, v := range expected {
		if result[i] != v {
			t.Errorf("result[%d] = %d, expected %d", i, result[i], v)
		}
	}
}

func TestBufferStress(t *testing.T) {
	var count atomic.Uint64

	buf := NewBuffer[int](128, func(v []int) error {
		count.Add(uint64(len(v)))
		return nil
	})

	var wg sync.WaitGroup
	numGoroutines := 1000
	writesPerGoroutine := 100

	wg.Add(numGoroutines)
	for i := 0; i < numGoroutines; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < writesPerGoroutine; j++ {
				buf.Write([]int{j})
			}
		}()
	}

	wg.Wait()
	buf.Flush()

	expected := uint64(numGoroutines * writesPerGoroutine)
	if count.Load() != expected {
		t.Errorf("expected %d items, got %d", expected, count.Load())
	}
}

func TestBufferCallbackError(t *testing.T) {
	errTest := errors.New("test error")
	callCount := 0

	buf := NewBuffer(10, func(v []int) error {
		callCount++
		if callCount == 2 {
			return errTest
		}
		return nil
	})

	// First write fits in buffer
	n, err := buf.Write([]int{1, 2, 3})
	if err != nil || n != 3 {
		t.Errorf("first write: n=%d, err=%v", n, err)
	}

	// Flush triggers first callback (success)
	err = buf.Flush()
	if err != nil {
		t.Errorf("first flush failed: %v", err)
	}

	// More writes
	buf.Write([]int{4, 5})

	// This flush triggers second callback (error)
	err = buf.Flush()
	if err != errTest {
		t.Errorf("expected test error, got: %v", err)
	}

	// Subsequent writes should fail
	_, err = buf.Write([]int{6})
	if err != errTest {
		t.Errorf("write after error should fail with test error, got: %v", err)
	}
}

func TestBufferPanicRecovery(t *testing.T) {
	buf := NewBuffer(2, func(v []int) error {
		panic("test panic")
	})

	buf.Write([]int{1})
	err := buf.Flush()

	if err == nil {
		t.Error("expected error from panic, got nil")
	} else if err.Error() != "callback panic: test panic" {
		t.Errorf("unexpected error message: %v", err)
	}

	// Subsequent operations should return the error
	_, err = buf.Write([]int{2})
	if err == nil {
		t.Error("write after panic should fail")
	}
}

func TestBufferFlushEmpty(t *testing.T) {
	callCount := 0

	buf := NewBuffer(128, func(v []int) error {
		callCount++
		return nil
	})

	// Flush on empty buffer should not call callback
	buf.Flush()

	if callCount != 0 {
		t.Errorf("expected 0 callbacks, got %d", callCount)
	}
}

func TestBufferZeroLengthWrite(t *testing.T) {
	callCount := 0

	buf := NewBuffer(128, func(v []int) error {
		callCount++
		return nil
	})

	// Zero length write should be no-op
	n, err := buf.Write([]int{})
	if n != 0 || err != nil {
		t.Errorf("zero write: n=%d, err=%v", n, err)
	}

	buf.Flush()

	if callCount != 0 {
		t.Errorf("expected 0 callbacks for empty writes, got %d", callCount)
	}
}

func TestBufferLargeWrite(t *testing.T) {
	var chunks [][]int

	buf := NewBuffer(10, func(v []int) error {
		// Make a copy since we're storing it
		c := make([]int, len(v))
		copy(c, v)
		chunks = append(chunks, c)
		return nil
	})

	// Write more than buffer size
	large := make([]int, 25)
	for i := range large {
		large[i] = i
	}

	n, err := buf.Write(large)
	if err != nil {
		t.Errorf("large write failed: %v", err)
	}
	if n != 25 {
		t.Errorf("expected n=25, got n=%d", n)
	}

	// Should have flushed directly without buffering
	if len(chunks) != 1 {
		t.Errorf("expected 1 chunk for large write, got %d", len(chunks))
	}
	if len(chunks[0]) != 25 {
		t.Errorf("expected chunk of 25, got %d", len(chunks[0]))
	}
}

func TestBufferExactlyFull(t *testing.T) {
	callCount := 0

	buf := NewBuffer(5, func(v []int) error {
		callCount++
		if len(v) != 5 {
			return errors.New("expected exactly 5 items")
		}
		return nil
	})

	// Fill exactly to capacity
	buf.Write([]int{1, 2, 3})
	buf.Write([]int{4, 5})

	// Next write should trigger flush
	buf.Write([]int{6})

	if callCount < 1 {
		t.Error("expected at least one flush when buffer fills")
	}

	buf.Flush()
}

func TestBufferIOWriter(t *testing.T) {
	var result bytes.Buffer

	buf := NewBuffer[byte](64, func(v []byte) error {
		result.Write(v)
		return nil
	})

	// Buffer[byte] should work with io.Writer pattern
	var w io.Writer = buf

	w.Write([]byte("hello "))
	w.Write([]byte("world"))
	buf.Flush()

	if result.String() != "hello world" {
		t.Errorf("expected 'hello world', got '%s'", result.String())
	}
}

func TestBufferDataCopied(t *testing.T) {
	var received []byte

	buf := NewBuffer[byte](64, func(v []byte) error {
		received = make([]byte, len(v))
		copy(received, v)
		return nil
	})

	// Write data
	data := []byte("original")
	buf.Write(data)
	buf.Flush()

	// Modify the original slice
	data[0] = 'X'

	// Buffer should have copied the data, not referenced it
	if received[0] != 'o' {
		t.Error("buffer did not copy data, modification affected stored value")
	}
}

func TestBufferConcurrentFlush(t *testing.T) {
	var count atomic.Int32

	buf := NewBuffer[int](10, func(v []int) error {
		count.Add(int32(len(v)))
		return nil
	})

	var wg sync.WaitGroup

	// Writers
	wg.Add(50)
	for i := 0; i < 50; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				buf.Write([]int{j})
			}
		}()
	}

	// Concurrent flushers
	wg.Add(10)
	for i := 0; i < 10; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				buf.Flush()
			}
		}()
	}

	wg.Wait()
	buf.Flush()

	expected := int32(50 * 100)
	if count.Load() != expected {
		t.Errorf("expected %d items, got %d", expected, count.Load())
	}
}

func TestBufferWriteDuringFlush(t *testing.T) {
	flushStarted := make(chan struct{})
	flushContinue := make(chan struct{})

	buf := NewBuffer[int](10, func(v []int) error {
		close(flushStarted)
		<-flushContinue
		return nil
	})

	// Start a write that will trigger flush
	go func() {
		for i := 0; i < 20; i++ {
			buf.Write([]int{i})
		}
	}()

	// Wait for flush to start
	<-flushStarted

	// Try to write while flush is happening (should block or queue)
	done := make(chan struct{})
	go func() {
		buf.Write([]int{999})
		close(done)
	}()

	// Let the flush complete
	close(flushContinue)

	// Write should complete
	<-done
}

func TestBufferMixedSizeWrites(t *testing.T) {
	var totalItems int

	buf := NewBuffer[int](100, func(v []int) error {
		totalItems += len(v)
		return nil
	})

	// Various sizes
	buf.Write([]int{1})
	buf.Write([]int{1, 2, 3, 4, 5})
	buf.Write([]int{1, 2})
	buf.Write(make([]int, 50))
	buf.Write([]int{1, 2, 3})

	buf.Flush()

	expected := 1 + 5 + 2 + 50 + 3
	if totalItems != expected {
		t.Errorf("expected %d items, got %d", expected, totalItems)
	}
}

func TestBufferOverflowBoundary(t *testing.T) {
	var flushCount int
	var mu sync.Mutex

	buf := NewBuffer[int](10, func(v []int) error {
		mu.Lock()
		flushCount++
		mu.Unlock()
		return nil
	})

	// Write exactly at boundary
	buf.Write([]int{1, 2, 3, 4, 5})  // 5 items, half full
	buf.Write([]int{6, 7, 8, 9, 10}) // 10 items, exactly full

	// This should trigger a flush since we're at capacity
	buf.Write([]int{11})

	buf.Flush()

	if flushCount < 1 {
		t.Errorf("expected at least 1 flush at boundary, got %d", flushCount)
	}
}

func TestBufferRapidSmallWrites(t *testing.T) {
	var count atomic.Int64

	buf := NewBuffer[byte](1024, func(v []byte) error {
		count.Add(int64(len(v)))
		return nil
	})

	// Simulate many small writes like logging
	var wg sync.WaitGroup
	wg.Add(100)

	for i := 0; i < 100; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				buf.Write([]byte{byte(id), byte(j)})
			}
		}(i)
	}

	wg.Wait()
	buf.Flush()

	expected := int64(100 * 100 * 2)
	if count.Load() != expected {
		t.Errorf("expected %d bytes, got %d", expected, count.Load())
	}
}

func TestBufferErrorPersists(t *testing.T) {
	errTest := errors.New("permanent error")

	buf := NewBuffer[int](10, func(v []int) error {
		return errTest
	})

	buf.Write([]int{1})
	err1 := buf.Flush()

	if err1 != errTest {
		t.Errorf("first flush: expected test error, got %v", err1)
	}

	// Error should persist
	_, err2 := buf.Write([]int{2})
	if err2 != errTest {
		t.Errorf("write after error: expected test error, got %v", err2)
	}

	err3 := buf.Flush()
	if err3 != errTest {
		t.Errorf("second flush: expected test error, got %v", err3)
	}
}

func TestBufferGenericTypes(t *testing.T) {
	// Test with strings
	var strResult []string
	strBuf := NewBuffer(10, func(v []string) error {
		strResult = append(strResult, v...)
		return nil
	})

	strBuf.Write([]string{"hello", "world"})
	strBuf.Flush()

	if len(strResult) != 2 || strResult[0] != "hello" || strResult[1] != "world" {
		t.Errorf("string test failed: %v", strResult)
	}

	// Test with structs
	type item struct {
		id   int
		name string
	}

	var structResult []item
	structBuf := NewBuffer(10, func(v []item) error {
		structResult = append(structResult, v...)
		return nil
	})

	structBuf.Write([]item{{1, "first"}, {2, "second"}})
	structBuf.Flush()

	if len(structResult) != 2 || structResult[0].id != 1 || structResult[1].name != "second" {
		t.Errorf("struct test failed: %v", structResult)
	}

	// Test with pointers (GC clearing matters here)
	type bigStruct struct {
		_ [1024]byte
	}

	var ptrCount int
	ptrBuf := NewBuffer(5, func(v []*bigStruct) error {
		ptrCount += len(v)
		return nil
	})

	for i := 0; i < 20; i++ {
		ptrBuf.Write([]*bigStruct{{}, {}})
	}
	ptrBuf.Flush()

	if ptrCount != 40 {
		t.Errorf("pointer test: expected 40, got %d", ptrCount)
	}
}

func TestBufferWriteExactBufferSize(t *testing.T) {
	var chunks int

	buf := NewBuffer[int](10, func(v []int) error {
		chunks++
		return nil
	})

	// Write exactly buffer size - should trigger immediate flush
	data := make([]int, 10)
	buf.Write(data)

	// Should have been flushed immediately since len >= size
	if chunks != 1 {
		t.Errorf("expected 1 chunk for exact-size write, got %d", chunks)
	}
}

func TestBufferMultipleLargeWrites(t *testing.T) {
	var totalBytes int

	buf := NewBuffer[byte](100, func(v []byte) error {
		totalBytes += len(v)
		return nil
	})

	// Multiple large writes
	for i := 0; i < 10; i++ {
		buf.Write(make([]byte, 200))
	}

	if totalBytes != 2000 {
		t.Errorf("expected 2000 bytes, got %d", totalBytes)
	}
}

func TestBufferFlushDuringLargeWrite(t *testing.T) {
	var mu sync.Mutex
	var order []string

	buf := NewBuffer[int](5, func(v []int) error {
		mu.Lock()
		order = append(order, "flush")
		mu.Unlock()
		return nil
	})

	// Put some data in buffer
	buf.Write([]int{1, 2, 3})

	// Large write should flush existing data first, then write large data
	buf.Write(make([]int, 10))

	mu.Lock()
	if len(order) < 2 {
		t.Errorf("expected at least 2 flushes, got %d", len(order))
	}
	mu.Unlock()
}

func BenchmarkBufferWrite(b *testing.B) {
	buf := NewBuffer[int](1024, func(v []int) error {
		return nil
	})

	data := []int{1, 2, 3, 4, 5}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(data)
	}
	buf.Flush()
}

func BenchmarkBufferWriteParallel(b *testing.B) {
	buf := NewBuffer[int](1024, func(v []int) error {
		return nil
	})

	data := []int{1, 2, 3, 4, 5}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf.Write(data)
		}
	})
	buf.Flush()
}

func BenchmarkBufferByteWrite(b *testing.B) {
	buf := NewBuffer[byte](4096, func(v []byte) error {
		return nil
	})

	data := []byte("hello world this is a test message")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.Write(data)
	}
	buf.Flush()
}

func BenchmarkBufferByteWriteParallel(b *testing.B) {
	buf := NewBuffer[byte](4096, func(v []byte) error {
		return nil
	})

	data := []byte("hello world this is a test message")

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf.Write(data)
		}
	})
	buf.Flush()
}

// WriteV tests

func TestBufferWriteVBasic(t *testing.T) {
	var result []int

	buf := NewBuffer(128, func(v []int) error {
		result = append(result, v...)
		return nil
	})

	n, err := buf.WriteV([][]int{{1, 2}, {3, 4, 5}, {6}})
	if err != nil {
		t.Errorf("WriteV failed: %v", err)
	}
	if n != 6 {
		t.Errorf("expected n=6, got n=%d", n)
	}

	buf.Flush()

	expected := []int{1, 2, 3, 4, 5, 6}
	if len(result) != len(expected) {
		t.Errorf("expected %d items, got %d", len(expected), len(result))
	}
	for i, v := range expected {
		if i < len(result) && result[i] != v {
			t.Errorf("result[%d] = %d, expected %d", i, result[i], v)
		}
	}
}

func TestBufferWriteVEmpty(t *testing.T) {
	callCount := 0

	buf := NewBuffer(128, func(v []int) error {
		callCount++
		return nil
	})

	// Empty slices
	n, err := buf.WriteV([][]int{})
	if err != nil || n != 0 {
		t.Errorf("empty WriteV: n=%d, err=%v", n, err)
	}

	// Slices of empty slices
	n, err = buf.WriteV([][]int{{}, {}, {}})
	if err != nil || n != 0 {
		t.Errorf("empty slices WriteV: n=%d, err=%v", n, err)
	}

	buf.Flush()
}

func TestBufferWriteVLarge(t *testing.T) {
	var totalItems int

	buf := NewBuffer(10, func(v []int) error {
		totalItems += len(v)
		return nil
	})

	// WriteV with chunks larger than buffer
	large1 := make([]int, 25)
	large2 := make([]int, 30)
	small := []int{1, 2, 3}

	n, err := buf.WriteV([][]int{large1, small, large2})
	if err != nil {
		t.Errorf("WriteV failed: %v", err)
	}
	if n != 58 {
		t.Errorf("expected n=58, got n=%d", n)
	}

	buf.Flush()

	if totalItems != 58 {
		t.Errorf("expected 58 items, got %d", totalItems)
	}
}

func TestBufferWriteVMixed(t *testing.T) {
	var chunks [][]int

	buf := NewBuffer(10, func(v []int) error {
		c := make([]int, len(v))
		copy(c, v)
		chunks = append(chunks, c)
		return nil
	})

	// Mix of sizes that will trigger various code paths
	buf.WriteV([][]int{
		{1, 2},      // small, fits
		{3, 4, 5},   // small, fits
		{6, 7, 8, 9, 10, 11}, // will trigger flush
	})

	buf.Flush()

	// Verify all data made it through
	var total int
	for _, c := range chunks {
		total += len(c)
	}
	if total != 11 {
		t.Errorf("expected 11 total items, got %d", total)
	}
}

func TestBufferWriteVConcurrent(t *testing.T) {
	var count atomic.Int64

	buf := NewBuffer[int](100, func(v []int) error {
		count.Add(int64(len(v)))
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(100)

	for i := 0; i < 100; i++ {
		go func(id int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				buf.WriteV([][]int{{id, j}, {id + 100}})
			}
		}(i)
	}

	wg.Wait()
	buf.Flush()

	// 100 goroutines * 50 iterations * 3 items per WriteV = 15000
	expected := int64(100 * 50 * 3)
	if count.Load() != expected {
		t.Errorf("expected %d items, got %d", expected, count.Load())
	}
}

func TestBufferWriteVError(t *testing.T) {
	errTest := errors.New("test error")
	callCount := 0

	buf := NewBuffer(5, func(v []int) error {
		callCount++
		if callCount == 2 {
			return errTest
		}
		return nil
	})

	// First WriteV succeeds
	buf.WriteV([][]int{{1, 2}})
	buf.Flush()

	// Second WriteV triggers error on flush
	buf.WriteV([][]int{{3, 4}})
	err := buf.Flush()
	if err != errTest {
		t.Errorf("expected test error, got: %v", err)
	}

	// Subsequent WriteV should fail
	_, err = buf.WriteV([][]int{{5}})
	if err != errTest {
		t.Errorf("WriteV after error should fail, got: %v", err)
	}
}

func TestBufferWriteVBytes(t *testing.T) {
	var result bytes.Buffer

	buf := NewBuffer[byte](64, func(v []byte) error {
		result.Write(v)
		return nil
	})

	// Simulate writev-style scatter-gather
	buf.WriteV([][]byte{
		[]byte("HTTP/1.1 200 OK\r\n"),
		[]byte("Content-Type: text/plain\r\n"),
		[]byte("\r\n"),
		[]byte("Hello, World!"),
	})
	buf.Flush()

	expected := "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\nHello, World!"
	if result.String() != expected {
		t.Errorf("expected %q, got %q", expected, result.String())
	}
}

func BenchmarkBufferWriteV(b *testing.B) {
	buf := NewBuffer[int](1024, func(v []int) error {
		return nil
	})

	data := [][]int{{1, 2, 3}, {4, 5}, {6, 7, 8, 9}}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.WriteV(data)
	}
	buf.Flush()
}

func BenchmarkBufferWriteVParallel(b *testing.B) {
	buf := NewBuffer[int](1024, func(v []int) error {
		return nil
	})

	data := [][]int{{1, 2, 3}, {4, 5}, {6, 7, 8, 9}}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			buf.WriteV(data)
		}
	})
	buf.Flush()
}
