// Package background provides polling infrastructure.
package background

import "context"

// ReadNodeFunc reads events for a node starting from the given position.
// It must respect context cancellation and return immediately without blocking.
type ReadNodeFunc func(ctx context.Context, nodeID string, pos string) (string, error)

// PollerConfig holds configuration for a Poller read cycle.
type PollerConfig struct {
	// Nodes lists node identifiers to poll in input order.
	Nodes []string
}

// PollerState holds mutable state across read cycles.
type PollerState struct {
	// Positions tracks per-node cursors. Nil map is initialized after validation.
	Positions map[string]string
	// FleetCursor is opaque compatibility data; never read or change it.
	FleetCursor string
}

// RunPoller executes one read cycle over all configured nodes.
// Each node is visited in input order. A successful read replaces only that
// node's position (including valid empty-string cursors); failure leaves it
// unchanged. Missing entries default to "". Unlisted positions are preserved.
func RunPoller(ctx context.Context, cfg PollerConfig, st *PollerState, r ReadNodeFunc) {
	if r == nil {
		return
	}
	if len(cfg.Nodes) == 0 {
		return
	}
	if st.Positions == nil {
		st.Positions = make(map[string]string)
	}
	for _, id := range cfg.Nodes {
		if ctx.Err() != nil {
			return
		}
		pos, ok := st.Positions[id]
		if !ok {
			pos = ""
		}
		next, err := r(ctx, id, pos)
		if err != nil {
			continue
		}
		st.Positions[id] = next
	}
}
