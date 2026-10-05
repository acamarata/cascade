package daemon

// Purpose: the status.widget JSON-RPC 2.0 method (P1-E38-W8-S74-T1, completed
// by P1-WID-08): composes a capacity.WidgetSnapshot from a real, owned
// capacity.Compositor over the provider registry (providers.db, passed in by
// the composition root) and the node records, plus the attention queue
// (P1-E18-W4-S39-T1) and jobs domain (status_widget_jobs.go) counts. The
// refresh loop (status_widget_refresh.go) and the single emit path
// (status_widget_sse.go) share this file's deps.
//
// CONTRACT DEVIATION (files_scope, recorded - R-16.79). The original
// ticket's files_scope names internal/daemon/server.go, which does not exist
// in this tree. The real registration site is internal/rpc.Registry.Register,
// called from this package's RegisterStatusWidgetHandler, which
// cmd/cascade/wire_status_widget.go (the registration the composition root's
// growth rule prescribes) invokes.
//
// CONTRACT DEVIATION (scope resolution, recorded). The ticket says the
// handler resolves the caller scope from the connection identity. The real
// connection identity (internal/rpc/handler.go) is only a boolean owner-UID
// gate, and fleet.attention.list itself requires an explicit own_scope, so
// when status.widget's optional scope param is absent defaultWidgetScope()
// resolves it to scope.ScopeKindGlobal, the widest defined ScopeKind.
//
// SPORT: daemon.status_widget (ADD, P1-E38-W8-S74-T1; CHANGE, P1-WID-08).

import (
	"context"
	"encoding/json"
	"sync"
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

// widgetCompositorTTL bounds how long a provider or node slot may go
// without a successful read before Snapshot reports it Unknown. Every
// request and every refresh tick re-reads the sources (a pull model, as
// internal/daemon/supervisor_rpc.go does), so a slot only reaches this age
// when reads keep failing: the row then ages (updated_at falls behind
// generated_at) and finally reads unknown instead of keeping a stale value.
const widgetCompositorTTL = time.Minute

// statusWidgetParams is status.widget's params shape: {scope: optional}.
type statusWidgetParams struct {
	Scope *supervision.ScopeRef `json:"scope"`
}

// defaultWidgetScope is the fallback own-scope used when a request omits
// scope - see this file's header CONTRACT DEVIATION note.
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

// StatusWidgetDeps bundles the real sources statusWidgetHandler and the
// emit path (status_widget_sse.go) both read through. The zero value is not
// usable; construct via RegisterStatusWidgetHandler.
type StatusWidgetDeps struct {
	comp             *capacity.Compositor
	providerSrc      capacity.ProviderSource // providers.db, supplied by the composition root
	nodeSrc          capacity.NodeSource
	attention        *supervision.Store
	activeJobsCount  func(context.Context) (*int, error)
	showProjectNames func() bool
	clock            runtime.Clock
	// seq is the last EMITTED sequence number; it moves only when a frame
	// is published (emitStatusWidgetChanged).
	seq atomic.Uint64
	// reads records whether each source has ever been read successfully.
	reads sourceReads
	// emitMu serialises the emit path; lastKey is the change key of the
	// last frame published (status_widget_key.go). Both guarded by emitMu.
	emitMu  sync.Mutex
	lastKey []byte
	// jobsCloser closes the second cascade.db connection
	// openWidgetJobsStore opened for activeJobsCount; Close calls it.
	jobsCloser func() error
}

// Close releases the handle the jobs counter holds. The composition root
// calls it after the refresh loop has stopped; a test that owns deps calls it
// on cleanup so its t.TempDir() can be removed on Windows.
func (d *StatusWidgetDeps) Close() error {
	if d == nil || d.jobsCloser == nil {
		return nil
	}
	return d.jobsCloser()
}

// composeFrom builds the UN-redacted WidgetSnapshot from an already-obtained
// FleetSnapshot, plus a fresh attention-queue read and jobs-domain count.
// Callers (statusWidgetHandler, emitStatusWidgetChanged) apply
// redactSnapshot themselves so the RPC and SSE paths share one composition
// step. Seq is the last emitted sequence number.
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

// buildSnapshot is capacitySnapshot+composeFrom combined: the RPC handler's
// path. A source read that failed after an earlier success is served from
// the last rows (they keep their own updated_at); only a source that has
// never been read is an error.
func (d *StatusWidgetDeps) buildSnapshot(ctx context.Context, ownScope supervision.ScopeRef) (capacity.WidgetSnapshot, error) {
	snap, _, err := d.capacitySnapshot(ctx)
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

// RegisterStatusWidgetHandler mounts status.widget on registry and wires its
// attention-push emit trigger, over:
//   - a real, OWNED capacity.Compositor pulled fresh from providers (the
//     provider registry, opened by cmd/cascade/wire_status_widget.go through
//     openMigratedDB; nil reports zero rows) and a real nodes.RecordStore on
//     every request and refresh tick;
//   - a real, second supervision.Store over the SAME store;
//   - a real jobs.Store-backed active-jobs counter (status_widget_jobs.go).
//
// ctx is the daemon's run context (daemonWiring.Ctx): it opens the jobs
// store and is the context every emit this registration triggers runs under.
// The periodic refresh is started separately, by the wire file, through
// RunStatusWidgetRefresh. A nil store registers nothing, matching every
// sibling registerXHandler's documented nil-store degradation.
//
// Returns the constructed *StatusWidgetDeps (nil when store is nil) so the
// caller can run the refresh loop and Close it, and a test can drive
// attention Push/Ack through the SAME Store this handler reads.
func RegisterStatusWidgetHandler(ctx context.Context, registry *rpc.Registry, store provider.Store, providers capacity.ProviderSource, clock runtime.Clock, bus *events.Bus, paths runtime.PathProvider, showProjectNames func() bool) (*StatusWidgetDeps, error) {
	if store == nil {
		return nil, nil
	}
	activeJobsCount, jobsCloser, err := openWidgetJobsStore(ctx, paths, clock)
	if err != nil {
		return nil, err
	}

	deps := &StatusWidgetDeps{
		comp:             capacity.NewCompositor(clock, widgetCompositorTTL, ""),
		providerSrc:      providers,
		nodeSrc:          nodes.NewRecordStore(nodes.NewFileRecordBackend(paths.DataDir()), clock),
		activeJobsCount:  activeJobsCount,
		showProjectNames: showProjectNames,
		clock:            clock,
		jobsCloser:       jobsCloser,
	}

	// Attention-push trigger: a Push or Ack through deps' OWN store emits
	// through the one emit path under the run context. A change made through
	// any other Store over the same data is picked up by the next refresh
	// tick instead (status_widget_refresh.go).
	var emitBus statusWidgetEventBus
	var forwardReal supervision.EventBus
	if bus != nil {
		emitBus, forwardReal = bus, bus
	}
	forward := &attentionForwardBus{real: forwardReal, onPush: func(context.Context) { emitStatusWidgetChanged(ctx, deps, emitBus) }}
	deps.attention = supervision.NewStore(store, clock, forward, supervision.NewSystemIDGenerator(), 0)

	registry.Register(MethodStatusWidget, statusWidgetHandler(deps))
	return deps, nil
}
