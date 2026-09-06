package scope

// Purpose: the R-21.157 closed traversal table as DATA, not prose:
//   Traversal(kind, edge) returns the TraversalRule for every (ScopeKind,
//   EdgeClass) pair, with NO permissive default — an unlisted or unknown
//   pair denies. traversal_test.go's golden test pins the whole table so a
//   later widening fails CI.
// Inputs: a ScopeKind and an EdgeClass.
// Outputs: a TraversalRule plus an ok bool (ok=false means "not
//   traversable", never "look elsewhere for a default").
// Constraints: the three persisted EdgeKind values map onto EdgeClass with
//   no new stored value and no fifth table: member_of -> EdgeClassMember,
//   depends_on/shares_context_with -> EdgeClassRoute, and the resolved own
//   scope chain -> EdgeClassParent. CandidateScopeRefs (resolver.go) reads
//   this table only; it never re-derives direction or transitivity.
// SPORT: context/scope-graph/ADD (R-21.157 traversal table).

// EdgeClass is the R-21.157 closed edge-class vocabulary the traversal
// table is keyed on. It is distinct from EdgeKind: EdgeKind is the
// PERSISTED value (three of them); EdgeClass is the smaller closed set
// those persisted values map onto, plus the one class (parent) that has
// no persisted counterpart at all — it names the resolved session's own
// scope chain, which PutEdge never stores as a row.
type EdgeClass string

const (
	// EdgeClassParent is the resolved session's own scope chain (session
	// -> task -> project -> product/workspace), never a persisted
	// scope_edge row.
	EdgeClassParent EdgeClass = "parent"
	// EdgeClassMember is the member_of persisted relationship.
	EdgeClassMember EdgeClass = "member"
	// EdgeClassRoute is the depends_on and shares_context_with persisted
	// relationships.
	EdgeClassRoute EdgeClass = "route"
)

// Valid reports whether c is one of the three closed EdgeClass values.
func (c EdgeClass) Valid() bool {
	switch c {
	case EdgeClassParent, EdgeClassMember, EdgeClassRoute:
		return true
	}
	return false
}

// EdgeClassFor maps a persisted EdgeKind onto its EdgeClass, per R-21.157:
// member_of -> member, depends_on and shares_context_with -> route. Called
// only after ValidateEdgeKind has already accepted k; an invalid k reports
// ok=false rather than guessing a class.
func EdgeClassFor(k EdgeKind) (EdgeClass, bool) {
	switch k {
	case EdgeKindMemberOf:
		return EdgeClassMember, true
	case EdgeKindDependsOn, EdgeKindSharesContextWith:
		return EdgeClassRoute, true
	default:
		return "", false
	}
}

// Direction is the traversal direction a TraversalRule permits.
type Direction string

const (
	// DirectionOutbound: traverse from the resolved scope TOWARD the
	// edge's declared target.
	DirectionOutbound Direction = "outbound"
	// DirectionInbound: traverse from the edge's declared target TOWARD
	// the resolved scope (the reverse of how the edge was declared).
	DirectionInbound Direction = "inbound"
)

// TraversalRule is what Traversal returns for one (ScopeKind, EdgeClass)
// pair: the permitted Direction and whether the rule is Transitive
// (follows more than one hop) or applies only to the immediate edge.
type TraversalRule struct {
	Direction  Direction
	Transitive bool
}

// traversalTable is the CLOSED (ScopeKind x EdgeClass) rule table.
// A pair absent from this map is NOT traversable (06 SS5.15/SS5.16
// fail-closed pattern) -- Traversal has no permissive default. The table
// is total over the six ScopeKind values R-21.157 names (session, task,
// project, workspace, product, global); ScopeKindGeneral is deliberately
// ABSENT -- a general-kind session has no graph membership to traverse
// from, so every (general, *) pair correctly denies via the same "absent
// pair" path, with no special-cased branch needed in Traversal itself.
//
// Rule shape, by class:
//   - parent: the resolved session's own scope chain is always visible,
//     outbound only (a session sees its own chain; the chain does not
//     reach back down into every session that resolves through it), and
//     transitive (session -> task -> project -> product/workspace is a
//     multi-hop chain).
//   - member: member_of is visible outbound and transitive at the
//     narrower kinds (a task or project may walk up its declared
//     membership chain), but is NOT granted at workspace/product/global --
//     those kinds sit at or above the top of any membership chain R-16.3
//     defines, so there is nothing further for a member edge to reach
//     from them, and this ticket adds no such reach.
//   - route: depends_on/shares_context_with are visible outbound and
//     NON-transitive at every kind that can hold one (R-16.4's leak
//     fixture requires a DECLARED edge per hop, never a chain of them)
//     except global, which is deliberately absent -- R-21.157's forward
//     note reserves global-critical routing for AK/S-73.T2, not this
//     ticket.
var traversalTable = map[ScopeKind]map[EdgeClass]TraversalRule{
	ScopeKindSession: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
	},
	ScopeKindTask: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
	},
	ScopeKindProject: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassMember: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
	},
	ScopeKindWorkspace: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
	},
	ScopeKindProduct: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
		EdgeClassRoute:  {Direction: DirectionOutbound, Transitive: false},
	},
	ScopeKindGlobal: {
		EdgeClassParent: {Direction: DirectionOutbound, Transitive: true},
	},
}

// Traversal returns the TraversalRule for (kind, edge), and ok=false when
// the pair is absent from the closed table -- the fail-closed contract
// CandidateScopeRefs and every visibility decision in this package relies
// on. No caller may pass a rule of its own; this is the sole source.
func Traversal(kind ScopeKind, edge EdgeClass) (TraversalRule, bool) {
	byEdge, ok := traversalTable[kind]
	if !ok {
		return TraversalRule{}, false
	}
	rule, ok := byEdge[edge]
	return rule, ok
}
