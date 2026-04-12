// SPDX-License-Identifier: Apache-2.0
// Modifications by M/S Omukk

package port

import (
	"context"

	"github.com/rs/zerolog"
)

// If we want to create a listener/subscriber pattern somewhere else we should move
// from a concrete implementation to combination of generics and interfaces.

type ScannerSubscriber struct {
	logger   *zerolog.Logger
	filter   *ScannerFilter
	Messages chan ([]ConnStat)
	id       string
}

func NewScannerSubscriber(logger *zerolog.Logger, id string, filter *ScannerFilter) *ScannerSubscriber {
	return &ScannerSubscriber{
		logger:   logger,
		id:       id,
		filter:   filter,
		Messages: make(chan []ConnStat),
	}
}

func (ss *ScannerSubscriber) ID() string {
	return ss.id
}

func (ss *ScannerSubscriber) Destroy() {
	close(ss.Messages)
}

// Signal sends the (filtered) connection list to the subscriber. It respects
// ctx cancellation so the scanner goroutine is never stuck waiting for a
// consumer that has already exited.
func (ss *ScannerSubscriber) Signal(ctx context.Context, conns []ConnStat) {
	var payload []ConnStat

	if ss.filter == nil {
		payload = conns
	} else {
		filtered := []ConnStat{}
		for i := range conns {
			if ss.filter.Match(&conns[i]) {
				filtered = append(filtered, conns[i])
			}
		}
		payload = filtered
	}

	select {
	case ss.Messages <- payload:
	case <-ctx.Done():
	}
}
