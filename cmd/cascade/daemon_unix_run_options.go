//go:build !windows

// Purpose: the optional contributions a caller makes to the RPC server
//
//	buildRPCServer builds — the rpcServerOption type, its constructors,
//	and the two helpers buildRPCServer reads them through.
//
// Inputs: whatever collaborator each constructor is handed (a policy
//
//	wiring, the status-widget dependencies, the controller-side tunnel
//	registry).
//
// Outputs: an rpcServerOption carrying a registration, a collaborator, or
//
//	neither.
//
// Constraints: split out of daemon_unix_run.go to stay under Art.10.3's
//
//	300-line cap. An option that only contributes a collaborator
//	registers nothing, and an option built from a nil collaborator
//	contributes nothing at all rather than a stand-in.
//
// SPORT: cmd/cascade/daemon (CHG) — P1-E17-W4-S37-T1.
package main

import (
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// rpcServerOption is one further registration buildRPCServer applies to
// the registry it just built. It is variadic rather than a parameter
// because the composition roots that have a policy engine to register are
// not the only callers of buildRPCServer, and a caller with nothing to add
// should not have to name it.
type rpcServerOption struct {
	// nodeTunnels is an optional collaborator consumed DURING wiring
	// (P1-E17-W4-S37-T1's node-placement seam), not a registration. It is
	// a separate field rather than a second option type because
	// buildRPCServer must read it before it wires conductor.execute,
	// while register runs against the finished registry.
	nodeTunnels nodes.TunnelStateLookup
	// register is the further registration to apply to the built
	// registry. Nil for an option that only contributes a collaborator.
	register func(*rpc.Registry) error
	// policyEngine is the process's one policy engine, carried so the MCP
	// tool registry can gate its first-party tools on the SAME engine
	// every other call site evaluates through (P1-E16-W4-S34-T2). It
	// rides on withPolicyHandlers rather than on an option of its own,
	// because a daemon with policy handlers and a daemon with a policy
	// engine are the same daemon.
	policyEngine coretools.Evaluator
}

// withPolicyHandlers registers the approval/policy method set built by
// wirePolicy. A nil wiring registers nothing: the verbs are simply absent
// at the far end, which is what a caller that never built a policy engine
// should present, rather than verbs backed by nothing.
func withPolicyHandlers(pol *policyWiring) rpcServerOption {
	opt := rpcServerOption{register: func(registry *rpc.Registry) error {
		if pol == nil || len(pol.Handlers) == 0 {
			return nil
		}
		return daemon.RegisterPolicyHandlers(registry, pol.Handlers)
	}}
	if pol != nil && pol.Engine != nil {
		opt.policyEngine = pol.Engine
	}
	return opt
}

// withNodePlacement supplies the live controller-side tunnel registry as
// the placement engine's connection source (P1-E17-W4-S37-T1). It
// registers nothing: it hands wireConductorAndReachability the one input
// it cannot build for itself, because tunnel state lives in the memory of
// the process that holds the Manager and nowhere else.
//
// A nil manager yields a nil lookup, which the placement engine reads as
// "no connection source wired" and which therefore places NOTHING — the
// fail-closed reading, and one the resulting error names in as many words
// rather than reporting every node as merely disconnected.
func withNodePlacement(tunnels *nodes.Manager) rpcServerOption {
	if tunnels == nil {
		return rpcServerOption{}
	}
	return rpcServerOption{nodeTunnels: func(nodeID string) nodes.TunnelState {
		state, _ := tunnels.State(nodeID)
		return state
	}}
}

// withStatusWidgetHandler registers status.widget (P1-E38-W8-S74-T1) the
// same optional-registration way withPolicyHandlers does — added as an
// rpcServerOption rather than a new wireFleetAndNodeHandlers parameter so
// this ticket touches none of that function's other call sites (its own
// tests included), matching R-16.79's "smallest real change, not a
// signature ripple" precedent. showProjectNames is read once, at daemon
// startup, from the already-loaded *runtime.Config
// (platformDaemonRun's own cfg) — see status_widget.go's own doc comment
// for why no live config-reload subscription reaches this composition
// path today (the same disclosed gap [logging]'s hot keys are the one
// exception to, via LogProvider.SetLevel/Reconfigure).
func withStatusWidgetHandler(store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, showProjectNames bool) rpcServerOption {
	return rpcServerOption{register: func(registry *rpc.Registry) error {
		_, err := daemon.RegisterStatusWidgetHandler(registry, store, clock, bus, paths, func() bool { return showProjectNames })
		return err
	}}
}

// nodeTunnelLookup returns the last node-tunnel lookup any option
// supplied, or nil when none did.
//
// Last wins rather than first purely so a caller that appends an override
// after a default gets the override; no production call site passes two,
// and a nil result is a documented, fail-closed state (see
// withNodePlacement), not a missing configuration to guess around.
func nodeTunnelLookup(opts []rpcServerOption) nodes.TunnelStateLookup {
	var lookup nodes.TunnelStateLookup
	for _, opt := range opts {
		if opt.nodeTunnels != nil {
			lookup = opt.nodeTunnels
		}
	}
	return lookup
}

// applyServerOptions runs each option's registration against the built
// registry, skipping the options that contribute a collaborator only (see
// rpcServerOption). Factored out of buildRPCServer purely to keep that
// function under Art.10.3's 50-line cap, the same reason
// registerDBPathHandlers is.
func applyServerOptions(registry *rpc.Registry, opts []rpcServerOption) error {
	for _, opt := range opts {
		if opt.register == nil {
			continue
		}
		if err := opt.register(registry); err != nil {
			return err
		}
	}
	return nil
}
