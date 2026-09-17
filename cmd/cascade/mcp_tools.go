// Purpose: the composition root for the first-party MCP tool set
//
//	(P1-E16-W4-S34-T2, R-14.251) — the JSON-RPC method table those tools
//	dispatch through, and the policy filter that decides which of them a
//	client may see.
//
// Inputs: resolved paths and a clock.
// Outputs: an *rpc.Registry serving the methods internal/mcp/coretools
//
//	names, a capability filter over a real policy engine, and the closer
//	for the databases both opened.
//
// Constraints: platform-neutral (Art.5). `cascade mcp serve --stdio` is
//
//	the surface the first-party harness client speaks to on every OS, so
//	this file may not
//	depend on daemon_unix_run.go's !windows composition and instead calls
//	the SAME internal/daemon and internal/memory registration functions
//	that file does. Every failure degrades to a filter that exposes
//	nothing rather than to a tool that answers wrongly.
//
// SPORT: cmd/cascade/mcp (ADD) — P1-E16-W4-S34-T2.
package main

import (
	"context"
	"path/filepath"

	"github.com/acamarata/cascade/internal/daemon"
	"github.com/acamarata/cascade/internal/mcp"
	"github.com/acamarata/cascade/internal/mcp/coretools"
	"github.com/acamarata/cascade/internal/memory"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/retrieval/fusion"
	"github.com/acamarata/cascade/internal/retrieval/recall"
	"github.com/acamarata/cascade/internal/retrieval/rrf"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// mcpToolSubject is the subject an MCP session acts as when the policy
// engine is asked whether a tool may be listed.
//
// It is one fixed subject, not a per-session identity, and that is a
// stated limit rather than an oversight: the MCP transport carries no
// authenticated principal, so there is nobody else this process could
// honestly name. Granting a capability to this subject grants it to every
// MCP client of this machine, which is the same scope the harness's own
// configuration already has.
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
			// ClassRead, not ClassLocalDev: recalling a record changes
			// nothing, and the rung the §5.15 ladder derives from the
			// class is what decides whether an autonomous loop may
			// proceed past it without a human turn.
			DefaultPolicy: policy.ClassRead,
		},
		{
			Name: coretools.CapabilityMemoryWrite,
			Desc: "write and retire records in the memory store",
			// ClassWorkspaceMutation: remembering and forgetting change
			// files on this machine. Not ClassExternalSideEffect — the
			// store never leaves the device — and not ClassLocalDev,
			// which is for build and test work that leaves no record.
			DefaultPolicy: policy.ClassWorkspaceMutation,
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

// buildMCPToolWiring assembles both halves.
//
// It never returns an error. Every failure here is a reason to expose
// FEWER tools, not a reason to refuse to serve MCP at all: a client that
// cannot list cascade_memory_recall because the store would not open is
// in a worse position than one that can, but it is in a far better
// position than one whose whole server failed to start.
func buildMCPToolWiring(ctx context.Context, paths runtime.PathProvider, clock runtime.Clock) mcpToolWiring {
	registry := rpc.NewRegistry()
	// No paths means no data directory, which means no store, no index
	// and no grant file. The honest wiring for that process is an empty
	// method table and a filter that exposes nothing — every tool is then
	// reported as unservable, which is exactly what it is.
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
// registry, and returns the closers for the handles it opened.
//
// A registration that fails is SKIPPED rather than fatal, and the tool
// that needed it is then unservable and simply absent — see
// coretools.Registrations, which registers no tool for an unserved
// method.
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
// anyway is the single outcome this whole filter exists to prevent. The
// store is NOT closed on the success path — the engine reads grants
// through it on every Allow call, so it lives as long as the server does.
func mcpToolFilter(paths runtime.PathProvider, clock runtime.Clock) (mcp.CapabilityFilter, func()) {
	store, closeStore, err := openMCPPolicyStore(paths, clock)
	if err != nil {
		return mcp.DenyAllFilter{}, nil
	}
	engine, err := buildMCPPolicyEngine(store, clock)
	if err != nil {
		closeStore()
		return mcp.DenyAllFilter{}, nil
	}
	return coretools.NewPolicyFilter(engine, mcpToolSubject), closeStore
}

// buildMCPPolicyEngine assembles the engine over store: a memory
// capability registry seeded with the three capabilities the first-party
// tools declare, the durable grant store, and a controller at its default
// autonomy profile.
func buildMCPPolicyEngine(store provider.Store, clock runtime.Clock) (*policy.Engine, error) {
	registry := policy.NewMemoryRegistry()
	for _, capability := range mcpToolCapabilities() {
		if err := registry.Add(context.Background(), capability); err != nil {
			return nil, err
		}
	}
	grants, err := policy.NewStoreGrants(store, registry, clock)
	if err != nil {
		return nil, err
	}
	return policy.NewEngine(registry, grants, policy.NewController(nil))
}

// runClosers runs every closer, ignoring nils.
func runClosers(closers []func()) {
	for _, closer := range closers {
		if closer != nil {
			closer()
		}
	}
}
