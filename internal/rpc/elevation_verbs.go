package rpc

// Purpose (this file): the elevated-verb table's registration, its seal and
//   the verb-level questions asked of it, split out of elevation.go under the
//   300-line file cap.
// WHY THESE TOGETHER: all of it is about the TABLE rather than one request:
//   which verbs are listed, how a verb gets listed, when listing stops, and
//   whether one particular listed verb's params describe the destructive case.
// Constraints: one table. A verb is added only through RegisterElevatedVerb,
//   which panics (at init, so a bad registration never ships) on an empty
//   method, a duplicate, a rule that elevates nothing, a rule that says both
//   things, and any call after the table is sealed. The table seals on its
//   first read or first dispatch, whichever comes first, so no verb can be
//   registered after internal/mcp/coretools has filtered the MCP surface by
//   it. Registration never replaces or removes a row.
// SPORT: internal/rpc elevation verbs (ADD) — P1-E17-W4-S38-T3; registration
//   and seal P1-SEC-03.

import (
	"encoding/json"
	"sort"
	"sync"
	"sync/atomic"
)

// ElevationRule says when one verb is elevated. Exactly one of Always and
// Conditional is set. Conditional is consulted for envelope-free params only
// and MUST fail closed: report true when the params cannot be shown to be
// harmless. A client-declared flag never narrows a rule; a handler that
// computes loosening server-side may.
type ElevationRule struct {
	Always      bool
	Conditional func(params json.RawMessage) bool
}

// verbRule is one table row.
type verbRule struct {
	method      string
	always      bool
	conditional func(params json.RawMessage) bool
}

// elevationTable holds the rows. Written only by RegisterElevatedVerb, under
// elevationMu and before elevationSealed is set; read through
// lookupElevationRule (which seals) or, in this package's tests, directly.
var elevationTable []verbRule

var (
	elevationMu     sync.RWMutex
	elevationSealed atomic.Bool
)

// RegisterElevatedVerb adds method to the elevation table. It panics with a
// message naming the case ("empty method", "duplicate method", "zero rule",
// "both set", "sealed") on every misuse; the argument checks run before the
// seal check.
func RegisterElevatedVerb(method string, rule ElevationRule) {
	switch {
	case method == "":
		panic("rpc: RegisterElevatedVerb: empty method")
	case !rule.Always && rule.Conditional == nil:
		panic("rpc: RegisterElevatedVerb: zero rule for " + method + " (neither Always nor Conditional)")
	case rule.Always && rule.Conditional != nil:
		panic("rpc: RegisterElevatedVerb: both set for " + method + " (Always and Conditional)")
	}
	elevationMu.Lock()
	defer elevationMu.Unlock()
	for _, r := range elevationTable {
		if r.method == method {
			panic("rpc: RegisterElevatedVerb: duplicate method " + method)
		}
	}
	if elevationSealed.Load() {
		panic("rpc: RegisterElevatedVerb: table sealed; " + method + " registered after the first read or dispatch")
	}
	elevationTable = append(elevationTable, verbRule{method: method, always: rule.Always, conditional: rule.Conditional})
}

// sealElevationTable closes the table to registration.
func sealElevationTable() { elevationSealed.Store(true) }

// lookupElevationRule seals the table and returns the row for method.
func lookupElevationRule(method string) (verbRule, bool) {
	sealElevationTable()
	elevationMu.RLock()
	defer elevationMu.RUnlock()
	for _, rule := range elevationTable {
		if rule.method == method {
			return rule, true
		}
	}
	return verbRule{}, false
}

// ElevatedVerbNames returns a sorted copy of every method in the elevation
// table, conditional rows included. Reading the table seals it.
func ElevatedVerbNames() []string {
	sealElevationTable()
	elevationMu.RLock()
	defer elevationMu.RUnlock()
	names := make([]string, 0, len(elevationTable))
	for _, rule := range elevationTable {
		names = append(names, rule.method)
	}
	sort.Strings(names)
	return names
}

// IsElevatedVerb reports whether method appears in the elevated-verb
// table AT ALL, conditionally or not.
//
// Separate from IsElevated because the two questions have different
// callers. IsElevated decides one request; this decides whether a verb
// belongs on a surface that cannot carry an attestation — the MCP tool
// registry, where "elevated only sometimes" still means the verb must not
// be exposed, since an unelevated call through that surface would reach
// the handler with no gate in front of it.
func IsElevatedVerb(method string) bool {
	_, ok := lookupElevationRule(method)
	return ok
}

// discardsServerPrimary reports whether a sync.conflicts_resolve request
// throws the server's copy away.
//
// FAIL CLOSED, like its neighbours in elevation.go: only an explicit,
// readable request to keep the SERVER side is exempt. Params this cannot
// read, a missing side, and a side it does not recognise all elevate — the
// cost is one authorisation prompt on a malformed request, and the cost of
// guessing the other way is that the gate disappears for anyone who sends a
// payload this helper chokes on.
func discardsServerPrimary(raw json.RawMessage) bool {
	m, bad := unparseableParams(raw)
	if bad {
		return true
	}
	v, ok := m["keep"]
	if !ok {
		return true
	}
	var keep string
	if err := json.Unmarshal(v, &keep); err != nil {
		return true
	}
	// "server" is the only side that keeps what the merge decided.
	return keep != "server"
}
