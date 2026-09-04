package memory

// Purpose: the candidate vocabulary the Q-6 consolidation ladder counts
//   against: the CandidateStatus lifecycle, the CandidateEntry view a
//   caller reads, the Observation a caller records, the CandidateLedger
//   contract, and the domain sentinels every refusal carries.
// Inputs: caller-supplied observations, each naming the session that made
//   it and carrying the draft record a promotion would make durable.
// Outputs: candidate views, or a typed pkg/cascade error naming exactly
//   which refusal happened.
// Constraints: promotion is a one-way door into what the system later
//   treats as true, so every refusal here fails closed: an observation
//   whose draft would not be a valid durable record is refused at Observe
//   rather than counted and discovered at Promote. No I/O and no clock in
//   this file.
// SPORT: G/memory-candidate-ledger (ADD, placeholder per T-1
//   sport_updates).

import (
	"context"
	"time"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Candidate sentinel errors. Each names one refusal precisely so a caller
// can tell them apart with errors.Is rather than by matching message text,
// and each wraps exactly one frozen pkg/cascade Kind; no Kind is invented.
var (
	// ErrNoSuchCandidate is returned when no candidate is recorded under
	// the given kind and name.
	ErrNoSuchCandidate = cascade.New(cascade.KindNotFound, "memory candidate not found")
	// ErrAlreadyPromoted is returned by Promote for a candidate that is
	// already promoted. It is a conflict rather than a success: a caller
	// asking to promote twice has lost track of the state, and returning
	// nil would hide that.
	ErrAlreadyPromoted = cascade.New(cascade.KindConflict, "memory candidate is already promoted")
	// ErrNotPromoted is returned by Revert for a candidate that was never
	// promoted. There is nothing to take back, and marking a pending
	// candidate reverted would erase its accumulated references.
	ErrNotPromoted = cascade.New(cascade.KindConflict, "memory candidate is not promoted")
	// ErrInvalidSessionID is returned for an empty or unusable session
	// identifier. The session is what makes an observation distinct, so an
	// unusable one cannot be counted toward a threshold.
	ErrInvalidSessionID = cascade.New(cascade.KindInvalidInput, "invalid session identifier")
	// ErrInvalidCandidateStatus is returned for a status outside the three
	// ratified values.
	ErrInvalidCandidateStatus = cascade.New(cascade.KindInvalidInput, "invalid candidate status")
	// ErrMalformedCandidate is returned when a candidate file exists but
	// cannot be parsed whole. Reads fail closed on it, and no promotion
	// decision is ever taken from evidence that could not be read.
	ErrMalformedCandidate = cascade.New(cascade.KindIntegrity, "malformed memory candidate record")
	// ErrUnsupportedCandidateFormat is returned for a candidate record
	// whose declared format version this build does not know. It is
	// separate from ErrMalformedCandidate for the same reason
	// ErrUnsupportedFormat is separate from ErrMalformedEntry: this is the
	// forward-compatibility refusal, a file written by a newer build, not
	// a damaged one.
	ErrUnsupportedCandidateFormat = cascade.New(cascade.KindUnsupported,
		"unsupported memory candidate record format version")
)

// maxSessionIDLen caps a session identifier. It is generous next to the
// identifiers this repo actually mints (UUIDs) and exists so a caller
// cannot grow a candidate record without bound by observing under
// ever-longer session names.
const maxSessionIDLen = 128

// CandidateStatus is a candidate's position on the promotion ladder.
type CandidateStatus string

// The three ratified statuses. This set is closed: a fourth would change
// what a stored record can say and is a format change, not an additive
// one.
const (
	// CandidatePending means the candidate is still accumulating
	// references and has never been made durable.
	CandidatePending CandidateStatus = "pending"
	// CandidatePromoted means a durable record was written from this
	// candidate. Further observations do not change it (R-14.22).
	CandidatePromoted CandidateStatus = "promoted"
	// CandidateReverted means a promotion was taken back. The next
	// observation restarts the count from one (R-14.22).
	CandidateReverted CandidateStatus = "reverted"
)

// allCandidateStatuses lists every valid status in a fixed order, for the
// same determinism reason as allKinds: a map would make any output derived
// from it vary between runs.
var allCandidateStatuses = []CandidateStatus{
	CandidatePending, CandidatePromoted, CandidateReverted,
}

// Valid reports whether s is one of the three ratified statuses.
func (s CandidateStatus) Valid() bool {
	for _, c := range allCandidateStatuses {
		if s == c {
			return true
		}
	}
	return false
}

// String returns the status's on-disk spelling.
func (s CandidateStatus) String() string { return string(s) }

// ParseCandidateStatus converts an on-disk string to a CandidateStatus,
// refusing anything else. It fails closed rather than defaulting to
// pending: reading an unknown status as pending would let a record written
// by a newer build be re-promoted from zero.
func ParseCandidateStatus(s string) (CandidateStatus, error) {
	c := CandidateStatus(s)
	if !c.Valid() {
		return "", cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidCandidateStatus,
			"unknown candidate status %q", s)
	}
	return c, nil
}

// CandidateEntry is the caller-visible state of one candidate.
//
// It is a view, not the stored record: the ledger also keeps the draft the
// candidate would be promoted into, and the revert history, neither of
// which a counting caller needs. Nothing here is a pointer into ledger
// state, so a caller cannot mutate the ledger by editing what it received.
type CandidateEntry struct {
	// Name is the candidate's identifier within its kind, and the name the
	// durable record takes when it is promoted.
	Name string
	// Kind is the candidate's taxonomy member.
	Kind MemoryKind
	// SessionIDs are the distinct sessions that have observed this
	// candidate, in lexical order. Each session appears at most once.
	SessionIDs []string
	// RefCount is how many references have been counted toward promotion.
	// Repeat observations from a session already in SessionIDs do not add
	// to it, so a chatty caller in one session cannot climb the ladder.
	RefCount int
	// PromotedAt is when the durable record was written, or nil when this
	// candidate has never been promoted or its promotion was reverted.
	PromotedAt *time.Time
	// SnoozeUntil is when a deferred review becomes due again, or nil.
	// Nothing in this ticket's code writes it; it is set only by the
	// review-queue defer action, and is carried through the record here so
	// that action has a field to write.
	SnoozeUntil *time.Time
	// Status is the candidate's position on the ladder.
	Status CandidateStatus
}

// Observation is one recorded reference to a candidate.
//
// Draft carries the record a promotion would write, not merely a name:
// promotion is mechanical and happens with no caller present, so the
// content has to already be in hand when the threshold is crossed. Draft
// is validated at Observe, which is what keeps the ledger from counting
// evidence that could never become a durable record.
type Observation struct {
	// SessionID identifies the session making the observation. It is what
	// makes one observation distinct from another, so it is required.
	SessionID string
	// Draft is the record this candidate becomes when promoted. Its Kind
	// and Name are the candidate's identity.
	Draft MemoryEntry
}

// CandidateLedger records observations of memory candidates and moves them
// along the promotion ladder.
//
// Every method returns a pkg/cascade taxonomy error or nil; no raw
// os.PathError escapes an implementation. Reads fail closed: a candidate
// record that cannot be parsed whole is refused rather than treated as
// absent, because treating unreadable evidence as "no evidence" would
// silently restart a count that had already been earned.
type CandidateLedger interface {
	// Observe records one reference to a candidate and returns its state
	// afterwards. It creates the candidate when it does not exist yet.
	//
	// Observing a PROMOTED candidate is a no-op: the returned state is
	// unchanged, nothing is written, and no event is emitted. Observing a
	// REVERTED candidate restarts the count at one with a fresh session
	// set containing only the observing session (R-14.22).
	Observe(ctx context.Context, obs Observation) (CandidateEntry, error)
	// Promote writes the candidate's draft as a durable record through the
	// MemoryStore and marks the candidate promoted, emitting a promotion
	// event. It returns ErrNoSuchCandidate when there is no such
	// candidate and ErrAlreadyPromoted when it is already promoted.
	Promote(ctx context.Context, kind MemoryKind, name string) (CandidateEntry, error)
	// Revert takes a promotion back: the candidate becomes reverted and a
	// revert event is emitted. It returns ErrNotPromoted for a candidate
	// that was never promoted.
	//
	// Revert does not delete the durable record the promotion wrote. The
	// candidate keeps its counts and gains the revert reason, so a
	// promotion a user later asks about can still be accounted for;
	// removing the durable record is the forget pipeline's decision, not
	// this one's.
	Revert(ctx context.Context, kind MemoryKind, name, reason string) (CandidateEntry, error)
	// Get returns the current state of one candidate, or
	// ErrNoSuchCandidate.
	Get(ctx context.Context, kind MemoryKind, name string) (CandidateEntry, error)
	// List returns the names of every candidate of a kind, in lexical
	// order.
	//
	// It returns names rather than records for the same reason
	// MemoryStore.List does: parsing every record would let one damaged
	// file fail the whole listing, and a damaged candidate must stay
	// visible enough to be inspected and removed.
	List(ctx context.Context, kind MemoryKind) ([]string, error)
}

// validateSessionID refuses a session identifier that cannot be counted.
// The rule is deliberately narrow: a session identifier reaches a stored
// record and a listing, so a control character or an unbounded string in
// it would make stored evidence unreadable or unbounded.
func validateSessionID(id string) error {
	if id == "" {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSessionID,
			"session id is empty")
	}
	if len(id) > maxSessionIDLen {
		return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSessionID,
			"session id is %d bytes, over the %d-byte limit", len(id), maxSessionIDLen)
	}
	for i := 0; i < len(id); i++ {
		if id[i] < 0x20 || id[i] == 0x7f {
			return cascade.Wrapf(cascade.KindInvalidInput, ErrInvalidSessionID,
				"session id contains a control byte at offset %d", i)
		}
	}
	return nil
}
