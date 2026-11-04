//go:build !windows

// Purpose: registers the mcp.tool.* and mcp.profile.* capabilities in the
// daemon's capability registry and mounts mcp.capability.check over the
// daemon's one policy engine.
// Inputs: w.Deps.Policy (set by composeDaemon from wirePolicy).
// Outputs: one capability per mcp.CompactWriteToolNames member, mcp.profile.compact-write,
// mcp.profile.full, and one registered method.
// Constraints: an identical capability already registered is accepted; a
// different one under the same name fails the wiring. A wiring without a
// policy engine (a test-built registry) mounts a handler over a nil engine,
// which answers deny for a principal-less caller and refuses otherwise.
// SPORT: cmd/cascade mcp profile capability wiring (P1-HRN-05).
package main

import (
	"context"

	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/pkg/cascade"
)

// mcpCapabilityAskClass is the class the contract calls ClassAsk: a write
// that stays on this machine, which the default profile asks for rather
// than allows unless a standing grant covers it.
const mcpCapabilityAskClass = policy.ClassWorkspaceMutation

var _ = registerDaemonWiring(daemonRegistration{
	Name: "mcp-profile-capabilities", Phase: phaseCore, Order: 70,
	Wire: func(w *daemonWiring) error {
		pol := w.Deps.Policy
		if pol == nil || pol.Engine == nil || pol.Registry == nil {
			mountMCPCapabilityCheck(w.Registry, nil)
			return nil
		}
		if err := registerMCPProfileCapabilities(w.Ctx, pol.Registry); err != nil {
			return err
		}
		mountMCPCapabilityCheck(w.Registry, pol.Engine)
		return nil
	},
})

// mcpProfileCapabilities is every capability this wiring registers.
func mcpProfileCapabilities() []policy.Capability {
	out := make([]policy.Capability, 0, len(mcp.CompactWriteToolNames)+2)
	for _, name := range mcp.CompactWriteToolNames {
		class := mcpCapabilityAskClass
		if name == "cascade_ci_status" {
			class = policy.ClassLocalDev
		}
		out = append(out, policy.Capability{
			Name: mcpToolCapabilityPrefix + name, Desc: "call the MCP tool " + name, DefaultPolicy: class,
		})
	}
	return append(out,
		policy.Capability{Name: mcpProfileCapabilityPrefix + "compact-write",
			Desc: "serve the compact-write MCP tool profile", DefaultPolicy: mcpCapabilityAskClass},
		policy.Capability{Name: mcpProfileCapabilityPrefix + "full",
			Desc: "serve the full MCP tool profile", DefaultPolicy: mcpCapabilityAskClass},
	)
}

// registerMCPProfileCapabilities adds each capability to reg, accepting one
// that is already registered with identical fields.
func registerMCPProfileCapabilities(ctx context.Context, reg *policy.MemoryRegistry) error {
	for _, c := range mcpProfileCapabilities() {
		err := reg.Add(ctx, c)
		if err == nil {
			continue
		}
		if k, ok := cascade.KindOf(err); !ok || k != cascade.KindConflict {
			return err
		}
		existing, lerr := reg.Lookup(ctx, c.Name)
		if lerr != nil {
			return lerr
		}
		if existing != c {
			return cascade.Newf(cascade.KindConflict,
				"mcp profile wiring: capability %q is registered with different fields", c.Name)
		}
	}
	return nil
}
