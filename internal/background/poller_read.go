package background

import "context"

// readCycle performs one complete read pass across all configured nodes.
// It visits each node in input order, calling Reader.ReadNode with the node's
// previous position. Successful reads update only that node's position; errors
// leave it unchanged. FleetCursor is never read or modified.
func (p *Poller) readCycle(ctx context.Context) {
	if p.Positions == nil {
		p.Positions = make(map[string]string)
	}
	for _, nodeID := range p.Nodes {
		select {
		case <-ctx.Done():
			return
		default:
		}

		prevPos := p.Positions[nodeID]
		newPos, err := p.Reader.ReadNode(ctx, nodeID, prevPos)
		if err != nil {
			if p.Reporter != nil {
				p.Reporter.Unreadable(nodeID, err)
			}
			continue
		}
		p.Positions[nodeID] = newPos
	}
}
