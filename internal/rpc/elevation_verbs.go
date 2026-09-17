package rpc

// Purpose (this file): the two verb-level questions about the elevated-verb
//   table, split out of elevation.go under the 300-line file cap.
// WHY THESE TWO TOGETHER: both answer something about a VERB rather than
//   about one request — whether a verb is listed at all, and whether one
//   particular listed verb's params describe the destructive case. The cut
//   is at "about the table" versus "about the flow", which is the seam a
//   reader would draw anyway.
// SPORT: internal/rpc elevation verbs (ADD) — P1-E17-W4-S38-T3.

import "encoding/json"

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
	for _, rule := range elevationTable {
		if rule.method == method {
			return true
		}
	}
	return false
}

// discardsServerPrimary reports whether a sync.conflicts_resolve request
// throws the server's copy away.
//
// FAIL CLOSED, like its neighbours above: only an explicit, readable
// request to keep the SERVER side is exempt. Params this cannot read, a
// missing side, and a side it does not recognise all elevate — the cost
// is one authorisation prompt on a malformed request, and the cost of
// guessing the other way is that the gate disappears for anyone who sends
// a payload this helper chokes on.
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
