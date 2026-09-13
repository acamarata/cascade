package rpc

// Purpose (this file): the supervisor.snapshot and supervisor.events_schema
// JSON-RPC 2.0 methods (P1-E18-W4-S40-T4). The namespace is reserved for a
// future product consumer (a dashboard); this package stays product-agnostic
// and knows nothing about who reads it.
//
// CONTRACT DEVIATION (data-source imports, recorded, not papered over —
// R-16.79). The full_desc names two sources for supervisor.snapshot's
// fields: the counters/gauges snapshot API (C-S05.T4, internal/runtime's
// *Registry) and the fleet session state domain (L/S-24.T3,
// internal/fleet/sessions). A fully-populated snapshot also needs the
// attention queue and stall count (internal/fleet/supervision, S-39.T1)
// and the active autonomy profile (internal/policy, S-18.T1). None of
// those four packages can be imported here: internal/fleet/supervision,
// internal/fleet/sessions and internal/policy all already import
// internal/rpc (supervision/rpc.go, sessions/rpc.go, and half a dozen
// internal/policy files respectively), so the reverse edge is a real
// import cycle `go build` confirms the same way internal/rpc/jobs.go's own
// CONTRACT NOTE already proves it for internal/jobs and internal/nodes.
// This file uses that exact, already-established technique: SupervisorSource
// is a single duck-typed seam, and the composition root
// (internal/daemon/supervisor_rpc.go, per this ticket's wiring instruction)
// implements it against the four real packages, none of which internal/rpc
// itself ever imports.
//
// Inputs: an optional SupervisorSource (nil is a legitimate "daemon
// supervision surfaces not running" state — jobs.go's JobStore/LeaseStore
// seams and capacity.Handler's nil *Compositor share the identical
// convention).
// Outputs: SupervisorSnapshotResult / SupervisorEventsSchemaResult, or a
// pkg/cascade taxonomy error (task 7: a nil source is KindUnavailable,
// never a panic or a zero-value read as "confirmed empty").
//
// SPORT: supervisor.rpc_surface/ADDED (P1-E18-W4-S40-T4).

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/acamarata/cascade/pkg/cascade"
)

// The supervisor.* JSON-RPC 2.0 method names.
const (
	MethodSupervisorSnapshot     = "supervisor.snapshot"
	MethodSupervisorEventsSchema = "supervisor.events_schema"
)

// SupervisorSchemaVersion is stamped on every supervisor.snapshot result
// and every SSE event payload (supervisor_sse.go), versioned from day one
// per the ticket's own instruction.
const SupervisorSchemaVersion = 1

// SupervisorSessionCounts is one session's activity counters in a
// supervisor.snapshot response.
//
// ActionCount maps to internal/fleet/sessions.SessionRecord.ToolCount (the
// R-16.48 activity column closest to "an action this session took" — the
// sessions schema has no field literally named "action_count").
// InterruptCount is the count of unacknowledged internal/fleet/supervision
// attention items scoped to this session (an item filed against a session
// is exactly that session's supervision queue being interrupted for human
// attention) — the sessions schema likewise has no "interrupt_count"
// column of its own. Both mappings are recorded here, not invented
// silently, since neither field exists under these exact names anywhere
// in the tree.
type SupervisorSessionCounts struct {
	SessionID      string `json:"session_id"`
	ActionCount    int64  `json:"action_count"`
	InterruptCount int64  `json:"interrupt_count"`
}

// SupervisorSnapshotResult is supervisor.snapshot's wire result shape.
type SupervisorSnapshotResult struct {
	SchemaVersion int `json:"schema_version"`
	// AttentionQueueDepth is the total unacknowledged attention-item count
	// across every known session (internal/fleet/supervision.Store).
	AttentionQueueDepth int `json:"attention_queue_depth"`
	// StallCount is the subset of AttentionQueueDepth whose Kind is
	// KindStall.
	StallCount int `json:"stall_count"`
	// Sessions carries one entry per known session (possibly empty).
	Sessions []SupervisorSessionCounts `json:"sessions"`
	// HeadroomCeiling is the fleet_headroom_enforced_ceiling gauge
	// (S-40.T2, published through the C-S05.T4 Registry) at read time.
	// Nil means no metrics registry is wired, or the gauge has never been
	// published — an honest "unavailable", never a fabricated zero.
	HeadroomCeiling *int64 `json:"headroom_ceiling,omitempty"`
	// AutonomyProfile is the running internal/policy profile's name
	// ("locked" before any [policy] config has loaded — the profile's own
	// documented nil default, never invented here).
	AutonomyProfile string `json:"autonomy_profile"`
	// AutoAdvanceTier is the highest L0-L4 rung the running profile
	// currently allows an autonomous loop to pass without a human turn
	// ("none" when no rung qualifies, e.g. before any profile has
	// loaded). Only L0/L1 can ever qualify (internal/policy's own
	// auto-advance ceiling), so this is always one of "L0", "L1" or
	// "none".
	AutoAdvanceTier string `json:"auto_advance_tier"`
}

// SupervisorSource is the single seam supervisor.snapshot reads through.
// See this file's CONTRACT DEVIATION note above for why internal/rpc
// cannot import any of the four packages a real implementation composes.
type SupervisorSource interface {
	// Snapshot returns the current supervisor snapshot, or an error (the
	// composition-root adapter is expected to fail closed the same way
	// every other seam in this tree does — never a partially-populated
	// zero value on a read error).
	Snapshot(ctx context.Context) (SupervisorSnapshotResult, error)
}

// errSupervisorUnknownField is decodeSupervisorSnapshotParams' refusal for
// any field on a request that takes none — the wire shape IS the
// allow-list, mirroring internal/fleet/supervision's decodeAttnParams and
// internal/fleet/sessions' decodeListParams precedent.
var errSupervisorUnknownField = cascade.New(cascade.KindInvalidInput, "rpc: supervisor.snapshot: unknown request field")

// supervisorSnapshotParams is supervisor.snapshot's wire params shape: no
// fields today. It exists (rather than skipping decode entirely) so
// FuzzSupervisorSnapshotParams (task 6, supervisor_fuzz_test.go) has a real
// JSON param decoder to exercise — an unknown field is a genuine, typed
// refusal, not merely ignored.
type supervisorSnapshotParams struct{}

// decodeSupervisorSnapshotParams decodes raw, rejecting unknown fields.
// Empty/absent params (raw is nil or "") are valid — supervisor.snapshot
// takes no required input.
func decodeSupervisorSnapshotParams(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	var p supervisorSnapshotParams
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return cascade.Wrapf(cascade.KindInvalidInput, errSupervisorUnknownField, "rpc: decoding supervisor.snapshot params: %v", err)
	}
	return nil
}

// SupervisorSnapshotHandler returns the supervisor.snapshot rpc.HandlerFunc
// bound to src. A nil src returns a taxonomy KindUnavailable error on every
// call (task 7: "daemon not running" must never be a panic or a
// nil/zero-value read as confirmed-empty), mirroring
// internal/fleet/capacity.Handler's identical nil-comp convention.
func SupervisorSnapshotHandler(src SupervisorSource) HandlerFunc {
	return func(ctx context.Context, raw json.RawMessage) (any, error) {
		if err := decodeSupervisorSnapshotParams(raw); err != nil {
			return nil, err
		}
		if src == nil {
			return nil, cascade.New(cascade.KindUnavailable, "supervisor.snapshot: daemon supervision surface not running")
		}
		if err := ctx.Err(); err != nil {
			return nil, cascade.Wrap(cascade.KindCanceled, err, "supervisor.snapshot: context canceled")
		}
		return src.Snapshot(ctx)
	}
}

// SupervisorEventSchemaField describes one field of one SSE event payload.
type SupervisorEventSchemaField struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// SupervisorEventSchemaEntry describes one of the four SSE event kinds.
type SupervisorEventSchemaEntry struct {
	Kind   string                       `json:"kind"`
	Fields []SupervisorEventSchemaField `json:"fields"`
}

// SupervisorEventsSchemaResult is supervisor.events_schema's wire result:
// the versioned schema doc describing all four SSE event types
// (supervisor_sse.go).
type SupervisorEventsSchemaResult struct {
	SchemaVersion int                          `json:"schema_version"`
	Events        []SupervisorEventSchemaEntry `json:"events"`
}

// schemaField is a small constructor for readability below.
func schemaField(name, typ string) SupervisorEventSchemaField {
	return SupervisorEventSchemaField{Name: name, Type: typ}
}

// supervisorEventsSchemaDoc builds the static schema doc. It is a pure
// function of this file's own payload types (supervisor_sse.go) and takes
// no input, so there is nothing here that could ever be a stub: it always
// describes the same four real wire shapes this package emits.
func supervisorEventsSchemaDoc() SupervisorEventsSchemaResult {
	return SupervisorEventsSchemaResult{
		SchemaVersion: SupervisorSchemaVersion,
		Events: []SupervisorEventSchemaEntry{
			{
				Kind: string(EventSupervisorAttentionAdded),
				Fields: []SupervisorEventSchemaField{
					schemaField("schema_version", "int"),
					schemaField("item_id", "string"),
					schemaField("kind", "string"),
					schemaField("scope_kind", "string"),
					schemaField("scope_id", "string"),
				},
			},
			{
				Kind: string(EventSupervisorStallDetected),
				Fields: []SupervisorEventSchemaField{
					schemaField("schema_version", "int"),
					schemaField("session_id", "string"),
					schemaField("stall_kind", "string"),
				},
			},
			{
				Kind: string(EventSupervisorEscalation),
				Fields: []SupervisorEventSchemaField{
					schemaField("schema_version", "int"),
					schemaField("session_id", "string"),
					schemaField("rung", "string"),
				},
			},
			{
				Kind: string(EventSupervisorHeadroomUpdate),
				Fields: []SupervisorEventSchemaField{
					schemaField("schema_version", "int"),
					schemaField("resource", "string"),
					schemaField("enforced_ceiling", "int64"),
					schemaField("ratio", "float64"),
				},
			},
		},
	}
}

// SupervisorEventsSchemaHandler returns the supervisor.events_schema
// rpc.HandlerFunc. It takes no params and never fails: the doc is static.
func SupervisorEventsSchemaHandler() HandlerFunc {
	return func(_ context.Context, _ json.RawMessage) (any, error) {
		return supervisorEventsSchemaDoc(), nil
	}
}

// RegisterSupervisorHandlers binds supervisor.snapshot and
// supervisor.events_schema on reg. See this file's CONTRACT DEVIATION note
// for why registry.go itself needs no change to support this call — the
// same reason capacity/rpc.go's and
// internal/fleet/supervision/rpc.go's own identical notes already give:
// Registry.Register is method-name-agnostic.
func RegisterSupervisorHandlers(reg *Registry, src SupervisorSource) {
	reg.Register(MethodSupervisorSnapshot, SupervisorSnapshotHandler(src))
	reg.Register(MethodSupervisorEventsSchema, SupervisorEventsSchemaHandler())
}
