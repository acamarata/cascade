// Package capacity implements the fleet capacity snapshot (P1-E31-W6-S63-T1):
// the FleetSnapshot type, its per-source aggregation (compositor.go), pure
// Clone/Diff (diff.go), and the fleet.capacity JSON-RPC method plus the
// fleet.capacity_changed SSE mirror (rpc.go).
//
// Purpose (this file): the wire/in-memory shapes a FleetSnapshot is built
// from - ProviderSlot, NodeSlot, TaskCapabilityRow, and the enclosing
// FleetSnapshot itself.
//
// Inputs: none (pure data shapes).
// Outputs: none.
//
// Constraints: State and BucketKind are NOT redeclared here as new
// enums - see the CONTRACT DEVIATION note below. PresenceState is likewise
// NOT a new enum as of P1-E36-W7-S72-T2: that ticket landed the real
// nodes.Presence classifier (nodes/presence.go, nodes/prober.go), so this
// file now re-exports it under the same "PresenceState" name the ticket
// asked for, exactly like State/BucketKind below - one classifier, not
// two competing ones. Before S-72.T2 landed, this file defined a second,
// local PresenceState enum as an honest stopgap (compositor.go's own
// derivePresence heuristic); that stopgap is retired by this change - see
// compositor.go's updated doc comment.
//
// CONTRACT DEVIATION (naming, recorded, not papered over). The ticket's task
// list asks for a new State enum ("no permissive zero-value ...
// exported constant must be StateUnknown") and a new BucketKind. Both
// already exist, verbatim, as internal/providers/registry.LaneState
// {available, constrained, exhausted, auth-required, unknown - zero value
// excluded from Valid()} and registry.CapacityBucket {interactive_usage,
// agent_sdk_credit, api_credit} (P1-E10-W3-S20-T2, real and landed).
// Duplicating them here would be exactly the "reintroduce a second copy of
// the same closed vocabulary" defect this phase's dependency and engineering
// standards forbid. This file re-exports them under the ticket's requested
// names via type aliases and value copies instead, so a caller writing
// capacity.StateUnknown or capacity.BucketKind sees the name the ticket
// asked for while there is structurally only one enum in the tree.
//
// SPORT: fleet.capacity.snapshot (ADD, per T-1 sport_updates).
package capacity

import (
	"time"

	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/internal/providers/registry"
)

// State is registry.LaneState under the ticket's requested name -
// see this file's CONTRACT DEVIATION note. No permissive zero value: the
// empty string is not a member (registry.LaneState.Valid rejects it).
type State = registry.LaneState

// The five State members, re-exported from registry.LaneState.
const (
	StateAvailable    = registry.LaneStateAvailable
	StateConstrained  = registry.LaneStateConstrained
	StateExhausted    = registry.LaneStateExhausted
	StateAuthRequired = registry.LaneStateAuthRequired
	// StateUnknown is the fail-closed value a bucket or slot reports when
	// its source is absent, stale past its TTL, or erroring - never a
	// silently-retained last-good value (compositor.go's TTL-expiry rule).
	StateUnknown = registry.LaneStateUnknown
)

// BucketKind is registry.CapacityBucket under the ticket's requested name.
type BucketKind = registry.CapacityBucket

// The three BucketKind members, re-exported from registry.CapacityBucket.
const (
	BucketInteractiveUsage = registry.CapacityInteractiveUsage
	BucketAgentSDKCredit   = registry.CapacityAgentSDKCredit
	BucketAPICredit        = registry.CapacityAPICredit
)

// PresenceState is nodes.Presence under the ticket's requested name - the
// R-21.65 four-value node presence enum: {reachable, unavailable,
// remote-via-route, unknown}. Re-exported as a type alias (not a new
// type) so a caller writing capacity.PresenceState and one writing
// nodes.Presence see the exact same value - structurally one enum in the
// tree, per this ticket's own duplicate-registry rule.
type PresenceState = nodes.Presence

// The four closed PresenceState members, re-exported from nodes.Presence.
// presenceUnavailable and presenceRemoteViaRoute stay package-private:
// nothing outside this package assigns either directly (the real
// assignment happens in nodes.AdvancePresence; this package only ever
// reads the value back via DeviceRecord.Presence), and no ticket's
// contract names either as a public seam. This was corrected from an
// exported PresenceRemoteViaRoute (P1-E36-W7-S72's stale
// testonly-allow.json entry named internal/nodes/prober.go as this
// symbol's caller_site, which is architecturally impossible -
// internal/nodes cannot import internal/fleet/capacity without a cycle;
// prober.go's real production caller is nodes.PresenceRemoteViaRoute
// directly, which already has one via AdvancePresence/heartbeat.go and
// needs no allow-list entry of its own). Use nodes.PresenceUnavailable/
// nodes.PresenceRemoteViaRoute directly if an external caller ever needs
// either literal.
const (
	PresenceReachable      = nodes.PresenceReachable
	presenceUnavailable    = nodes.PresenceUnavailable
	presenceRemoteViaRoute = nodes.PresenceRemoteViaRoute
	// PresenceUnknown is the fail-closed value: a heartbeat-stream timeout
	// with no DIRECT probe evidence in the window (R-21.65) - distinct
	// from presenceUnavailable, which requires three consecutive DIRECT
	// probe misses.
	PresenceUnknown = nodes.PresenceUnknown
)

// Window is one rolling accounting window's utilization figure. UtilizationPct
// is negative (WindowUtilizationUnknown) when no real percentage source
// exists for this window - see compositor.go's derivation notes: never a
// silent 0, which would read as "confirmed empty" rather than "not measured".
type Window struct {
	UtilizationPct float64       `json:"utilization_pct"`
	ResetsIn       time.Duration `json:"resets_in"`
}

// WindowUtilizationUnknown is the sentinel Window.UtilizationPct value
// meaning "no real percentage source for this window" - a caller must check
// for it before treating UtilizationPct as a measured figure. Chosen as
// negative because 0 is itself a real reading (a fully-fresh window - a
// value indistinguishable from "not measured" if 0 were also used there,
// exactly the class of bug this ticket's "unknown must not report as zero"
// rule targets).
const WindowUtilizationUnknown = -1.0

// Bucket is one capacity bucket's state plus its two rolling windows.
type Bucket struct {
	State    State  `json:"state"`
	FiveHour Window `json:"five_hour"`
	SevenDay Window `json:"seven_day"`
}

// ProviderSlot is one provider's aggregated capacity view.
type ProviderSlot struct {
	ProfileRef     string                `json:"profile_ref"`
	Buckets        map[BucketKind]Bucket `json:"buckets"`
	State          State                 `json:"state"`
	ResetEstimate  time.Time             `json:"reset_estimate"`
	ReauthRequired bool                  `json:"reauth_required"`
	UpdatedAt      time.Time             `json:"updated_at"`
}

// NodeSlot is one node's aggregated presence/capacity view.
type NodeSlot struct {
	ID         string        `json:"id"`
	Presence   PresenceState `json:"presence"`
	CPUCores   int           `json:"cpu_cores"`
	RAMMB      int64         `json:"ram_mb"`
	GPU        bool          `json:"gpu"`
	Toolchains []string      `json:"toolchains"`
	Load       float64       `json:"load"`
	TrustTier  string        `json:"trust_tier"`
	UpdatedAt  time.Time     `json:"updated_at"`
	// Diagnostic carries a short, non-secret reason when a metric field is
	// omitted because its sampler source is unavailable (task 5 error
	// path: "sampler unavailable -> node metrics omitted with a
	// diagnostic field"). Empty means every populated field above is real.
	Diagnostic string `json:"diagnostic,omitempty"`
}

// TaskCapabilityRow is one (task_class, tier) row of the models x
// task-capability matrix.
type TaskCapabilityRow struct {
	TaskClass string  `json:"task_class"`
	Tier      string  `json:"tier"`
	Score     float64 `json:"score"`
}

// FleetSnapshot is the composed view of every capacity source at one
// instant. Seq is a monotonically increasing sequence number, incremented
// only when a material change occurs (diff.go's Diff), for SSE client
// dedup.
type FleetSnapshot struct {
	GeneratedAt      time.Time               `json:"generated_at"`
	Seq              uint64                  `json:"seq"`
	Providers        map[string]ProviderSlot `json:"providers"`
	Nodes            map[string]NodeSlot     `json:"nodes"`
	TaskCapabilities []TaskCapabilityRow     `json:"task_capabilities"`
	// Quota is the R-21.26 quota-domain snapshot (S-77.T3's
	// fleet.quota.snapshot payload), embedded ADDITIVELY: every
	// pre-existing field and JSON name above is unchanged, and an older
	// payload with no "quota" key decodes this field to its zero value
	// (topology.QuotaSnapshot{}, Domains nil, every domain confidence 0)
	// rather than failing to decode. Population is this ticket's own
	// composition-root concern -- see internal/fleet/capacity/compositor.go's
	// header (out of this ticket's files_scope) for where a future ticket
	// wires a live source into this field; until then it is always the
	// documented zero value on every Compositor-built snapshot.
	Quota topology.QuotaSnapshot `json:"quota"`
}
