// Package policy (approval_matrix.go): Purpose: where an approval may
//
//	travel and how it is spent. Two tables that together decide the reach
//	of an approval: the remote-approvability matrix (which classes and
//	verbs a bridge may carry a decision for) and the redemption state model
//	(how a controller turns one approval into exactly one execution).
//
// Inputs: an ActionClass, or a verb name, or a redemption state.
// Outputs: RemoteApprovabilityMatrix, CanBridge, CanBridgeVerb,
//
//	ElevationClassVerbs, RedemptionState, ExecutionID, NextRedemptionState.
//
// Constraints: FAIL CLOSED. CanBridge is an ALLOW-LIST over two classes;
//
//	every other class, including the invalid zero value, is false. Every
//	06-FORGE-SPEC §5.14 elevation-class verb is LOCAL-ONLY and can never be
//	bridged and never be standing, whatever class it carries. The
//	redemption states have no permissive zero value and the only legal
//	advance is approved -> consuming -> consumed.
//
// SPORT: internal/policy RemoteApprovabilityMatrix/ADDED, CanBridge/ADDED,
//
//	ElevationClassVerbs/ADDED, RedemptionState/ADDED, ExecutionID/ADDED
//	(P1-E08-W2-S16-T3).
package policy

import "github.com/acamarata/cascade/pkg/cascade"

// elevationClassVerbs is the complete 06-FORGE-SPEC §5.14 elevated-verb
// enumeration, transcribed from that rule's prose. These verbs are
// LOCAL-ONLY: they are never bridge-approvable and never standing,
// regardless of the class or rung they otherwise carry.
//
// approval_matrix_test.go re-transcribes the same spec prose into a second
// list and asserts set equality, so an omission here fails that test
// rather than quietly narrowing the local-only set.
var elevationClassVerbs = []string{
	"vault.get",
	"vault.rotate",
	"approval.grant",
	"standing_grant.create",
	"standing_grant.change",
	"backup.create",
	"backup.export",
	"backup.import",
	"backup.restore",
	"backup.key_export",
	"backup.key_import",
	"plugin.add",
	"perms.grant",
	"perms.revoke",
	"node.enroll",
	"node.remove",
	"node.upgrade",
	"sync.conflicts_resolve",
	"policy.set",
	"sensitivity.set",
	"elevation.set_allow_remote",
	"plugins.set_enable_remote_runtime",
	"uninstall.purge_data",
}

// ElevationClassVerbs returns the §5.14 enumeration. The slice is copied,
// so a caller cannot shrink the local-only set by writing to it.
func ElevationClassVerbs() []string {
	out := make([]string, len(elevationClassVerbs))
	copy(out, elevationClassVerbs)
	return out
}

// RemoteApprovabilityMatrix answers the §5.24 question: may a decision on
// this action be taken somewhere other than this machine.
//
// It is a value type with no configuration. There is deliberately no
// constructor taking an override list: the set of remotely approvable
// actions is a property of the specification, not of a deployment, and an
// operator-widened matrix is precisely the loosening §5.24 forbids.
type RemoteApprovabilityMatrix struct{}

// bridgeableClasses is the allow-list: the ask-tier classes at risk L1-L2.
// Everything absent from this map is false, which is what makes the zero
// value and any future class refuse until someone decides otherwise.
var bridgeableClasses = map[ActionClass]bool{
	ClassLocalDev:          true,
	ClassWorkspaceMutation: true,
}

// CanBridge reports whether a decision on an action of this class may
// cross a bridge. True ONLY for the ask-tier L1-L2 classes; false for
// read, for external side effects, for destructive/privileged work and for
// any value that names no class at all.
func (RemoteApprovabilityMatrix) CanBridge(class ActionClass) bool {
	if !class.Valid() {
		return false
	}
	return bridgeableClasses[class]
}

// CanBridgeVerb is the whole rule for a named action: the verb must not be
// elevation-class AND the class must be bridgeable. It is the entry point
// a caller that knows the verb should use, because the class test alone
// cannot see that a verb is local-only.
func (m RemoteApprovabilityMatrix) CanBridgeVerb(verb string, class ActionClass) bool {
	if IsElevationClassVerb(verb) {
		return false
	}
	return m.CanBridge(class)
}

// IsElevationClassVerb reports whether verb is one of the §5.14 elevated
// verbs. An unknown verb is NOT elevation-class by this test alone; the
// caller's own registry decides whether an unregistered verb is admitted
// at all, and R-21.207 requires that it refuse.
func IsElevationClassVerb(verb string) bool {
	for _, v := range elevationClassVerbs {
		if v == verb {
			return true
		}
	}
	return false
}

// ExecutionID names one execution of one approved action. It is minted
// once, before dispatch, and it is what makes a retry after a crash
// re-drive the SAME execution rather than start a second one.
type ExecutionID = cascade.ID

// RedemptionState is where an approval stands on the road from a decision
// to an execution (R-21.209). The zero value is deliberately not a member,
// so a row that reached memory without a state does not read as approved.
type RedemptionState uint8

// The three states, in the only order they may be reached.
const (
	_ RedemptionState = iota // 0 is deliberately not a valid state

	// RedemptionApproved has a decision and an unspent token.
	RedemptionApproved
	// RedemptionConsuming has won the compare-and-set and holds the
	// execution id. Exactly one caller ever reaches this state for a given
	// (request_id, nonce).
	RedemptionConsuming
	// RedemptionConsumed has completed. It is terminal.
	RedemptionConsumed
)

// redemptionStateNames holds each state's stable name, indexed by value.
var redemptionStateNames = [...]string{"", "approved", "consuming", "consumed"}

// String returns the state's stable name, e.g. "consuming". An invalid
// value renders as "invalid-redemption-state" and never as a real state.
func (s RedemptionState) String() string {
	if !s.Valid() {
		return "invalid-redemption-state"
	}
	return redemptionStateNames[s]
}

// Valid reports whether s names one of the three states.
func (s RedemptionState) Valid() bool {
	return s >= RedemptionApproved && s <= RedemptionConsumed
}

// NextRedemptionState is the CAS contract the controller implements: the
// ONE legal advance from each state, and a refusal from anywhere else.
//
// The controller is the sole redemption authority. It performs an atomic
// compare-and-set from approved to consuming under a unique
// (request_id, nonce) constraint, so exactly one caller wins; the winner
// appends a STABLE ExecutionID BEFORE dispatch. Executors de-duplicate by
// that id, so a process killed between the consume and the run recovers by
// re-driving the SAME execution id: the consume is never lost and a second
// execution is never issued. Nodes never redeem; they relay to the
// controller.
//
// This function is the state half of that contract and the storage half
// lives with the ledger. A transition this function refuses is a
// KindConflict, because it means someone else already advanced the row.
func NextRedemptionState(from RedemptionState) (RedemptionState, error) {
	switch from {
	case RedemptionApproved:
		return RedemptionConsuming, nil
	case RedemptionConsuming:
		return RedemptionConsumed, nil
	default:
		return 0, cascade.Newf(cascade.KindConflict,
			"policy: an approval in state %s cannot be advanced", from)
	}
}
