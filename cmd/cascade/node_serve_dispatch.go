// Purpose: `cascade node serve`'s dispatch-leg collaborators, split out of
//   node_serve.go under Art.10.3's 300-line cap.
// Inputs: the node's resolved data directory and local identity.
// Outputs: the durable action log and the enrollment id the node's result
//   frames are bound to.
// Constraints: the action log is FILE-backed, under the node's own data
//   dir, because the dedup guarantee is about what survives a crash — a
//   redelivery arrives in exactly the window a crash opens, so an in-memory
//   log would dedup only when it is not needed.
// SPORT: cmd/cascade/node-serve-dispatch (ADD, P1-E17-W4-S37-T2).

package main

import "github.com/acamarata/cascade/internal/nodes"

// nodeDispatchLeg returns the node-side dispatch collaborators for dataDir.
func nodeDispatchLeg(dataDir string, self nodes.Identity) (nodes.ActionLog, string) {
	enrollment := nodes.DeriveEnrollmentID(nodes.DeviceRecord{
		NodeID:    self.NodeID,
		PubKeyB64: self.PubKeyB64(),
	})
	return nodes.NewFileActionLog(dataDir), enrollment
}
