package mwsr

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Mwsr is an object able to accept multiple writes at the same time, and
// which will flush to a callback as soon as possible.
type Mwsr struct {
	// obj is a slice of objects written and pending flush
	obj []interface{}

	// pos holds the next write position
	pos uint32
	// lk is a lock used to prevent writes to run during flush
	lk sync.RWMutex

	// cd is used to wake up the flush routing
	cd *sync.Cond

	// callback performing the flush
	cb func([]interface{}) error

	// if an error happened, err will be set and Write will return immediately
	err error
}

// New will return a new Mwsr instance after starting a new gorouting to
// process writes in the background. If the callback returns an error, the
// goroutine will die and the Write method will return the error.
func New(siz int, cb func([]interface{}) error) *Mwsr {
	res := &Mwsr{
		obj: make([]interface{}, siz),
		cb:  cb,
	}
	// cond is created on the *write* side of the RWMutex
	res.cd = sync.NewCond(&res.lk)

	go res.processLoop()

	return res
}

// Write a single value to the queue. Multiple calls to Write can be performed
// from different threads and complete in parallel.
func (m *Mwsr) Write(v interface{}) error {
	m.lk.RLock()
	// we do not defer RUnlock() because we may switch to a write lock during the process

	if m.err != nil {
		// if m.err is set, it won't be modified, so thread safety is fine
		m.lk.RUnlock()
		return m.err
	}

	// generate new atomic value, check if it fits
	pos := atomic.AddUint32(&m.pos, 1) - 1
	if pos < uint32(len(m.obj)) {
		// it fits, store and return
		m.obj[pos] = v
		m.cd.Broadcast()
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

	// make sure we still don't have room in the queue (might have emptied between the locks)
	if m.pos >= uint32(len(m.obj)) {
		// the queue is indeed full, flush it now
		if err := m.doCallback(); err != nil {
			// an error occured, give up here
			m.err = err
			m.lk.Unlock()
			return err
		}
	}

	// add value to the queue now that there is room
	m.obj[m.pos] = v
	m.pos += 1 // no need for atomic since we hold the write lock

	m.cd.Broadcast()
	m.lk.Unlock()
	return nil
}

// Flush will force the queue to be flushed now, and return once the queue is
// empty.
func (m *Mwsr) Flush() error {
	m.processSingle()

	m.lk.RLock()
	defer m.lk.RUnlock()
	return m.err
}

// doCallback simply calls the callback after making sure the slice has the
// right size, then will reset the position. This needs to be called after m.lk
// has been acquired.
func (m *Mwsr) doCallback() (e error) {
	defer func() {
		// block panic to avoid deadlock
		if r := recover(); r != nil {
			e = fmt.Errorf("callback panic occured: %v", r)
		}
	}()

	pos := m.pos
	if pos > uint32(len(m.obj)) {
		pos = uint32(len(m.obj))
	}

	err := m.cb(m.obj[:pos])
	m.pos = 0
	return err
}

// processSingle performs a single call of the callback after obtaining the
// lock and checking no pending error exists.
func (m *Mwsr) processSingle() {
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
		m.cd.Broadcast()
	}
}

// processLoop is run in a gorouting by New() and will wait for activity, in
// order to call the callback method.
func (m *Mwsr) processLoop() {
	m.lk.Lock()
	defer m.lk.Unlock()

	for {
		if m.err != nil {
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
