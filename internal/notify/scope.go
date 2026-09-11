// Purpose: ScopeDeliveryPredicate (R-16.5), the fail-closed gate deciding
//
//	whether one candidate subscriber session may receive one
//	Notification. dispatch.go calls it before every fan-out; inbox.go
//	calls it before List/Read/Ack so a withheld record is never
//	discoverable by id either.
//
// Inputs: a CandidateSession (the session's own precomputed scope chain,
//
//	from internal/context/scope.CandidateScopeRefs) and a Notification.
//
// Outputs: a bool: true only when the Notification's Class, resolved
//
//	fail-closed, explicitly admits this session.
//
// Constraints: an unresolvable Class (see notify.go's Class.Resolve) is
//
//	withheld from every session, never delivered as if it were global.
//	An Addressed Notification is compared by exact session-ID match
//	only, never by scope-chain membership.
//
// SPORT: internal.notify.ScopeDeliveryPredicate/ADDED (P1-E23-W5-S49-T1).

package notify

// CandidateSession is the delivery-eligibility view of one subscriber
// session: its own session ID plus the deny-by-default candidate scope
// set internal/context/scope.CandidateScopeRefs computed for it. This
// package never calls CandidateScopeRefs itself (that requires a
// *scope.GraphStore and I/O); the composition root resolves it once per
// session and passes the result in, keeping this package's delivery logic
// pure and independently testable.
type CandidateSession struct {
	// SessionID is this session's own scope.Ref ID (ScopeKindSession).
	SessionID string
	// ScopeIDs is the set of scope-graph IDs (from CandidateScopeRefs,
	// across all Ref.Kind values) this session's chain admits. A Scoped
	// Notification is deliverable only when its TargetScope (falling back
	// to OriginScope when TargetScope is empty) appears in this set.
	ScopeIDs map[string]bool
}

// InScope reports whether id is a member of s's candidate scope set.
func (s CandidateSession) InScope(id string) bool {
	if id == "" {
		return false
	}
	return s.ScopeIDs[id]
}

// ScopeDeliveryPredicate reports whether session may receive n, per R-16.5:
//
//   - Class Scoped: n's TargetScope (or OriginScope when TargetScope is
//     unset) must be a member of session's candidate scope set.
//   - Class Addressed: n.TargetSession must equal session.SessionID
//     exactly.
//   - Class GlobalCritical: always deliverable.
//   - Anything else (unset, unrecognized, or a Scoped Notification whose
//     scope fields are both empty and therefore unresolvable): withheld
//     from every session — fail closed, never a default recipient.
func ScopeDeliveryPredicate(session CandidateSession, n Notification) bool {
	class := n.Class.Resolve()
	switch class {
	case ClassGlobalCritical:
		return true
	case ClassAddressed:
		return n.TargetSession != "" && n.TargetSession == session.SessionID
	case ClassScoped:
		target := n.TargetScope
		if target == "" {
			target = n.OriginScope
		}
		if target == "" {
			// Unresolvable scope: withheld from every session, matching
			// the fail-closed contract for an unset/unknown Class.
			return false
		}
		return session.InScope(target)
	case classUnresolvable:
		// Resolve() never actually returns this value (it maps
		// classUnresolvable to ClassScoped), so this branch is
		// unreachable in practice; it is listed explicitly, fail closed,
		// so the exhaustive-switch gate cannot silently stop covering
		// Class's full vocabulary if that mapping ever changes.
		return false
	default:
		return false
	}
}

// visible reports whether session may discover rec at all: Visibility
// resolved fail-closed, then ScopeDeliveryPredicate. It lives beside
// ScopeDeliveryPredicate because both implement the same fail-closed
// discoverability contract; inbox.go's List/Read/Ack call it before a
// record is ever returned to a caller.
func visible(session SessionScopeRef, rec *inboxRecord) bool {
	switch rec.n.Visibility.Resolve() {
	case VisibilityPrivate:
		return rec.n.TargetSession != "" && rec.n.TargetSession == session.SessionID
	case VisibilityShared:
		// The `--all` global view for the same user; session identity is
		// sufficient at this ticket's layer (per-user session sets are a
		// composition-root concern — see R-21.227's projection note).
		return true
	case VisibilityScoped, VisibilityExecutive:
		return ScopeDeliveryPredicate(session, rec.n)
	default:
		return false
	}
}
