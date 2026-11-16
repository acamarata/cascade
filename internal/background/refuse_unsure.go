package background

// PolicyDecision represents the outcome of a policy check.
type PolicyDecision struct {
	Decision   string
	Code       string
	Supervisor string
}

// RefuseUnsure evaluates a state and its panels, returning a refuse decision
// with code M1888 when the state is unsure, empty, or missing panels.
// It always leaves the supervisor active instead of parking or stalling.
func RefuseUnsure(state string, panels []string) PolicyDecision {
	if state == "" || state == "unsure" || len(panels) == 0 {
		return PolicyDecision{
			Decision:   "refuse",
			Code:       "M1888",
			Supervisor: "active",
		}
	}
	return PolicyDecision{
		Decision:   "proceed",
		Code:       "",
		Supervisor: "active",
	}
}
