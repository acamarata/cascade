package sync

import (
	"errors"

	"github.com/acamarata/cascade/pkg/cascade"
)

// Purpose (this file): the merge layer's typed refusals.
//
// EACH ONE IS A SENTINEL PLUS A FORMATTER. The sentinel is what a caller
//
//	matches on — `sync conflicts list` has to tell a divergence from a
//	stale cursor from an unadmitted blob — and the formatter carries the
//	numbers an operator needs to act. Returning only a formatted error
//	would make every caller match on message text, which is how a reworded
//	message becomes a behaviour change.
//
// SPORT: internal/sync merge errors (ADD) — P1-E17-W4-S38-T2.

var (
	// ErrCursorTooOld refuses a peer whose cursor predates the oldest
	// retained tombstone. KindConflict: the request conflicts with the
	// retention policy, it does not name something missing.
	ErrCursorTooOld = errors.New("sync: peer cursor predates the oldest retained tombstone")
	// ErrPhaseStateDiverged refuses a non-fast-forward phase-state
	// carriage. KindConflict for the same reason.
	ErrPhaseStateDiverged = errors.New("sync: phase state diverged; carriage is fetch and fast-forward only")
	// ErrBlobNotAdmitted refuses a blob into the union that the staging
	// layer never admitted. KindIntegrity: the bytes are not what their
	// address says they are.
	ErrBlobNotAdmitted = errors.New("sync: blob was never admitted under its content address")
)

// ErrCursorTooOldf reports a stale peer cursor, naming both revisions so
// an operator can see how far behind the peer is.
//
// The remedy is in the message because it is not obvious: the peer is not
// broken and retrying will not help — it has to full-resync, and a message
// that only said "too old" would send somebody looking for a repair that
// does not exist.
func ErrCursorTooOldf(peerCursor, oldestTombstone uint64) error {
	return cascade.Wrapf(cascade.KindConflict, ErrCursorTooOld,
		"sync: the peer's cursor is at revision %d and the oldest retained tombstone is %d; "+
			"catching it up incrementally would resurrect deleted records, so it must full-resync",
		peerCursor, oldestTombstone)
}

// ErrPhaseStateDivergedf reports a refused phase-state carriage, naming
// both refs so the divergence is inspectable with git.
func ErrPhaseStateDivergedf(localRef, remoteRef, detail string) error {
	return cascade.Wrapf(cascade.KindConflict, ErrPhaseStateDiverged,
		"sync: phase state at %s cannot fast-forward to %s (%s); "+
			"the engine does not merge phase state — resolve it in the repository",
		localRef, remoteRef, detail)
}

// ErrBlobNotAdmittedf reports a blob that never passed staging admission.
func ErrBlobNotAdmittedf(address string) error {
	return cascade.Wrapf(cascade.KindIntegrity, ErrBlobNotAdmitted,
		"sync: blob %s was not admitted; the union carries admitted blobs only, because an "+
			"address is only a name until the bytes under it have been verified", address)
}
