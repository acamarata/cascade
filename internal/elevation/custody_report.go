// Purpose: report custody without authenticating or changing state.
// Inputs: selector, trust, clock. Outputs: status for diagnostics.
// Constraints: never generates or signs. SPORT: elevation custody reporting.

package elevation

// CustodyStatus describes selected custody and enrollment binding.
type CustodyStatus struct {
	Tier     CustodyTier
	Source   string
	Enrolled bool
	Bound    bool
	Reason   string
}

// CustodyReport reads custody state without generating or signing.
func CustodyReport(sel Selector, trust Backend, clock Clock) CustodyStatus {
	c := sel.Select()
	status := CustodyStatus{Tier: c.Tier(), Source: c.Source(), Reason: c.Reason()}
	if trust == nil {
		return status
	}
	_, ok, err := trust.Load()
	status.Enrolled = err == nil && ok
	if err != nil {
		status.Reason = err.Error()
		return status
	}
	_, _, err = BoundTrust(c, trust, clock)
	status.Bound = err == nil
	if err != nil {
		status.Reason = err.Error()
	}
	return status
}
