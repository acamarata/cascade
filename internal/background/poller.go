// Package background provides polling infrastructure for fleet nodes.
package background

import (
	"context"
	"os"
	"strings"
	"time"
)

// PollClock abstracts ticker creation for testing.
type PollClock interface {
	// NewTicker creates a ticker that sends on the returned channel and can be stopped.
	NewTicker(time.Duration) (<-chan time.Time, func())
}

// PollNodeReader reads node state.
type PollNodeReader interface {
	// ReadNode fetches the current value for a node.
	ReadNode(context.Context, string, string) (string, error)
}

// PollReporter reports unreadable nodes.
type PollReporter interface {
	// Unreadable is called when a node cannot be read.
	Unreadable(nodeID string, err error)
}

// Poller polls nodes on a fixed interval using injected collaborators.
type Poller struct {
	// Clock provides ticker functionality.
	Clock PollClock
	// Reader reads node values.
	Reader PollNodeReader
	// Reporter reports errors.
	Reporter PollReporter
	// Nodes is the list of node IDs to poll.
	Nodes []string
	// Signals receives OS signals.
	Signals <-chan os.Signal
	// Positions tracks last-read positions per node.
	Positions map[string]string
	// FleetCursor tracks the fleet-level cursor.
	FleetCursor string
}

// Run starts the polling loop. It returns 1 on configuration errors and 0 on clean shutdown.
func (p *Poller) Run(ctx context.Context) int {
	if p == nil || ctx == nil || p.Clock == nil || p.Reader == nil || p.Reporter == nil {
		return 1
	}

	seen := make(map[string]bool, len(p.Nodes))
	for _, n := range p.Nodes {
		if strings.TrimSpace(n) == "" {
			return 1
		}
		if seen[n] {
			return 1
		}
		seen[n] = true
	}

	ch, stop := p.Clock.NewTicker(10 * time.Second)
	if ch == nil || stop == nil {
		return 1
	}
	defer stop()

	for {
		select {
		case _, ok := <-ch:
			if !ok {
				return 0
			}
			for _, node := range p.Nodes {
				pos := p.Positions[node]
				val, err := p.Reader.ReadNode(ctx, node, pos)
				if err != nil {
					p.Reporter.Unreadable(node, err)
					continue
				}
				if p.Positions == nil {
					p.Positions = make(map[string]string)
				}
				p.Positions[node] = val
			}
		case <-ctx.Done():
			return 0
		case <-p.Signals:
			return 0
		}
	}
}
