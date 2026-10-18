// Purpose: the data shapes of contract:fleet-lease: what a caller
// hands Fleet.Lease (DispatchRef, PlacementNeed), what it gets back (Lease),
// the pluggable lane floor (LaneFilter), the scheduler-decision port
// (DecisionRecord, DecisionRecorder) and FleetDeps, the one construction
// input.
//
// Inputs: none (shapes and their validators).
// Outputs: validated dispatch references and dependency sets.
// Constraints: PlacementNeed has no sensitivity field: the placement
// requirement's tier is DERIVED from the request, never supplied (M-6). The
// decision port carries the SQL handle as an interface so capacity never
// imports internal/learn. FleetDeps.Reservations extends the contract's field
// list by one read-only port: the Reserver exposes no read by execution id,
// and Fleet.ReservationByExecution and RepoOfLease need one.
//
// SPORT: fleet.capacity.lease.

package capacity

import (
	"context"
	"database/sql"
	"time"

	"github.com/acamarata/cascade/internal/conductor"
	"github.com/acamarata/cascade/internal/fleet/economics"
	"github.com/acamarata/cascade/internal/jobs"
	"github.com/acamarata/cascade/internal/nodes"
	"github.com/acamarata/cascade/pkg/cascade"
	"github.com/acamarata/cascade/pkg/provider"
)

// PlacementNeed names the node capabilities a dispatch requires. It carries
// no sensitivity: the requirement's tier is derived from the request.
type PlacementNeed struct {
	Capabilities []string
}

// DispatchRef is the caller's description of the work being leased.
// ProjectID is required; a nil Placement runs on the controller; Requests is
// at least 1. ScopeGlobs (repo leases) need a JobID and a RepoID.
type DispatchRef struct {
	JobID, RepoID, ProjectID string
	ScopeGlobs               []string
	AgentOnly                bool
	Placement                *PlacementNeed
	HydratedTokens, Requests int64
}

// Lease is one granted reservation: the ids that tie the reservation, the
// execution row and the decision row together, and where the work runs.
type Lease struct {
	ID, ReservationID, ExecutionID, DecisionID string
	SelectedTier                               Tier
	LaneID                                     conductor.LaneID
	NodeID                                     string
	Sensitivity                                provider.SensitivityTier
	RepoID                                     string // = DispatchRef.RepoID
	RepoLeaseIDs                               []string
	Issued                                     time.Time
	TTL                                        time.Duration
}

// LaneFilter is a pluggable floor that runs after the sensitivity filter and
// before the Selector. Admit reports whether slot may serve req; a refusal
// carries a short reason.
type LaneFilter interface {
	Name() string
	Admit(req ResourceRequest, slot ProviderSlot) (ok bool, reason string)
}

// DecisionRecord is the scheduler decision written in the Bind transaction.
type DecisionRecord struct {
	ID, JobID, ExecutionID string
	TaskClass              conductor.TaskClass
	SelectedTier           Tier
	LaneID                 conductor.LaneID
	NodeID                 string
	ScoreAtSelection       float64
	FallbackLevel          string
	JumpRuleFired          bool
	JumpReasonCode         JumpReasonCode
	ReserveTier0           bool
	DecidedAt              time.Time
}

// DecisionExecer is the write surface a *sql.Tx and a *sql.DB share.
type DecisionExecer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// DecisionRecorder records one decision through q, the Bind transaction.
type DecisionRecorder interface {
	Record(ctx context.Context, q DecisionExecer, d DecisionRecord) error
}

// ReservationReader is the read side of the reservation ledger
// (*economics.ReservationStore satisfies it).
type ReservationReader interface {
	GetByExecution(ctx context.Context, executionID string) (economics.Reservation, bool, error)
}

// FleetDeps is NewFleet's input. Every field is required except Floors.
type FleetDeps struct {
	Snapshot          func(context.Context) (FleetSnapshot, error)
	Selector          Selector
	Scorer            func(scopeKey string) CapabilityScorer
	Reserver          *economics.Reserver
	Reservations      ReservationReader
	Jobs              *jobs.Store
	Decisions         DecisionRecorder
	DomainOf          func(context.Context, conductor.LaneID) (string, error)
	Place             func(context.Context, nodes.Requirement) (nodeID string, err error)
	ControllerNodeID  string
	RestrictedPermits conductor.RestrictedPermits
	Floors            []LaneFilter
	Clock             Clock
	TTL               time.Duration
}

// missing names the first nil or empty required field, or "".
func (d FleetDeps) missing() string {
	checks := []struct {
		name  string
		unset bool
	}{
		{"Snapshot", d.Snapshot == nil}, {"Selector", d.Selector == nil}, {"Scorer", d.Scorer == nil},
		{"Reserver", d.Reserver == nil}, {"Reservations", d.Reservations == nil}, {"Jobs", d.Jobs == nil},
		{"Decisions", d.Decisions == nil}, {"DomainOf", d.DomainOf == nil}, {"Place", d.Place == nil},
		{"ControllerNodeID", d.ControllerNodeID == ""}, {"Clock", d.Clock == nil}, {"TTL", d.TTL <= 0},
	}
	for _, c := range checks {
		if c.unset {
			return c.name
		}
	}
	for _, f := range d.Floors {
		if f == nil {
			return "Floors"
		}
	}
	return ""
}

// validateDispatch refuses a DispatchRef the Fleet cannot lease, before any
// read or write.
func validateDispatch(d DispatchRef, attempt int) error {
	switch {
	case d.ProjectID == "":
		return cascade.New(cascade.KindInvalidInput, "capacity: DispatchRef.ProjectID is required")
	case d.Requests < 1:
		return cascade.Newf(cascade.KindInvalidInput, "capacity: DispatchRef.Requests must be >= 1, got %d", d.Requests)
	case d.HydratedTokens < 0:
		return cascade.Newf(cascade.KindInvalidInput, "capacity: DispatchRef.HydratedTokens must be >= 0, got %d", d.HydratedTokens)
	case attempt < 1:
		return cascade.Newf(cascade.KindInvalidInput, "capacity: attempt must be >= 1, got %d", attempt)
	case len(d.ScopeGlobs) > 0 && (d.JobID == "" || d.RepoID == ""):
		return cascade.New(cascade.KindInvalidInput, "capacity: DispatchRef.ScopeGlobs needs a JobID and a RepoID")
	}
	return nil
}
