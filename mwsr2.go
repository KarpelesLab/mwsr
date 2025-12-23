package mwsr

import (
	"fmt"
	"sync"
	"sync/atomic"
)

// Buffer is a synchronous multi-writer buffer that batches writes and flushes
// them via a callback. Unlike AsyncBuffer, it has no background goroutine and
// flushes occur inline during Write calls or explicit Flush calls.
//
// Buffer is safe for concurrent use. Multiple goroutines can call Write
// simultaneously, and data is copied into the internal buffer to avoid
// retaining references to caller-provided slices.
//
// When T is byte, Buffer[byte] implements io.Writer.
type Buffer[T any] struct {
	// obj is a slice of objects written and pending flush
	obj []T

	// pos holds the next write position
	pos uint32
	siz uint32

	// lk is a lock used to prevent writes to run during flush
	//
	// multiple RLock can be acquired at the same time, this is used during Write()
	// only one routine can hold Lock, this is used during flush
	lk sync.RWMutex

	// callback performing the flush
	// it will always be called only once at a time
	cb func([]T) error

	// if an error happened, err will be set and Write will return immediately
	err error
}

// NewBuffer creates a new Buffer with the specified size and flush callback.
// The callback is invoked whenever the buffer is flushed, receiving the
// accumulated data. If the callback returns an error, all subsequent writes
// will fail with that error.
func NewBuffer[T any](ln uint32, cb func([]T) error) *Buffer[T] {
	res := &Buffer[T]{
		obj: make([]T, ln),
		siz: ln,
		cb:  cb,
	}
	return res
}

// Write appends data to the buffer. If the data fits, it is copied into the
// buffer. If the buffer would overflow, it is flushed first. If the data is
// larger than the buffer size, the buffer is flushed and the data is passed
// directly to the callback.
//
// Write is safe for concurrent use. Data is copied, so the caller may reuse
// the slice immediately after Write returns.
//
// For Buffer[byte], this method satisfies io.Writer.
func (mwsr *Buffer[T]) Write(p []T) (int, error) {
	ln := uint32(len(p))
	if ln >= mwsr.siz {
		// need to switch to full blocking write
		mwsr.lk.Lock()
		defer mwsr.lk.Unlock()
		mwsr.flushLockedAndBuf(p)
		return int(ln), mwsr.err
	}

	// Buffer[byte] will comply with io.Writer
	mwsr.lk.RLock()
	if ln == 0 || mwsr.err != nil {
		defer mwsr.lk.RUnlock() // defer, so we unlock after reading mwsr.err
		return 0, mwsr.err
	}
	wpose := atomic.AddUint32(&mwsr.pos, ln)
	if wpose <= mwsr.siz {
		defer mwsr.lk.RUnlock()
		// it fits!
		wpos := wpose - ln
		copy(mwsr.obj[wpos:], p)
		return int(ln), mwsr.err
	}

	// would go over end - give back the space we reserved
	atomic.AddUint32(&mwsr.pos, ^(ln - 1))
	mwsr.lk.RUnlock()
	mwsr.lk.Lock()
	defer mwsr.lk.Unlock()

	if mwsr.pos+ln <= mwsr.siz {
		// was flushed by someone else
		wpos := mwsr.pos
		mwsr.pos += ln
		copy(mwsr.obj[wpos:], p)
		return int(ln), mwsr.err
	}

	mwsr.flushLockedAndBuf(p)
	return int(ln), mwsr.err
}

// WriteV writes multiple slices as a single atomic operation, guaranteeing
// that the slices appear in order in the output without interleaving from
// concurrent writers. This is useful for scatter-gather I/O patterns where
// multiple buffers must be written together (e.g., HTTP headers + body).
//
// WriteV is designed for zero additional allocations in the fast path when
// all data fits in the buffer. Data is copied, so callers may reuse slices
// immediately after WriteV returns.
//
// If the total size fits in the buffer, all slices are copied atomically.
// Otherwise, each slice is processed individually, flushing as needed while
// maintaining order within this WriteV call.
func (mwsr *Buffer[T]) WriteV(p [][]T) (int, error) {
	var ln uint32
	for _, b := range p {
		ln += uint32(len(b))
	}

	if ln <= mwsr.siz {
		// attempt to add to buffer
		mwsr.lk.RLock()
		if ln == 0 || mwsr.err != nil {
			defer mwsr.lk.RUnlock() // defer, so we unlock after reading mwsr.err
			return 0, mwsr.err
		}
		wpose := atomic.AddUint32(&mwsr.pos, ln)
		if wpose <= mwsr.siz {
			defer mwsr.lk.RUnlock()
			// it fits!
			wpos := wpose - ln
			for _, b := range p {
				copy(mwsr.obj[wpos:], b)
				wpos += uint32(len(b))
			}
			return int(ln), mwsr.err
		}
		// couldn't add to buffer - give back the space we reserved
		atomic.AddUint32(&mwsr.pos, ^(ln - 1))
		mwsr.lk.RUnlock()
	}

	// need to switch to full blocking write
	mwsr.lk.Lock()
	defer mwsr.lk.Unlock()

	var wln int // write len

	if mwsr.err != nil {
		return 0, mwsr.err
	}

	for _, b := range p {
		sln := uint32(len(b))
		if sln == 0 {
			continue
		}
		// case 1: b fits at end of mwsr.buf
		if mwsr.pos+sln <= mwsr.siz {
			wpos := mwsr.pos
			mwsr.pos += sln
			copy(mwsr.obj[wpos:], b)
			wln += int(sln)
			continue
		}
		// trigger flush, add b to buffer (or flush it immediately if too big), continue
		mwsr.flushLockedAndBuf(b)
		wln += int(sln)
		if mwsr.err != nil {
			// an error happened? stop the loop
			break
		}
	}
	return wln, mwsr.err
}

// Flush forces the buffer to be flushed immediately, invoking the callback
// with any pending data. If the buffer is empty, no callback is made.
// Returns any error from the callback or any previous error.
func (mwsr *Buffer[T]) Flush() error {
	mwsr.lk.Lock()
	defer mwsr.lk.Unlock()

	mwsr.flushLockedAndBuf(nil)
	return mwsr.err
}

// flushLockedAndBuf flush buffer, and fill it with buf if buf fits
// if buf doesn't fit, it's flushed immedately too
// call with a nil param to ensure buffer flush
func (mwsr *Buffer[T]) flushLockedAndBuf(buf []T) {
	if mwsr.err != nil {
		// fail
		return
	}
	defer func() {
		if e := recover(); e != nil {
			mwsr.err = fmt.Errorf("callback panic: %v", e)
		}
	}()
	// at this point, mwsr.lk is already fully locked
	if mwsr.pos > 0 {
		// flush
		pos := mwsr.pos
		err := mwsr.cb(mwsr.obj[:pos])
		// Clear slice elements to allow garbage collection
		clear(mwsr.obj[:pos])
		mwsr.pos = 0
		if err != nil {
			mwsr.err = err
		}
	}
	blen := uint32(len(buf))
	if blen > 0 {
		if blen < mwsr.siz {
			// fits in, copy it
			copy(mwsr.obj, buf)
			mwsr.pos = blen
		} else {
			// flush buf immediately
			err := mwsr.cb(buf)
			if err != nil {
				mwsr.err = err
			}
		}
	}
}
