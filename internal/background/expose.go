package background

// Portfolio represents read-only projections for a portfolio.
type Portfolio struct {
	// DecisionValues stores the decisions for defensive copying.
	DecisionValues []string
	// StatusValue stores the current status string.
	StatusValue string
	// TickValue stores the current tick count.
	TickValue int
	// ReplayValue stores whether a replay is active.
	ReplayValue bool
}

// Decisions returns a defensive copy of DecisionValues.
// It returns a non-nil empty slice if the receiver is nil or DecisionValues is empty.
func (p *Portfolio) Decisions() []string {
	if p == nil || len(p.DecisionValues) == 0 {
		return []string{}
	}
	result := make([]string, len(p.DecisionValues))
	copy(result, p.DecisionValues)
	return result
}

// Status returns the StatusValue.
// It returns "idle" if the receiver is nil or StatusValue is empty.
func (p *Portfolio) Status() string {
	if p == nil || p.StatusValue == "" {
		return "idle"
	}
	return p.StatusValue
}

// Tick returns the TickValue.
// It returns 0 if the receiver is nil.
func (p *Portfolio) Tick() int {
	if p == nil {
		return 0
	}
	return p.TickValue
}

// Replay returns the ReplayValue.
// It returns false if the receiver is nil.
func (p *Portfolio) Replay() bool {
	if p == nil {
		return false
	}
	return p.ReplayValue
}
