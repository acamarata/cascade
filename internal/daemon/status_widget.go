package daemon

// Purpose: T0-daemon-composition-root — the status.widget JSON-RPC 2.0
// method (P1-E38-W8-S74-T1): composes a capacity.WidgetSnapshot from a
// real, owned capacity.Compositor (P1-E31-W6-S63-T1) plus the attention
// queue (P1-E18-W4-S39-T1) and jobs domain (P1-E29-W6-S59-T1,
// status_widget_jobs.go) counts, mirroring attention_rpc.go's
// RegisterFleetAttentionHandler/supervisor_rpc.go's RegisterSupervisorHandler
// precedent exactly: the composition logic (building real domain sources
// from the already-open provider.Store/clock/bus/paths) lives in this
// package, cmd/cascade only calls it with what buildRPCServer already has
// open.
//
// CONTRACT DEVIATION (files_scope, recorded — R-16.79, same class of
// finding this ticket's own BLOCKED-P1-E38-W8-S74-T1.md journal and
// AGENT-BRIEF already name). The ticket's files_scope.change names
// internal/daemon/server.go, which does not exist anywhere in this tree
// (confirmed by this ticket's own prior BLOCKED pass). The real
// registration site is internal/rpc.Registry.Register, consumed via a
// registerXHandler function in this package and called from
// cmd/cascade/daemon_unix_run_fleetjobs.go's wireFleetAndNodeHandlers —
// this file's own RegisterStatusWidgetHandler follows that exact,
// already-landed pattern.
//
// CONTRACT DEVIATION (scope resolution, recorded). The ticket's HOW
// section says the handler "resolves the caller scope from the
// connection identity established by D/S-06.T3". internal/rpc/handler.go
// (D/S-06.T3's real deliverable) resolves exactly one fact from a
// connection: whether its peer UID equals the daemon owner's — a boolean
// gate, not a session/task/project scope. No mechanism anywhere in this
// tree derives a SessionScopeRef (a type that also does not exist; the
// real type is supervision.ScopeRef, i.e. scope.Ref) from a unix-socket
// peer credential. internal/fleet/supervision's OWN fleet.attention.list
// RPC (rpc.go) requires own_scope as an EXPLICIT, caller-supplied,
// REQUIRED param for exactly this reason ("own_scope is required" —
// there is no connection-derived fallback even there). This daemon has
// exactly one owner and no per-connection session identity, so when
// status.widget's optional scope param is absent, defaultWidgetScope()
// resolves it to scope.ScopeKindGlobal — the widest defined ScopeKind,
// matching supervision.ResolveVisibleScopes's own nil-graph-store
// fallback shape (a one-element scope list) rather than inventing a new
// resolution mechanism.
//
// SPORT: daemon.status_widget (ADD, P1-E38-W8-S74-T1).

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/capacity"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MethodStatusWidget is the status.widget JSON-RPC 2.0 method name.
const MethodStatusWidget = "status.widget"

// widgetCompositorTTL bounds how long a provider/node slot may go without
// a fresh Update* call before Snapshot reports it Unknown — see
// buildSnapshot below, which calls UpdateNodes on every request (a pull
// model, matching internal/daemon/supervisor_rpc.go's own "read fresh
// from the store on every call" precedent), so this TTL only matters
// between the pull and the read within the same request.
const widgetCompositorTTL = time.Minute

// statusWidgetParams is status.widget's params shape: {scope: optional}.
type statusWidgetParams struct {
	Scope *supervision.ScopeRef `json:"scope"`
}

// defaultWidgetScope is the fallback own-scope used when a request omits
// scope — see this file's header CONTRACT DEVIATION note.
func defaultWidgetScope() supervision.ScopeRef {
	return supervision.ScopeRef{Kind: scope.ScopeKindGlobal}
}

// resolveWidgetScope returns p's scope, or the default when absent.
func resolveWidgetScope(p *supervision.ScopeRef) supervision.ScopeRef {
	if p != nil {
		return *p
	}
	return defaultWidgetScope()
}

// StatusWidgetDeps bundles the real sources statusWidgetHandler and
// emitStatusWidgetChanged (status_widget_sse.go) both read through. The
// zero value is not usable; construct via RegisterStatusWidgetHandler.
type StatusWidgetDeps struct {
	comp             *capacity.Compositor
	providerSrc      capacity.ProviderSource // nil: disclosed gap, see RegisterStatusWidgetHandler
	nodeSrc          capacity.NodeSource
	attention        *supervision.Store
	activeJobsCount  func(context.Context) (*int, error)
	showProjectNames func() bool
	clock            runtime.Clock
	seq              atomic.Uint64
	// jobsCloser closes the second cascade.db connection
	// openWidgetJobsStore opened for activeJobsCount. Production
	// (cmd/cascade's withStatusWidgetHandler) discards the returned deps
	// entirely and relies on process exit to reclaim it (see
	// openWidgetJobsStore's own doc comment); a test that owns deps
	// directly must call this on cleanup instead, or its t.TempDir()
	// store directory outlives the open handle, which os.RemoveAll
	// refuses on Windows (unlike POSIX, which unlinks happily).
	jobsCloser func() error
}

// capacitySnapshot pulls the provider/node sources fresh (nil-safe — see
// RegisterStatusWidgetHandler's DISCLOSED GAP for providerSrc) and
// returns the resulting FleetSnapshot.
//
// MUTEX NOTE (recorded, not guessed — a real deadlock this ticket hit and
// fixed while building it). Compositor.notifyLocked (compositor.go) calls
// this ticket's own onChange callback (SetOnChange, registered by
// RegisterStatusWidgetHandler) WHILE HOLDING its own mutex via Lock, not
// RLock — sync.RWMutex is not reentrant, so that callback must never call
// back into d.comp.Snapshot() (which takes RLock) or any Update* (which
// takes Lock) on the SAME Compositor, or the single goroutine driving the
// Update* call that triggered the callback deadlocks against itself.
// composeFrom below exists precisely so the onChange path
// (status_widget_sse.go's emitStatusWidgetChanged) can reuse the
// FleetSnapshot ALREADY PASSED to the callback instead of re-entering the
// Compositor at all; only capacitySnapshot (called outside any
// Compositor-held lock — the RPC handler path, and the attention-push
// trigger, which never touches c.mu) is allowed to call Snapshot itself.
func (d *StatusWidgetDeps) capacitySnapshot(ctx context.Context) (capacity.FleetSnapshot, error) {
	if d.providerSrc != nil {
		if err := d.comp.UpdateProviders(ctx, d.providerSrc); err != nil {
			return capacity.FleetSnapshot{}, err
		}
	}
	if d.nodeSrc != nil {
		if err := d.comp.UpdateNodes(ctx, d.nodeSrc); err != nil {
			return capacity.FleetSnapshot{}, err
		}
	}
	return d.comp.Snapshot(), nil
}

// composeFrom builds the UN-redacted WidgetSnapshot from an
// ALREADY-OBTAINED FleetSnapshot (never calling back into the Compositor
// — see capacitySnapshot's MUTEX NOTE), plus a fresh attention-queue read
// and jobs-domain count. Callers (statusWidgetHandler,
// emitStatusWidgetChanged) apply redactSnapshot themselves so the SSE and
// RPC paths share one composition step.
func (d *StatusWidgetDeps) composeFrom(ctx context.Context, snap capacity.FleetSnapshot, ownScope supervision.ScopeRef) (capacity.WidgetSnapshot, error) {
	attentionCount := 0
	if d.attention != nil {
		items, err := d.attention.ListInScopes(ctx, []supervision.ScopeRef{ownScope}, supervision.Filter{IncludeAcked: false})
		if err != nil {
			return capacity.WidgetSnapshot{}, err
		}
		attentionCount = len(items)
	}

	var activeJobs *int
	if d.activeJobsCount != nil {
		n, err := d.activeJobsCount(ctx)
		if err != nil {
			return capacity.WidgetSnapshot{}, err
		}
		activeJobs = n
	}

	return capacity.Compose(snap, attentionCount, activeJobs, d.clock.Now(), d.seq.Load()), nil
}

// buildSnapshot is capacitySnapshot+composeFrom combined — the RPC
// handler's own path, called OUTSIDE any Compositor-held lock, so
// capacitySnapshot's internal Snapshot() call is always safe here.
func (d *StatusWidgetDeps) buildSnapshot(ctx context.Context, ownScope supervision.ScopeRef) (capacity.WidgetSnapshot, error) {
	snap, err := d.capacitySnapshot(ctx)
	if err != nil {
		return capacity.WidgetSnapshot{}, err
	}
	return d.composeFrom(ctx, snap, ownScope)
}

// redactSnapshot applies capacity.Redact using deps' resolved
// show_project_names flag.
func redactSnapshot(deps *StatusWidgetDeps, snap capacity.WidgetSnapshot) capacity.WidgetSnapshot {
	show := false
	if deps.showProjectNames != nil {
		show = deps.showProjectNames()
	}
	return capacity.Redact(snap, show)
}

// statusWidgetHandler returns the status.widget rpc.HandlerFunc bound to
// deps. A nil deps/Compositor returns a typed KindUnavailable error (the
// task-spec error path: "daemon not yet initialised"), matching
// capacity.Handler's identical convention.
func statusWidgetHandler(deps *StatusWidgetDeps) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if deps == nil || deps.comp == nil {
			return nil, cascade.New(cascade.KindUnavailable, "status.widget: daemon capacity compositor not yet initialised")
		}
		var p statusWidgetParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "status.widget: malformed params")
			}
		}
		snap, err := deps.buildSnapshot(ctx, resolveWidgetScope(p.Scope))
		if err != nil {
			return nil, err
		}
		return redactSnapshot(deps, snap), nil
	}
}

// RegisterStatusWidgetHandler mounts status.widget on registry and wires
// its status.widget_changed SSE emitter (status_widget_sse.go), over:
//   - a real, OWNED capacity.Compositor, pulled fresh from a real
//     nodes.RecordStore on every request/tick (see DISCLOSED GAP below for
//     the provider side);
//   - a real, second supervision.Store instance over the SAME store
//     (status_widget_sse.go's ATTENTION-PUSH TRIGGER note);
//   - a real jobs.Store-backed active-jobs counter (status_widget_jobs.go).
//
// A nil store registers nothing, matching every sibling registerXHandler's
// documented nil-store degradation.
//
// DISCLOSED GAP (provider source): this ticket wires nodeSrc (cheap,
// file-backed: nodes.NewRecordStore/NewFileRecordBackend, already used
// directly by internal/daemon/node_upgrade_rpc.go) but leaves
// providerSrc nil. Populating it needs the provider registry's own
// dedicated sqlite database (providers.db under paths.DataDir()), which
// today has no daemon-composition-root reader at all —
// cmd/cascade/provider_health_cmd.go's own header names this in full:
// "no daemon RPC... these five subcommands operate directly against
// local storage" (P1-E10-W3-S21-T2). Opening a fourth daemon-side sqlite
// connection and constructing a real registry.Registry there is a
// separate, real change (a new subsystem, not a two-line addition to an
// already-open connection the way jobs/nodes are here) that this
// ticket's scope does not include; a nil providerSrc means
// Compositor.Snapshot() reports zero providers (an honestly empty
// FleetSnapshot.Providers map) rather than a fabricated one. Rows never
// populate until a follow-up wires this the way this file wires nodes.
//
// Returns the constructed *StatusWidgetDeps (nil when store is nil) so a
// caller that also needs to drive attention Push/Ack through the SAME
// Store instance this handler reads (status_widget_test.go's
// TestStatusWidgetChangedSSE) can do so — mirroring
// cmd/cascade/daemon_unix_jobs_rpc_test.go's setupJobsRPC, which returns
// wireJobRPC's own *jobs.Store for the identical reason.
func RegisterStatusWidgetHandler(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, showProjectNames func() bool) (*StatusWidgetDeps, error) {
	if store == nil {
		return nil, nil
	}
	activeJobsCount, jobsCloser, err := openWidgetJobsStore(context.Background(), paths, clock)
	if err != nil {
		return nil, err
	}

	deps := &StatusWidgetDeps{
		comp:             capacity.NewCompositor(clock, widgetCompositorTTL, ""),
		nodeSrc:          nodes.NewRecordStore(nodes.NewFileRecordBackend(paths.DataDir()), clock),
		activeJobsCount:  activeJobsCount,
		showProjectNames: showProjectNames,
		clock:            clock,
		jobsCloser:       jobsCloser,
	}

	// Attention-push trigger: safe to call comp.Snapshot() (via
	// emitStatusWidgetChanged's nil preSnap path) here — Push/Ack never
	// touch the Compositor's mutex, so there is no reentrant-lock risk
	// (see capacitySnapshot's MUTEX NOTE for the trigger that DOES have
	// one).
	var forwardBus supervision.EventBus
	if bus != nil {
		forwardBus = &attentionForwardBus{real: bus, onPush: func(ctx context.Context) { emitStatusWidgetChanged(ctx, deps, bus, nil) }}
	} else {
		forwardBus = &attentionForwardBus{onPush: func(ctx context.Context) { emitStatusWidgetChanged(ctx, deps, nil, nil) }}
	}
	deps.attention = supervision.NewStore(store, clock, forwardBus, supervision.NewSystemIDGenerator(), 0)

	// Compositor-tick trigger: MUST pass the snap the callback already
	// received (capacitySnapshot's MUTEX NOTE) — notifyLocked invokes
	// this callback while still holding the Compositor's own write lock.
	if bus != nil {
		deps.comp.SetOnChange(func(snap capacity.FleetSnapshot, _ *capacity.Delta, _ uint64) {
			emitStatusWidgetChanged(context.Background(), deps, bus, &snap)
		})
	}

	registry.Register(MethodStatusWidget, statusWidgetHandler(deps))
	return deps, nil
}
