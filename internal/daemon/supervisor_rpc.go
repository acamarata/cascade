package daemon

// Purpose: T0-daemon-composition-root — registers supervisor.snapshot/
//   events_schema (P1-E18-W4-S40-T4) on the daemon's RPC router over real
//   internal/fleet/supervision.Store and internal/fleet/sessions.Store
//   instances, mirroring attention_rpc.go's RegisterFleetAttentionHandler
//   precedent exactly: the composition logic (building real domain stores
//   from the already-open provider.Store/clock/bus) lives in this package,
//   cmd/cascade only calls it with what buildRPCServer already has open.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over — R-16.79).
// The ticket's files_scope names only internal/rpc/*. internal/rpc cannot
// import internal/fleet/supervision, internal/fleet/sessions or
// internal/policy (each already imports internal/rpc — a real cycle), so
// the composition that reads all three, exactly like
// internal/fleet/capacity/rpc.go's and attention_rpc.go's own identical
// notes, has to live at the daemon composition root instead. See
// internal/rpc/supervisor.go's own CONTRACT DEVIATION note for the cycle
// detail.
//
// SEAM AUDIT (ticket task 9): supervisorSource reads fleet census state
// ONLY through internal/fleet/sessions.Store — never a census.Reader or
// poller directly. internal/fleet/supervision's own Store also never
// touches census; the L/S-25.T4 CensusReader interface is S-40.T2's
// (headroom.go) seam, not this ticket's — this file reads no census state
// at all, so there is nothing here that could bypass that interface.
//
// DISCLOSED GAPS this file does not close (see internal/rpc/supervisor_sse.go's
// own WIRING GAP note for the SSE half): no *runtime.Registry (C-S05.T4)
// is constructed anywhere in cmd/cascade's production path today, so
// metrics/autonomy are threaded through as optional (nil-safe) dependencies
// here rather than invented. A nil metrics registry reports
// HeadroomCeiling as absent (never a fabricated zero); a nil autonomy
// controller reports AutonomyProfile "locked" and AutoAdvanceTier "none" —
// both are the real, documented behavior of a nil policy.Controller
// (Controller.Profile()/AutonomyProfile.Name() are nil-safe by design),
// not values invented by this file.
//
// SPORT: internal/daemon (ADD, T0-daemon-composition-root; P1-E18-W4-S40-T4).

import (
	"context"

	"github.com/acamarata/cascade/internal/context/scope"
	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/fleet/supervision"
	"github.com/acamarata/cascade/internal/policy"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/provider"
)

// headroomCeilingGaugeName is the S-40.T2 gauge name
// (internal/fleet/headroom.go's gaugeSet.ceiling), read by name rather than
// by importing internal/fleet — headroom.go's own doc comment names this
// exact gauge as "the C-S05.T4 counters/gauges snapshot API surface... so
// downstream consumers (S-40.T1's TUI, S-40.T4's RPC/SSE) read published
// headroom without importing internal/fleet directly", and S-40.T4 is this
// ticket.
const headroomCeilingGaugeName = "fleet_headroom_enforced_ceiling"

// supervisorSource implements rpc.SupervisorSource against real S-39/S-40
// components. The zero value is not usable; construct with
// newSupervisorSource.
type supervisorSource struct {
	attention *supervision.Store
	sessions  *sessions.Store
	metrics   *runtime.Registry  // C-S05.T4; nil is a legitimate degradation
	autonomy  *policy.Controller // nil before any [policy] config load
}

var _ rpc.SupervisorSource = (*supervisorSource)(nil)

// Snapshot implements rpc.SupervisorSource. It fails closed on any real
// storage error (never returns a partially-populated result on failure),
// and reports every field it cannot honestly source as an explicit
// absence rather than a fabricated value.
func (s *supervisorSource) Snapshot(ctx context.Context) (rpc.SupervisorSnapshotResult, error) {
	records, err := s.sessions.List(ctx, sessions.Filter{})
	if err != nil {
		return rpc.SupervisorSnapshotResult{}, err
	}

	result := rpc.SupervisorSnapshotResult{
		SchemaVersion:   rpc.SupervisorSchemaVersion,
		Sessions:        make([]rpc.SupervisorSessionCounts, 0, len(records)),
		AutonomyProfile: s.autonomy.Profile().Name(),
		AutoAdvanceTier: autoAdvanceTier(s.autonomy),
	}

	for _, rec := range records {
		items, itemErr := s.attention.ListInScopes(ctx,
			[]supervision.ScopeRef{{Kind: scope.ScopeKindSession, ID: rec.SessionID}},
			supervision.Filter{IncludeAcked: false})
		if itemErr != nil {
			return rpc.SupervisorSnapshotResult{}, itemErr
		}
		stalls := 0
		for _, item := range items {
			if item.Kind == supervision.KindStall {
				stalls++
			}
		}
		result.AttentionQueueDepth += len(items)
		result.StallCount += stalls
		result.Sessions = append(result.Sessions, rpc.SupervisorSessionCounts{
			SessionID:      rec.SessionID,
			ActionCount:    rec.ToolCount,
			InterruptCount: int64(len(items)),
		})
	}

	if s.metrics != nil {
		if m, ok := s.metrics.Get(headroomCeilingGaugeName); ok {
			v := m.Value()
			result.HeadroomCeiling = &v
		}
	}
	return result, nil
}

// autoAdvanceTier returns the highest L0/L1 rung profile currently allows
// an autonomous loop to pass without a human turn, or "none". A nil
// controller (no [policy] config loaded yet) resolves to "none" via
// Controller.Profile()'s own documented nil-deny-everything default.
func autoAdvanceTier(controller *policy.Controller) string {
	profile := controller.Profile()
	for _, level := range []policy.RiskLevel{policy.L1, policy.L0} {
		if profile.AllowsAutoAdvance(level) {
			return level.String()
		}
	}
	return "none"
}

// RegisterSupervisorHandler mounts supervisor.snapshot/events_schema on
// registry over real supervision.Store and sessions.Store instances built
// from store/clock/bus. A nil store registers nothing, matching every
// sibling registerXHandler's documented nil-store degradation. metrics and
// autonomy may be nil (see this file's DISCLOSED GAPS note).
func RegisterSupervisorHandler(registry *rpc.Registry, store provider.Store, clock runtime.Clock, bus *events.Bus, metrics *runtime.Registry, autonomy *policy.Controller) {
	if store == nil {
		return
	}
	var attnBus supervision.EventBus
	var sessBus sessions.EventBus
	if bus != nil {
		attnBus = bus
		sessBus = bus
	}
	src := &supervisorSource{
		attention: supervision.NewStore(store, clock, attnBus, supervision.NewSystemIDGenerator(), 0),
		sessions:  sessions.New(store, clock, sessBus),
		metrics:   metrics,
		autonomy:  autonomy,
	}
	rpc.RegisterSupervisorHandlers(registry, src)
}
