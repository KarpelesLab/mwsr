package mwsr

import (
	"log"
	"sync"
	"sync/atomic"
	"testing"
)

func TestMwsr(t *testing.T) {
	log.Printf("testing")

	var check uint32

	q := New(128, func(v []interface{}) error {
		log.Printf("got values: %+v", v)
		for _, sub := range v {
			atomic.AddUint32(&check, uint32(sub.(int)))
		}
		return nil
	})

	// this will typically result in less than 10 calls to log.Printf
	var wg sync.WaitGroup
	wg.Add(10)

	for i := 0; i < 10; i++ {
		go func(i int) {
			q.Write(i)
			wg.Done()
		}(i)
	}

	wg.Wait()
	q.Flush()

	if check != 45 {
		t.Errorf("expected check=45, got check=%d", check)
	}

	// stress testing
	check = 0

	wg.Add(65536)
	for i := 0; i < 65536; i++ {
		go func(i int) {
			q.Write(i)
			wg.Done()
		}(i)
	}

	wg.Wait()
	q.Flush()

	if check != 2147450880 {
		t.Errorf("expected check=2147450880, got check=%d", check)
	}
}
