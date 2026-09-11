// Purpose: the render views node.go's verbs return through
//
//	internal/output.Writer.Result -- split out of node.go to stay under
//	the 300-line cap.
//
// SPORT: cmd/cascade/node (view types, P1-E17-W4-S36-T4).
package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/output"
	cruntime "github.com/acamarata/cascade/internal/runtime"
)

// nodeRowView is one device record rendered for `node list`/`node status`.
type nodeRowView struct {
	NodeID   string `json:"node_id"`
	Tier     string `json:"trust_tier"`
	Liveness string `json:"liveness"`
	Drained  bool   `json:"drained"`
	LastSeen string `json:"last_seen,omitempty"`
}

func newNodeRowView(rec nodes.DeviceRecord, clock cruntime.Clock) nodeRowView {
	live := nodes.ComputeLiveness(rec, clock.Now(), nodes.DefaultHeartbeatTimeout)
	v := nodeRowView{NodeID: rec.NodeID, Tier: string(rec.Tier), Liveness: string(live), Drained: rec.Drained}
	if !rec.LastSeen.IsZero() {
		v.LastSeen = rec.LastSeen.UTC().Format(time.RFC3339)
	}
	return v
}

func (v nodeRowView) String() string {
	return fmt.Sprintf("%-32s  tier=%-15s  liveness=%-10s  drained=%v", v.NodeID, v.Tier, v.Liveness, v.Drained)
}

// nodeListView renders `node list`'s full table.
type nodeListView struct {
	Nodes []nodeRowView `json:"nodes"`
}

func (v nodeListView) String() string {
	if len(v.Nodes) == 0 {
		return "no enrolled nodes"
	}
	lines := make([]string, len(v.Nodes))
	for i, n := range v.Nodes {
		lines[i] = n.String()
	}
	return strings.Join(lines, "\n")
}

// nodeStatusView renders `node status <id>`: the device record joined
// with liveness and the tunnel's in-process connection state.
//
// TUNNEL STATE (CONTRADICTION — full quote in the ticket journal). S-36.T3's
// internal/nodes.Manager tracks tunnel state ONLY in the memory of whichever
// process called Manager.Start; `node status` is a fresh, separate CLI
// process with no such Manager, and nothing in this tree persists tunnel
// state across processes or exposes it over an RPC this package may call
// (the cmd-rpc-server boundary again). Tunnel is therefore always
// "unknown (no in-process tunnel session)" here rather than a fabricated
// value; the field exists so a future ticket that adds a queryable daemon
// endpoint can fill it in without changing this view's shape.
type nodeStatusView struct {
	nodeRowView
	Tunnel string `json:"tunnel"`
}

func (v nodeStatusView) String() string {
	return v.nodeRowView.String() + fmt.Sprintf("  tunnel=%s", v.Tunnel)
}

func nodeOutputWriter(cmd *cobra.Command) *output.Writer {
	jsonOut, _ := cmd.Flags().GetBool("json")
	quiet, _ := cmd.Flags().GetBool("quiet")
	verbose, _ := cmd.Flags().GetBool("verbose")
	noColor, _ := cmd.Flags().GetBool("no-color")
	return output.New(cmd.OutOrStdout(), cmd.OutOrStderr(), jsonOut, quiet, verbose, noColor)
}
