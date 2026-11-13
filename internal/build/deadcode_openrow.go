package build

import "sort"

// DeadCodeAllowedByOpenRow splits dead internal/ symbols into those that
// still fail the dead-code gate and those held by an open test-only row.
//
// A symbol is held when the test-only allow list has a row keyed
// "<Dir>.<Name>" whose RetireTicket is a known ticket that has not closed.
// That row records a promise: a later ticket adds the caller. Until that
// ticket lands, the symbol is expected to be unreferenced, so flagging it
// would report planned work as a defect. Once the ticket closes, the hold
// ends and the symbol is reported again if nothing calls it. This is the
// same rule checkCallerSitesByTicketState applies to caller sites.
//
// Every other case stays a violation: no row, a closed retire ticket,
// UnownedTicket, a capability key, or an id the manifest does not know.
// held lists the allow keys that covered a symbol, sorted.
func DeadCodeAllowedByOpenRow(v []DeadCodeViolation, allow map[string]TestOnlyAllowEntry,
	known, closed map[string]bool) (remaining []DeadCodeViolation, held []string) {
	seen := make(map[string]bool)
	for _, d := range v {
		key := d.Dir + "." + d.Name
		e, ok := allow[key]
		if !ok || !known[e.RetireTicket] || closed[e.RetireTicket] {
			remaining = append(remaining, d)
			continue
		}
		if !seen[key] {
			seen[key] = true
			held = append(held, key)
		}
	}
	sort.Strings(held)
	return remaining, held
}
