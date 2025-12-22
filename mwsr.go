package mwsr

import (
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

// ErrClosed is returned when Write is called on a closed Mwsr.
var ErrClosed = errors.New("mwsr: queue is closed")

// Mwsr is an object able to accept multiple writes at the same time, and
// which will flush to a callback as soon as possible. Writes can continue
// while the callback is executing.
type Mwsr[T any] struct {
	// obj is a slice of objects written and pending flush
	obj []T

	// pos holds the next write position
	pos uint32

	// lk is a lock used to prevent writes to run during buffer swap
	//
	// multiple RLock can be acquired at the same time, this is used during Write()
	// only one routine can hold Lock, this is used during flush setup
	lk sync.RWMutex

	// cbLk ensures only one callback runs at a time
	cbLk sync.Mutex

	// cd is used to wake up the flush routine after writes
	cd *sync.Cond

	// callback performing the flush
	// it will always be called only once at a time
	cb func([]T) error

	// if an error happened, err will be set and Write will return immediately
	err error

	// closed indicates the queue has been closed
	closed bool

	// done is closed when the background goroutine exits
	done chan struct{}
}

// New will return a new Mwsr instance after starting a new goroutine to
// process writes in the background. If the callback returns an error, the
// goroutine will die and the Write method will return the error.
//
// The size parameter specifies the buffer size. If size < 1, it defaults to 1.
func New[T any](size int, cb func([]T) error) *Mwsr[T] {
	if size < 1 {
		size = 1
	}

	res := &Mwsr[T]{
		obj:  make([]T, size),
		cb:   cb,
		done: make(chan struct{}),
	}
	// cond is created on the *write* side of the RWMutex
	res.cd = sync.NewCond(&res.lk)

	go res.processLoop()

	return res
}

// Write a single value to the queue. Multiple calls to Write can be performed
// from different threads and complete in parallel, even while a flush callback
// is executing.
func (m *Mwsr[T]) Write(v T) error {
	m.lk.RLock()
	// we do not defer RUnlock() because we may switch to a write lock during the process

	if m.err != nil {
		// if m.err is set, it won't be modified, so thread safety is fine
		m.lk.RUnlock()
		return m.err
	}

	if m.closed {
		m.lk.RUnlock()
		return ErrClosed
	}

	// generate new atomic value, check if it fits
	pos := atomic.AddUint32(&m.pos, 1) - 1
	if pos < uint32(len(m.obj)) {
		// it fits, store and return
		m.obj[pos] = v
		m.cd.Signal()
		m.lk.RUnlock()
		return nil
	}

	// too full, attempt to process now. First, let's switch to write lock
	m.lk.RUnlock()
	m.lk.Lock()

	// did an error occur between the unlock/lock? return it
	if m.err != nil {
		m.lk.Unlock()
		return m.err
	}

	if m.closed {
		m.lk.Unlock()
		return ErrClosed
	}

	// make sure we have room in the queue, flushing as needed
	// (loop because doCallback releases lock, allowing other writes to fill the new buffer)
	for m.pos >= uint32(len(m.obj)) {
		if err := m.doCallback(); err != nil {
			// an error occurred, give up here
			m.err = err
			m.lk.Unlock()
			return err
		}
		// check for errors/closed that may have occurred while lock was released
		if m.err != nil {
			m.lk.Unlock()
			return m.err
		}
		if m.closed {
			m.lk.Unlock()
			return ErrClosed
		}
	}

	// add value to the queue now that there is room
	m.obj[m.pos] = v
	m.pos += 1 // no need for atomic since we hold the write lock

	m.cd.Signal()
	m.lk.Unlock()
	return nil
}

// Flush will force the queue to be flushed now, and return once the queue is
// empty.
func (m *Mwsr[T]) Flush() error {
	m.processSingle()

	m.lk.RLock()
	defer m.lk.RUnlock()
	return m.err
}

// Close stops the background goroutine and flushes any remaining items.
// After Close is called, all subsequent Write calls will return ErrClosed.
// Close is safe to call multiple times.
func (m *Mwsr[T]) Close() error {
	m.lk.Lock()

	if m.closed {
		m.lk.Unlock()
		// Wait for goroutine to finish if already closing
		<-m.done
		return m.err
	}

	m.closed = true

	// Flush remaining items if no error
	if m.err == nil && m.pos > 0 {
		if err := m.doCallback(); err != nil {
			m.err = err
		}
	}

	// Wake up the processLoop so it can exit
	m.cd.Signal()
	m.lk.Unlock()

	// Wait for the goroutine to finish
	<-m.done

	m.lk.RLock()
	err := m.err
	m.lk.RUnlock()
	return err
}

// doCallback prepares the buffer for flushing, releases the lock to allow
// writes during the callback, then calls the callback. The lock is re-acquired
// before returning. This must be called while holding m.lk.
func (m *Mwsr[T]) doCallback() (e error) {
	pos := m.pos
	if pos > uint32(len(m.obj)) {
		pos = uint32(len(m.obj))
	}
	if pos == 0 {
		return nil
	}

	// Take the current buffer for flushing
	toFlush := m.obj[:pos]
	bufSize := cap(m.obj)

	// Allocate a new buffer for incoming writes
	m.obj = make([]T, bufSize)
	m.pos = 0

	// Release main lock to allow writes during callback
	m.lk.Unlock()

	// Serialize callback execution (only one callback at a time)
	m.cbLk.Lock()

	// Call callback with panic recovery
	func() {
		defer func() {
			if r := recover(); r != nil {
				e = fmt.Errorf("callback panic occurred: %v", r)
			}
		}()
		e = m.cb(toFlush)
	}()

	m.cbLk.Unlock()

	// Re-acquire main lock before returning
	m.lk.Lock()

	// toFlush slice will be garbage collected

	return e
}

// processSingle performs a single call of the callback after obtaining the
// lock and checking no pending error exists.
func (m *Mwsr[T]) processSingle() {
	m.lk.Lock()
	defer m.lk.Unlock()

	if m.err != nil {
		return
	}

	if m.pos == 0 {
		return
	}

	if err := m.doCallback(); err != nil {
		m.err = err
	}
}

// processLoop is run in a goroutine by New() and will wait for activity, in
// order to call the callback method.
func (m *Mwsr[T]) processLoop() {
	defer close(m.done)

	m.lk.Lock()
	defer m.lk.Unlock()

	for {
		if m.err != nil || m.closed {
			return
		}

		if m.pos == 0 {
			m.cd.Wait()
			continue
		}

		if err := m.doCallback(); err != nil {
			m.err = err
			return
		}
	}
}
