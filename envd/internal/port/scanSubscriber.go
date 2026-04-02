// SPDX-License-Identifier: Apache-2.0

package port

import (
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

func (ss *ScannerSubscriber) Signal(conns []ConnStat) {
	// Filter isn't specified. Accept everything.
	if ss.filter == nil {
		ss.Messages <- conns
	} else {
		filtered := []ConnStat{}
		for i := range conns {
			// We need to access the list directly otherwise there will be implicit memory aliasing
			// If the filter matched a connection, we will send it to a channel.
			if ss.filter.Match(&conns[i]) {
				filtered = append(filtered, conns[i])
			}
		}
		ss.Messages <- filtered
	}
}
