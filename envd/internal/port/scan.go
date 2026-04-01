// SPDX-License-Identifier: Apache-2.0

package port

import (
	"sync"
	"time"

	"github.com/rs/zerolog"
)

type Scanner struct {
	scanExit chan struct{}
	period   time.Duration

	// Plain mutex-protected map instead of concurrent-map. The concurrent-map
	// library's Items() spawns goroutines and uses a WaitGroup internally,
	// which corrupts Go runtime semaphore state across Firecracker snapshot/restore.
	mu   sync.RWMutex
	subs map[string]*ScannerSubscriber
}

func (s *Scanner) Destroy() {
	close(s.scanExit)
}

func NewScanner(period time.Duration) *Scanner {
	return &Scanner{
		period:   period,
		subs:     make(map[string]*ScannerSubscriber),
		scanExit: make(chan struct{}),
	}
}

func (s *Scanner) AddSubscriber(logger *zerolog.Logger, id string, filter *ScannerFilter) *ScannerSubscriber {
	subscriber := NewScannerSubscriber(logger, id, filter)

	s.mu.Lock()
	s.subs[id] = subscriber
	s.mu.Unlock()

	return subscriber
}

func (s *Scanner) Unsubscribe(sub *ScannerSubscriber) {
	s.mu.Lock()
	delete(s.subs, sub.ID())
	s.mu.Unlock()

	sub.Destroy()
}

// ScanAndBroadcast starts scanning open TCP ports and broadcasts every open port to all subscribers.
func (s *Scanner) ScanAndBroadcast() {
	for {
		// Read directly from /proc/net/tcp and /proc/net/tcp6 instead of
		// using gopsutil's net.Connections(), which walks /proc/{pid}/fd
		// and causes Go runtime corruption after Firecracker snapshot/restore.
		conns, _ := ReadTCPConnections()

		s.mu.RLock()
		for _, sub := range s.subs {
			sub.Signal(conns)
		}
		s.mu.RUnlock()

		select {
		case <-s.scanExit:
			return
		default:
			time.Sleep(s.period)
		}
	}
}
