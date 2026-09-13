// Purpose (this file): the fleet.mode.show and fleet.mode.set JSON-RPC
//   2.0 methods (P1-E41-W9-S79-T2). Mirrors internal/fleet/topology's
//   rpc_quota.go precedent: a generic *rpc.Registry.Register call (no
//   method-specific change to registry.go), and the composition-root
//   wiring lives in internal/daemon.
//
// CONTRACT DEVIATION (files_scope, recorded, not papered over). The
// ticket names internal/rpc/fleet_mode.go in files_scope.add. This
// package (economics) imports internal/fleet/topology, and
// topology/rpc_quota.go imports internal/rpc -> internal/events ->
// internal/runtime -- so internal/rpc importing internal/fleet/economics
// (to reach SchedulerModeStore) would close a real, compiler-enforced
// import cycle, identical in shape to config_fleet.go's own documented
// CONTRACT DEVIATION for the same reason. Every sibling domain package
// with an RPC method in this tree (topology's rpc_quota.go, capacity's
// rpc.go) resolves this the same way: the handler file lives IN the
// domain package, importing internal/rpc for the HandlerFunc/Registry
// types (domain -> rpc is fine; only rpc -> domain would cycle). This
// file follows that established precedent; registry.go itself needs no
// edit (Registry.Register is already a generic, method-name-agnostic
// call).
//
// CONTRACT DEVIATION (SessionScope, recorded). The ticket text says an
// omitted project_id "resolves to the request's SessionScope project".
// No RPC handler in this tree threads a SessionScope through ctx today
// (internal/context/scope owns that type but nothing wires it into
// internal/rpc's dispatch path yet); this file accepts a duck-typed
// ProjectResolver seam instead (matching topology.DomainSource's own
// precedent), rather than inventing that composition-root wiring on this
// ticket's behalf. A nil resolver, or a resolver returning an empty
// project id, is the same typed "unresolved project" error the ticket
// asks for -- never a fallback to a global mode.
//
// SPORT: fleet.mode.rpc (ADD, per T2 sport_updates).

package economics

import (
	"context"
	"encoding/json"
	"time"

	"github.com/acamarata/cascade/internal/rpc"
	"github.com/acamarata/cascade/pkg/cascade"
)

// MethodFleetModeShow and MethodFleetModeSet are the fleet.mode.* JSON-RPC
// 2.0 method names.
const (
	MethodFleetModeShow = "fleet.mode.show"
	MethodFleetModeSet  = "fleet.mode.set"
)

// modeEnvelopeVersion is this pair's wire-format version (C/S-05.T2 /
// D/S-06.T5's versioned-envelope convention).
const modeEnvelopeVersion = "1"

// ProjectResolver resolves the caller's default project when a
// fleet.mode.* request omits project_id. Duck-typed so this package never
// depends on a concrete SessionScope resolution implementation.
type ProjectResolver interface {
	ResolveProject(ctx context.Context) (string, error)
}

// ErrFleetModeUnresolvedProject is returned when project_id is omitted
// and the resolver cannot supply one -- never a fallback to a global
// mode.
var ErrFleetModeUnresolvedProject = cascade.New(cascade.KindInvalidInput, "fleet.mode: unresolved project")

// ModeResultWire is fleet.mode.show/set's data payload.
type ModeResultWire struct {
	Mode             string `json:"mode"`
	Source           string `json:"source"`
	SetAt            string `json:"set_at,omitempty"`
	ExpiresAt        string `json:"expires_at,omitempty"`
	LifecycleStage   string `json:"lifecycle_stage"`
	LifecycleDefault string `json:"lifecycle_default"`
}

// ModeEnvelope is the {version: "1", data: ...} wire shape C/S-05.T2 and
// D/S-06.T5 established.
type ModeEnvelope struct {
	Version string         `json:"version"`
	Data    ModeResultWire `json:"data"`
}

// fleetModeShowParams/fleetModeSetParams are the two methods' params
// shapes.
type fleetModeShowParams struct {
	ProjectID      string `json:"project_id"`
	LifecycleStage string `json:"lifecycle_stage"`
}

type fleetModeSetParams struct {
	ProjectID string `json:"project_id"`
	Mode      string `json:"mode"`
	TTL       string `json:"ttl"`
}

// resolveProjectID returns projectID unchanged when non-empty, else
// resolver.ResolveProject(ctx); a nil resolver or an empty resolved value
// is ErrFleetModeUnresolvedProject.
func resolveProjectID(ctx context.Context, projectID string, resolver ProjectResolver) (string, error) {
	if projectID != "" {
		return projectID, nil
	}
	if resolver == nil {
		return "", ErrFleetModeUnresolvedProject
	}
	resolved, err := resolver.ResolveProject(ctx)
	if err != nil {
		return "", cascade.Wrap(cascade.KindInvalidInput, err, "fleet.mode: resolve project")
	}
	if resolved == "" {
		return "", ErrFleetModeUnresolvedProject
	}
	return resolved, nil
}

// buildModeResult renders state and stage's lifecycle default into the
// wire shape.
func buildModeResult(state ModeState, stage string) (ModeResultWire, error) {
	out := ModeResultWire{Mode: string(state.Mode), Source: string(state.Source), LifecycleStage: stage}
	if !state.SetAt.IsZero() {
		out.SetAt = state.SetAt.UTC().Format(time.RFC3339)
	}
	if !state.ExpiresAt.IsZero() {
		out.ExpiresAt = state.ExpiresAt.UTC().Format(time.RFC3339)
	}
	if stage != "" {
		def, err := ModeForLifecycleStage(stage)
		if err != nil {
			return ModeResultWire{}, err
		}
		out.LifecycleDefault = string(def)
	}
	return out, nil
}

// ModeShowHandler returns the fleet.mode.show rpc.HandlerFunc.
func ModeShowHandler(store *SchedulerModeStore, resolver ProjectResolver, clock Clock) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if store == nil || clock == nil {
			return nil, cascade.New(cascade.KindUnavailable, "fleet.mode.show: daemon scheduler-mode subsystem not yet initialised")
		}
		var params fleetModeShowParams
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &params); err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "fleet.mode.show: decode params")
			}
		}
		projectID, err := resolveProjectID(ctx, params.ProjectID, resolver)
		if err != nil {
			return nil, err
		}
		state, err := store.Resolve(ctx, projectID, params.LifecycleStage)
		if err != nil {
			return nil, err
		}
		result, err := buildModeResult(state, params.LifecycleStage)
		if err != nil {
			return nil, err
		}
		return ModeEnvelope{Version: modeEnvelopeVersion, Data: result}, nil
	}
}

// ModeSetHandler returns the fleet.mode.set rpc.HandlerFunc.
func ModeSetHandler(store *SchedulerModeStore, resolver ProjectResolver, clock Clock) rpc.HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if store == nil || clock == nil {
			return nil, cascade.New(cascade.KindUnavailable, "fleet.mode.set: daemon scheduler-mode subsystem not yet initialised")
		}
		var params fleetModeSetParams
		if err := json.Unmarshal(raw, &params); err != nil {
			return nil, cascade.Wrap(cascade.KindInvalidInput, err, "fleet.mode.set: decode params")
		}
		projectID, err := resolveProjectID(ctx, params.ProjectID, resolver)
		if err != nil {
			return nil, err
		}
		m, err := ParseMode(params.Mode)
		if err != nil {
			return nil, err
		}
		var ttl time.Duration
		if params.TTL != "" {
			ttl, err = time.ParseDuration(params.TTL)
			if err != nil {
				return nil, cascade.Wrap(cascade.KindInvalidInput, err, "fleet.mode.set: parse ttl")
			}
		}
		state, err := store.Set(ctx, projectID, m, ttl)
		if err != nil {
			return nil, err
		}
		result, err := buildModeResult(state, "")
		if err != nil {
			return nil, err
		}
		return ModeEnvelope{Version: modeEnvelopeVersion, Data: result}, nil
	}
}

// RegisterHandlers binds MethodFleetModeShow/Set on reg. Registry.Register
// is already a generic, method-name-agnostic call -- see this file's
// header CONTRACT DEVIATION note for why registry.go itself needs no
// change.
func RegisterHandlers(reg *rpc.Registry, store *SchedulerModeStore, resolver ProjectResolver, clock Clock) {
	reg.Register(MethodFleetModeShow, ModeShowHandler(store, resolver, clock))
	reg.Register(MethodFleetModeSet, ModeSetHandler(store, resolver, clock))
}
