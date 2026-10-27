// Purpose: the composition root for the first-party MCP tool set: the
// JSON-RPC method table those tools dispatch through, and the policy filter
// that decides which of them a client may see.
//
// Inputs: resolved paths and a clock. Outputs: an *rpc.Registry serving the
// methods internal/mcp/coretools names, a capability filter over a real
// policy engine, and the closer for the databases both opened.
//
// Constraints: platform-neutral (Art.5): `cascade mcp serve --stdio` is the
// surface the first-party harness client speaks on every OS, so this file may
// not depend on daemon_unix_run.go's !windows composition and calls the SAME
// internal/daemon and internal/memory registration functions that file does.
// Every failure degrades to a filter that exposes nothing rather than to a
// tool that answers wrongly.
//
// SPORT: cmd/cascade/mcp (ADD) — P1-E16-W4-S34-T2.
package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/acamarata/cascade/internal/ci"
	"github.com/acamarata/cascade/internal/client"
	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/principal"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// mcpToolSubject is the subject an MCP session acts as when the policy
// engine is asked whether a tool may be listed.
//
// One fixed subject, not a per-session identity: the MCP transport carries
// no authenticated principal, so a grant to it covers every MCP client of
// this machine, the same scope the harness's own configuration has.
var mcpToolSubject = policy.Subject{Kind: policy.SubjectUser, ID: "mcp"}

// mcpToolCapabilities are the capabilities the first-party tool set
// declares, seeded into the engine's registry so a grant for one of them
// is resolvable at all (an unregistered capability denies).
func mcpToolCapabilities() []policy.Capability {
	return []policy.Capability{
		{
			Name:          coretools.CapabilityContextRead,
			Desc:          "read the local knowledge base: retrieval, context assembly and harness context",
			DefaultPolicy: policy.ClassRead,
		},
		{
			Name: coretools.CapabilityMemoryRead,
			Desc: "read records from the memory store",
			// ClassRead: recalling a record changes nothing; the §5.15
			// ladder derives its rung from this class.
			DefaultPolicy: policy.ClassRead,
		},
		{
			Name: coretools.CapabilityMemoryWrite,
			Desc: "write and retire records in the memory store",
			// ClassWorkspaceMutation: remembering and forgetting change
			// files on this machine, none of which leaves the device.
			DefaultPolicy: policy.ClassWorkspaceMutation,
		},
		{
			Name: coretools.CapabilityPluginsRead,
			Desc: "search the plugin registry catalog",
			// ClassRead: a catalog search changes nothing, the same
			// reasoning as CapabilityMemoryRead above.
			DefaultPolicy: policy.ClassRead,
		},
		{
			Name:          coretools.CapabilityCIRead,
			Desc:          "read recorded CI results",
			DefaultPolicy: policy.ClassRead,
		},
		{
			Name: coretools.CapabilityCIRun,
			Desc: "run the local lint, test and build gate",
			// ClassLocalDev: it runs the operator's configured build and
			// test commands in the checkout and records the run locally.
			DefaultPolicy: policy.ClassLocalDev,
		},
	}
}

// mcpToolWiring is what buildMCPToolWiring produced: the method table the
// tools dispatch through, the filter that gates them, and the closer for
// everything both opened.
type mcpToolWiring struct {
	Methods *rpc.Registry
	Filter  mcp.CapabilityFilter
	Close   func()
}

// buildMCPToolWiring assembles both halves. It never returns an error:
// every failure here is a reason to expose FEWER tools, not to refuse to
// serve MCP at all.
func buildMCPToolWiring(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) mcpToolWiring {
	registry := rpc.NewRegistry()
	// The MCP tool process holds no custody session: every elevated verb is
	// refused before a handler runs, whether or not one is registered here.
	registry.Use(daemon.RefuseAllElevation(clock))
	// No paths means no store, index or grant file: an empty method table
	// and a filter that exposes nothing, every tool reported unservable.
	if paths == nil {
		return mcpToolWiring{Methods: registry, Filter: mcp.DenyAllFilter{}, Close: func() {}}
	}
	closers := registerMCPToolMethods(ctx, registry, paths, clock)
	filter, closeFilter := mcpToolFilter(paths, clock)
	closers = append(closers, closeFilter)
	return mcpToolWiring{
		Methods: registry,
		Filter:  filter,
		Close:   func() { runClosers(closers) },
	}
}

// registerMCPToolMethods binds every method coretools.Specs names onto
// registry, and returns the closers for the handles it opened. A failed
// registration is skipped, and its tool is then unservable and absent (see
// coretools.Registrations).
func registerMCPToolMethods(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider, clock runtime.Clock) []func() {
	var closers []func()
	memory.NewHandler(memory.NewFileStore(memoryStoreDir(paths), clock), clock).Register(registry)
	if db, err := daemon.RegisterContextAssembleHandler(registry, paths, clock); err == nil {
		closers = append(closers, func() { _ = db.Close() })
	}
	_ = daemon.RegisterContextSyncHandler(registry, paths, clock)
	if closer, err := registerMCPRecall(ctx, registry, paths); err == nil {
		closers = append(closers, closer)
	}
	// plugin.search: the SAME wiring function the daemon calls
	// (plugin_rpc.go's registerDBPathHandlers); it needs no closer.
	wirePluginSearchHandler(ctx, registry, paths, clock)
	// ci.run and ci.status: process-local, never daemon RPCs. They run over
	// the serve process's own paths and clock, in its working directory.
	ciDeps := ci.ProductionCmdDeps()
	ciDeps.Paths, ciDeps.Clock = paths, clock
	ci.RegisterMCPMethods(registry, ciDeps, os.Getwd)
	return closers
}

// registerMCPRecall binds recall.query over the SAME service composition
// resolveRecallEmbedded builds (recall_embedded.go) — one catalog, one
// vector leg with no embedder, one full-text leg over cascade.db — so the
// tool and `cascade recall` can never disagree about what one search
// means.
func registerMCPRecall(ctx context.Context, registry *rpc.Registry, paths runtime.PathProvider) (func(), error) {
	leg, closeStore, err := openEmbeddedFTSLeg(ctx, paths)
	if err != nil {
		return nil, err
	}
	catalog := recall.NewFileCatalog(filepath.Join(paths.DataDir(), "retrieval", recall.CatalogFileName))
	svc, err := recall.NewService(catalog, rrf.Params{}, fusion.NewVectorLeg(nil, nil, nil), leg)
	if err != nil {
		closeStore()
		return nil, err
	}
	recall.NewHandler(svc).Register(registry)
	return closeStore, nil
}

// mcpToolFilter builds the production capability filter and the closer
// for the grant store it reads through.
//
// Every failure returns mcp.DenyAllFilter: with no engine there is no
// basis on which to expose a capability-gated tool, and exposing one
// anyway is the single outcome this whole filter exists to prevent. That
// includes a config.toml that does not parse and a deny store that cannot
// be read: the operator's profile and deny rows decide what may be listed,
// so a process that cannot read them lists nothing. The store is NOT
// closed on the success path -- the engine reads grants through it on
// every Allow call, so it lives as long as the server does.
func mcpToolFilter(paths runtime.PathProvider, clock runtime.Clock) (mcp.CapabilityFilter, func()) {
	store, closeStore, err := openMCPPolicyStore(paths, clock)
	if err != nil {
		return mcp.DenyAllFilter{}, nil
	}
	filter := mcpPolicyFilter(context.Background(), store, clock, paths)
	if _, denied := filter.(mcp.DenyAllFilter); denied {
		closeStore()
		return filter, nil
	}
	return filter, closeStore
}

// mcpPolicyFilter is mcpToolFilter over an opened store: the capability
// filter over the MCP engine, or mcp.DenyAllFilter when the engine cannot be
// built (a config.toml that does not parse, a malformed [policy] section, an
// unreadable deny store, an audit log that will not open). The engine
// re-reads the deny rows on every decision rather than trusting a cache it
// does not own (defence in depth: an MCP server outlives the rows it started
// with).
func mcpPolicyFilter(ctx context.Context, store provider.Store, clock runtime.Clock, paths runtime.PathProvider) mcp.CapabilityFilter {
	engine, err := buildMCPPolicyEngine(ctx, store, clock, paths)
	if err != nil {
		return mcp.DenyAllFilter{}
	}
	return coretools.NewPolicyFilter(engine, mcpToolSubject)
}

// buildMCPPolicyEngine assembles the MCP tool engine over store through the
// one policy constructor: the first-party capability set, the durable
// grants, the stored deny list and the operator's [policy] profile from
// config.toml (read-only; a missing file is the default profile).
func buildMCPPolicyEngine(ctx context.Context, store provider.Store, clock runtime.Clock, paths runtime.PathProvider) (*policy.Engine, error) {
	cfg, err := loadPolicyConfig(ctx, paths)
	if err != nil {
		return nil, err
	}
	parts, err := newPolicyEngine(ctx, policyEngineInputs{
		Store: store, Clock: clock, Config: cfg, Capabilities: mcpToolCapabilities(), Paths: paths,
	})
	if err != nil {
		return nil, err
	}
	return withFreshDenyList(parts, store, nil), nil
}

// runClosers runs every closer, ignoring nils.
func runClosers(closers []func()) {
	for _, closer := range closers {
		if closer != nil {
			closer()
		}
	}
}

// applyPrincipalMode re-binds reg for a principal-bearing process. With no
// token it changes nothing (the owner view). With a token, every registered
// method with a declared access class is re-bound to scopedForwarder, every
// method without one refuses KindPermissionDenied, and a dial failure leaves
// the classed methods refusing KindUnavailable. The token is read once and
// bound into the client; a present but empty token refuses every classed
// method without dialing.
func applyPrincipalMode(reg *rpc.Registry, getenv func(string) string, dial func(token string) (*client.Client, error)) error {
	if reg == nil || getenv == nil || dial == nil {
		return cascade.New(cascade.KindInternal, "mcp: principal mode needs a registry, getenv and a dialer")
	}
	token := getenv(principal.EnvToken)
	if token == "" {
		return nil
	}
	var c *client.Client
	var dialErr error
	emptyToken := token == emptyTokenMarker
	if !emptyToken {
		c, dialErr = dial(token)
	}
	if !emptyToken && dialErr == nil && c == nil {
		dialErr = cascade.New(cascade.KindUnavailable, "mcp: daemon dialer returned no client")
	}
	for _, method := range reg.Methods() {
		switch {
		case !mcpAccessClassed(method):
			reg.Register(method, refuseUnclassed(method))
		case emptyToken:
			reg.Register(method, refuseWith(errEmptyToken))
		case dialErr != nil:
			reg.Register(method, refuseUnreachable(method, dialErr))
		default:
			reg.Register(method, scopedForwarder(c, method))
		}
	}
	return nil
}

// mcpAccessClassed reports whether method carries a caller-principal access
// declaration. It asks a scratch registry with the access policy on to admit
// a caller-less request: an undeclared or unknown class is refused as an
// internal error, a declared one either admits or refuses as permission
// denied. Reading the answer from the registry keeps the declarations the
// only source of truth.
func mcpAccessClassed(method string) bool {
	probe := rpc.NewRegistry()
	probe.SetAccessPolicy(nil)
	probe.Register(method, func(context.Context, json.RawMessage) (any, error) { return nil, nil })
	_, eo := probe.Dispatch(context.Background(), &rpc.Request{JSONRPC: "2.0", Method: method})
	if eo == nil {
		return true
	}
	data, _ := eo.Data.(map[string]string)
	return data["kind"] == cascade.KindPermissionDenied.String()
}
