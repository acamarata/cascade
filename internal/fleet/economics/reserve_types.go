// Purpose: the R-21.35 (as amended by R-21.82/.97/.114/.115/.121/.122/.129)
//
//	Reservation record -- the one reservation ledger row Fleet leases are
//	created on -- its two closed enums (ReservationState,
//	ReservationKind) with fail-closed parsers, the legal transition
//	table, the step-ledger entry shape and ReserveRequest. The seams the
//	Reserver calls through live in reserve_ledger.go.
//
// Inputs: none at this layer -- this file declares vocabulary only.
// Outputs: Reservation, Estimate, Actual, Step, the two enums and their
//
//	Parse functions, ReserveRequest.
//
// Constraints: no bare time.Now (Art.7.3 -- every timestamp field is set
//
//	by a caller reading the injected Clock); neither enum has a
//	permissive zero value (06 Sec5.15); this package imports no jobs
//	package. The row carries its placement (NodeID,
//	SelectedTier, Sensitivity, DecisionID, written by Bind) and the
//	inputs recovery and resume need (RepoID, ScopeGlobs), so nothing is
//	read back from an ephemeral request after a restart.
//
// SPORT: fleet/economics/reservation/ADD (P1-E41-W9-S79-T4).

package economics

import "github.com/acamarata/cascade/pkg/cascade"

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

// StepName is one of the step ledger's acquisition steps. There is no
// permit step: the permit is taken per adapter call by WithPermit
// (see reserve_permit.go), never inside Reserve.
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

// The four declared StepState members. StepNotRun is written only by
// RecoverSteps, when the re-query proves a pending step's effect never
// happened.
const (
	StepPending     StepState = "pending"
	StepAcquired    StepState = "acquired"
	StepCompensated StepState = "compensated"
	StepNotRun      StepState = "not_run"
)

// Step is one step-ledger entry, written BEFORE the acquisition
// it describes and keyed `<reservation id>:<step>`. A crash between an
// acquisition and the persistence of its handle is recoverable because
// RecoverSteps re-queries the subsystem by the inputs the row persists
// (repo_id, job_id, scope_globs for leases; the recorded lease id for
// the worktree).
type Step struct {
	Step           StepName  `json:"step"`
	IdempotencyKey string    `json:"idempotency_key"`
	Handle         string    `json:"handle"`
	State          StepState `json:"state"`
}

// Reservation is the R-21.35 ledger row: the single record that makes
// the quota/leases/worktree acquisitions one atomic unit.
type Reservation struct {
	ID                string           `json:"id"`
	ExecutionID       string           `json:"execution_id"`
	JobID             string           `json:"job_id"`
	ProjectID         string           `json:"project_id"`
	LaneID            string           `json:"lane_id"`
	DomainID          string           `json:"domain_id"`
	ScopeID           string           `json:"scope_id"`
	RepoID            string           `json:"repo_id"`
	ScopeGlobs        []string         `json:"scope_globs"`
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
	NodeID            string           `json:"node_id"`
	SelectedTier      string           `json:"selected_tier"`
	Sensitivity       string           `json:"sensitivity"`
	DecisionID        string           `json:"decision_id"`
}

// ReserveRequest is Reserve's argument: everything the pipeline needs to
// persist the held row and drive the acquisition chain. ExecutionID is
// required (unique per row); JobID and RepoID are required only when
// ScopeGlobs is non-empty. ScopeID is derived from the domain by the
// Reserver; a caller that sets it must name that same scope.
type ReserveRequest struct {
	ExecutionID  string
	JobID        string
	ProjectID    string
	LaneID       string
	DomainID     string
	ScopeID      string
	RepoID       string
	Kind         ReservationKind
	Estimate     Estimate
	BasePrice    float64
	ScopeGlobs   []string
	Priority     int
	AllowReserve bool
}
