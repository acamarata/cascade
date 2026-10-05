// Purpose: the Reserver itself: the seams it calls through, NewReserver's
//
//	required-seam check, the in-memory set of reservation ids this
//	process holds (the liveness half of the ledger), and the step-ledger
//	helpers every acquisition and recovery path shares.
//
// Inputs: a ReservationStore, an injected Clock, this daemon's owner
//
//	epoch and a ReserverSeams value.
//
// Outputs: Reserver, ReserverSeams, the seam function types, NewReserver.
// Constraints: every seam is required -- a nil seam is a construction
//
//	error, never a silent no-op. This package imports no jobs package:
//	leases and worktrees arrive through these seams, and the recovery
//	lookups are keyed only by what the ledger row itself stores.
//
// SPORT: fleet/economics/reservation/ADD.

package economics

import (
	"context"
	"sort"
	"sync"

	"github.com/acamarata/cascade/internal/fleet/governor"
	"github.com/acamarata/cascade/internal/fleet/topology"
	"github.com/acamarata/cascade/pkg/cascade"
)

// PermitFn is the one admission API this package calls, and only from
// WithPermit: the permit is per adapter call, never taken by Reserve.
type PermitFn func(ctx context.Context, req governor.AdmissionRequest) (governor.Permit, error)

// AcquireLeasesFn acquires the leases scopeGlobs names in repoID for
// jobID, returning the acquired lease ids.
type AcquireLeasesFn func(ctx context.Context, repoID, jobID string, scopeGlobs []string) ([]string, error)

// ReleaseLeasesFn releases previously acquired leases by id.
type ReleaseLeasesFn func(ctx context.Context, leaseIDs []string) error

// AllocateWorktreeFn allocates the worktree bound to leaseID, returning
// its id.
type AllocateWorktreeFn func(ctx context.Context, leaseID string) (string, error)

// RemoveWorktreeFn removes a previously allocated worktree by id.
type RemoveWorktreeFn func(ctx context.Context, worktreeID string) error

// ValidateLeasesFn validates stored lease ids for jobID on resume,
// returning the subset still valid (holder and expiry both match).
type ValidateLeasesFn func(ctx context.Context, jobID string, leaseIDs []string) (valid []string, err error)

// RenewLeasesFn renews the given leases for one heartbeat tick. A refusal
// of kind Conflict (a fenced lease, or one past its ttl plus grace) means
// the lease is lost.
type RenewLeasesFn func(ctx context.Context, leaseIDs []string) error

// FindLeasesFn returns every live lease id jobID holds in repoID on any of
// scopeGlobs. A partial set left by a crash mid-acquisition is returned
// as is; empty means none is held.
type FindLeasesFn func(ctx context.Context, repoID, jobID string, scopeGlobs []string) ([]string, error)

// FindWorktreeFn reports the worktree allocated for leaseID, if any.
type FindWorktreeFn func(ctx context.Context, leaseID string) (path string, found bool, err error)

// AttentionFn raises one attention item for reservationID. reason is a
// closed code (AttentionLostLease), never free text.
type AttentionFn func(ctx context.Context, reservationID, reason string) error

// AttentionLostLease is the attention reason Heartbeat raises when a
// reservation's lease is fenced or expired.
const AttentionLostLease = "lost_lease"

// ReserverSeams carries every dependency the Reserver calls through.
// Every field is required.
type ReserverSeams struct {
	// Buckets returns the domain's kind, its limit scope and its buckets
	// keyed by dimension name.
	Buckets          func(ctx context.Context, domainID string) (topology.QuotaDomainKind, topology.LimitScopeID, map[string]topology.Bucket, error)
	RenewLeases      RenewLeasesFn
	Permit           PermitFn
	AcquireLeases    AcquireLeasesFn
	ReleaseLeases    ReleaseLeasesFn
	AllocateWorktree AllocateWorktreeFn
	RemoveWorktree   RemoveWorktreeFn
	ValidateLeases   ValidateLeasesFn
	// ReserveFraction returns the domain account's preserved weekly
	// reserve (its role default).
	ReserveFraction func(ctx context.Context, domainID string) (float64, error)
	// Projects is the live active-project count the share divides by.
	Projects       *ActiveProjectCount
	FindLeases     FindLeasesFn
	FindWorktree   FindWorktreeFn
	RaiseAttention AttentionFn
}

// missing names the first nil seam, or "" when every seam is set.
func (s ReserverSeams) missing() string {
	checks := []struct {
		name  string
		unset bool
	}{
		{"Buckets", s.Buckets == nil}, {"RenewLeases", s.RenewLeases == nil}, {"Permit", s.Permit == nil},
		{"AcquireLeases", s.AcquireLeases == nil}, {"ReleaseLeases", s.ReleaseLeases == nil},
		{"AllocateWorktree", s.AllocateWorktree == nil}, {"RemoveWorktree", s.RemoveWorktree == nil},
		{"ValidateLeases", s.ValidateLeases == nil}, {"ReserveFraction", s.ReserveFraction == nil},
		{"Projects", s.Projects == nil}, {"FindLeases", s.FindLeases == nil},
		{"FindWorktree", s.FindWorktree == nil}, {"RaiseAttention", s.RaiseAttention == nil},
	}
	for _, c := range checks {
		if c.unset {
			return c.name
		}
	}
	return ""
}

// Reserver drives the reservation ledger. The zero value is not usable;
// construct with NewReserver.
type Reserver struct {
	store      *ReservationStore
	clock      Clock
	ownerEpoch string
	seams      ReserverSeams
	// permit is seams.Permit, called only by WithPermit.
	permit PermitFn

	// mu serializes the held insert, the availability check and the
	// share check across concurrent Reserve calls, so two racing callers
	// never read the same pre-decision outstanding total.
	mu sync.Mutex

	// liveMu guards live, the ids this process holds (added by Reserve
	// and Adopt, removed at a terminal state or a lost lease).
	liveMu sync.Mutex
	live   map[string]struct{}

	// claimMu orders Adopt against ExpireStale's per-row claim: an id in
	// expiring is being retired and cannot be adopted, and an id Adopt
	// has tracked is never claimed. Held only for the check and Adopt's
	// own row write, never across a seam call.
	claimMu  sync.Mutex
	expiring map[string]struct{}

	// acqMu guards acquiring: the ids Reserve is still admitting or
	// acquiring leases and a worktree for, each with whether a cascade
	// asked for it to be parked once that Reserve finishes.
	acqMu     sync.Mutex
	acquiring map[string]bool
}

// NewReserver constructs a Reserver. Every seam is required; a nil
// argument is a construction-time KindInvalidInput naming it.
func NewReserver(store *ReservationStore, clock Clock, ownerEpoch string, s ReserverSeams) (*Reserver, error) {
	switch {
	case store == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil store")
	case clock == nil:
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil Clock")
	case ownerEpoch == "":
		return nil, cascade.New(cascade.KindInvalidInput, "economics: NewReserver requires a non-empty ownerEpoch")
	}
	if name := s.missing(); name != "" {
		return nil, cascade.Newf(cascade.KindInvalidInput, "economics: NewReserver requires a non-nil ReserverSeams.%s", name)
	}
	return &Reserver{store: store, clock: clock, ownerEpoch: ownerEpoch, seams: s, permit: s.Permit,
		live: map[string]struct{}{}, expiring: map[string]struct{}{}, acquiring: map[string]bool{}}, nil
}

// track adds id to the in-memory set this process holds.
func (rv *Reserver) track(id string) {
	rv.liveMu.Lock()
	defer rv.liveMu.Unlock()
	rv.live[id] = struct{}{}
}

// untrack removes id from the in-memory set.
func (rv *Reserver) untrack(id string) {
	rv.liveMu.Lock()
	defer rv.liveMu.Unlock()
	delete(rv.live, id)
}

// isTracked reports whether this process holds id.
func (rv *Reserver) isTracked(id string) bool {
	rv.liveMu.Lock()
	defer rv.liveMu.Unlock()
	_, ok := rv.live[id]
	return ok
}

// trackedIDs returns the held ids, sorted for a deterministic tick.
func (rv *Reserver) trackedIDs() []string {
	rv.liveMu.Lock()
	defer rv.liveMu.Unlock()
	out := make([]string, 0, len(rv.live))
	for id := range rv.live {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

// appendStep appends a pending Step for name keyed `<id>:<step>`,
// written BEFORE the acquisition it describes.
func appendStep(r Reservation, name StepName) Reservation {
	r.Steps = append(r.Steps, Step{Step: name, IdempotencyKey: r.ID + ":" + string(name), State: StepPending})
	return r
}

// setStep sets the most recent Step for name to state with handle.
func setStep(r Reservation, name StepName, state StepState, handle string) Reservation {
	steps := make([]Step, len(r.Steps))
	copy(steps, r.Steps)
	for i := len(steps) - 1; i >= 0; i-- {
		if steps[i].Step == name {
			steps[i].State, steps[i].Handle = state, handle
			break
		}
	}
	r.Steps = steps
	return r
}
