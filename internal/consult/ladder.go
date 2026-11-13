package consult

// Response represents a single response in the proxy ladder.
type Response struct {
	Role   string
	Answer string
	GC     bool
}

// LadderResult holds the outcome of evaluating the proxy ladder.
type LadderResult struct {
	Outcome      string
	CarriedAlone bool
}

// EvaluateLadder evaluates the proxy ladder per M1894.
func EvaluateLadder(rs []Response) LadderResult {
	for _, r := range rs {
		if r.Answer == "sure no" {
			return LadderResult{Outcome: "refuse", CarriedAlone: false}
		}
	}

	gcCEOYes := false
	nonGcCEOYes := false
	for _, r := range rs {
		if r.Role == "ceo" && r.Answer == "yes" {
			if r.GC {
				gcCEOYes = true
			} else {
				nonGcCEOYes = true
			}
		}
	}

	if gcCEOYes {
		if len(rs) == 1 {
			return LadderResult{Outcome: "approve", CarriedAlone: true}
		}
		return LadderResult{Outcome: "approve", CarriedAlone: false}
	}

	if nonGcCEOYes {
		return LadderResult{Outcome: "proceed", CarriedAlone: false}
	}

	return LadderResult{Outcome: "proceed", CarriedAlone: false}
}
