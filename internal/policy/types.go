// Package policy (types.go): Purpose: the shared type vocabulary of the
//
//	policy package: the L0-L4 risk ladder and the action classes that
//	name what a command does.
//
// Inputs: none; this file is pure declarations plus total functions over
//
//	its own enums.
//
// Outputs: RiskLevel (L0..L4), ActionClass (read .. destructive_privileged),
//
//	ActionClass.Risk (the ladder mapping), and the unexported safeLevel /
//	maxLevel / safeActionClass helpers every classifier and capability
//	path funnels through.
//
// Constraints: 06-FORGE-SPEC §5.15 is the normative ladder and it is
//
//	FAIL-CLOSED: neither enum has a permissive zero value. RiskLevel's iota
//	starts at 1 so a forgotten field reads as invalid rather than as L0,
//	and safeLevel maps every invalid value to L4. Ordering is load-bearing:
//	a higher numeric value is strictly more restrictive, which is what
//	makes maxLevel a correct combinator for the wrapper rule
//	max(wrapper, inner).
//
// SPORT: internal/policy RiskLevel/ADDED, ActionClass/ADDED (P1-E09-W2-S17-T3);
//
//	safeActionClass/ADDED (P1-E09-W2-S17-T1).
package policy

// RiskLevel is one rung of the 06-FORGE-SPEC §5.15 risk ladder.
//
// The zero value is deliberately NOT a member: §5.15 states the level enum
// has no permissive zero value, because a struct field left unset must not
// read as "read-only, allow". Every value that reaches a decision passes
// through safeLevel, which maps the zero value and any out-of-range value
// to L4.
type RiskLevel uint8

// The ladder, in §5.15 order. Numeric order is restrictiveness order.
const (
	_ RiskLevel = iota // 0 is deliberately not a valid RiskLevel

	// L0 is a read-only operation. Default disposition: allow.
	L0
	// L1 is safe local development work: tests, lint, build. Default
	// disposition: allow.
	L1
	// L2 mutates the workspace. Default disposition: ask.
	L2
	// L3 has an external side effect: push, PR, network, messages.
	// Default disposition: ask, and never auto-advance.
	L3
	// L4 is destructive or privileged. Default disposition: deny, subject
	// to same-turn authorization only. L4 is also where every
	// unclassifiable input lands.
	L4
)

// riskLevelNames holds the display string for each rung, indexed by value.
// Index 0 is the invalid zero value's placeholder.
var riskLevelNames = [...]string{"", "L0", "L1", "L2", "L3", "L4"}

// String returns the rung's stable name, e.g. "L2". An invalid value
// renders as "invalid-risk-level" rather than as any rung, so a bad value
// can never be mistaken for a real classification in a log or an audit row.
func (r RiskLevel) String() string {
	if !r.Valid() {
		return "invalid-risk-level"
	}
	return riskLevelNames[r]
}

// Valid reports whether r names a rung of the ladder.
func (r RiskLevel) Valid() bool { return r >= L0 && r <= L4 }

// disposition returns the §5.15 default disposition for the rung: what the
// ladder says happens when nothing more specific applies. It is a string
// rather than a Verdict because the Verdict enum belongs to the policy
// evaluation engine (S-17.T2), and this ticket must not pre-empt it; the
// strings are exactly the words §5.15 uses.
func (r RiskLevel) disposition() string {
	switch safeLevel(r) {
	case L0, L1:
		return "allow"
	case L2, L3:
		return "ask"
	case L4:
		return "deny"
	default:
		return "deny"
	}
}

// safeLevel maps any RiskLevel to a level that is safe to act on. A valid
// rung passes through unchanged; the zero value and anything out of range
// become L4. This is the single choke point for §5.15's fail-closed
// mandate, so no caller has to remember the rule.
func safeLevel(r RiskLevel) RiskLevel {
	if !r.Valid() {
		return L4
	}
	return r
}

// maxLevel returns the more restrictive of a and b, after passing both
// through safeLevel. It implements the §5.15 wrapper rule
// max(wrapper, resolved-inner) and the combination of the several commands
// in one chained line, where the line as a whole is as dangerous as its
// most dangerous member.
func maxLevel(a, b RiskLevel) RiskLevel {
	sa, sb := safeLevel(a), safeLevel(b)
	if sa > sb {
		return sa
	}
	return sb
}

// ActionClass names what an action DOES, in the vocabulary R-16.46 fixed:
// the five classes mirror the five §5.15 rungs one for one. The classifier
// assigns a class to a command and derives the rung from it, so there is
// one ladder in the code rather than two parallel tables that can drift.
//
// Like RiskLevel, the zero value is not a member.
type ActionClass uint8

// The five classes, in ladder order.
const (
	_ ActionClass = iota // 0 is deliberately not a valid ActionClass

	// ClassRead reads state and changes nothing (§5.15 L0).
	ClassRead
	// ClassLocalDev runs safe local development work: tests, lint,
	// build (§5.15 L1).
	ClassLocalDev
	// ClassWorkspaceMutation changes files or repository state on this
	// machine (§5.15 L2).
	ClassWorkspaceMutation
	// ClassExternalSideEffect reaches outside this machine: push, PR,
	// network, messages (§5.15 L3).
	ClassExternalSideEffect
	// ClassDestructivePrivileged destroys data or exercises privilege
	// (§5.15 L4).
	ClassDestructivePrivileged
)

// actionClassNames holds the stable wire name for each class, in the
// spelling R-16.46 uses. Index 0 is the invalid zero value's placeholder.
var actionClassNames = [...]string{
	"",
	"read",
	"local_dev",
	"workspace_mutation",
	"external_side_effect",
	"destructive_privileged",
}

// String returns the class's stable name, e.g. "workspace_mutation".
func (a ActionClass) String() string {
	if !a.Valid() {
		return "invalid-action-class"
	}
	return actionClassNames[a]
}

// Valid reports whether a names one of the five classes.
func (a ActionClass) Valid() bool {
	return a >= ClassRead && a <= ClassDestructivePrivileged
}

// Risk returns the §5.15 rung this class sits on. The mapping is one to
// one by construction (R-16.46), and an invalid class maps to L4 rather
// than to any permissive rung.
func (a ActionClass) Risk() RiskLevel {
	switch a {
	case ClassRead:
		return L0
	case ClassLocalDev:
		return L1
	case ClassWorkspaceMutation:
		return L2
	case ClassExternalSideEffect:
		return L3
	case ClassDestructivePrivileged:
		return L4
	default:
		return L4
	}
}

// safeActionClass maps any ActionClass to one that is safe to act on, the
// twin of safeLevel one rung up the vocabulary. A valid class passes
// through unchanged; the zero value and anything out of range become
// ClassDestructivePrivileged, which sits on L4 and therefore denies.
//
// This is the binding S-17.T1 needs: Capability.DefaultPolicy is an
// ActionClass, and Capability.Class reads it through here, so a capability
// row that reached memory without passing Validate — decoded from storage,
// say, or built by a caller that forgot the field — presents as
// destructive_privileged rather than as read. There is no path by which an
// unset default policy reads as "allow".
func safeActionClass(a ActionClass) ActionClass {
	if !a.Valid() {
		return ClassDestructivePrivileged
	}
	return a
}
