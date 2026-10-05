//go:build !windows

// Purpose: the optional contributions a caller makes to the RPC server
//
//	buildRPCServer builds — the rpcServerOption type, its constructors,
//	and the two helpers buildRPCServer reads them through.
//
// Inputs: whatever collaborator each constructor is handed (a policy
//
//	wiring, the controller-side tunnel registry).
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
	"context"
	"database/sql"
	"net/http"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
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
	// observe, when non-nil, is handed the finished registry and events mux
	// once wiring completes. It registers nothing and is the one seam that
	// lets the composition tests read what composeDaemon built.
	observe registryObserver
	// runtime carries the values composeDaemon holds that registrations read
	// through daemonWiring.Deps. The zero value (a test-built server) is valid.
	runtime *daemonRuntime
	// runCtx is the daemon's one run context (withRunContext): every
	// registration reads it as daemonWiring.Ctx, so nothing buildRPCServer
	// reaches runs under context.Background().
	runCtx context.Context
	// manifest is composeDaemon's one Manifest (withManifest): status.get
	// reports it and GoSupervised registers on it, so the two never diverge.
	manifest *daemon.Manifest
	// bridgeHTTPClient is the bridge poll's HTTP client
	// (withBridgeTransport). nil, every production call, keeps the
	// telegram module's own default client.
	bridgeHTTPClient *http.Client
}

// withRunContext hands registrations the daemon's run context as
// daemonWiring.Ctx. composeDaemon derives it with context.WithCancel and
// cancels it before joining the supervised goroutines.
func withRunContext(runCtx context.Context) rpcServerOption {
	return rpcServerOption{runCtx: runCtx}
}

// withManifest hands registrations composeDaemon's Manifest as
// daemonWiring.Manifest, the same one wireBackgroundSubsystems already
// started its supervised consumers on.
func withManifest(manifest *daemon.Manifest) rpcServerOption {
	return rpcServerOption{manifest: manifest}
}

// seedWiringFromOptions overrides w.Ctx and w.Manifest with the last run
// context and Manifest the options supplied. A test-built server supplies
// neither: it keeps the literal's background context and gets a fresh
// Manifest over its own logger and clock.
func seedWiringFromOptions(w *daemonWiring) *daemonWiring {
	for _, opt := range w.Opts {
		if opt.runCtx != nil {
			w.Ctx = opt.runCtx
		}
		if opt.manifest != nil {
			w.Manifest = opt.manifest
		}
	}
	if w.Manifest == nil {
		w.Manifest = daemon.NewManifest(w.Logger, w.Clock)
	}
	return w
}

// bridgeRoundTrip answers one bridge HTTP request from its method and URL
// path alone. Returning an error fails the request the way a transport
// failure would; withBridgeTransport is its only consumer.
type bridgeRoundTrip func(ctx context.Context, method, path string) error

// RoundTrip implements http.RoundTripper over the func: it never dials, and
// it always fails the request, with the func's error or a refusal.
func (f bridgeRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := f(req.Context(), req.Method, req.URL.Path); err != nil {
		return nil, err
	}
	return nil, cascade.New(cascade.KindUnavailable, "bridge transport: no response recorded")
}

// withBridgeTransport routes the bridge poll's HTTP traffic through fn
// instead of the network. It is the seam the daemon tests record the poll
// through: an untagged test may not import net/http (the no-network unit
// test rule), so the client is built here from a plain func.
func withBridgeTransport(fn bridgeRoundTrip) rpcServerOption {
	return rpcServerOption{bridgeHTTPClient: &http.Client{Transport: fn}}
}

// bridgeHTTPClientFrom returns the last bridge HTTP client an option
// supplied, or nil (the telegram default) when none did.
func bridgeHTTPClientFrom(opts []rpcServerOption) *http.Client {
	var client *http.Client
	for _, opt := range opts {
		if opt.bridgeHTTPClient != nil {
			client = opt.bridgeHTTPClient
		}
	}
	return client
}

// registryObserver receives the finished RPC registry and GET /events mux.
type registryObserver func(registry *rpc.Registry, events *rpc.SSEMux)

// withDaemonRuntime hands composeDaemon's held values to the registrations as
// daemonWiring.Deps.
func withDaemonRuntime(cfg *runtime.Config, rawDB *sql.DB, pol *policyWiring, logProvider *runtime.LogProvider, deps daemonDeps) rpcServerOption {
	return rpcServerOption{runtime: &daemonRuntime{Config: cfg, RawDB: rawDB, Policy: pol, LogProvider: logProvider, Daemon: deps}}
}

// daemonRuntimeFromOptions returns the last runtime an option supplied, or the
// zero value when none did.
func daemonRuntimeFromOptions(opts []rpcServerOption) daemonRuntime {
	var rt daemonRuntime
	for _, opt := range opts {
		if opt.runtime != nil {
			rt = *opt.runtime
		}
	}
	return rt
}

// withRegistryObserver hands the finished registry and events mux to observe.
// A nil observe contributes nothing, which is every production call.
func withRegistryObserver(observe registryObserver) rpcServerOption {
	return rpcServerOption{observe: observe}
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
