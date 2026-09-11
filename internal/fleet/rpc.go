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
// SPORT: internal.fleet.rpc.bench_lane/ADDED (P1-E12-W3-S25-T5).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/internal/rpc"
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

// RegisterBenchHandlers binds fleet.bench_lane to registry, dispatching
// through exec. See this file's CONTRACT DEVIATION note for why the
// composition-root call site (which must inject a live
// pkg/provider.ModelExecutor) is out of this ticket's scope.
func RegisterBenchHandlers(registry *rpc.Registry, exec provider.ModelExecutor) {
	registry.Register(MethodBenchLane, func(ctx context.Context, raw json.RawMessage) (any, error) {
		p, err := decodeBenchLaneParams(raw)
		if err != nil {
			return nil, err
		}
		return Bench(ctx, p.Config, exec)
	})
}
