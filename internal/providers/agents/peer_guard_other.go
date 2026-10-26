//go:build !linux && !darwin

package agents

// Purpose: no peer inspector off linux and darwin. The daemon does not run on
// windows, so AgentPeerGuard there refuses every peer rather than admit one it
// could not inspect.

import "github.com/acamarata/cascade/pkg/cascade"

// platformPeerInspector is KindUnsupported here.
func platformPeerInspector() (peerInspector, error) {
	return nil, cascade.New(cascade.KindUnsupported, "agents: the peer guard is not supported on this platform")
}
