// Purpose: the R-21.35 (as amended by R-21.82/.97/.114/.115/.121/.122/.129)
//
//	Reservation record, its two closed enums (ReservationState,
//	ReservationKind) with fail-closed parsers, the legal transition
//	table, the step-ledger entry shape and the seven func-typed
//	injection seams reserve.go's pipeline calls through.
//
// Inputs: none at this layer -- this file declares vocabulary only.
// Outputs: Reservation, Estimate, Actual, Step, the two enums and their
//
//	Parse functions, and the Reserver's seam function types.
//
// Constraints: no bare time.Now (Art.7.3 -- every timestamp field is set
//
//	by a caller reading the injected Clock); neither enum has a
//	permissive zero value (06 Sec5.15); this package imports no jobs
//	package (Art.10.2) -- the lease and worktree acquisition steps arrive
//	as func-typed seams instead.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import (
	"context"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/pkg/cascade"
)

// ReservationState is the closed R-21.122 reservation lifecycle
// vocabulary. The zero value is intentionally not a member.
type ReservationState string

// The five declared ReservationState members.
const (
	ReservationHeld       ReservationState = "held"
	ReservationParked     ReservationState = "parked"
	ReservationCommitted  ReservationState = "committed"
	ReservationReleased   ReservationState = "released"
	ReservationRolledBack ReservationState = "rolled_back"
)

// Valid reports whether s is one of the five declared members.
func (s ReservationState) Valid() bool {
	switch s {
	case ReservationHeld, ReservationParked, ReservationCommitted, ReservationReleased, ReservationRolledBack:
		return true
	}
	return false
}

// ParseReservationState parses s, failing closed to
// ErrUnknownReservationState for anything outside the five members
// (06 Sec5.15 -- no permissive zero value).
func ParseReservationState(s string) (ReservationState, error) {
	st := ReservationState(s)
	if !st.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationState, "%q", s)
	}
	return st, nil
}

// Terminal reports whether s is one of the two terminal states
// (released, rolled_back) that no further transition ever leaves.
func (s ReservationState) Terminal() bool {
	return s == ReservationReleased || s == ReservationRolledBack
}

// ReservationKind is the closed R-21.82 reservation kind vocabulary. The
// zero value is intentionally not a member.
type ReservationKind string

// The two declared ReservationKind members.
const (
	// ReservationInteractive is the default: takes leases and a
	// worktree.
	ReservationInteractive ReservationKind = "interactive"
	// ReservationBatch takes quota only: no worktree, no write lease,
	// results written to an artifact, a separate long expiry and no
	// count against workspace.max_writable_worktrees.
	ReservationBatch ReservationKind = "batch"
)

// Valid reports whether k is one of the two declared members.
func (k ReservationKind) Valid() bool {
	return k == ReservationInteractive || k == ReservationBatch
}

// ParseReservationKind parses k, failing closed to
// ErrUnknownReservationKind for anything outside interactive|batch.
func ParseReservationKind(k string) (ReservationKind, error) {
	rk := ReservationKind(k)
	if !rk.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrUnknownReservationKind, "%q", k)
	}
	return rk, nil
}

// legalTransitions is the R-21.122 closed transition table: every legal
// (from, to) move. held<->parked, held->committed, committed<->parked,
// held->released, held->rolled_back, parked->released,
// parked->rolled_back and committed->released; every other move is
// illegal. released and rolled_back are terminal (no outbound row).
var legalTransitions = map[ReservationState]map[ReservationState]bool{
	ReservationHeld: {
		ReservationParked:     true,
		ReservationCommitted:  true,
		ReservationReleased:   true,
		ReservationRolledBack: true,
	},
	ReservationParked: {
		ReservationHeld:       true,
		ReservationReleased:   true,
		ReservationRolledBack: true,
	},
	ReservationCommitted: {
		ReservationParked:   true,
		ReservationReleased: true,
	},
}

// TransitionAllowed reports whether moving from `from` to `to` is one of
// the closed table's legal rows.
func TransitionAllowed(from, to ReservationState) bool {
	row, ok := legalTransitions[from]
	if !ok {
		return false
	}
	return row[to]
}

// Estimate is the R-21.35 pre-dispatch resource estimate: hydrated
// context tokens (plus the R-21.35 10% margin), the task-class default
// output tokens, and the provider-call count the dispatch will make.
type Estimate struct {
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
	Requests  int64 `json:"requests"`
}

// Actual is the R-21.121 post-dispatch usage report Release reconciles
// against Estimate.
type Actual struct {
	TokensIn  int64 `json:"tokens_in"`
	TokensOut int64 `json:"tokens_out"`
	Requests  int64 `json:"requests"`
}

// ActualSource records whether Actual came from a real adapter usage
// report ("reported") or was left at the estimate because none arrived
// ("estimated") -- R-21.121 never silently zeroes a missing report.
type ActualSource string

// The two declared ActualSource members.
const (
	ActualSourceReported  ActualSource = "reported"
	ActualSourceEstimated ActualSource = "estimated"
)

// StepName is one of the R-21.97 ledger's acquisition steps. The permit
// step is deferred to the ticket that builds reserve_permit.go's
// WithPermit (R-21.122 -- the permit is taken per adapter call, never
// inside Reserve, so this package's own pipeline never appends one).
type StepName string

// The three StepName members this package's pipeline appends, in
// R-21.35 application order.
const (
	StepQuota    StepName = "quota"
	StepLeases   StepName = "leases"
	StepWorktree StepName = "worktree"
)

// StepState is one ledger Step's own lifecycle.
type StepState string

// The three declared StepState members.
const (
	StepPending     StepState = "pending"
	StepAcquired    StepState = "acquired"
	StepCompensated StepState = "compensated"
)

// Step is one R-21.97 step-ledger entry: the reservation-derived
// idempotency key `<reservation id>:<step>` is written BEFORE the
// acquisition it describes, so a crash between an acquisition and the
// persistence of its handle is always recoverable by re-querying the
// subsystem for that same key.
type Step struct {
	Step           StepName  `json:"step"`
	IdempotencyKey string    `json:"idempotency_key"`
	Handle         string    `json:"handle"`
	State          StepState `json:"state"`
}

// Reservation is the R-21.35 ledger row: the single record that makes
// the quota/leases/worktree/permit acquisitions one atomic unit.
type Reservation struct {
	ID                string           `json:"id"`
	JobID             string           `json:"job_id"`
	ProjectID         string           `json:"project_id"`
	LaneID            string           `json:"lane_id"`
	DomainID          string           `json:"domain_id"`
	ScopeID           string           `json:"scope_id"`
	Kind              ReservationKind  `json:"kind"`
	Estimate          Estimate         `json:"estimate"`
	Actual            Actual           `json:"actual"`
	ActualSource      ActualSource     `json:"actual_source"`
	BasePrice         float64          `json:"base_price"`
	PriceTableVersion string           `json:"price_table_version"`
	ScarceUnits       float64          `json:"scarce_units"`
	PermitID          string           `json:"permit_id"`
	WorktreeID        string           `json:"worktree_id"`
	LeaseIDs          []string         `json:"lease_ids"`
	Steps             []Step           `json:"steps"`
	State             ReservationState `json:"state"`
	OwnerEpoch        string           `json:"owner_epoch"`
	HeartbeatAt       int64            `json:"heartbeat_at"`
	ExpiresAt         int64            `json:"expires_at"`
	Created           int64            `json:"created"`
}

// ReserveRequest is Reserve's argument: everything the pipeline needs to
// persist the held row and drive the acquisition chain.
type ReserveRequest struct {
	JobID        string
	ProjectID    string
	LaneID       string
	DomainID     string
	ScopeID      string
	Kind         ReservationKind
	Estimate     Estimate
	BasePrice    float64
	ScopeGlobs   []string
	Priority     int
	AllowReserve bool
}

// DomainBucketsFn resolves a domain's dimension buckets for the
// admission check. Bound at the composition root to
// topology.Store-backed lookups; this package performs no bucket
// counter mutation of its own (R-21.114).
type DomainBucketsFn func(ctx context.Context, domainID string) (map[string]Bucket, error)

// Bucket is the subset of topology.Bucket the admission check needs,
// declared locally so this package imports topology for identifier
// types only, per the ticket's IMPORT DIRECTION note.
type Bucket struct {
	CapacityObserved          int64
	CommittedSinceObservation int64
}

// PermitFn is the ONE R-16.64 admission API this package calls: given an
// AdmissionRequest it returns a Permit (or a typed error). Reserve
// itself never calls PermitFn (R-21.122 -- the permit is taken
// immediately before each adapter call, by WithPermit); NewReserver
// still refuses construction with a nil PermitFn so a future WithPermit
// caller can rely on it being present.
type PermitFn func(ctx context.Context, req governor.AdmissionRequest) (governor.Permit, error)

// AcquireLeasesFn acquires the AC/S-59.T2 leases scopeGlobs names for
// jobID, returning the acquired lease ids.
type AcquireLeasesFn func(ctx context.Context, jobID string, scopeGlobs []string) ([]string, error)

// ReleaseLeasesFn releases previously acquired leases by id.
type ReleaseLeasesFn func(ctx context.Context, leaseIDs []string) error

// AllocateWorktreeFn allocates the AC/S-59.T3 worktree bound to leaseID,
// returning the worktree id.
type AllocateWorktreeFn func(ctx context.Context, leaseID string) (string, error)

// RemoveWorktreeFn removes a previously allocated worktree by id.
type RemoveWorktreeFn func(ctx context.Context, worktreeID string) error

// ValidateLeasesFn validates each checkpointed lease id against the
// AC/S-59.T2 lease table on resume, returning the subset still valid
// (holder job id AND expiry both match).
type ValidateLeasesFn func(ctx context.Context, jobID string, leaseIDs []string) (valid []string, err error)
