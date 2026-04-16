// SPDX-License-Identifier: Apache-2.0

package handler

import (
	"sync"
	"sync/atomic"
)

type MultiplexedChannel[T any] struct {
	Source   chan T
	channels []chan T
	mu       sync.RWMutex
	exited   atomic.Bool
}

func NewMultiplexedChannel[T any](buffer int) *MultiplexedChannel[T] {
	c := &MultiplexedChannel[T]{
		channels: nil,
		Source:   make(chan T, buffer),
	}

	go func() {
		for v := range c.Source {
			c.mu.RLock()

			for _, cons := range c.channels {
				select {
				case cons <- v:
				default:
					// Consumer not reading — skip to prevent deadlock
				}
			}

			c.mu.RUnlock()
		}

		c.mu.Lock()
		c.exited.Store(true)
		for _, cons := range c.channels {
			close(cons)
		}
		c.mu.Unlock()
	}()

	return c
}

func (m *MultiplexedChannel[T]) Fork() (chan T, func()) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.exited.Load() {
		ch := make(chan T)
		close(ch)
		return ch, func() {}
	}

	consumer := make(chan T, 4096)

	m.channels = append(m.channels, consumer)

	return consumer, func() {
		m.remove(consumer)
	}
}

func (m *MultiplexedChannel[T]) remove(consumer chan T) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for i, ch := range m.channels {
		if ch == consumer {
			m.channels = append(m.channels[:i], m.channels[i+1:]...)

			return
		}
	}
}
