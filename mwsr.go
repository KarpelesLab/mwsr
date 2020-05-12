package mwsr

import (
	"sync"
	"sync/atomic"
)

type Mwsr struct {
	pos uint32
	obj []interface{}
	lk  sync.RWMutex
	cd  *sync.Cond
	cb  func([]interface{}) error // callback
	err error
}

func New(siz int, cb func([]interface{}) error) *Mwsr {
	res := &Mwsr{
		obj: make([]interface{}, siz),
		cb:  cb,
	}
	res.cd = sync.NewCond(&res.lk)

	go res.processLoop()

	return res
}

func (m *Mwsr) Write(v interface{}) error {
	m.lk.RLock()
	defer m.lk.RUnlock()

	for {
		if m.err != nil {
			return m.err
		}

		pos := atomic.AddUint32(&m.pos, 1) - 1
		if pos >= uint32(len(m.obj)) {
			// too full, attempt to process now
			m.lk.RUnlock()

			m.processSingle()

			m.lk.RLock()
			continue
		}

		m.obj[pos] = v
		m.cd.Broadcast()
		return nil
	}
}

func (m *Mwsr) Flush() error {
	m.processSingle()

	m.lk.RLock()
	defer m.lk.RUnlock()
	return m.err
}

func (m *Mwsr) doCallback() error {
	pos := m.pos
	if pos > uint32(len(m.obj)) {
		pos = uint32(len(m.obj))
	}

	err := m.cb(m.obj[:pos])
	m.pos = 0
	return err
}

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
