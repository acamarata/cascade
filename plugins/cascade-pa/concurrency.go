// Purpose (this file): the two things that make a bridge mutation SAFE when
//
//	more than one goroutine touches one subject: a per-subject mutex every
//	store in this package shares, and the single-retry wrapper that turns the
//	durable store's compare-and-swap refusal into a re-read instead of a lost
//	write.
//
// WHY BOTH, AND WHY NEITHER ALONE IS ENOUGH. Every mutation here is a
//
//	read-modify-write: load the row, change two fields, save it. Two of them
//	interleaving inside ONE process silently reverted each other's columns —
//	the poll loop advancing the offset while issuance wrote back the copy it
//	had loaded, and (worse) a Bind's AllowedFrom being reverted, which reads
//	as "this bot is not paired" and quietly unpairs a bound bot. The mutex
//	fixes the in-process race. It cannot fix a SECOND process (or a second
//	*Stores over the same database), which is why the durable store also
//	compare-and-swaps on a version and why a refused write is RETRIED from a
//	fresh Load rather than forced through.
//
// Inputs: a subject id and the caller's own read-modify-write closure.
// Outputs: the closure's result, having run at most twice.
// Constraints:
//   - THE RETRY RE-READS. attempt() must perform its own Load; retrying with
//     the state the first attempt had loaded is exactly the lost update the
//     compare-and-swap exists to refuse.
//   - ONE RETRY, NOT A LOOP. A second conflict means real, sustained
//     contention on one subject, which for a bridge (one bot, one owner) is a
//     signal worth surfacing, not something to spin on.
//   - pkg/cascade's Is compares the KIND only, so the conflict is recognised
//     by kind AND message marker. A kind-only check would treat every other
//     KindConflict in the tree as a CAS miss and retry it.
//
// SPORT: plugins/cascade-pa subjectLocks/ADDED, IsStateConflict/ADDED
//
//	(P1-E23-W5-S48-T1).

package cascadepa

import (
	"strings"
	"sync"

	"github.com/acamarata/cascade/pkg/cascade"
)

// stateConflictMarker is the fragment a host's BridgeState.Save puts in a
// compare-and-swap refusal and IsStateConflict recognises. A constant so the
// sentinel's text and the predicate cannot drift apart.
const stateConflictMarker = "changed since it was read"

// ErrStateConflict is the plugin-facing compare-and-swap refusal: the row was
// written by somebody else between this caller's Load and its Save, so the
// write was refused rather than applied over newer state. A host's BridgeState
// implementation returns this (or any KindConflict error carrying the same
// marker) so the retry wrapper below can recognise it.
var ErrStateConflict = cascade.New(cascade.KindConflict,
	"cascade-pa: the bridge state row "+stateConflictMarker+"; the write was refused")

// IsStateConflict reports whether err is a compare-and-swap refusal.
func IsStateConflict(err error) bool {
	if err == nil {
		return false
	}
	return cascade.HasKind(err, cascade.KindConflict) && strings.Contains(err.Error(), stateConflictMarker)
}

// subjectLocks serialises mutations per subject inside one process. One
// instance is shared by every store NewStores builds, so IssueCode, Bind and
// UpdateLedger.Accept cannot interleave on the same subject.
type subjectLocks struct {
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// newSubjectLocks builds an empty lock table.
func newSubjectLocks() *subjectLocks {
	return &subjectLocks{locks: map[string]*sync.Mutex{}}
}

// lock acquires subject's mutex and returns its release function. Locks are
// created on demand and kept: a bridge host has a handful of subjects for the
// life of the process, so reclaiming them would add a second lifetime problem
// for no measurable gain.
func (l *subjectLocks) lock(subject string) func() {
	l.mu.Lock()
	m, ok := l.locks[subject]
	if !ok {
		m = &sync.Mutex{}
		l.locks[subject] = m
	}
	l.mu.Unlock()
	m.Lock()
	return m.Unlock
}

// mutate runs one subject's read-modify-write under that subject's lock,
// retrying it ONCE if the durable store refused the write because the row had
// changed. attempt must re-Load the row itself — see this file's header.
func mutate[T any](locks *subjectLocks, subject string, attempt func() (T, error)) (T, error) {
	unlock := locks.lock(subject)
	defer unlock()
	out, err := attempt()
	if IsStateConflict(err) {
		return attempt()
	}
	return out, err
}
