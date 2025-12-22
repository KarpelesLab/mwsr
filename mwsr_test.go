package mwsr

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestBasic(t *testing.T) {
	var check uint32

	q := New(128, func(v []int) error {
		t.Logf("got values: %+v", v)
		for _, sub := range v {
			atomic.AddUint32(&check, uint32(sub))
		}
		return nil
	})
	defer q.Close()

	var wg sync.WaitGroup
	wg.Add(10)

	for i := 0; i < 10; i++ {
		go func(i int) {
			defer wg.Done()
			q.Write(i)
		}(i)
	}

	wg.Wait()
	q.Flush()

	if check != 45 {
		t.Errorf("expected check=45, got check=%d", check)
	}
}

func TestStress(t *testing.T) {
	var check uint32

	q := New(128, func(v []int) error {
		for _, sub := range v {
			atomic.AddUint32(&check, uint32(sub))
		}
		return nil
	})
	defer q.Close()

	var wg sync.WaitGroup
	wg.Add(65536)

	for i := 0; i < 65536; i++ {
		go func(i int) {
			defer wg.Done()
			q.Write(i)
		}(i)
	}

	wg.Wait()
	q.Flush()

	if check != 2147450880 {
		t.Errorf("expected check=2147450880, got check=%d", check)
	}
}

func TestCallbackError(t *testing.T) {
	errTest := errors.New("test error")
	callCount := 0

	q := New(2, func(v []int) error {
		callCount++
		if callCount == 2 {
			return errTest
		}
		return nil
	})
	defer q.Close()

	// First write should succeed
	if err := q.Write(1); err != nil {
		t.Errorf("first write failed: %v", err)
	}

	// Second write triggers flush (buffer full), should succeed
	if err := q.Write(2); err != nil {
		t.Errorf("second write failed: %v", err)
	}

	// Third write triggers another flush which returns error
	if err := q.Write(3); err != nil {
		t.Errorf("third write failed unexpectedly: %v", err)
	}

	// Fourth write triggers flush with error
	err := q.Write(4)
	if err == nil {
		// The error might be returned on flush instead
		err = q.Flush()
	}

	// Eventually we should see the error
	if q.Flush() != errTest && err != errTest {
		t.Errorf("expected test error, got: %v", q.Flush())
	}
}

func TestPanicRecovery(t *testing.T) {
	q := New(1, func(v []int) error {
		panic("test panic")
	})
	defer q.Close()

	// Write should trigger flush due to small buffer
	q.Write(1)
	q.Write(2) // This should trigger flush and panic

	err := q.Flush()
	if err == nil {
		t.Error("expected error from panic, got nil")
	} else if err.Error() != "callback panic occurred: test panic" {
		t.Errorf("unexpected error message: %v", err)
	}
}

func TestClose(t *testing.T) {
	var flushed bool

	q := New(128, func(v []int) error {
		flushed = true
		return nil
	})

	// Write something
	q.Write(1)

	// Close should flush
	err := q.Close()
	if err != nil {
		t.Errorf("close returned error: %v", err)
	}

	if !flushed {
		t.Error("expected flush on close")
	}

	// Write after close should fail
	err = q.Write(2)
	if err != ErrClosed {
		t.Errorf("expected ErrClosed, got: %v", err)
	}
}

func TestCloseMultipleTimes(t *testing.T) {
	q := New(128, func(v []int) error {
		return nil
	})

	// Close multiple times should be safe
	q.Close()
	q.Close()
	q.Close()
}

func TestZeroBufferSize(t *testing.T) {
	callCount := 0

	q := New(0, func(v []int) error {
		callCount++
		return nil
	})
	defer q.Close()

	// Should work despite 0 size (defaults to 1)
	q.Write(1)
	q.Flush()

	if callCount == 0 {
		t.Error("expected at least one callback")
	}
}

func TestNegativeBufferSize(t *testing.T) {
	callCount := 0

	q := New(-5, func(v []int) error {
		callCount++
		return nil
	})
	defer q.Close()

	// Should work despite negative size (defaults to 1)
	q.Write(1)
	q.Flush()

	if callCount == 0 {
		t.Error("expected at least one callback")
	}
}

func TestFlushEmpty(t *testing.T) {
	callCount := 0

	q := New(128, func(v []int) error {
		callCount++
		return nil
	})
	defer q.Close()

	// Flush on empty queue should not call callback
	q.Flush()

	if callCount != 0 {
		t.Errorf("expected 0 callbacks, got %d", callCount)
	}
}

func TestGenericTypes(t *testing.T) {
	// Test with strings
	var strResult []string
	strQ := New(10, func(v []string) error {
		strResult = append(strResult, v...)
		return nil
	})

	strQ.Write("hello")
	strQ.Write("world")
	strQ.Flush()
	strQ.Close()

	if len(strResult) != 2 || strResult[0] != "hello" || strResult[1] != "world" {
		t.Errorf("string test failed: %v", strResult)
	}

	// Test with structs
	type item struct {
		id   int
		name string
	}

	var structResult []item
	structQ := New(10, func(v []item) error {
		structResult = append(structResult, v...)
		return nil
	})

	structQ.Write(item{1, "first"})
	structQ.Write(item{2, "second"})
	structQ.Flush()
	structQ.Close()

	if len(structResult) != 2 || structResult[0].id != 1 || structResult[1].name != "second" {
		t.Errorf("struct test failed: %v", structResult)
	}
}

func TestConcurrentWriteAndClose(t *testing.T) {
	q := New(128, func(v []int) error {
		return nil
	})

	var wg sync.WaitGroup
	wg.Add(100)

	for i := 0; i < 100; i++ {
		go func(i int) {
			defer wg.Done()
			q.Write(i)
		}(i)
	}

	// Close while writes are happening
	go q.Close()

	wg.Wait()
}

func BenchmarkWrite(b *testing.B) {
	q := New(1024, func(v []int) error {
		return nil
	})
	defer q.Close()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.Write(i)
	}
	q.Flush()
}

func BenchmarkWriteParallel(b *testing.B) {
	q := New(1024, func(v []int) error {
		return nil
	})
	defer q.Close()

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			q.Write(i)
			i++
		}
	})
	q.Flush()
}
