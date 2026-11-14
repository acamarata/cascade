// Purpose: wire the conductor delegate tools into the MCP composition: their
// capability, and the forwarding of their two methods to the daemon.
// Constraints: delegation runs in the daemon, so the serve process holds no
// in-process handler; it forwards with the session principal, the same path
// and wording the other scoped tools use, and refuses without a token.
package main

import (
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// The conductor delegate method names. They live in an untagged file so the
// MCP tool table compiles on every platform; the daemon handlers use them too.
const (
	delegateMethod     = "conductor.delegate"
	delegateWaitMethod = "conductor.delegate_wait"
)

// mcpToolCapabilities are every capability the first-party tool set declares.
func mcpToolCapabilities() []policy.Capability {
	return append(mcpBaseCapabilities(), policy.Capability{
		Name: coretools.CapabilityAgentsDelegate,
		Desc: "hand an intent to the conductor and read the outcome of the job",
		// ClassLocalDev: the conductor starts an agent on this machine.
		DefaultPolicy: policy.ClassLocalDev,
	})
}

var errMCPDelegatePrincipal = cascade.New(cascade.KindPermissionDenied,
	"mcp: delegation requires a caller principal token; refused")
