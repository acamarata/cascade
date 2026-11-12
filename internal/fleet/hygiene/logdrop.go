// Package hygiene contains policy checks for keeping stored log material
// only as long as it is still needed by the fleet hygiene pipeline.
package hygiene

// LearnedNote describes the state of a learned note that may allow its logs
// to be dropped.
type LearnedNote struct {
	// Gaps is the number of unresolved gaps in the learned note.
	Gaps int

	// Pending reports whether the learned note is still pending processing.
	Pending bool

	// InDigest reports whether the note summary is present in the digest.
	InDigest bool

	// QueueLen is the number of queued items associated with the note.
	QueueLen int
}

// CanDropLogs reports whether logs for the given learned note may be dropped.
//
// Logs may be dropped only when every condition is met: the note has no gaps,
// is not pending, has its summary in the digest, and has an empty queue.
// The conditions are checked in that order, and the first unmet condition is
// returned as the reason. When all conditions are met it returns true and an
// empty reason.
//
// The function fails closed: any unexpected or non-zero negative counter is
// treated as not safe to drop.
func CanDropLogs(n LearnedNote) (bool, string) {
	if n.Gaps != 0 {
		return false, "gaps"
	}
	if n.Pending {
		return false, "pending"
	}
	if !n.InDigest {
		return false, "digest"
	}
	if n.QueueLen != 0 {
		return false, "queue"
	}
	return true, ""
}
