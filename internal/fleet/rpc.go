package fleet

// Purpose (this file): the fleet.bench_lane JSON-RPC 2.0 door
//   (P1-E12-W3-S25-T5, R-21.272/R-21.273): a server-side HandlerFunc
//   RegisterBenchHandlers binds into an *rpc.Registry, dispatching a
//   {lane_id, config} request through Bench and returning its
//   BenchResult.
//
// Inputs: fleet.bench_lane's params: lane_id (string) and an optional
//   BenchConfig, JSON-decoded with unknown fields rejected.
// Outputs: the BenchResult, or a pkg/cascade taxonomy error.
// Constraints: this handler carries no lane-selection logic of its own -
//   exec (the pkg/provider.ModelExecutor the composition root injects) is
//   expected to already resolve requests to lane_id, matching Bench's own
//   "no lane identity" contract (see bench.go).
//
// CONTRACT DEVIATION (wiring, recorded, not papered over). R-21.273 lists
// internal/rpc/registry.go in files_scope.change "for every ticket that
// registers RPC methods," but Registry (registry.go) is a fully generic
// name->HandlerFunc map with no method-specific code - the landed
// precedent (internal/fleet/sessions.RegisterHandlers, P1-E12-W3-S24-T3)
// registers via a Register*Handlers function called from
// cmd/cascade/daemon_unix_run.go's buildRPCServer, neither of which is in
// this ticket's files_scope. RegisterBenchHandlers below is that same
// shape, ready for buildRPCServer to call once a live ModelExecutor
// (Conductor, K/S-22.T1) exists to inject; the actual call site is out of
// scope here, recorded in internal/build/testonly-allow.json.
//
// LANE HEALTH (R-16.78 §1/§2). Once a probe completes, this handler
// optionally persists it and publishes it, through two narrow injected
// seams rather than a direct dependency on either package's concrete
// type: store (LaneProbeStore, satisfied by *registry.LaneProbeRecord's
// owner, *registry.Registry) and bus (*events.Bus, the same bus
// internal/fleet/sessions.SSEHandler streams). Both are nil-safe: with
// neither injected, the handler stays bench-only, exactly its previously
// shipped behavior. This keeps the dependency direction fleet ->
// registry / fleet -> fleet/sessions, one way, never the reverse.
//
// SPORT: internal.fleet.rpc.bench_lane/ADDED (P1-E12-W3-S25-T5).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/events"
	"github.com/acamarata/cascade/internal/fleet/sessions"
	"github.com/acamarata/cascade/internal/providers/registry"
	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/internal/runtime"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// MethodBenchLane is the fleet.bench_lane JSON-RPC 2.0 method name.
const MethodBenchLane = "fleet.bench_lane"

// benchLaneParams is fleet.bench_lane's wire request shape. Config is
// optional; its zero value normalizes to one probe (see BenchConfig.
// normalize).
type benchLaneParams struct {
	LaneID string      `json:"lane_id"`
	Config BenchConfig `json:"config,omitempty"`
}

// ErrLaneIDRequired is returned when fleet.bench_lane's params omit
// lane_id.
var ErrLaneIDRequired = cascade.New(cascade.KindInvalidInput, "fleet: lane_id is required")

// decodeBenchLaneParams decodes raw into benchLaneParams, rejecting any
// key raw carries that the type does not declare.
func decodeBenchLaneParams(raw json.RawMessage) (benchLaneParams, error) {
	if len(raw) == 0 {
		return benchLaneParams{}, ErrLaneIDRequired
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var p benchLaneParams
	if err := dec.Decode(&p); err != nil {
		return benchLaneParams{}, cascade.Wrapf(cascade.KindInvalidInput, err, "fleet: decoding fleet.bench_lane params")
	}
	if p.LaneID == "" {
		return benchLaneParams{}, ErrLaneIDRequired
	}
	return p, nil
}

// LaneProbeStore persists one lane's latest probe/bench reading.
// Satisfied by *registry.Registry; narrowed to exactly what this file
// calls so rpc_test.go can inject a fake without a real database.
type LaneProbeStore interface {
	UpsertLaneProbe(ctx context.Context, rec registry.LaneProbeRecord) error
	GetLaneProbe(ctx context.Context, laneName string) (*registry.LaneProbeRecord, error)
}

// RegisterBenchHandlers binds fleet.bench_lane to reg, dispatching
// through exec. store and bus are OPTIONAL (either or both may be nil):
// see this file's LANE HEALTH note. clock is only read when store is
// non-nil (ProbedAt, Art.7.3 - never a bare time.Now); pass
// runtime.NewSystemClock() in production. See this file's CONTRACT
// DEVIATION note for why the composition-root call site (which must
// inject a live pkg/provider.ModelExecutor) is out of this ticket's
// scope.
func RegisterBenchHandlers(reg *rpc.Registry, exec provider.ModelExecutor, store LaneProbeStore, bus *events.Bus, clock runtime.Clock) {
	reg.Register(MethodBenchLane, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeBenchLaneParams(raw)
		if err != nil {
			return nil, err
		}
		result, err := Bench(ctx, p.Config, exec)
		if err != nil {
			return nil, err
		}
		if err := recordLaneHealth(ctx, store, bus, clock, p.LaneID, result); err != nil {
			return nil, err
		}
		return result, nil
	})
}

// recordLaneHealth persists result as laneID's latest reading (when
// store is non-nil) and publishes it on bus (when bus is also non-nil)
// ONLY if the reading differs from what was previously stored (R-16.78
// §2: lane health is emitted on change, never on a timer). With store
// nil, this is a no-op - the handler stays bench-only.
func recordLaneHealth(ctx context.Context, store LaneProbeStore, bus *events.Bus, clock runtime.Clock, laneID string, result BenchResult) error {
	if store == nil {
		return nil
	}
	prev, err := store.GetLaneProbe(ctx, laneID)
	if err != nil {
		return err
	}
	next := registry.LaneProbeRecord{
		LaneName: laneID, LatencyP50MS: result.P50MS, LatencyP95MS: result.P95MS,
		ErrorRate: result.ErrorRate, CostEstimate: result.CostEstimate, ProbedAt: clock.Now(),
	}
	if err := store.UpsertLaneProbe(ctx, next); err != nil {
		return err
	}
	if bus == nil || laneHealthUnchanged(prev, next) {
		return nil
	}
	return sessions.PublishLaneHealth(ctx, bus, sessions.LaneHealth{
		LaneID: laneID, LatencyP50MS: next.LatencyP50MS, LatencyP95MS: next.LatencyP95MS,
		ErrorRate: next.ErrorRate, CostEstimate: next.CostEstimate, Healthy: next.ErrorRate == 0,
	})
}

// laneHealthUnchanged reports whether next carries the same reading as
// prev (nil prev - never probed before - always counts as a change).
func laneHealthUnchanged(prev *registry.LaneProbeRecord, next registry.LaneProbeRecord) bool {
	if prev == nil {
		return false
	}
	return prev.LatencyP50MS == next.LatencyP50MS && prev.LatencyP95MS == next.LatencyP95MS &&
		prev.ErrorRate == next.ErrorRate && prev.CostEstimate == next.CostEstimate
}
